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
	c, err := store.GetConnectorByName(ctx, proj.ID, "sup", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "secret-conn" {
		t.Fatalf("connector password=%q", c.Password)
	}
	if c.Engine != "postgres" {
		t.Fatalf("default engine=%q", c.Engine)
	}
	if err := store.UpdateConnector(ctx, Connector{
		ID: c.ID, ProjectID: proj.ID, Name: "sup", Engine: "mongodb", Mode: "logical",
		Status: ConnectorReplicating, Port: 55434, Password: "secret-conn",
	}); err != nil {
		t.Fatal(err)
	}
	c, err = store.GetConnectorByName(ctx, proj.ID, "sup", "")
	if err != nil || c.Engine != "mongodb" {
		t.Fatalf("engine round-trip: %+v %v", c, err)
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
	lab, err := store.FindBranch(ctx, proj.ID, "testdb", "lab", "")
	if err != nil || lab.Port != 55440 {
		t.Fatalf("from lab: %+v %v", lab, err)
	}
	supa, err := store.FindBranch(ctx, proj.ID, "testdb", "supabase", "")
	if err != nil || supa.Port != 55441 {
		t.Fatalf("from supabase: %+v %v", supa, err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "b3", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55442, SourceConnector: "lab",
	}); err == nil {
		t.Fatal("expected unique (project, source, name, created_by) to reject duplicate testdb from lab")
	}
}

func TestFindBranchOwnerIsolation(t *testing.T) {
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
		ID: "old", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55440, SourceConnector: "supabase",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "a", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55441, SourceConnector: "supabase", CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBranch(ctx, BranchRecord{
		ID: "b", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: StatusActive, Port: 55442, SourceConnector: "supabase", CreatedBy: "bob",
	}); err != nil {
		t.Fatal(err)
	}
	alice, err := store.FindBranch(ctx, proj.ID, "testdb", "supabase", "alice")
	if err != nil || alice.ID != "a" {
		t.Fatalf("alice: %+v %v", alice, err)
	}
	if _, err := store.FindBranch(ctx, proj.ID, "testdb", "supabase", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindBranch(ctx, proj.ID, "testdb", "", "carol"); err == nil {
		t.Fatal("carol should not see others' branches")
	}
	if _, err := store.GetBranch(ctx, proj.ID, "testdb"); err == nil || !strings.Contains(err.Error(), "ambiguous_branch") {
		t.Fatalf("machine token should see all testdb rows as ambiguous, got %v", err)
	}
}

func TestConnectorOwnerIsolation(t *testing.T) {
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
		ID: "old", ProjectID: proj.ID, Name: "supabase", Status: ConnectorReplicating,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnector(ctx, Connector{
		ID: "a", ProjectID: proj.ID, Name: "supabase", CreatedBy: "alice", Status: ConnectorReplicating,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnector(ctx, Connector{
		ID: "b", ProjectID: proj.ID, Name: "supabase", CreatedBy: "bob", Status: ConnectorReplicating,
	}); err != nil {
		t.Fatal(err)
	}
	alice, err := store.GetConnectorByName(ctx, proj.ID, "supabase", "alice")
	if err != nil || alice.ID != "a" {
		t.Fatalf("alice: %+v %v", alice, err)
	}
	unowned, err := store.GetConnectorByName(ctx, proj.ID, "supabase", "")
	if err != nil || unowned.ID != "old" {
		t.Fatalf("machine unowned: %+v %v", unowned, err)
	}
	if _, err := store.GetConnectorByName(ctx, proj.ID, "supabase", "carol"); err == nil {
		t.Fatal("carol should not see supabase")
	}
	if err := store.PutConnector(ctx, Connector{
		ID: "a2", ProjectID: proj.ID, Name: "supabase", CreatedBy: "alice", Status: ConnectorReplicating,
	}); err == nil {
		t.Fatal("expected unique (project, name, created_by)")
	}
}

func TestSQLiteBackfillDefaultOrg(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	proj, err := store.EnsureProject(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnector(ctx, Connector{
		ID: "c1", ProjectID: proj.ID, Name: "sup", Mode: "logical",
		Status: ConnectorReplicating, Port: 55434, CreatedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, err := store.GetConnectorByName(ctx, proj.ID, "sup", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if c.OrgID == "" {
		t.Fatal("expected org_id backfill")
	}
	orgs, err := store.ListOrgs(ctx, "alice")
	if err != nil || len(orgs) != 1 || orgs[0].Name != DefaultOrg || orgs[0].ID != c.OrgID {
		t.Fatalf("orgs=%+v org_id=%s err=%v", orgs, c.OrgID, err)
	}
}
