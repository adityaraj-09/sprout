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

// ConnectOpts controls connector bootstrap.
type ConnectOpts struct {
	Name   string
	URL    string
	Mode   string
	Wipe   bool     // default true — destroy local replica and rebootstrap
	DryRun bool     // estimate only (logical)
	Tables []string // optional table allowlist (logical)
}

// ConnectResult is returned for dry-run or full connect.
type ConnectResult struct {
	Connector *meta.Connector   `json:"connector,omitempty"`
	Lag       *replica.Lag      `json:"lag,omitempty"`
	Estimate  map[string]any    `json:"estimate,omitempty"`
	DryRun    bool              `json:"dry_run,omitempty"`
}

// Connect bootstraps a named local replica from an upstream Postgres.
func (s *Service) Connect(ctx context.Context, projectID string, opts ConnectOpts) (ConnectResult, error) {
	if opts.Mode == "" {
		opts.Mode = ModePhysical
	}
	if opts.Mode != ModePhysical && opts.Mode != ModeLogical {
		return ConnectResult{}, fmt.Errorf("invalid_mode: use physical or logical")
	}
	if opts.Name == "" {
		opts.Name = "primary"
	}
	if !nameRe.MatchString(opts.Name) {
		return ConnectResult{}, fmt.Errorf("invalid_name: connector name must match [a-z][a-z0-9-]*")
	}
	if opts.Mode == ModeLogical {
		return s.connectLogical(ctx, projectID, opts)
	}
	if opts.DryRun {
		return ConnectResult{}, fmt.Errorf("dry_run only supported for mode=logical")
	}
	c, lag, err := s.connectPhysical(ctx, projectID, opts.Name, opts.URL, opts.Wipe)
	if err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{Connector: &c, Lag: &lag}, nil
}

func (s *Service) connectPhysical(ctx context.Context, projectID, name, primaryURL string, wipe bool) (meta.Connector, replica.Lag, error) {
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
	if err := rm.CheckToolsForPrimary(ctx, conn); err != nil {
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
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		running, _ := s.Compute.IsRunning(ctx, h)
		if !running {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if wipe {
		if err := os.RemoveAll(c.DataDir); err != nil {
			return s.failConnector(ctx, c, err)
		}
		if err := rm.BaseBackup(ctx, conn, c.DataDir); err != nil {
			return s.failConnector(ctx, c, err)
		}
		if err := rm.PrepareStandbyDataDir(c.DataDir, c.Port); err != nil {
			return s.failConnector(ctx, c, err)
		}
	} else if _, err := os.Stat(filepath.Join(c.DataDir, "PG_VERSION")); err != nil {
		return s.failConnector(ctx, c, fmt.Errorf("no local replica to resume — reconnect with wipe=true"))
	}

	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port, LogFile: s.logPath(replicaComputeName(c.Name)),
	}); err != nil {
		return s.failConnector(ctx, c, err)
	}
	inst := &postgres.Instance{
		Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port,
		LogFile: s.logPath(replicaComputeName(c.Name)), Bins: s.Bins,
	}
	_ = inst.EnsureAppRoles()

	lag, err := rm.Status(ctx, "127.0.0.1", c.Port)
	if err != nil {
		return s.failConnector(ctx, c, err)
	}
	return s.finishConnector(ctx, projectID, c, lag)
}

func (s *Service) connectLogical(ctx context.Context, projectID string, opts ConnectOpts) (ConnectResult, error) {
	unlock := s.lockBranch("connector:" + opts.Name)
	defer unlock()

	conn, err := replica.ParseURL(opts.URL)
	if err != nil {
		return ConnectResult{}, err
	}
	if err := replica.EnsurePrimaryReachable(conn.Host, conn.Port, 5*time.Second); err != nil {
		return ConnectResult{}, err
	}
	rm := &replica.Manager{Bins: s.Bins}
	if err := rm.Ping(ctx, conn); err != nil {
		return ConnectResult{}, err
	}
	if err := rm.CheckToolsForPrimary(ctx, conn); err != nil {
		return ConnectResult{}, err
	}

	if opts.DryRun {
		est, err := rm.EstimateLogicalBootstrap(ctx, conn, "public", opts.Tables)
		if err != nil {
			return ConnectResult{}, err
		}
		return ConnectResult{DryRun: true, Estimate: est}, nil
	}

	c, err := s.prepareConnectorRecord(ctx, projectID, opts.Name, opts.URL, ModeLogical)
	if err != nil {
		return ConnectResult{}, err
	}

	pub := pubName(c.Name)
	sub := subName(c.Name)

	fmt.Println("=== connect logical ===")
	fmt.Printf("  name=%s port=%d dir=%s pub=%s wipe=%v\n", c.Name, c.Port, c.DataDir, pub, opts.Wipe)
	fmt.Printf("  primary: %s:%d user=%s sslmode=%s\n", conn.Host, conn.Port, conn.User, conn.SSLMode)
	if len(opts.Tables) > 0 {
		fmt.Printf("  tables: %s\n", strings.Join(opts.Tables, ", "))
	}

	h := s.connectorHandle(c)
	needBootstrap := opts.Wipe
	if !needBootstrap {
		if _, err := os.Stat(filepath.Join(c.DataDir, "PG_VERSION")); err != nil {
			needBootstrap = true
		}
	}

	if needBootstrap {
		fmt.Println("→ ensure publication on primary (hits prod)")
		if err := rm.EnsurePublication(ctx, conn, pub, "public", opts.Tables); err != nil {
			_, _, e := s.failConnector(ctx, c, fmt.Errorf("publication: %w", err))
			return ConnectResult{}, e
		}

		_ = s.Compute.Stop(ctx, h)
		// Wait for postmaster to release the port before wiping PGDATA.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			running, _ := s.Compute.IsRunning(ctx, h)
			if !running {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err := os.RemoveAll(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		inst := &postgres.Instance{
			Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port,
			LogFile: s.logPath(replicaComputeName(c.Name)), Bins: s.Bins,
		}
		fmt.Println("→ initdb local replica (subscriber)")
		if err := inst.Init(); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		if _, err := s.Compute.Start(ctx, compute.Spec{
			Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port,
			LogFile: s.logPath(replicaComputeName(c.Name)),
		}); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		_ = inst.EnsureAppRoles()

		if err := rm.DumpSchema(ctx, conn, "127.0.0.1", c.Port, "public", opts.Tables); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		if err := rm.CreateSubscription(ctx, conn, "127.0.0.1", c.Port, sub, pub); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
	} else {
		fmt.Println("→ resume existing replica (--no-wipe)")
		running, _ := s.Compute.IsRunning(ctx, h)
		if !running {
			if _, err := s.Compute.Start(ctx, compute.Spec{
				Name: replicaComputeName(c.Name), DataDir: c.DataDir, Port: c.Port,
				LogFile: s.logPath(replicaComputeName(c.Name)),
			}); err != nil {
				_, _, e := s.failConnector(ctx, c, err)
				return ConnectResult{}, e
			}
		}
	}

	st, err := rm.WaitLogicalSync(ctx, "127.0.0.1", c.Port, sub, 10*time.Minute)
	if err != nil {
		c.LastLSN = st.ReceivedLSN
		_, _, e := s.failConnector(ctx, c, err)
		return ConnectResult{}, e
	}

	lag := replica.Lag{ReceiveLSN: st.ReceivedLSN, ReplayLSN: st.ReceivedLSN}
	fmt.Printf("✓ logical sync ready  tables=%d/%d lsn=%s\n", st.TableReady, st.TableTotal, st.ReceivedLSN)
	c, lag, err = s.finishConnector(ctx, projectID, c, lag)
	if err != nil {
		return ConnectResult{}, err
	}
	fmt.Println("  ", postgres.FormatConnString(c.Port, "postgres"))
	fmt.Println("  ", postgres.PsqlOneLiner(c.Port))
	return ConnectResult{Connector: &c, Lag: &lag}, nil
}

func (s *Service) prepareConnectorRecord(ctx context.Context, projectID, name, primaryURL, mode string) (meta.Connector, error) {
	dataDir := s.ReplicaDir(name)
	if existing, err := s.Store.GetConnectorByName(ctx, projectID, name); err == nil {
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
		ID: uuid.NewString(), ProjectID: projectID, Name: name, PrimaryURL: primaryURL, Mode: mode,
		Status: meta.ConnectorBootstrapping, DataDir: dataDir, Port: port,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Store.PutConnector(ctx, c); err != nil {
		return meta.Connector{}, err
	}
	return c, nil
}

func (s *Service) connectorHandle(c meta.Connector) compute.Handle {
	return compute.Handle{Provider: s.Compute.Name(), Name: replicaComputeName(c.Name), Port: c.Port, DataDir: c.DataDir}
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
		ID: "replica-" + c.ID, ProjectID: projectID, Name: "replica-" + c.Name, Role: "replica",
		Status: meta.StatusActive, Port: c.Port, DataDir: c.DataDir, Compute: s.Compute.Name(),
		ConnString: postgres.FormatConnString(c.Port, "postgres"),
		SourceConnector: c.Name, SourceConnectorID: c.ID,
	})
	fmt.Printf("✓ connector %q mode=%s status=replicating port=%d lsn=%s\n", c.Name, c.Mode, c.Port, c.LastLSN)
	fmt.Println("  ", postgres.FormatConnString(c.Port, "postgres"))
	fmt.Println("  ", postgres.PsqlOneLiner(c.Port))
	return c, lag, nil
}

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

// DeleteConnector stops the local replica, drops logical pub/sub when possible, and removes metadata.
func (s *Service) DeleteConnector(ctx context.Context, projectID, name string) error {
	unlock := s.lockBranch("connector:" + name)
	defer unlock()

	c, err := s.Store.GetConnectorByName(ctx, projectID, name)
	if err != nil {
		return fmt.Errorf("connector_not_found: %s", name)
	}
	rm := &replica.Manager{Bins: s.Bins}
	h := s.connectorHandle(c)

	if c.Mode == ModeLogical {
		running, _ := s.Compute.IsRunning(ctx, h)
		if running {
			_ = rm.DropSubscriptionLocal(ctx, "127.0.0.1", c.Port, subName(c.Name))
		}
		if conn, err := replica.ParseURL(c.PrimaryURL); err == nil {
			if err := replica.EnsurePrimaryReachable(conn.Host, conn.Port, 5*time.Second); err == nil {
				if err := rm.Ping(ctx, conn); err == nil {
					_ = rm.DropPublication(ctx, conn, pubName(c.Name))
					_ = rm.DropReplicationSlot(ctx, conn, subName(c.Name))
					fmt.Printf("→ dropped publication %s + slot on primary\n", pubName(c.Name))
				} else {
					fmt.Printf("! could not drop publication on primary: %v\n", err)
				}
			} else {
				fmt.Printf("! primary unreachable — skipped DROP PUBLICATION: %v\n", err)
			}
		}
	}

	_ = s.Compute.Stop(ctx, h)
	_ = os.RemoveAll(c.DataDir)

	// Remove synthetic replica-* branch row if present.
	if br, err := s.Store.GetBranch(ctx, projectID, "replica-"+c.Name); err == nil {
		_ = s.Store.DeleteBranch(ctx, br.ID)
	}
	if err := s.Store.DeleteConnector(ctx, c.ID); err != nil {
		return err
	}
	fmt.Printf("✓ connector %q deleted\n", name)
	return nil
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

func replicaComputeName(connectorName string) string { return "replica-" + connectorName }
func pubName(connectorName string) string {
	return "sprout_pub_" + strings.ReplaceAll(connectorName, "-", "_")
}
func subName(connectorName string) string {
	return "sprout_sub_" + strings.ReplaceAll(connectorName, "-", "_")
}
