package branch

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
)

func TestPrepareConnectorSkipsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:55433")
	if err != nil {
		t.Skipf("cannot bind 55433: %v", err)
	}
	defer ln.Close()

	svc, proj := testService(t)
	c, err := svc.prepareConnectorRecord(context.Background(), proj.ID, "pgtest",
		"postgres://u@h/db", ModeLogical, engine.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port == 55433 {
		t.Fatal("allocator handed out a port that is already listening")
	}
	if c.Port == 0 {
		t.Fatal("expected a port")
	}
}

func TestPrepareConnectorReallocatesBusyExistingPort(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	svc, proj := testService(t)
	ctx := context.Background()
	existing := meta.Connector{
		ID: "c-pgtest", ProjectID: proj.ID, Name: "pgtest",
		Status: meta.ConnectorError, Port: busy, Password: "keep-me",
	}
	if err := svc.Store.PutConnector(ctx, existing); err != nil {
		t.Fatal(err)
	}

	c, err := svc.prepareConnectorRecord(ctx, proj.ID, "pgtest",
		"postgres://u@h/db", ModeLogical, engine.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port == busy {
		t.Fatalf("kept busy port %d", busy)
	}
	if c.Password != "keep-me" {
		t.Fatalf("password should be reused, got %q", c.Password)
	}
	fc := svc.Compute.(*fakeCompute)
	if len(fc.stopped) == 0 {
		t.Fatal("expected leftover compute stop before reallocating")
	}
	if postgres.PortListening(c.Port) {
		t.Fatalf("new port %d is listening", c.Port)
	}
}

func TestClearFailedBranchAllowsRetry(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	dir := filepath.Join(svc.Root, "branches", "feat-mongo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := meta.BranchRecord{
		ID: "failed", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusError, Port: 55499, DataDir: dir,
		SourceConnector: "mongo", SourceConnectorID: "c-mongo", CreatedBy: "alice",
		SnapshotRef: "sprout/data/replica-mongo@feat", ErrorMessage: "storage_failed",
	}
	if err := svc.Store.PutBranch(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := svc.clearFailedBranch(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.GetBranchByID(ctx, "failed"); err == nil {
		t.Fatal("failed branch record should be deleted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("failed branch datadir should be destroyed")
	}
	fc := svc.Compute.(*fakeCompute)
	if len(fc.stopped) == 0 {
		t.Fatal("expected leftover compute stop")
	}
}

func TestFailConnectorStopsCompute(t *testing.T) {
	svc, proj := testService(t)
	ctx := context.Background()
	c := meta.Connector{ID: "c1", ProjectID: proj.ID, Name: "pgtest", Port: 55434}
	if err := svc.Store.PutConnector(ctx, c); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.failConnector(ctx, c, fmt.Errorf("boom"))
	if err == nil || err.Error() != "boom" {
		t.Fatalf("got %v", err)
	}
	got, err := svc.Store.GetConnectorByID(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != meta.ConnectorError {
		t.Fatalf("status=%s", got.Status)
	}
	fc := svc.Compute.(*fakeCompute)
	if len(fc.stopped) != 1 {
		t.Fatalf("stopped=%v", fc.stopped)
	}
}
