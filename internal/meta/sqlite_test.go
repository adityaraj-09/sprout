package meta

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLitePasswordRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, err := store.EnsureProject(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnector(ctx, Connector{
		ID: "c1", ProjectID: proj.ID, Name: "sup", Mode: "logical",
		Status: ConnectorReplicating, Port: 55434, Password: "secret-conn",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: StatusActive, Port: 55440, Password: "secret-branch",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := store.GetConnectorByName(ctx, proj.ID, "sup")
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "secret-conn" {
		t.Fatalf("connector password=%q", c.Password)
	}
	b, err := store.GetBranch(ctx, proj.ID, "feat")
	if err != nil {
		t.Fatal(err)
	}
	if b.Password != "secret-branch" {
		t.Fatalf("branch password=%q", b.Password)
	}
}

func TestFindBranchSameNameDifferentConnectors(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	proj, err := store.EnsureProject(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55440, SourceConnector: "lab",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "b2", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55441, SourceConnector: "supabase",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetBranch(ctx, proj.ID, "testdb"); err == nil || !strings.Contains(err.Error(), "ambiguous_branch") {
		t.Fatalf("expected ambiguous_branch, got %v", err)
	}
	lab, err := store.FindBranch(ctx, proj.ID, "testdb", "lab")
	if err != nil || lab.Port != 55440 {
		t.Fatalf("from lab: %+v %v", lab, err)
	}
	supa, err := store.FindBranch(ctx, proj.ID, "testdb", "supabase")
	if err != nil || supa.Port != 55441 {
		t.Fatalf("from supabase: %+v %v", supa, err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "b3", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55442, SourceConnector: "lab",
	}); err == nil {
		t.Fatal("expected unique (project, source, name) to reject duplicate testdb from lab")
	}
}
