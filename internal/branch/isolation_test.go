package branch

import (
	"context"
	"strings"
	"testing"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/meta"
)

func TestListIsolatesGitHubUsers(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c-old", ProjectID: proj.ID, Name: "supabase", Status: meta.ConnectorReplicating,
	})
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c-a", ProjectID: proj.ID, Name: "supabase", CreatedBy: "alice", Status: meta.ConnectorReplicating,
	})
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c-b", ProjectID: proj.ID, Name: "supabase", CreatedBy: "bob", Status: meta.ConnectorReplicating,
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "old", ProjectID: proj.ID, Name: "testdb", Role: "branch",
		Status: meta.StatusIdle, SourceConnector: "supabase", SourceConnectorID: "c-old",
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "a", ProjectID: proj.ID, Name: "testdb", Role: "branch", CreatedBy: "alice",
		Status: meta.StatusIdle, SourceConnector: "supabase", SourceConnectorID: "c-a",
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "b", ProjectID: proj.ID, Name: "testdb", Role: "branch", CreatedBy: "bob",
		Status: meta.StatusIdle, SourceConnector: "supabase", SourceConnectorID: "c-b",
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "main", ProjectID: proj.ID, Name: "main", Role: "main", Status: meta.StatusActive,
	})

	alice := auth.WithActor(ctx, auth.Actor{Kind: auth.KindGitHub, Login: "alice"})
	list, err := svc.List(alice, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "a" {
		t.Fatalf("alice branches=%+v", list)
	}
	conns, err := svc.ListConnectors(alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 || conns[0].ID != "c-a" {
		t.Fatalf("alice connectors=%+v", conns)
	}
	if _, err := svc.Get(alice, proj.ID, "testdb", "supabase"); err != nil {
		t.Fatalf("alice get own branch: %v", err)
	}

	bob := auth.WithActor(ctx, auth.Actor{Kind: auth.KindGitHub, Login: "bob"})
	got, err := svc.Get(bob, proj.ID, "testdb", "supabase")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b" {
		t.Fatalf("bob should get own testdb, got %+v", got)
	}

	machine, err := svc.List(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(machine) != 4 {
		t.Fatalf("machine should see all branches, got %d", len(machine))
	}
}

func TestGitHubCannotUseSharedMain(t *testing.T) {
	svc, proj := testService(t)
	alice := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindGitHub, Login: "alice"})
	_, _, _, _, err := svc.resolveBranchSource(alice, proj.ID, "main")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected forbidden, got %v", err)
	}
	_, _, _, _, err = svc.resolveBranchSource(alice, proj.ID, "")
	if err == nil || !strings.Contains(err.Error(), "no source") {
		t.Fatalf("expected no source, got %v", err)
	}
}

func TestBranchesFromConnectorStayOnOwner(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	aliceC := meta.Connector{ID: "c-a", ProjectID: proj.ID, Name: "supabase", CreatedBy: "alice"}
	bobC := meta.Connector{ID: "c-b", ProjectID: proj.ID, Name: "supabase", CreatedBy: "bob"}
	_ = svc.Store.PutConnector(ctx, aliceC)
	_ = svc.Store.PutConnector(ctx, bobC)
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "a", ProjectID: proj.ID, Name: "feat", Role: "branch", CreatedBy: "alice",
		Status: meta.StatusIdle, SourceConnector: "supabase", SourceConnectorID: "c-a",
	})
	_ = svc.Store.PutBranch(ctx, meta.BranchRecord{
		ID: "b", ProjectID: proj.ID, Name: "feat", Role: "branch", CreatedBy: "bob",
		Status: meta.StatusIdle, SourceConnector: "supabase", SourceConnectorID: "c-b",
	})
	got, err := svc.branchesFromConnector(ctx, proj.ID, aliceC)
	if err != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("alice children=%+v %v", got, err)
	}
}
