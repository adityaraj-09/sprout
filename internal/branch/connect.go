package branch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adityaraj/ardent-clone/internal/compute"
	"github.com/adityaraj/ardent-clone/internal/meta"
	"github.com/adityaraj/ardent-clone/internal/postgres"
	"github.com/adityaraj/ardent-clone/internal/replica"
	"github.com/google/uuid"
)

const (
	ModePhysical = "physical"
	ModeLogical  = "logical"
)

// Connect bootstraps main from an upstream Postgres.
// mode=physical → pg_basebackup + hot standby (full PGDATA twin)
// mode=logical  → schema dump + PUBLICATION/SUBSCRIPTION (not a PGDATA twin; table sync)
func (s *Service) Connect(ctx context.Context, projectID, primaryURL, mode string) (meta.Connector, replica.Lag, error) {
	if mode == "" {
		mode = ModePhysical
	}
	if mode != ModePhysical && mode != ModeLogical {
		return meta.Connector{}, replica.Lag{}, fmt.Errorf("invalid_mode: use physical or logical")
	}
	if mode == ModeLogical {
		return s.connectLogical(ctx, projectID, primaryURL)
	}
	return s.connectPhysical(ctx, projectID, primaryURL)
}

func (s *Service) connectPhysical(ctx context.Context, projectID, primaryURL string) (meta.Connector, replica.Lag, error) {
	s.mainMu.Lock()
	defer s.mainMu.Unlock()

	conn, err := replica.ParseURL(primaryURL)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}
	if err := replica.EnsurePrimaryReachable(conn.Host, conn.Port, 5*time.Second); err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	rm := &replica.Manager{Bins: s.Bins}
	if err := rm.Ping(ctx, conn); err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	c := newConnector(projectID, primaryURL, ModePhysical)
	if err := s.Store.PutConnector(ctx, c); err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	fmt.Println("=== connect physical (pg_basebackup full copy) ===")
	fmt.Printf("  primary: %s:%d user=%s\n", conn.Host, conn.Port, conn.User)

	_ = s.Compute.Stop(ctx, s.mainHandle())
	if err := os.RemoveAll(s.MainDir()); err != nil {
		return s.failConnector(ctx, c, err)
	}

	if err := rm.BaseBackup(ctx, conn, s.MainDir()); err != nil {
		return s.failConnector(ctx, c, err)
	}
	if err := rm.PrepareStandbyDataDir(s.MainDir(), MainPort); err != nil {
		return s.failConnector(ctx, c, err)
	}
	standbySignal := filepath.Join(s.MainDir(), "standby.signal")
	if _, err := os.Stat(standbySignal); err != nil {
		_ = os.WriteFile(standbySignal, []byte(""), 0o600)
	}

	fmt.Println("→ starting main as hot standby on", MainPort)
	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: "main", DataDir: s.MainDir(), Port: MainPort, LogFile: s.logPath("main"),
	}); err != nil {
		return s.failConnector(ctx, c, err)
	}

	lag, err := rm.WaitCaughtUp(ctx, "127.0.0.1", MainPort, s.maxLagBytes(), 2*time.Minute)
	if err != nil {
		c.LastLagBytes = lag.LagBytes
		c.LastLSN = lag.ReplayLSN
		return s.failConnector(ctx, c, err)
	}
	return s.finishConnector(ctx, projectID, c, lag)
}

func (s *Service) connectLogical(ctx context.Context, projectID, primaryURL string) (meta.Connector, replica.Lag, error) {
	s.mainMu.Lock()
	defer s.mainMu.Unlock()

	conn, err := replica.ParseURL(primaryURL)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}
	if err := replica.EnsurePrimaryReachable(conn.Host, conn.Port, 5*time.Second); err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	rm := &replica.Manager{Bins: s.Bins}
	if err := rm.Ping(ctx, conn); err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	c := newConnector(projectID, primaryURL, ModeLogical)
	if err := s.Store.PutConnector(ctx, c); err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	fmt.Println("=== connect logical (publication + subscription) ===")
	fmt.Println("  NOT a full PGDATA twin — copies table data via logical decoding")
	fmt.Printf("  primary: %s:%d user=%s sslmode=%s\n", conn.Host, conn.Port, conn.User, conn.SSLMode)

	// 1) Publication on upstream (may fail on locked-down hosts)
	fmt.Println("→ ensure publication on primary")
	if err := rm.EnsurePublication(ctx, conn, replica.DefaultPublication, "public"); err != nil {
		return s.failConnector(ctx, c, fmt.Errorf("publication: %w", err))
	}

	// 2) Fresh local primary (empty cluster)
	_ = s.Compute.Stop(ctx, s.mainHandle())
	if err := os.RemoveAll(s.MainDir()); err != nil {
		return s.failConnector(ctx, c, err)
	}
	main := &postgres.Instance{
		Name: "main", DataDir: s.MainDir(), Port: MainPort,
		LogFile: s.logPath("main"), Bins: s.Bins,
	}
	fmt.Println("→ initdb local main (subscriber)")
	if err := main.Init(); err != nil {
		return s.failConnector(ctx, c, err)
	}
	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: "main", DataDir: s.MainDir(), Port: MainPort, LogFile: s.logPath("main"),
	}); err != nil {
		return s.failConnector(ctx, c, err)
	}

	// 3) Schema must exist before SUBSCRIPTION
	if err := rm.DumpSchema(ctx, conn, "127.0.0.1", MainPort, "public"); err != nil {
		return s.failConnector(ctx, c, err)
	}

	// 4) Subscription + initial copy_data
	if err := rm.CreateSubscription(ctx, conn, "127.0.0.1", MainPort, replica.DefaultSubscription, replica.DefaultPublication); err != nil {
		return s.failConnector(ctx, c, err)
	}

	st, err := rm.WaitLogicalSync(ctx, "127.0.0.1", MainPort, replica.DefaultSubscription, 10*time.Minute)
	if err != nil {
		c.LastLSN = st.ReceivedLSN
		return s.failConnector(ctx, c, err)
	}

	lag := replica.Lag{
		IsStandby:   false,
		InRecovery:  false,
		ReceiveLSN:  st.ReceivedLSN,
		ReplayLSN:   st.ReceivedLSN,
		LagBytes:    0,
		ReplayPause: false,
	}
	fmt.Printf("✓ logical sync ready  tables=%d/%d lsn=%s\n", st.TableReady, st.TableTotal, st.ReceivedLSN)
	return s.finishConnector(ctx, projectID, c, lag)
}

func newConnector(projectID, primaryURL, mode string) meta.Connector {
	now := time.Now().UTC()
	return meta.Connector{
		ID:         uuid.NewString(),
		ProjectID:  projectID,
		Name:       "primary",
		PrimaryURL: primaryURL,
		Mode:       mode,
		Status:     meta.ConnectorBootstrapping,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func (s *Service) failConnector(ctx context.Context, c meta.Connector, err error) (meta.Connector, replica.Lag, error) {
	c.Status = meta.ConnectorError
	c.ErrorMessage = err.Error()
	_ = s.Store.UpdateConnector(ctx, c)
	return c, replica.Lag{}, err
}

func (s *Service) finishConnector(ctx context.Context, projectID string, c meta.Connector, lag replica.Lag) (meta.Connector, replica.Lag, error) {
	c.Status = meta.ConnectorReplicating
	c.ErrorMessage = ""
	c.LastLSN = lag.ReplayLSN
	if c.LastLSN == "" {
		c.LastLSN = lag.ReceiveLSN
	}
	c.LastLagBytes = lag.LagBytes
	_ = s.Store.UpdateConnector(ctx, c)
	_ = s.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "main", ProjectID: projectID, Name: "main", Role: "main",
		Status: meta.StatusActive, Port: MainPort, DataDir: s.MainDir(),
		Compute: s.Compute.Name(), ConnString: fmt.Sprintf("postgresql://localhost:%d/postgres", MainPort),
	})
	fmt.Printf("✓ connector mode=%s status=replicating lsn=%s\n", c.Mode, c.LastLSN)
	return c, lag, nil
}

func (s *Service) ReplicationStatus(ctx context.Context, projectID string) (meta.Connector, replica.Lag, error) {
	c, err := s.Store.GetConnector(ctx, projectID)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, fmt.Errorf("no connector — run: ardent connect <url>")
	}
	rm := &replica.Manager{Bins: s.Bins}

	if c.Mode == ModeLogical {
		st, err := rm.LogicalSyncStatus(ctx, "127.0.0.1", MainPort, replica.DefaultSubscription)
		if err != nil {
			return c, replica.Lag{}, err
		}
		lag := replica.Lag{ReceiveLSN: st.ReceivedLSN, ReplayLSN: st.ReceivedLSN, IsStandby: false}
		c.LastLSN = st.ReceivedLSN
		c.LastLagBytes = 0
		if st.Enabled && st.TableReady >= st.TableTotal {
			c.Status = meta.ConnectorReplicating
		}
		_ = s.Store.UpdateConnector(ctx, c)
		return c, lag, nil
	}

	lag, err := rm.Status(ctx, "127.0.0.1", MainPort)
	if err != nil {
		return c, replica.Lag{}, err
	}
	c.LastLSN = lag.ReplayLSN
	c.LastLagBytes = lag.LagBytes
	if lag.IsStandby {
		c.Status = meta.ConnectorReplicating
	}
	_ = s.Store.UpdateConnector(ctx, c)
	return c, lag, nil
}

func (s *Service) maxLagBytes() int64 {
	if s.MaxLagBytes > 0 {
		return s.MaxLagBytes
	}
	return 16 * 1024 * 1024
}

func (s *Service) hasConnector(ctx context.Context, projectID string) bool {
	_, err := s.Store.GetConnector(ctx, projectID)
	return err == nil
}
