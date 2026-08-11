package branch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/adityaraj/ardent-clone/internal/compute"
	"github.com/adityaraj/ardent-clone/internal/meta"
	"github.com/adityaraj/ardent-clone/internal/postgres"
	"github.com/adityaraj/ardent-clone/internal/replica"
	"github.com/adityaraj/ardent-clone/internal/storage"
	"github.com/google/uuid"
)

const MainPort = 55432
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
	MaxLagBytes int64 // refuse branch create if replica lag exceeds this

	mainMu sync.Mutex // serialize snapshots of main
	opsMu  sync.Map   // per-branch name locks
}

func (s *Service) MainDir() string {
	return filepath.Join(s.Root, "main")
}

func (s *Service) BranchDir(name string) string {
	return filepath.Join(s.Root, "branches", name)
}

func (s *Service) logPath(name string) string {
	return filepath.Join(s.Root, "logs", name+".log")
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

	main := s.mainInst()
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

	// Seed demo rows (idempotent).
	if err := main.SeedDemo(); err != nil {
		return proj, fmt.Errorf("seed: %w", err)
	}

	// Upsert main row for reconciler awareness.
	_ = s.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "main", ProjectID: proj.ID, Name: "main", Role: "main",
		Status: meta.StatusActive, Port: MainPort, DataDir: s.MainDir(),
		Compute: s.Compute.Name(), ConnString: main.ConnString("postgres"),
	})

	fmt.Println("✓ main ready:", main.ConnString("postgres"))
	return proj, nil
}

func (s *Service) Create(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	if !nameRe.MatchString(name) || name == "main" {
		return meta.BranchRecord{}, fmt.Errorf("invalid_name: use lowercase [a-z0-9-], not 'main'")
	}
	unlock := s.lockBranch(name)
	defer unlock()

	if _, err := s.Store.GetBranch(ctx, projectID, name); err == nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_exists: %q", name)
	}

	mainRunning, _ := s.Compute.IsRunning(ctx, s.mainHandle())
	if !mainRunning {
		return meta.BranchRecord{}, fmt.Errorf("main_not_ready: run init first")
	}

	port, err := s.Store.AllocPort(ctx)
	if err != nil {
		return meta.BranchRecord{}, err
	}

	rec := meta.BranchRecord{
		ID: uuid.NewString(), ProjectID: projectID, Name: name, Role: "branch",
		Status: meta.StatusCreating, Port: port, DataDir: s.BranchDir(name),
		Compute: s.Compute.Name(),
	}
	if err := s.Store.PutBranch(ctx, rec); err != nil {
		return meta.BranchRecord{}, err
	}

	total := time.Now()
	fmt.Printf("\n=== branch create %q (storage=%s compute=%s) ===\n", name, s.Storage.Name(), s.Compute.Name())

	if err := s.createPipeline(ctx, &rec); err != nil {
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
	return rec, nil
}

func (s *Service) createPipeline(ctx context.Context, rec *meta.BranchRecord) error {
	main := s.mainInst()
	rm := &replica.Manager{Bins: s.Bins}

	s.mainMu.Lock()
	defer s.mainMu.Unlock()

	st, stErr := rm.Status(ctx, "127.0.0.1", MainPort)
	useReplayPause := stErr == nil && st.IsStandby

	if useReplayPause {
		fmt.Println("→ Step 0: replica lag check")
		if st.LagBytes > s.maxLagBytes() {
			return fmt.Errorf("replica_lag: lag_bytes=%d exceeds max=%d — wait for catch-up", st.LagBytes, s.maxLagBytes())
		}
		fmt.Printf("  lag_bytes=%d replay_lsn=%s\n", st.LagBytes, st.ReplayLSN)
		fmt.Println("→ Step 1: pause WAL replay + CHECKPOINT")
		if err := rm.PauseReplay(ctx, "127.0.0.1", MainPort); err != nil {
			return fmt.Errorf("storage_failed: pause replay: %w", err)
		}
		if err := rm.Checkpoint(ctx, "127.0.0.1", MainPort); err != nil {
			_ = rm.ResumeReplay(ctx, "127.0.0.1", MainPort)
			return fmt.Errorf("storage_failed: checkpoint: %w", err)
		}
		rec.SourceLSN = st.ReplayLSN
	} else {
		fmt.Println("→ Step 1: CHECKPOINT (+ cold stop if enabled)")
		if err := main.Checkpoint(); err != nil {
			return fmt.Errorf("storage_failed: checkpoint: %w", err)
		}
		if s.ColdSnap {
			if err := s.Compute.Stop(ctx, s.mainHandle()); err != nil {
				return err
			}
		}
	}

	fmt.Println("→ Step 2: snapshot")
	snapRef, err := s.Storage.Snapshot(main.DataDir, rec.Name)
	if err != nil {
		if useReplayPause {
			_ = rm.ResumeReplay(ctx, "127.0.0.1", MainPort)
		} else if s.ColdSnap {
			_, _ = s.Compute.Start(ctx, compute.Spec{Name: "main", DataDir: s.MainDir(), Port: MainPort, LogFile: s.logPath("main")})
		}
		return fmt.Errorf("storage_failed: %w", err)
	}
	rec.SnapshotRef = snapRef

	if useReplayPause {
		fmt.Println("→ resume WAL replay on main")
		if err := rm.ResumeReplay(ctx, "127.0.0.1", MainPort); err != nil {
			return err
		}
	} else if s.ColdSnap {
		fmt.Println("→ restart main")
		if _, err := s.Compute.Start(ctx, compute.Spec{Name: "main", DataDir: s.MainDir(), Port: MainPort, LogFile: s.logPath("main")}); err != nil {
			return err
		}
	}

	fmt.Println("→ Step 3: clone")
	if err := s.Storage.Clone(snapRef, rec.DataDir); err != nil {
		_ = s.Storage.Destroy(snapRef)
		return fmt.Errorf("storage_failed: %w", err)
	}

	inst := &postgres.Instance{
		Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port,
		LogFile: s.logPath(rec.Name), Bins: s.Bins,
	}
	fmt.Println("→ Step 4: PrepareClone (promote branch to independent primary)")
	if err := inst.PrepareClone(); err != nil {
		_ = s.Storage.Destroy(rec.DataDir)
		_ = s.Storage.Destroy(snapRef)
		return err
	}

	fmt.Println("→ Step 5: start compute")
	h, err := s.Compute.Start(ctx, compute.Spec{
		Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(rec.Name),
	})
	if err != nil {
		_ = s.Storage.Destroy(rec.DataDir)
		_ = s.Storage.Destroy(snapRef)
		return fmt.Errorf("compute_failed: %w", err)
	}
	rec.ContainerID = h.ContainerID
	rec.ConnString = inst.ConnString("postgres")
	return nil
}

func (s *Service) Reset(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	unlock := s.lockBranch(name)
	defer unlock()

	rec, err := s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_not_found")
	}
	if rec.Role != "branch" {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: cannot reset main")
	}
	if rec.Status != meta.StatusActive && rec.Status != meta.StatusIdle && rec.Status != meta.StatusError {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}

	rec.Status = meta.StatusResetting
	_ = s.Store.UpdateBranch(ctx, rec)

	h := compute.Handle{Provider: s.Compute.Name(), Name: rec.Name, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	_ = s.Compute.Stop(ctx, h)
	_ = s.Storage.Destroy(rec.DataDir)

	if err := s.Storage.Clone(rec.SnapshotRef, rec.DataDir); err != nil {
		rec.Status = meta.StatusError
		rec.ErrorMessage = err.Error()
		_ = s.Store.UpdateBranch(ctx, rec)
		return meta.BranchRecord{}, err
	}
	inst := &postgres.Instance{Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(rec.Name), Bins: s.Bins}
	if err := inst.PrepareClone(); err != nil {
		return meta.BranchRecord{}, err
	}
	started, err := s.Compute.Start(ctx, compute.Spec{Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(rec.Name)})
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
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) Delete(ctx context.Context, projectID, name string) error {
	unlock := s.lockBranch(name)
	defer unlock()

	rec, err := s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return fmt.Errorf("branch_not_found")
	}
	if rec.Role != "branch" {
		return fmt.Errorf("invalid_state: cannot delete main via branch API")
	}
	rec.Status = meta.StatusDeleting
	_ = s.Store.UpdateBranch(ctx, rec)

	h := compute.Handle{Provider: s.Compute.Name(), Name: rec.Name, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	_ = s.Compute.Stop(ctx, h)
	_ = s.Storage.Destroy(rec.DataDir)
	_ = s.Storage.Destroy(rec.SnapshotRef)
	return s.Store.DeleteBranch(ctx, rec.ID)
}

func (s *Service) Suspend(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	unlock := s.lockBranch(name)
	defer unlock()
	rec, err := s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_not_found")
	}
	if rec.Status != meta.StatusActive {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}
	h := compute.Handle{Provider: s.Compute.Name(), Name: rec.Name, Port: rec.Port, DataDir: rec.DataDir, ContainerID: rec.ContainerID}
	if err := s.Compute.Stop(ctx, h); err != nil {
		return meta.BranchRecord{}, err
	}
	rec.Status = meta.StatusIdle
	rec.ContainerID = ""
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) Resume(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	unlock := s.lockBranch(name)
	defer unlock()
	rec, err := s.Store.GetBranch(ctx, projectID, name)
	if err != nil {
		return meta.BranchRecord{}, fmt.Errorf("branch_not_found")
	}
	if rec.Status != meta.StatusIdle {
		return meta.BranchRecord{}, fmt.Errorf("invalid_state: status=%s", rec.Status)
	}
	started, err := s.Compute.Start(ctx, compute.Spec{
		Name: rec.Name, DataDir: rec.DataDir, Port: rec.Port, LogFile: s.logPath(rec.Name),
	})
	if err != nil {
		return meta.BranchRecord{}, err
	}
	rec.ContainerID = started.ContainerID
	rec.Status = meta.StatusActive
	rec.LastUsedAt = time.Now().UTC()
	_ = s.Store.UpdateBranch(ctx, rec)
	return rec, nil
}

func (s *Service) List(ctx context.Context, projectID string) ([]meta.BranchRecord, error) {
	return s.Store.ListBranches(ctx, projectID)
}

func (s *Service) Get(ctx context.Context, projectID, name string) (meta.BranchRecord, error) {
	return s.Store.GetBranch(ctx, projectID, name)
}
