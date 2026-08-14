package branch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/replica"
	"github.com/adityaraj/sprout/internal/storage"
	"github.com/google/uuid"
)

const MainPort = 55432 // local demo init only (sprout init)
const DefaultProject = "default"

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Service is the Phase 2/3 control-plane orchestrator.
type Service struct {
	Root        string
	Store       meta.Store
	Storage     storage.Provider
	Compute     compute.Provider
	Bins        postgres.Binaries
	ColdSnap    bool
	MaxLagBytes int64

	opsMu sync.Map // per-branch / per-connector locks
}

func (s *Service) MainDir() string {
	return filepath.Join(s.Root, "main")
}

func (s *Service) ReplicaDir(name, owner string) string {
	return filepath.Join(s.Root, "replicas", postgres.HostLabel(name, "", owner))
}

func (s *Service) BranchDir(name, from, owner string) string {
	return filepath.Join(s.Root, "branches", postgres.HostLabel(name, from, owner))
}

func (s *Service) logPath(name string) string {
	return filepath.Join(s.Root, "logs", name+".log")
}

func (s *Service) instKey(rec meta.BranchRecord) string {
	if k := postgres.HostLabel(rec.Name, rec.SourceConnector, rec.CreatedBy); k != "" {
		return k
	}
	return rec.Name
}

func (s *Service) lookupBranch(ctx context.Context, projectID, name, from string) (meta.BranchRecord, error) {
	rec, err := s.Store.FindBranch(ctx, projectID, name, from, auth.OwnerFrom(ctx))
	if err != nil {
		if strings.HasPrefix(err.Error(), "ambiguous_branch") {
			return meta.BranchRecord{}, err
		}
		return meta.BranchRecord{}, fmt.Errorf("branch_not_found")
	}
	return rec, nil
}

func (s *Service) lookupConnector(ctx context.Context, projectID, name string) (meta.Connector, error) {
	c, err := s.Store.GetConnectorByName(ctx, projectID, name, auth.OwnerFrom(ctx))
	if err != nil {
		if strings.HasPrefix(err.Error(), "ambiguous_connector") {
			return meta.Connector{}, err
		}
		return meta.Connector{}, fmt.Errorf("connector_not_found: %s", name)
	}
	return c, nil
}

func (s *Service) connectorLockKey(name, owner string) string {
	return "connector:" + postgres.HostLabel(name, "", owner)
}

func (s *Service) lockBranch(name string) func() {
	v, _ := s.opsMu.LoadOrStore(name, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

func (s *Service) mainHandle() compute.Handle {
	return compute.Handle{
		Provider: s.Compute.Name(),
		Name:     "main",
		Port:     MainPort,
		DataDir:  s.MainDir(),
	}
}

func (s *Service) mainInst() *postgres.Instance {
	return &postgres.Instance{
		Name: "main", DataDir: s.MainDir(), Port: MainPort,
		LogFile: s.logPath("main"), Bins: s.Bins,
	}
}

// InitMain ensures project + main PGDATA + running compute.
func (s *Service) InitMain(ctx context.Context) (meta.Project, error) {
	proj, err := s.Store.EnsureProject(ctx, DefaultProject)
	if err != nil {
		return meta.Project{}, err
	}

	if err := s.Storage.EnsureVolume(s.MainDir()); err != nil {
		return proj, fmt.Errorf("storage_failed: ensure main volume: %w", err)
	}

	password := postgres.GeneratePassword()
	if existing, err := s.Store.GetBranch(ctx, proj.ID, "main"); err == nil && existing.Password != "" {
		password = existing.Password
	}

	main := s.mainInst()
	main.Password = password
	if _, err := os.Stat(filepath.Join(main.DataDir, "PG_VERSION")); os.IsNotExist(err) {
		fmt.Println("→ initdb into", main.DataDir)
		if err := main.Init(); err != nil {
			return proj, err
		}
	}

	running, _ := s.Compute.IsRunning(ctx, s.mainHandle())
	if !running {
		// Local needs Start via compute; also works if data already prepared.
		fmt.Println("→ starting main on port", MainPort)
		if _, err := s.Compute.Start(ctx, compute.Spec{
			Name: "main", DataDir: main.DataDir, Port: MainPort, LogFile: s.logPath("main"),
		}); err != nil {
			return proj, err
		}
	}

	_ = main.EnsureAppRoles()

	// Seed demo rows (idempotent).
	if err := main.SeedDemo(); err != nil {
		return proj, fmt.Errorf("seed: %w", err)
	}

	// Upsert main row for reconciler awareness.
	_ = s.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "main", ProjectID: proj.ID, Name: "main", Role: "main",
		Status: meta.StatusActive, Port: MainPort, DataDir: s.MainDir(),
		Compute: s.Compute.Name(), ConnString: main.ConnString("postgres"),
		Password: password,
	})

	fmt.Println("✓ main ready:", main.ConnString("postgres"))
	fmt.Println("  ", postgres.PsqlOneLiner(MainPort, password, "main", ""))
	return proj, nil
}

func (s *Service) Create(ctx context.Context, projectID, name, fromConnector string) (meta.BranchRecord, error) {
	if !nameRe.MatchString(name) || name == "main" {
		return meta.BranchRecord{}, fmt.Errorf("invalid_name: use lowercase [a-z0-9-], not 'main'")
	}

	srcDir, srcPort, srcName, srcID, err := s.resolveBranchSource(ctx, projectID, fromConnector)
	if err != nil {
		return meta.BranchRecord{}, err
	}

	owner := auth.OwnerFrom(ctx)

	unlock := s.lockBranch(postgres.HostLabel(name, srcName, owner))
	defer unlock()

	if _, err := s.Store.FindBranch(ctx, projectID, name, srcName, owner); err == nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_exists: %q already exists from %s", name, srcName)
	}

	running, _ := s.Compute.IsRunning(ctx, compute.Handle{Port: srcPort, DataDir: srcDir})
	if !running {
		return meta.BranchRecord{}, fmt.Errorf("source_not_ready: %s is not running — connect/init first", srcName)
	}

	port, err := s.Store.AllocPort(ctx)
	if err != nil {
		return meta.BranchRecord{}, err
	}

	rec := meta.BranchRecord{
		ID: uuid.NewString(), ProjectID: projectID, Name: name, Role: "branch",
		Status: meta.StatusCreating, Port: port, DataDir: s.BranchDir(name, srcName, owner),
		Compute:         s.Compute.Name(),
		SourceConnector: srcName, SourceConnectorID: srcID,
		Password: postgres.GeneratePassword(), CreatedBy: owner,
	}
	if err := s.Store.PutBranch(ctx, rec); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return meta.BranchRecord{}, fmt.Errorf("branch_exists: %q already exists from %s", name, srcName)
		}
		return meta.BranchRecord{}, err
	}

	total := time.Now()
	fmt.Printf("\n=== branch create %q from %q (storage=%s) ===\n", name, srcName, s.Storage.Name())

	if err := s.createPipeline(ctx, &rec, srcDir, srcPort, srcName); err != nil {
		rec.Status = meta.StatusError
		rec.ErrorMessage = err.Error()
		_ = s.Store.UpdateBranch(ctx, rec)
		return meta.BranchRecord{}, err
	}

	rec.Status = meta.StatusActive
	rec.ErrorMessage = ""
	rec.LastUsedAt = time.Now().UTC()
	if err := s.Store.UpdateBranch(ctx, rec); err != nil {
		return meta.BranchRecord{}, err
	}
	fmt.Printf("✓ branch %q ready in %s\n", name, time.Since(total).Round(time.Millisecond))
	fmt.Println("  ", rec.ConnString)
	fmt.Println("  ", postgres.PsqlOneLiner(rec.Port, rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy))
	return rec, nil
}

// resolveBranchSource picks the parent dataset for CoW.
// fromConnector empty → sole connector, else local "main" if running, else error.
func (s *Service) resolveBranchSource(ctx context.Context, projectID, fromConnector string) (dataDir string, port int, name, id string, err error) {
	if fromConnector == "main" {
		if auth.IsUser(ctx) {
			return "", 0, "", "", fmt.Errorf("forbidden: GitHub users cannot use shared main — run sprout connect for your own replica")
		}
		return s.MainDir(), MainPort, "main", "", nil
	}
	if fromConnector != "" {
		c, err := s.lookupConnector(ctx, projectID, fromConnector)
		if err != nil {
			return "", 0, "", "", err
		}
		return s.connectorSource(c)
	}

	list, err := s.visibleConnectors(ctx, projectID)
	if err != nil {
		return "", 0, "", "", err
	}
	if len(list) == 1 {
		return s.connectorSource(list[0])
	}
	if len(list) > 1 {
		names := make([]string, 0, len(list))
		for _, c := range list {
			names = append(names, c.Name)
		}
		return "", 0, "", "", fmt.Errorf("multiple connectors — pass --from (%s)", strings.Join(names, ", "))
	}

	if auth.IsUser(ctx) {
		return "", 0, "", "", fmt.Errorf("no source — run sprout connect --name <n> <url>")
	}

	// No connectors: fall back to local init main if present (machine token only).
	if _, err := os.Stat(filepath.Join(s.MainDir(), "PG_VERSION")); err == nil {
		return s.MainDir(), MainPort, "main", "", nil
	}
	return "", 0, "", "", fmt.Errorf("no source — run sprout connect --name <n> <url> or sprout init")
}

func (s *Service) visibleConnectors(ctx context.Context, projectID string) ([]meta.Connector, error) {
	list, err := s.Store.ListConnectorsByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return meta.FilterConnectorsByOwner(auth.OwnerFrom(ctx), list), nil
}

func (s *Service) ListConnectors(ctx context.Context) ([]meta.Connector, error) {
	list, err := s.Store.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	return meta.FilterConnectorsByOwner(auth.OwnerFrom(ctx), list), nil
}

// connectorSource maps a connector to its local PGDATA (legacy connectors used data/main).
func (s *Service) connectorSource(c meta.Connector) (dataDir string, port int, name, id string, err error) {
	dir := c.DataDir
	port = c.Port
	if dir == "" {
		dir = s.MainDir()
	}
	if port == 0 {
		port = MainPort
	}
	return dir, port, c.Name, c.ID, nil
}

func (s *Service) createPipeline(ctx context.Context, rec *meta.BranchRecord, srcDir string, srcPort int, srcName string) error {
	rm := &replica.Manager{Bins: s.Bins}
	srcInst := &postgres.Instance{
		Name: srcName, DataDir: srcDir, Port: srcPort,
		LogFile: s.logPath(srcName), Bins: s.Bins,
	}
	srcHandle := compute.Handle{
		Provider: s.Compute.Name(), Name: srcName, Port: srcPort, DataDir: srcDir,
	}
	if rec.SourceConnectorID != "" {
		if c, err := s.Store.GetConnectorByID(ctx, rec.SourceConnectorID); err == nil {
			srcHandle.Name = postgres.ReplicaComputeName(c.Name, c.CreatedBy)
		}
	} else if srcName != "main" && !strings.HasPrefix(srcName, "replica-") {
		srcHandle.Name = postgres.ReplicaComputeName(srcName, rec.CreatedBy)
	}

	unlock := s.lockBranch("snap:" + srcName)
	defer unlock()

	st, stErr := rm.Status(ctx, "127.0.0.1", srcPort)
	useReplayPause := stErr == nil && st.IsStandby

	if useReplayPause {
		fmt.Println("→ Step 0: replica lag check")
		if st.LagBytes > s.maxLagBytes() {
			return fmt.Errorf("replica_lag: lag_bytes=%d exceeds max=%d — wait for catch-up", st.LagBytes, s.maxLagBytes())
		}
		fmt.Printf("  lag_bytes=%d replay_lsn=%s\n", st.LagBytes, st.ReplayLSN)
		fmt.Println("→ Step 1: pause WAL replay + CHECKPOINT")
		if err := rm.PauseReplay(ctx, "127.0.0.1", srcPort); err != nil {
			return fmt.Errorf("storage_failed: pause replay: %w", err)
		}
		if err := rm.Checkpoint(ctx, "127.0.0.1", srcPort); err != nil {
			_ = rm.ResumeReplay(ctx, "127.0.0.1", srcPort)
			return fmt.Errorf("storage_failed: checkpoint: %w", err)
		}
		rec.SourceLSN = st.ReplayLSN
	} else {
		fmt.Println("→ Step 1: CHECKPOINT (+ cold stop if enabled)")
		if err := srcInst.Checkpoint(); err != nil {
			return fmt.Errorf("storage_failed: checkpoint: %w", err)
		}
		if s.ColdSnap {
			if err := s.Compute.Stop(ctx, srcHandle); err != nil {
				return err
			}
		}
	}

	fmt.Println("→ Step 2: snapshot")
	snapRef, err := s.Storage.Snapshot(srcDir, s.instKey(*rec))
	if err != nil {
		if useReplayPause {
			_ = rm.ResumeReplay(ctx, "127.0.0.1", srcPort)
		} else if s.ColdSnap {
			_, _ = s.Compute.Start(ctx, compute.Spec{
				Name: srcHandle.Name, DataDir: srcDir, Port: srcPort, LogFile: s.logPath(srcHandle.Name),
			})
		}
		return fmt.Errorf("storage_failed: %w", err)
	}
	rec.SnapshotRef = snapRef

	if useReplayPause {
		fmt.Println("→ resume WAL replay on source")
		if err := rm.ResumeReplay(ctx, "127.0.0.1", srcPort); err != nil {
			return err
		}
	} else if s.ColdSnap {
		fmt.Println("→ restart source")
		if _, err := s.Compute.Start(ctx, compute.Spec{
			Name: srcHandle.Name, DataDir: srcDir, Port: srcPort, LogFile: s.logPath(srcHandle.Name),
		}); err != nil {
			return err
		}
	}

	fmt.Println("→ Step 3: clone")
	if err := s.Storage.Clone(snapRef, rec.DataDir); err != nil {
		_ = s.Storage.Destroy(snapRef)
		return fmt.Errorf("storage_failed: %w", err)
	}

	inst := &postgres.Instance{
		Name: rec.Name, Source: rec.SourceConnector, Owner: rec.CreatedBy, DataDir: rec.DataDir, Port: rec.Port,
		LogFile: s.logPath(s.instKey(*rec)), Bins: s.Bins, Password: rec.Password,
	}
	fmt.Println("→ Step 4: PrepareClone (promote branch to independent primary)")
	if err := inst.PrepareClone(); err != nil {
		_ = s.Storage.Destroy(rec.DataDir)
		_ = s.Storage.Destroy(snapRef)
		return err
	}

	fmt.Println("→ Step 5: start compute")
	h, err := s.Compute.Start(ctx, compute.Spec{
		Name: s.instKey(*rec), DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(s.instKey(*rec)),
	})
	if err != nil {
		_ = s.Storage.Destroy(rec.DataDir)
		_ = s.Storage.Destroy(snapRef)
		return fmt.Errorf("compute_failed: %w", err)
	}
	rec.ContainerID = h.ContainerID
	rec.ConnString = inst.ConnString("postgres")
	_ = inst.EnsureAppRoles()
	return nil
}

func (s *Service) Reset(ctx context.Context, projectID, name, from string) (meta.BranchRecord, error) {
	rec, err := s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	unlock := s.lockBranch(s.instKey(rec))
	defer unlock()
	rec, err = s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	if rec.Role != "branch" {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: cannot reset main")
	}
	if rec.Status != meta.StatusActive && rec.Status != meta.StatusIdle && rec.Status != meta.StatusError {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}

	rec.Status = meta.StatusResetting
	_ = s.Store.UpdateBranch(ctx, rec)

	key := s.instKey(rec)
	h := compute.Handle{Provider: s.Compute.Name(), Name: key, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	_ = s.Compute.Stop(ctx, h)
	_ = s.Storage.Destroy(rec.DataDir)

	if err := s.Storage.Clone(rec.SnapshotRef, rec.DataDir); err != nil {
		rec.Status = meta.StatusError
		rec.ErrorMessage = err.Error()
		_ = s.Store.UpdateBranch(ctx, rec)
		return meta.BranchRecord{}, err
	}
	inst := &postgres.Instance{Name: rec.Name, Source: rec.SourceConnector, Owner: rec.CreatedBy, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key), Bins: s.Bins, Password: rec.Password}
	if err := inst.PrepareClone(); err != nil {
		return meta.BranchRecord{}, err
	}
	started, err := s.Compute.Start(ctx, compute.Spec{Name: key, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key)})
	if err != nil {
		rec.Status = meta.StatusError
		rec.ErrorMessage = err.Error()
		_ = s.Store.UpdateBranch(ctx, rec)
		return meta.BranchRecord{}, err
	}
	rec.ContainerID = started.ContainerID
	rec.Status = meta.StatusActive
	rec.ErrorMessage = ""
	rec.ConnString = inst.ConnString("postgres")
	rec.LastUsedAt = time.Now().UTC()
	_ = inst.EnsureAppRoles()
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) Delete(ctx context.Context, projectID, name, from string) error {
	rec, err := s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return err
	}
	unlock := s.lockBranch(s.instKey(rec))
	defer unlock()
	rec, err = s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return err
	}
	if rec.Role != "branch" {
		return fmt.Errorf("invalid_state: cannot delete main via branch API")
	}
	rec.Status = meta.StatusDeleting
	_ = s.Store.UpdateBranch(ctx, rec)

	key := s.instKey(rec)
	h := compute.Handle{Provider: s.Compute.Name(), Name: key, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	_ = s.Compute.Stop(ctx, h)
	_ = s.Storage.Destroy(rec.DataDir)
	_ = s.Storage.Destroy(rec.SnapshotRef)
	return s.Store.DeleteBranch(ctx, rec.ID)
}

func (s *Service) Suspend(ctx context.Context, projectID, name, from string) (meta.BranchRecord, error) {
	rec, err := s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	unlock := s.lockBranch(s.instKey(rec))
	defer unlock()
	rec, err = s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	if rec.Status != meta.StatusActive {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}
	key := s.instKey(rec)
	h := compute.Handle{Provider: s.Compute.Name(), Name: key, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	if err := s.Compute.Stop(ctx, h); err != nil {
		return meta.BranchRecord{}, err
	}
	rec.Status = meta.StatusIdle
	rec.ContainerID = ""
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) Resume(ctx context.Context, projectID, name, from string) (meta.BranchRecord, error) {
	rec, err := s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	unlock := s.lockBranch(s.instKey(rec))
	defer unlock()
	rec, err = s.lookupBranch(ctx, projectID, name, from)
	if err != nil {
		return meta.BranchRecord{}, err
	}
	if rec.Status != meta.StatusIdle && rec.Status != meta.StatusCrashed {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}
	key := s.instKey(rec)
	started, err := s.Compute.Start(ctx, compute.Spec{
		Name: key, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key),
	})
	if err != nil {
		return meta.BranchRecord{}, err
	}
	rec.ContainerID = started.ContainerID
	rec.Status = meta.StatusActive
	rec.ErrorMessage = ""
	rec.LastUsedAt = time.Now().UTC()
	rec.ConnString = postgres.FormatConnString(rec.Port, "postgres", rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy)
	inst := &postgres.Instance{Name: rec.Name, Source: rec.SourceConnector, Owner: rec.CreatedBy, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(key), Bins: s.Bins, Password: rec.Password}
	_ = inst.EnsureAppRoles()
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) List(ctx context.Context, projectID string) ([]meta.BranchRecord, error) {
	list, err := s.Store.ListBranches(ctx, projectID)
	if err != nil {
		return nil, err
	}
	owner := auth.OwnerFrom(ctx)
	if owner == "" {
		return list, nil
	}
	out := make([]meta.BranchRecord, 0, len(list))
	for _, b := range list {
		if b.CreatedBy != owner {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, projectID, name, from string) (meta.BranchRecord, error) {
	return s.lookupBranch(ctx, projectID, name, from)
}
