package reconcile

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/storage"
)

type fakeCompute struct {
	running map[string]bool
	starts  int
}

func (f *fakeCompute) Name() string { return "fake" }
func (f *fakeCompute) key(h compute.Handle) string {
	if h.DataDir != "" {
		return h.DataDir
	}
	return h.Name
}
func (f *fakeCompute) Start(_ context.Context, spec compute.Spec) (compute.Handle, error) {
	f.starts++
	f.running[spec.DataDir] = true
	return compute.Handle{Provider: "fake", Name: spec.Name, Port: spec.Port, DataDir: spec.DataDir}, nil
}
func (f *fakeCompute) Stop(_ context.Context, h compute.Handle) error {
	f.running[f.key(h)] = false
	return nil
}
func (f *fakeCompute) IsRunning(_ context.Context, h compute.Handle) (bool, error) {
	return f.running[f.key(h)], nil
}

func TestReconcileActiveDownBecomesCrashedNotIdle(t *testing.T) {
	ctx := context.Background()
	store, err := meta.OpenFile(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, _ := store.EnsureProject(ctx, "default")
	dir := t.TempDir()
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusActive, Port: 55440, DataDir: dir,
	})
	comp := &fakeCompute{running: map[string]bool{dir: false}}
	r := &Reconciler{Store: store, Compute: comp, Storage: storage.NewCopy(t.TempDir()), Root: t.TempDir()}
	r.RunOnce(ctx)
	got, err := store.GetBranch(ctx, proj.ID, "feat")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != meta.StatusCrashed {
		t.Fatalf("status=%s want crashed", got.Status)
	}
}

func TestReconcileIdleStaysIdle(t *testing.T) {
	ctx := context.Background()
	store, err := meta.OpenFile(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, _ := store.EnsureProject(ctx, "default")
	dir := t.TempDir()
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusIdle, Port: 55440, DataDir: dir,
	})
	comp := &fakeCompute{running: map[string]bool{dir: false}}
	r := &Reconciler{Store: store, Compute: comp, Storage: storage.NewCopy(t.TempDir()), Root: t.TempDir()}
	r.RunOnce(ctx)
	got, _ := store.GetBranch(ctx, proj.ID, "feat")
	if got.Status != meta.StatusIdle {
		t.Fatalf("status=%s want idle", got.Status)
	}
	if comp.starts != 0 {
		t.Fatalf("idle branch should not auto-start")
	}
}

func TestReconcileAutoResumeCrashed(t *testing.T) {
	ctx := context.Background()
	store, err := meta.OpenFile(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, _ := store.EnsureProject(ctx, "default")
	dir := t.TempDir()
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusCrashed, Port: 55440, DataDir: dir,
	})
	comp := &fakeCompute{running: map[string]bool{dir: false}}
	r := &Reconciler{Store: store, Compute: comp, Storage: storage.NewCopy(t.TempDir()), Root: t.TempDir(), AutoResume: true}
	r.RunOnce(ctx)
	got, _ := store.GetBranch(ctx, proj.ID, "feat")
	if got.Status != meta.StatusActive {
		t.Fatalf("status=%s want active", got.Status)
	}
	if comp.starts != 1 {
		t.Fatalf("starts=%d want 1", comp.starts)
	}
}

func TestReconcileConnectorDown(t *testing.T) {
	ctx := context.Background()
	store, err := meta.OpenFile(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, _ := store.EnsureProject(ctx, "default")
	dir := t.TempDir()
	_ = store.PutConnector(ctx, meta.Connector{
		ID: "c1", ProjectID: proj.ID, Name: "sup", Mode: "logical",
		Status: meta.ConnectorReplicating, Port: 55434, DataDir: dir,
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "replica-c1", ProjectID: proj.ID, Name: "replica-sup", Role: "replica",
		Status: meta.StatusActive, Port: 55434, DataDir: dir,
	})
	comp := &fakeCompute{running: map[string]bool{dir: false}}
	r := &Reconciler{Store: store, Compute: comp, Storage: storage.NewCopy(t.TempDir()), Root: t.TempDir()}
	r.RunOnce(ctx)
	c, _ := store.GetConnectorByName(ctx, proj.ID, "sup")
	if c.Status != meta.ConnectorCrashed {
		t.Fatalf("connector status=%s want crashed", c.Status)
	}
	br, _ := store.GetBranch(ctx, proj.ID, "replica-sup")
	if br.Status != meta.StatusCrashed {
		t.Fatalf("replica row status=%s want crashed", br.Status)
	}
}

func TestReconcileSkipsReplicaRoleInBranchLoop(t *testing.T) {
	ctx := context.Background()
	store, err := meta.OpenFile(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, _ := store.EnsureProject(ctx, "default")
	dir := t.TempDir()
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "replica-x", ProjectID: proj.ID, Name: "replica-x", Role: "replica",
		Status: meta.StatusActive, Port: 1, DataDir: dir, UpdatedAt: time.Now(),
	})
	comp := &fakeCompute{running: map[string]bool{dir: false}}
	r := &Reconciler{Store: store, Compute: comp, Storage: storage.NewCopy(t.TempDir()), Root: t.TempDir()}
	r.reconcileBranches(ctx)
	br, _ := store.GetBranch(ctx, proj.ID, "replica-x")
	if br.Status != meta.StatusActive {
		t.Fatalf("replica role should be ignored by branch reconciler, got %s", br.Status)
	}
}
