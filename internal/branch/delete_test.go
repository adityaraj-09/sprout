package branch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
