package branch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/meta"
)

func githubCtx(login string) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindGitHub, Login: login})
}

func TestEnsureDefaultOrgOnLogin(t *testing.T) {
	svc, _ := testService(t)
	alice := githubCtx("alice")
	org, err := svc.EnsureDefaultOrg(alice)
	if err != nil {
		t.Fatal(err)
	}
	if org.Name != meta.DefaultOrg || org.CreatedBy != "alice" || org.Role != meta.OrgRoleOwner {
		t.Fatalf("%+v", org)
	}
	again, err := svc.EnsureDefaultOrg(alice)
	if err != nil || again.ID != org.ID {
		t.Fatalf("idempotent default org: %+v %v", again, err)
	}
	if err := svc.DeleteOrg(alice, meta.DefaultOrg); err == nil || !strings.Contains(err.Error(), "cannot delete") {
		t.Fatalf("expected cannot delete default org, got %v", err)
	}
}

func TestOrgMemberSharesConnectorNotCopy(t *testing.T) {
	svc, proj := testService(t)
	alice := githubCtx("alice")
	org, err := svc.EnsureDefaultOrg(alice)
	if err != nil {
		t.Fatal(err)
	}
	alice = auth.WithOrg(alice, org.ID, org.Name)
	dir := filepath.Join(svc.Root, "replicas", "prod-alice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	conn := meta.Connector{
		ID: "c-a", ProjectID: proj.ID, Name: "prod", CreatedBy: "alice", OrgID: org.ID,
		Status: meta.ConnectorReplicating, DataDir: dir, Port: 55434,
	}
	if err := svc.Store.PutConnector(alice, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddOrgMember(alice, org.Name, "bob"); err != nil {
		t.Fatal(err)
	}

	bob := auth.WithOrg(githubCtx("bob"), org.ID, org.Name)
	list, err := svc.ListConnectors(bob)
	if err != nil || len(list) != 1 || list[0].ID != "c-a" || list[0].DataDir != dir {
		t.Fatalf("bob should see alice's replica row, got %+v %v", list, err)
	}

	_, err = svc.Connect(bob, proj.ID, ConnectOpts{Name: "other", URL: "postgresql://x@h/db"})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("member connect: %v", err)
	}
	if err := svc.DeleteConnector(bob, proj.ID, "prod", true); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("member delete connector: %v", err)
	}
}

func TestOrgMemberCannotMutateOthersBranch(t *testing.T) {
	svc, proj := testService(t)
	alice := githubCtx("alice")
	org, err := svc.EnsureDefaultOrg(alice)
	if err != nil {
		t.Fatal(err)
	}
	alice = auth.WithOrg(alice, org.ID, org.Name)
	if _, err := svc.AddOrgMember(alice, org.Name, "bob"); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.PutBranch(alice, meta.BranchRecord{
		ID: "a", ProjectID: proj.ID, Name: "feat", Role: "branch", CreatedBy: "alice", OrgID: org.ID,
		Status: meta.StatusIdle, SourceConnector: "prod",
	})
	_ = svc.Store.PutBranch(alice, meta.BranchRecord{
		ID: "b", ProjectID: proj.ID, Name: "mine", Role: "branch", CreatedBy: "bob", OrgID: org.ID,
		Status: meta.StatusIdle, SourceConnector: "prod",
	})

	bob := auth.WithOrg(githubCtx("bob"), org.ID, org.Name)
	list, err := svc.List(bob, proj.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("member should list org branches, got %+v %v", list, err)
	}
	if err := svc.Delete(bob, proj.ID, "feat", "prod"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("member delete alice branch: %v", err)
	}
	if err := svc.Delete(alice, proj.ID, "mine", "prod"); err != nil {
		t.Fatalf("owner should delete member branch: %v", err)
	}
	if err := svc.Delete(bob, proj.ID, "feat", "prod"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("still forbidden after owner delete of mine: %v", err)
	}
}

func TestDoctorReportsDiskAndHung(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	replica := filepath.Join(svc.Root, "replicas", "prod-alice")
	if err := os.MkdirAll(replica, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replica, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(svc.Root, "replicas", "ghost")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = svc.Store.PutConnector(ctx, meta.Connector{
		ID: "c1", ProjectID: proj.ID, Name: "prod", Status: meta.ConnectorReplicating,
		DataDir: replica,
	})
	rep := svc.Doctor(ctx)
	by := map[string]DoctorCheck{}
	for _, c := range rep.Checks {
		by[c.Name] = c
	}
	if c, ok := by["disk_mismatch"]; !ok || !strings.Contains(c.Detail, "ghost") {
		t.Fatalf("disk_mismatch=%+v", by["disk_mismatch"])
	}
	if c, ok := by["disk"]; !ok || !c.OK {
		t.Fatalf("disk=%+v", c)
	}
}

func TestHungJobsDetectsStaleCreating(t *testing.T) {
	got := hungJobs([]meta.Connector{{
		Name: "prod", Status: meta.ConnectorBootstrapping, UpdatedAt: time.Now().Add(-30 * time.Minute),
	}}, []meta.BranchRecord{{
		Name: "feat", Status: meta.StatusCreating, UpdatedAt: time.Now().Add(-15 * time.Minute),
	}}, hungAfter)
	if len(got) != 2 {
		t.Fatalf("%v", got)
	}
	fresh := hungJobs([]meta.Connector{{
		Name: "prod", Status: meta.ConnectorBootstrapping, UpdatedAt: time.Now(),
	}}, nil, hungAfter)
	if len(fresh) != 0 {
		t.Fatalf("fresh should not hang: %v", fresh)
	}
}
