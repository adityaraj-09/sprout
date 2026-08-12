package branch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/replica"
	"github.com/google/uuid"
)

const (
	ModePhysical = "physical"
	ModeLogical  = "logical"
)

// Connect bootstraps a named local replica from an upstream Postgres.
// Each connector gets data/replicas/<name>/ and its own port.
func (s *Service) Connect(ctx context.Context, projectID, name, primaryURL, mode string) (meta.Connector, replica.Lag, error) {
	if mode == "" {
		mode = ModePhysical
	}
	if mode != ModePhysical && mode != ModeLogical {
		return meta.Connector{}, replica.Lag{}, fmt.Errorf("invalid_mode: use physical or logical")
	}
	if name == "" {
		name = "primary"
	}
	if !nameRe.MatchString(name) {
		return meta.Connector{}, replica.Lag{}, fmt.Errorf("invalid_name: connector name must match [a-z][a-z0-9-]*")
	}
	if mode == ModeLogical {
		return s.connectLogical(ctx, projectID, name, primaryURL)
	}
	return s.connectPhysical(ctx, projectID, name, primaryURL)
}

func (s *Service) connectPhysical(ctx context.Context, projectID, name, primaryURL string) (meta.Connector, replica.Lag, error) {
	unlock := s.lockBranch("connector:" + name)
	defer unlock()

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

	c, err := s.prepareConnectorRecord(ctx, projectID, name, primaryURL, ModePhysical)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	fmt.Println("=== connect physical ===")
	fmt.Printf("  name=%s port=%d dir=%s\n", c.Name, c.Port, c.DataDir)
	fmt.Printf("  primary: %s:%d user=%s\n", conn.Host, conn.Port, conn.User)

	h := s.connectorHandle(c)
	_ = s.Compute.Stop(ctx, h)
	if err := os.RemoveAll(c.DataDir); err != nil {
		return s.failConnector(ctx, c, err)
	}

	if err := rm.BaseBackup(ctx, conn, c.DataDir); err != nil {
		return s.failConnector(ctx, c, err)
	}
	if err := rm.PrepareStandbyDataDir(c.DataDir, c.Port); err != nil {
		return s.failConnector(ctx, c, err)
	}
	standbySignal := filepath.Join(c.DataDir, "standby.signal")
	if _, err := os.Stat(standbySignal); err != nil {
		_ = os.WriteFile(standbySignal, []byte(""), 0o600)
	}

	fmt.Println("→ starting replica as hot standby on", c.Port)
	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port, LogFile: s.logPath(replicaComputeName(c.Name)),
	}); err != nil {
		return s.failConnector(ctx, c, err)
	}

	lag, err := rm.WaitCaughtUp(ctx, "127.0.0.1", c.Port, s.maxLagBytes(), 2*time.Minute)
	if err != nil {
		c.LastLagBytes = lag.LagBytes
		c.LastLSN = lag.ReplayLSN
		return s.failConnector(ctx, c, err)
	}
	return s.finishConnector(ctx, projectID, c, lag)
}

func (s *Service) connectLogical(ctx context.Context, projectID, name, primaryURL string) (meta.Connector, replica.Lag, error) {
	unlock := s.lockBranch("connector:" + name)
	defer unlock()

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

	c, err := s.prepareConnectorRecord(ctx, projectID, name, primaryURL, ModeLogical)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	pub := pubName(c.Name)
	sub := subName(c.Name)

	fmt.Println("=== connect logical ===")
	fmt.Printf("  name=%s port=%d dir=%s pub=%s\n", c.Name, c.Port, c.DataDir, pub)
	fmt.Printf("  primary: %s:%d user=%s sslmode=%s\n", conn.Host, conn.Port, conn.User, conn.SSLMode)

	fmt.Println("→ ensure publication on primary")
	if err := rm.EnsurePublication(ctx, conn, pub, "public"); err != nil {
		return s.failConnector(ctx, c, fmt.Errorf("publication: %w", err))
	}

	h := s.connectorHandle(c)
	_ = s.Compute.Stop(ctx, h)
	if err := os.RemoveAll(c.DataDir); err != nil {
		return s.failConnector(ctx, c, err)
	}
	inst := &postgres.Instance{
		Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port,
		LogFile: s.logPath(replicaComputeName(c.Name)), Bins: s.Bins,
	}
	fmt.Println("→ initdb local replica (subscriber)")
	if err := inst.Init(); err != nil {
		return s.failConnector(ctx, c, err)
	}
	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port,
		LogFile: s.logPath(replicaComputeName(c.Name)),
	}); err != nil {
		return s.failConnector(ctx, c, err)
	}

	if err := rm.DumpSchema(ctx, conn, "127.0.0.1", c.Port, "public"); err != nil {
		return s.failConnector(ctx, c, err)
	}
	if err := rm.CreateSubscription(ctx, conn, "127.0.0.1", c.Port, sub, pub); err != nil {
		return s.failConnector(ctx, c, err)
	}

	st, err := rm.WaitLogicalSync(ctx, "127.0.0.1", c.Port, sub, 10*time.Minute)
	if err != nil {
		c.LastLSN = st.ReceivedLSN
		return s.failConnector(ctx, c, err)
	}

	lag := replica.Lag{
		ReceiveLSN: st.ReceivedLSN,
		ReplayLSN:  st.ReceivedLSN,
	}
	fmt.Printf("✓ logical sync ready  tables=%d/%d lsn=%s\n", st.TableReady, st.TableTotal, st.ReceivedLSN)
	return s.finishConnector(ctx, projectID, c, lag)
}

func (s *Service) prepareConnectorRecord(ctx context.Context, projectID, name, primaryURL, mode string) (meta.Connector, error) {
	dataDir := s.ReplicaDir(name)
	if existing, err := s.Store.GetConnectorByName(ctx, projectID, name); err == nil {
		// Reconnect: reuse id/port, refresh URL/mode/dir
		_ = s.Compute.Stop(ctx, s.connectorHandle(existing))
		existing.PrimaryURL = primaryURL
		existing.Mode = mode
		existing.Status = meta.ConnectorBootstrapping
		existing.ErrorMessage = ""
		existing.DataDir = dataDir
		if existing.Port == 0 {
			port, err := s.Store.AllocPort(ctx)
			if err != nil {
				return meta.Connector{}, err
			}
			existing.Port = port
		}
		if err := s.Store.UpdateConnector(ctx, existing); err != nil {
			return meta.Connector{}, err
		}
		return existing, nil
	}

	port, err := s.Store.AllocPort(ctx)
	if err != nil {
		return meta.Connector{}, err
	}
	c := meta.Connector{
		ID:         uuid.NewString(),
		ProjectID:  projectID,
		Name:       name,
		PrimaryURL: primaryURL,
		Mode:       mode,
		Status:     meta.ConnectorBootstrapping,
		DataDir:    dataDir,
		Port:       port,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := s.Store.PutConnector(ctx, c); err != nil {
		return meta.Connector{}, err
	}
	return c, nil
}

func (s *Service) connectorHandle(c meta.Connector) compute.Handle {
	return compute.Handle{
		Provider: s.Compute.Name(),
		Name:     replicaComputeName(c.Name),
		Port:     c.Port,
		DataDir:  c.DataDir,
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

	// Track replica as a branch-like record for list/reconcile (role=replica).
	replicaName := "replica-" + c.Name
	_ = s.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "replica-" + c.ID, ProjectID: projectID, Name: replicaName, Role: "replica",
		Status: meta.StatusActive, Port: c.Port, DataDir: c.DataDir,
		Compute: s.Compute.Name(),
		ConnString: postgres.FormatConnString(c.Port, "postgres"),
		SourceConnector: c.Name, SourceConnectorID: c.ID,
	})
	fmt.Printf("✓ connector %q mode=%s status=replicating port=%d lsn=%s\n", c.Name, c.Mode, c.Port, c.LastLSN)
	return c, lag, nil
}

// ReplicationStatus returns lag for a named connector (or the only one if name empty).
func (s *Service) ReplicationStatus(ctx context.Context, projectID, name string) (meta.Connector, replica.Lag, error) {
	c, err := s.resolveConnector(ctx, projectID, name)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}
	rm := &replica.Manager{Bins: s.Bins}

	if c.Mode == ModeLogical {
		st, err := rm.LogicalSyncStatus(ctx, "127.0.0.1", c.Port, subName(c.Name))
		if err != nil {
			return c, replica.Lag{}, err
		}
		lag := replica.Lag{ReceiveLSN: st.ReceivedLSN, ReplayLSN: st.ReceivedLSN}
		c.LastLSN = st.ReceivedLSN
		c.LastLagBytes = 0
		if st.TableTotal > 0 && st.TableReady >= st.TableTotal {
			c.Status = meta.ConnectorReplicating
		}
		_ = s.Store.UpdateConnector(ctx, c)
		return c, lag, nil
	}

	lag, err := rm.Status(ctx, "127.0.0.1", c.Port)
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

func (s *Service) resolveConnector(ctx context.Context, projectID, name string) (meta.Connector, error) {
	if name != "" {
		return s.Store.GetConnectorByName(ctx, projectID, name)
	}
	list, err := s.Store.ListConnectorsByProject(ctx, projectID)
	if err != nil {
		return meta.Connector{}, err
	}
	if len(list) == 0 {
		return meta.Connector{}, fmt.Errorf("no connector — run: sprout connect --name <n> <url>")
	}
	if len(list) > 1 {
		names := make([]string, 0, len(list))
		for _, c := range list {
			names = append(names, c.Name)
		}
		return meta.Connector{}, fmt.Errorf("multiple connectors (%s) — pass --from / name", strings.Join(names, ", "))
	}
	return list[0], nil
}

func (s *Service) maxLagBytes() int64 {
	if s.MaxLagBytes > 0 {
		return s.MaxLagBytes
	}
	return 16 * 1024 * 1024
}

func replicaComputeName(connectorName string) string {
	return "replica-" + connectorName
}

func pubName(connectorName string) string {
	return "sprout_pub_" + strings.ReplaceAll(connectorName, "-", "_")
}

func subName(connectorName string) string {
	return "sprout_sub_" + strings.ReplaceAll(connectorName, "-", "_")
}
