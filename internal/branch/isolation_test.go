package branch

import (
	"context"
	"os"
	"path/filepath"
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

func TestFindSeedReplicaSamePrimary(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	seedDir := filepath.Join(svc.Root, "replicas", "supabase-alice")
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c-a", ProjectID: proj.ID, Name: "supabase", CreatedBy: "alice",
		Status: meta.ConnectorReplicating, DataDir: seedDir, Port: 55434,
		PrimaryURL: "postgresql://postgres:old@db.example.supabase.co:5432/postgres",
	})
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c-self", ProjectID: proj.ID, Name: "testing", CreatedBy: "bob",
		Status:     meta.ConnectorBootstrapping,
		PrimaryURL: "postgresql://postgres:new@db.example.supabase.co:5432/postgres",
	})
	seed, ok := svc.findSeedReplica(ctx, proj.ID, "postgresql://postgres:new@db.example.supabase.co:5432/postgres", "c-self")
	if !ok || seed.ID != "c-a" {
		t.Fatalf("expected alice seed, got ok=%v %+v", ok, seed)
	}
	if _, ok := svc.findSeedReplica(ctx, proj.ID, "postgresql://postgres:x@other.example:5432/postgres", "c-self"); ok {
		t.Fatal("other host must not match")
	}
}

func TestRefuseBranchWhileLogicalCopy(t *testing.T) {
	svc, proj := testService(t)
	alice := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindGitHub, Login: "alice"})
	_ = svc.Store.PutConnector(alice, meta.Connector{
		ID: "c1", ProjectID: proj.ID, Name: "check", CreatedBy: "alice",
		Mode: ModeLogical, Status: meta.ConnectorBootstrapping, Port: 55433,
	})
	err := svc.ensureSourceReadyForBranch(alice, proj.ID, "check", 55433)
	if err == nil || !strings.Contains(err.Error(), "source_not_ready") || !strings.Contains(err.Error(), "still copying") {
		t.Fatalf("expected copy-in-progress error, got %v", err)
	}

	_ = svc.Store.UpdateConnector(alice, meta.Connector{
		ID: "c1", ProjectID: proj.ID, Name: "check", CreatedBy: "alice",
		Mode: ModeLogical, Status: meta.ConnectorReplicating, Port: 55433,
	})
	if err := svc.ensureSourceReadyForBranch(alice, proj.ID, "check", 55433); err != nil {
		t.Fatalf("replicating connector should be allowed when status cannot be queried: %v", err)
	}
	if err := svc.ensureSourceReadyForBranch(alice, proj.ID, "main", 55432); err != nil {
		t.Fatalf("main should skip logical checks: %v", err)
	}
}

func TestMongoRejectsPhysical(t *testing.T) {
	svc, proj := testService(t)
	_, err := svc.Connect(context.Background(), proj.ID, ConnectOpts{
		Name: "atlas", Engine: "mongodb", Mode: ModePhysical, URL: "mongodb://u@h:27017/shop",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_mode") {
		t.Fatalf("expected invalid_mode, got %v", err)
	}
}

func TestInferMongoEngine(t *testing.T) {
	svc, proj := testService(t)
	_, err := svc.Connect(context.Background(), proj.ID, ConnectOpts{
		Name: "atlas", URL: "mongodb://u@127.0.0.1:1/shop", Mode: ModeLogical,
	})
	if err == nil {
		t.Fatal("expected connect to fail without mongod tools / ping")
	}
	if strings.Contains(err.Error(), "invalid_engine") || strings.Contains(err.Error(), "url scheme must be postgres") {
		t.Fatalf("mongodb URL should be accepted as mongodb engine, got %v", err)
	}
}
