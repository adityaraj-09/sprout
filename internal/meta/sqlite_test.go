package meta

import (
	"context"
	"path/filepath"
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
