package branch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/mongo"
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
	Engine string
	Mode   string
	Wipe   bool     // default true — destroy local replica and rebootstrap
	DryRun bool     // estimate only (logical)
	Tables []string // optional table/collection allowlist (logical)
}

// ConnectResult is returned for dry-run or full connect.
type ConnectResult struct {
	Connector *meta.Connector `json:"connector,omitempty"`
	Lag       *replica.Lag    `json:"lag,omitempty"`
	Estimate  map[string]any  `json:"estimate,omitempty"`
	DryRun    bool            `json:"dry_run,omitempty"`
}

// Connect bootstraps a named local replica from an upstream database.
func (s *Service) Connect(ctx context.Context, projectID string, opts ConnectOpts) (ConnectResult, error) {
	if opts.Engine == "" {
		opts.Engine = engine.InferFromURL(opts.URL)
	}
	opts.Engine = engine.Normalize(opts.Engine)
	if !engine.IsKnown(opts.Engine) {
		return ConnectResult{}, fmt.Errorf("invalid_engine: use postgres or mongodb")
	}
	if opts.Mode == "" {
		if engine.IsMongo(opts.Engine) {
			opts.Mode = ModeLogical
		} else {
			opts.Mode = ModePhysical
		}
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
	if engine.IsMongo(opts.Engine) {
		if opts.Mode == ModePhysical {
			return ConnectResult{}, fmt.Errorf("invalid_mode: mongodb only supports mode=logical (mongodump snapshot) in this version")
		}
		return s.connectMongoLogical(ctx, projectID, opts)
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
	unlock := s.lockBranch(s.connectorLockKey(name, auth.OwnerFrom(ctx)))
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

	c, err := s.prepareConnectorRecord(ctx, projectID, name, primaryURL, ModePhysical, engine.Postgres)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}

	fmt.Println("=== connect physical ===")
	fmt.Printf("  name=%s port=%d dir=%s\n", c.Name, c.Port, c.DataDir)
	fmt.Printf("  primary: %s:%d user=%s\n", conn.Host, conn.Port, conn.User)

	if wipe {
		if err := s.stopConnectorForWipe(ctx, c); err != nil {
			return s.failConnector(ctx, c, fmt.Errorf("storage_failed: stop replica: %w", err))
		}
		if err := s.Storage.Destroy(c.DataDir); err != nil {
			return s.failConnector(ctx, c, fmt.Errorf("storage_failed: destroy replica volume: %w", err))
		}
		if err := s.Storage.EnsureVolume(c.DataDir); err != nil {
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
	} else if err := s.Storage.EnsureVolume(c.DataDir); err != nil {
		return s.failConnector(ctx, c, err)
	}

	if _, err := s.Compute.Start(ctx, compute.Spec{
		Name: postgres.ReplicaComputeName(c.Name, c.CreatedBy), DataDir: c.DataDir, Port: c.Port, LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)),
	}); err != nil {
		return s.failConnector(ctx, c, err)
	}
	inst := &postgres.Instance{
		Name: c.Name, Owner: c.CreatedBy, DataDir: c.DataDir, Port: c.Port,
		LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)), Bins: s.Bins, Password: c.Password,
	}
	_ = inst.EnsureAppRoles()

	lag, err := rm.Status(ctx, "127.0.0.1", c.Port)
	if err != nil {
		return s.failConnector(ctx, c, err)
	}
	return s.finishConnector(ctx, projectID, c, lag)
}

func (s *Service) connectLogical(ctx context.Context, projectID string, opts ConnectOpts) (ConnectResult, error) {
	unlock := s.lockBranch(s.connectorLockKey(opts.Name, auth.OwnerFrom(ctx)))
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

	c, err := s.prepareConnectorRecord(ctx, projectID, opts.Name, opts.URL, ModeLogical, engine.Postgres)
	if err != nil {
		return ConnectResult{}, err
	}

	pub := pubName(c)
	sub := subName(c)

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
		if seed, ok := s.findSeedReplica(ctx, projectID, opts.URL, c.ID); ok {
			fmt.Println("→ cloning existing local replica of this primary (no extra prod slot)")
			cloned, lag, err := s.cloneConnectorFromLocal(ctx, projectID, c, seed, conn)
			if err != nil {
				return ConnectResult{}, err
			}
			fmt.Println("  ", postgres.FormatConnString(cloned.Port, "postgres", cloned.Password, cloned.Name, "", cloned.CreatedBy))
			fmt.Println("  ", postgres.PsqlOneLiner(cloned.Port, cloned.Password, cloned.Name, "", cloned.CreatedBy))
			return ConnectResult{Connector: &cloned, Lag: &lag}, nil
		}

		fmt.Println("→ ensure publication on primary (hits prod)")
		if err := rm.EnsurePublication(ctx, conn, pub, "public", opts.Tables); err != nil {
			_, _, e := s.failConnector(ctx, c, fmt.Errorf("publication: %w", err))
			return ConnectResult{}, e
		}

		if err := s.stopConnectorForWipe(ctx, c); err != nil {
			_, _, e := s.failConnector(ctx, c, fmt.Errorf("storage_failed: stop replica: %w", err))
			return ConnectResult{}, e
		}
		if err := s.Storage.Destroy(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, fmt.Errorf("storage_failed: destroy replica volume: %w", err))
			return ConnectResult{}, e
		}
		if err := s.Storage.EnsureVolume(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		inst := &postgres.Instance{
			Name: c.Name, Owner: c.CreatedBy, DataDir: c.DataDir, Port: c.Port,
			LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)), Bins: s.Bins, Password: c.Password,
		}
		fmt.Println("→ initdb local replica (subscriber)")
		if err := inst.Init(); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		if _, err := s.Compute.Start(ctx, compute.Spec{
			Name: postgres.ReplicaComputeName(c.Name, c.CreatedBy), DataDir: c.DataDir, Port: c.Port,
			LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)),
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
		if err := s.Storage.EnsureVolume(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		running, _ := s.Compute.IsRunning(ctx, h)
		if !running {
			if _, err := s.Compute.Start(ctx, compute.Spec{
				Name: postgres.ReplicaComputeName(c.Name, c.CreatedBy), DataDir: c.DataDir, Port: c.Port,
				LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)),
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
	fmt.Println("  ", postgres.FormatConnString(c.Port, "postgres", c.Password, c.Name, "", c.CreatedBy))
	fmt.Println("  ", postgres.PsqlOneLiner(c.Port, c.Password, c.Name, "", c.CreatedBy))
	return ConnectResult{Connector: &c, Lag: &lag}, nil
}

func (s *Service) connectMongoLogical(ctx context.Context, projectID string, opts ConnectOpts) (ConnectResult, error) {
	unlock := s.lockBranch(s.connectorLockKey(opts.Name, auth.OwnerFrom(ctx)))
	defer unlock()

	conn, err := mongo.ParseURL(opts.URL)
	if err != nil {
		return ConnectResult{}, err
	}
	bins, err := mongo.LookBinaries()
	if err != nil {
		return ConnectResult{}, err
	}
	if err := bins.Ping(ctx, conn); err != nil {
		return ConnectResult{}, err
	}
	if opts.DryRun {
		est, err := bins.Estimate(ctx, conn, opts.Tables)
		if err != nil {
			return ConnectResult{}, err
		}
		return ConnectResult{DryRun: true, Estimate: est}, nil
	}

	c, err := s.prepareConnectorRecord(ctx, projectID, opts.Name, opts.URL, ModeLogical, engine.Mongo)
	if err != nil {
		return ConnectResult{}, err
	}

	fmt.Println("=== connect mongodb (dump snapshot, no oplog) ===")
	fmt.Printf("  name=%s port=%d dir=%s wipe=%v\n", c.Name, c.Port, c.DataDir, opts.Wipe)
	fmt.Printf("  primary: %s:%d user=%s db=%s\n", conn.Host, conn.Port, conn.User, conn.Database)
	if len(opts.Tables) > 0 {
		fmt.Printf("  collections: %s\n", strings.Join(opts.Tables, ", "))
	}

	h := s.connectorHandle(c)
	needBootstrap := opts.Wipe
	if !needBootstrap {
		if !mongo.HasDataDir(c.DataDir) {
			needBootstrap = true
		}
	}

	if needBootstrap {
		if err := s.stopConnectorForWipe(ctx, c); err != nil {
			_, _, e := s.failConnector(ctx, c, fmt.Errorf("storage_failed: stop replica: %w", err))
			return ConnectResult{}, e
		}
		if err := s.Storage.Destroy(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, fmt.Errorf("storage_failed: destroy replica volume: %w", err))
			return ConnectResult{}, e
		}
		if err := s.Storage.EnsureVolume(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		inst := &mongo.Instance{
			Name: c.Name, Owner: c.CreatedBy, DataDir: c.DataDir, Port: c.Port,
			LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)), Bins: bins, Password: c.Password,
		}
		fmt.Println("→ init local mongod (standalone)")
		if err := inst.Init(); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		if _, err := s.Compute.Start(ctx, compute.Spec{
			Name: postgres.ReplicaComputeName(c.Name, c.CreatedBy), DataDir: c.DataDir, Port: c.Port,
			LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)), Engine: engine.Mongo,
		}); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		fmt.Println("→ mongodump → mongorestore")
		if err := bins.DumpImport(ctx, conn, inst, opts.Tables); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		if err := inst.EnsureAppRoles(); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
	} else {
		fmt.Println("→ resume existing replica (--no-wipe)")
		if err := s.Storage.EnsureVolume(c.DataDir); err != nil {
			_, _, e := s.failConnector(ctx, c, err)
			return ConnectResult{}, e
		}
		running, _ := s.Compute.IsRunning(ctx, h)
		if !running {
			if _, err := s.Compute.Start(ctx, compute.Spec{
				Name: postgres.ReplicaComputeName(c.Name, c.CreatedBy), DataDir: c.DataDir, Port: c.Port,
				LogFile: s.logPath(postgres.ReplicaComputeName(c.Name, c.CreatedBy)), Engine: engine.Mongo,
			}); err != nil {
				_, _, e := s.failConnector(ctx, c, err)
				return ConnectResult{}, e
			}
		}
	}

	c, lag, err := s.finishConnector(ctx, projectID, c, replica.Lag{})
	if err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{Connector: &c, Lag: &lag}, nil
}

func (s *Service) findSeedReplica(ctx context.Context, projectID, primaryURL, excludeID string) (meta.Connector, bool) {
	want := replica.PrimaryKeyFromURL(primaryURL)
	if want == "" {
		return meta.Connector{}, false
	}
	list, err := s.Store.ListConnectorsByProject(ctx, projectID)
	if err != nil {
		return meta.Connector{}, false
	}
	var best meta.Connector
	bestScore := 0
	for _, cand := range list {
		if cand.ID == excludeID || cand.DataDir == "" {
			continue
		}
		switch cand.Status {
		case meta.ConnectorError, meta.ConnectorBootstrapping:
			continue
		}
		if replica.PrimaryKeyFromURL(cand.PrimaryURL) != want {
			continue
		}
		if _, err := os.Stat(filepath.Join(cand.DataDir, "PG_VERSION")); err != nil {
			continue
		}
		score := 1
		if cand.Status == meta.ConnectorReplicating {
			score += 2
		}
		if cand.Status == meta.ConnectorIdle {
			score++
		}
		running, _ := s.Compute.IsRunning(ctx, s.connectorHandle(cand))
		if running {
			score++
		}
		if score > bestScore {
			bestScore = score
			best = cand
		}
	}
	return best, bestScore > 0
}

func (s *Service) cloneConnectorFromLocal(ctx context.Context, projectID string, dest, seed meta.Connector, primary replica.Conn) (meta.Connector, replica.Lag, error) {
	unlock := s.lockBranch("snap:" + seed.ID)
	defer unlock()

	h := s.connectorHandle(dest)
	_ = s.Compute.Stop(ctx, h)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		running, _ := s.Compute.IsRunning(ctx, h)
		if !running {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = s.Storage.Destroy(dest.DataDir)

	srcHandle := s.connectorHandle(seed)
	srcInst := &postgres.Instance{
		Name: seed.Name, Owner: seed.CreatedBy, DataDir: seed.DataDir, Port: seed.Port,
		LogFile: s.logPath(srcHandle.Name), Bins: s.Bins, Password: seed.Password,
	}
	if running, _ := s.Compute.IsRunning(ctx, srcHandle); running {
		if err := srcInst.Checkpoint(); err != nil {
			return s.failConnector(ctx, dest, fmt.Errorf("storage_failed: checkpoint seed: %w", err))
		}
	} else if _, err := os.Stat(filepath.Join(seed.DataDir, "PG_VERSION")); err != nil {
		return s.failConnector(ctx, dest, fmt.Errorf("source_not_ready: no local replica to clone"))
	}

	snapName := postgres.ReplicaComputeName(dest.Name, dest.CreatedBy) + "-seed"
	snapRef, err := s.Storage.Snapshot(seed.DataDir, snapName)
	if err != nil {
		return s.failConnector(ctx, dest, fmt.Errorf("storage_failed: %w", err))
	}
	if err := s.Storage.Clone(snapRef, dest.DataDir); err != nil {
		_ = s.Storage.Destroy(snapRef)
		return s.failConnector(ctx, dest, fmt.Errorf("storage_failed: %w", err))
	}
	_ = s.Storage.Destroy(snapRef)

	inst := &postgres.Instance{
		Name: dest.Name, Owner: dest.CreatedBy, DataDir: dest.DataDir, Port: dest.Port,
		LogFile: s.logPath(h.Name), Bins: s.Bins, Password: dest.Password,
	}
	if _, err := s.startDetachedClone(ctx, inst, h.Name); err != nil {
		return s.failConnector(ctx, dest, err)
	}
	rm := &replica.Manager{Bins: s.Bins}

	_ = rm.DropReplicationSlot(ctx, primary, subName(dest))
	_ = rm.DropPublication(ctx, primary, pubName(dest))

	lag := replica.Lag{ReceiveLSN: seed.LastLSN, ReplayLSN: seed.LastLSN}
	fmt.Printf("✓ cloned local replica of this primary (independent copy, not a new Supabase slot)\n")
	return s.finishConnector(ctx, projectID, dest, lag)
}

func (s *Service) prepareConnectorRecord(ctx context.Context, projectID, name, primaryURL, mode, eng string) (meta.Connector, error) {
	owner := auth.OwnerFrom(ctx)
	dataDir := s.ReplicaDir(name, owner)
	eng = engine.Normalize(eng)
	if existing, err := s.Store.GetConnectorByName(ctx, projectID, name, owner); err == nil {
		existing.PrimaryURL = primaryURL
		existing.Engine = eng
		existing.Mode = mode
		existing.Status = meta.ConnectorBootstrapping
		existing.ErrorMessage = ""
		existing.DataDir = dataDir
		existing.CreatedBy = owner
		if existing.Password == "" {
			existing.Password = postgres.GeneratePassword()
		}
		if err := s.Store.UpdateConnector(ctx, existing); err != nil {
			return meta.Connector{}, err
		}
		if err := s.ensureConnectorPortFree(ctx, &existing); err != nil {
			return meta.Connector{}, err
		}
		return existing, nil
	}

	port, err := s.allocFreePort(ctx)
	if err != nil {
		return meta.Connector{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dataDir), 0o755); err != nil {
		return meta.Connector{}, err
	}
	if err := s.Storage.EnsureVolume(dataDir); err != nil {
		return meta.Connector{}, fmt.Errorf("storage_failed: ensure replica volume: %w", err)
	}
	c := meta.Connector{
		ID: uuid.NewString(), ProjectID: projectID, Name: name, PrimaryURL: primaryURL, Engine: eng, Mode: mode,
		Status: meta.ConnectorBootstrapping, DataDir: dataDir, Port: port,
		Password:  postgres.GeneratePassword(),
		CreatedBy: owner,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Store.PutConnector(ctx, c); err != nil {
		return meta.Connector{}, err
	}
	return c, nil
}

func (s *Service) connectorHandle(c meta.Connector) compute.Handle {
	return compute.Handle{
		Provider: s.Compute.Name(), Name: postgres.ReplicaComputeName(c.Name, c.CreatedBy),
		Port: c.Port, DataDir: c.DataDir, Engine: c.Engine, Password: c.Password,
	}
}

// stopConnectorForWipe stops local compute and waits until the listen port is
// free so ZFS can unmount/destroy the replica dataset.
func (s *Service) stopConnectorForWipe(ctx context.Context, c meta.Connector) error {
	h := s.connectorHandle(c)
	if engine.IsMongo(c.Engine) {
		inst := &mongo.Instance{
			Name: h.Name, DataDir: c.DataDir, Port: c.Port,
			Bins: mongo.FindOnPath(), Password: c.Password,
		}
		_ = inst.Stop()
		if err := inst.WaitPortFree(20 * time.Second); err != nil {
			return err
		}
		return nil
	}
	_ = s.Compute.Stop(ctx, h)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		running, _ := s.Compute.IsRunning(ctx, h)
		if !running && !postgres.PortListening(c.Port) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if postgres.PortListening(c.Port) {
		return fmt.Errorf("postgres on port %d still running after stop", c.Port)
	}
	return nil
}

const allocPortAttempts = 64

// allocFreePort returns the next control-plane port that is not already listening.
func (s *Service) allocFreePort(ctx context.Context) (int, error) {
	var last int
	for i := 0; i < allocPortAttempts; i++ {
		p, err := s.Store.AllocPort(ctx)
		if err != nil {
			return 0, err
		}
		last = p
		if !postgres.PortListening(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free listen port after %d allocations (last %d)", allocPortAttempts, last)
}

// ensureConnectorPortFree keeps c.Port if it is free (or we can stop leftover compute
// for this connector). Otherwise it assigns a new unused port.
func (s *Service) ensureConnectorPortFree(ctx context.Context, c *meta.Connector) error {
	if c.Port != 0 && !postgres.PortListening(c.Port) {
		return nil
	}
	if c.Port != 0 && postgres.PortListening(c.Port) && s.Compute != nil {
		h := s.connectorHandle(*c)
		_ = s.Compute.Stop(ctx, h)
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			running, _ := s.Compute.IsRunning(ctx, h)
			if !running && !postgres.PortListening(c.Port) {
				return nil
			}
			if !running {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !postgres.PortListening(c.Port) {
			return nil
		}
	}
	p, err := s.allocFreePort(ctx)
	if err != nil {
		return err
	}
	c.Port = p
	return s.Store.UpdateConnector(ctx, *c)
}

func (s *Service) failConnector(ctx context.Context, c meta.Connector, err error) (meta.Connector, replica.Lag, error) {
	if s.Compute != nil {
		h := s.connectorHandle(c)
		_ = s.Compute.Stop(ctx, h)
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			running, _ := s.Compute.IsRunning(ctx, h)
			if !running {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
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
		ConnString:      advertiseConnURL(c.Engine, c.Port, c.Password, c.Name, "", c.CreatedBy),
		SourceConnector: c.Name, SourceConnectorID: c.ID,
		Password: c.Password, CreatedBy: c.CreatedBy,
	})
	fmt.Printf("✓ connector %q engine=%s mode=%s status=replicating port=%d lsn=%s\n", c.Name, engine.Normalize(c.Engine), c.Mode, c.Port, c.LastLSN)
	cs, one := advertiseConnector(c)
	fmt.Println("  ", cs)
	fmt.Println("  ", one)
	return c, lag, nil
}

func advertiseConnector(c meta.Connector) (connURL, oneLiner string) {
	if engine.IsMongo(c.Engine) {
		return mongo.FormatConnString(c.Port, "", c.Password, c.Name, "", c.CreatedBy),
			mongo.MongoshOneLiner(c.Port, c.Password, c.Name, "", c.CreatedBy)
	}
	return postgres.FormatConnString(c.Port, "postgres", c.Password, c.Name, "", c.CreatedBy),
		postgres.PsqlOneLiner(c.Port, c.Password, c.Name, "", c.CreatedBy)
}

func advertiseBranch(rec meta.BranchRecord, eng string) (connURL, oneLiner string) {
	if engine.IsMongo(eng) {
		return mongo.FormatConnString(rec.Port, "", rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy),
			mongo.MongoshOneLiner(rec.Port, rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy)
	}
	return postgres.FormatConnString(rec.Port, "postgres", rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy),
		postgres.PsqlOneLiner(rec.Port, rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy)
}

func advertiseConnURL(eng string, port int, password, name, from, owner string) string {
	cs, _ := advertiseBranch(meta.BranchRecord{Port: port, Password: password, Name: name, SourceConnector: from, CreatedBy: owner}, eng)
	return cs
}

func (s *Service) ReplicationStatus(ctx context.Context, projectID, name string) (meta.Connector, replica.Lag, error) {
	c, err := s.resolveConnector(ctx, projectID, name)
	if err != nil {
		return meta.Connector{}, replica.Lag{}, err
	}
	if engine.IsMongo(c.Engine) {
		return c, replica.Lag{}, nil
	}
	rm := &replica.Manager{Bins: s.Bins}
	if c.Mode == ModeLogical {
		st, err := rm.LogicalSyncStatus(ctx, "127.0.0.1", c.Port, subName(c))
		if err != nil {
			return c, replica.Lag{}, err
		}
		lag := replica.Lag{ReceiveLSN: st.ReceivedLSN, ReplayLSN: st.ReceivedLSN}
		if st.ReceivedLSN != "" {
			c.LastLSN = st.ReceivedLSN
		}
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
// Child branches block the delete unless force is set (then they are destroyed first).
func (s *Service) DeleteConnector(ctx context.Context, projectID, name string, force bool) error {
	unlock := s.lockBranch(s.connectorLockKey(name, auth.OwnerFrom(ctx)))
	defer unlock()

	c, err := s.lookupConnector(ctx, projectID, name)
	if err != nil {
		return err
	}

	children, err := s.branchesFromConnector(ctx, projectID, c)
	if err != nil {
		return err
	}
	if len(children) > 0 && !force {
		names := make([]string, 0, len(children))
		for _, b := range children {
			names = append(names, b.Name)
		}
		return fmt.Errorf("connector_has_branches: %q has %d branch(es) (%s) — delete them first or pass --force",
			name, len(names), strings.Join(names, ", "))
	}
	for _, b := range children {
		if err := s.Delete(ctx, projectID, b.Name, b.SourceConnector); err != nil {
			return fmt.Errorf("delete branch %s: %w", b.Name, err)
		}
	}

	rm := &replica.Manager{Bins: s.Bins}
	h := s.connectorHandle(c)

	if c.Mode == ModeLogical && !engine.IsMongo(c.Engine) {
		running, _ := s.Compute.IsRunning(ctx, h)
		if running {
			_ = rm.DropSubscriptionLocal(ctx, "127.0.0.1", c.Port, subName(c))
		}
		if conn, err := replica.ParseURL(c.PrimaryURL); err == nil {
			if err := replica.EnsurePrimaryReachable(conn.Host, conn.Port, 5*time.Second); err == nil {
				if err := rm.Ping(ctx, conn); err == nil {
					_ = rm.DropPublication(ctx, conn, pubName(c))
					_ = rm.DropReplicationSlot(ctx, conn, subName(c))
					fmt.Printf("→ dropped publication %s + slot on primary\n", pubName(c))
				} else {
					fmt.Printf("! could not drop publication on primary: %v\n", err)
				}
			} else {
				fmt.Printf("! primary unreachable — skipped DROP PUBLICATION: %v\n", err)
			}
		}
	}

	_ = s.Compute.Stop(ctx, h)
	_ = s.Storage.Destroy(c.DataDir)

	// Remove synthetic replica-* branch row if present.
	if br, err := lookupReplicaRow(ctx, s.Store, projectID, c); err == nil {
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
		return s.lookupConnector(ctx, projectID, name)
	}
	list, err := s.visibleConnectors(ctx, projectID)
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

func lookupReplicaRow(ctx context.Context, store meta.Store, projectID string, c meta.Connector) (meta.BranchRecord, error) {
	if br, err := store.GetBranchByID(ctx, "replica-"+c.ID); err == nil {
		return br, nil
	}
	return store.FindBranch(ctx, projectID, "replica-"+c.Name, "", c.CreatedBy)
}

func replicaSlotSuffix(c meta.Connector) string {
	n := c.Name
	if c.CreatedBy != "" {
		n = c.Name + "_" + c.CreatedBy
	}
	return strings.ReplaceAll(n, "-", "_")
}

func pubName(c meta.Connector) string {
	return "sprout_pub_" + replicaSlotSuffix(c)
}
func subName(c meta.Connector) string {
	return "sprout_sub_" + replicaSlotSuffix(c)
}
