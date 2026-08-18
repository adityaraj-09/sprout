package branch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adityaraj/sprout/internal/compute"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/storage"
)

type fakeCompute struct {
	stopped []string
}

func (f *fakeCompute) Name() string { return "fake" }
func (f *fakeCompute) Start(_ context.Context, spec compute.Spec) (compute.Handle, error) {
	return compute.Handle{Provider: "fake", Name: spec.Name, Port: spec.Port, DataDir: spec.DataDir}, nil
}
func (f *fakeCompute) Stop(_ context.Context, h compute.Handle) error {
	f.stopped = append(f.stopped, h.Name)
	return nil
}
func (f *fakeCompute) IsRunning(_ context.Context, h compute.Handle) (bool, error) {
	return false, nil
}

func testService(t *testing.T) (*Service, meta.Project) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := meta.OpenFile(filepath.Join(root, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	proj, err := store.EnsureProject(ctx, DefaultProject)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{
		Root:    root,
		Store:   store,
		Storage: storage.NewCopy(root),
		Compute: &fakeCompute{},
	}
	return svc, proj
}

func TestDeleteConnectorBlockedByBranches(t *testing.T) {
	ctx := context.Background()
	svc, proj := testService(t)
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c1", ProjectID: proj.ID, Name: "sup", Mode: ModePhysical,
		Status: meta.ConnectorReplicating, DataDir: filepath.Join(svc.Root, "replicas", "sup"), Port: 55434,
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat-a", Role: "branch",
		Status: meta.StatusIdle, Port: 55440, DataDir: filepath.Join(svc.Root, "branches", "feat-a"),
		SourceConnector: "sup", SourceConnectorID: "c1",
	})

	err := svc.DeleteConnector(ctx, proj.ID, "sup", false)
	if err == nil || !strings.Contains(err.Error(), "connector_has_branches") {
		t.Fatalf("expected connector_has_branches, got %v", err)
	}
	if _, err := svc.Store.GetConnectorByName(ctx, proj.ID, "sup", ""); err != nil {
		t.Fatalf("connector should still exist: %v", err)
	}
	if _, err := svc.Store.GetBranch(ctx, proj.ID, "feat-a"); err != nil {
		t.Fatalf("branch should still exist: %v", err)
	}
}

func TestDeleteConnectorForceRemovesBranches(t *testing.T) {
	ctx := context.Background()
	svc, proj := testService(t)
	cdir := filepath.Join(svc.Root, "replicas", "sup")
	bdir := filepath.Join(svc.Root, "branches", "feat-a")
	_ = svc.Storage.EnsureVolume(cdir)
	_ = svc.Storage.EnsureVolume(bdir)
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c1", ProjectID: proj.ID, Name: "sup", Mode: ModePhysical,
		Status: meta.ConnectorReplicating, DataDir: cdir, Port: 55434,
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat-a", Role: "branch",
		Status: meta.StatusIdle, Port: 55440, DataDir: bdir,
		SourceConnector: "sup", SourceConnectorID: "c1",
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "replica-c1", ProjectID: proj.ID, Name: "replica-sup", Role: "replica",
		Status: meta.StatusActive, Port: 55434, DataDir: cdir,
		SourceConnector: "sup",
	})

	if err := svc.DeleteConnector(ctx, proj.ID, "sup", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.GetConnectorByName(ctx, proj.ID, "sup", ""); err == nil {
		t.Fatal("connector should be gone")
	}
	if _, err := svc.Store.GetBranch(ctx, proj.ID, "feat-a"); err == nil {
		t.Fatal("child branch should be gone")
	}
	if _, err := svc.Store.GetBranch(ctx, proj.ID, "replica-sup"); err == nil {
		t.Fatal("synthetic replica row should be gone")
	}
}

func TestDeleteTrimsFromComma(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	dir := filepath.Join(svc.Root, "branches", "mango-mongo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "b-mango", ProjectID: proj.ID, Name: "mango", Role: "branch",
		Status: meta.StatusActive, Port: 55450, DataDir: dir,
		SourceConnector: "mongo", SourceConnectorID: "c-mongo",
	})
	if err := svc.Delete(ctx, proj.ID, "mango", "mongo,"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.GetBranchByID(ctx, "b-mango"); err == nil {
		t.Fatal("branch record should be gone")
	}
}

func TestDeleteOrphanBranchDataset(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	dir := svc.BranchDir("mango", "mongo", "")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "WiredTiger.wt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, proj.ID, "mango", "mongo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("orphan dataset should be destroyed")
	}
}

func TestDeleteOrphanBranchWithOwnerInPath(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	dir := filepath.Join(svc.Root, "branches", "mango-adityaraj-09-mongo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "WiredTiger.wt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, proj.ID, "mango", "mongo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("owner-qualified orphan should be destroyed")
	}
}

func TestDeleteLockTimeout(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	dir := filepath.Join(svc.Root, "branches", "feat-mongo")
	_ = os.MkdirAll(dir, 0o700)
	rec := meta.BranchRecord{
		ID: "b-lock", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusCreating, Port: 55451, DataDir: dir,
		SourceConnector: "mongo", CreatedBy: "",
	}
	if err := svc.Store.PutBranch(ctx, rec); err != nil {
		t.Fatal(err)
	}
	unlock, err := svc.lockBranch(ctx, svc.instKey(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	dctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	err = svc.Delete(dctx, proj.ID, "feat", "mongo")
	if err == nil || !strings.Contains(err.Error(), "operation_in_progress") {
		t.Fatalf("expected operation_in_progress, got %v", err)
	}
}

func TestOrphanBranchDirName(t *testing.T) {
	if !orphanBranchDirName("mango-mongo", "mango", "mongo") {
		t.Fatal("name-from")
	}
	if !orphanBranchDirName("mango-adityaraj-09-mongo", "mango", "mongo") {
		t.Fatal("name-owner-from")
	}
	if orphanBranchDirName("other-mongo", "mango", "mongo") {
		t.Fatal("different branch")
	}
	if orphanBranchDirName("mango-pg", "mango", "mongo") {
		t.Fatal("different connector")
	}
}

func TestSourceEngineUsesMongoDataDirWhenEngineEmpty(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	dir := filepath.Join(svc.Root, "replicas", "mongo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "WiredTiger.wt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c-mongo", ProjectID: proj.ID, Name: "mongo", DataDir: dir, Engine: "",
	}); err != nil {
		t.Fatal(err)
	}
	rec := meta.BranchRecord{SourceConnectorID: "c-mongo", DataDir: filepath.Join(svc.Root, "branches", "feat")}
	if got := svc.sourceEngine(ctx, rec); got != "mongodb" {
		t.Fatalf("got %q, want mongodb", got)
	}
}

func TestTrimIdent(t *testing.T) {
	if got := trimIdent("mongo,"); got != "mongo" {
		t.Fatalf("got %q", got)
	}
	if got := trimIdent(" mango "); got != "mango" {
		t.Fatalf("got %q", got)
	}
}
