package qdrant

import (
	"context"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireQdrant(t *testing.T) Binaries {
	t.Helper()
	b, err := LookBinaries()
	if err != nil {
		t.Skip(err.Error())
	}
	return b
}

func TestLiveSnapshotCloneAndAuth(t *testing.T) {
	bins := requireQdrant(t)
	t.Setenv("SPROUT_DATA", t.TempDir())
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	t.Setenv("SPROUT_QDRANT_PROXY", "")

	srcPort := freeTestPort(t)
	dstPort := freeTestPort(t)
	clonePort := freeTestPort(t)

	srcDir := t.TempDir()
	src := &Instance{
		Name: "src", DataDir: srcDir, Port: srcPort,
		LogFile: filepath.Join(srcDir, "qdrant.log"), Bins: bins, Password: "src-key",
	}
	if err := src.Init(); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	srcCli := src.LocalClient()
	if err := srcCli.do(ctx, http.MethodPut, "/collections/docs", map[string]any{
		"vectors": map[string]any{"size": 2, "distance": "Cosine"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := srcCli.do(ctx, http.MethodPut, "/collections/docs/points?wait=true", map[string]any{
		"points": []map[string]any{
			{"id": 1, "vector": []float32{0.1, 0.2}, "payload": map[string]any{"sku": "sku-1"}},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	dstDir := t.TempDir()
	dst := &Instance{
		Name: "vectors", DataDir: dstDir, Port: dstPort,
		LogFile: filepath.Join(dstDir, "qdrant.log"), Bins: bins, Password: "s3cret-pass",
	}
	if err := dst.Init(); err != nil {
		t.Fatal(err)
	}
	if err := dst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Stop() })

	conn := Conn{Host: "127.0.0.1", Port: srcPort, APIKey: "src-key"}
	if err := DumpImport(ctx, conn, dst, nil); err != nil {
		t.Fatal(err)
	}
	if err := dst.EnsureAppRoles(); err != nil {
		t.Fatal(err)
	}
	counts, err := dst.CollectionCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["docs"] != 1 {
		t.Fatalf("restored counts=%v", counts)
	}

	if err := dst.Stop(); err != nil {
		t.Fatal(err)
	}
	resumed := &Instance{DataDir: dstDir, Port: dstPort, LogFile: dst.LogFile, Bins: bins, Password: "s3cret-pass"}
	if err := resumed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resumed.Stop() })

	cloneDir := t.TempDir()
	if err := dst.Stop(); err != nil {
		t.Fatal(err)
	}
	cp := exec.Command("cp", "-a", dstDir+"/.", cloneDir)
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("cp: %v (%s)", err, out)
	}
	if err := dst.Start(); err != nil {
		t.Fatal(err)
	}

	clone := &Instance{
		Name: "feat", Source: "vectors", DataDir: cloneDir, Port: clonePort,
		LogFile: filepath.Join(cloneDir, "qdrant.log"), Bins: bins, Password: "s3cret-pass",
	}
	if err := clone.PrepareClone(); err != nil {
		t.Fatal(err)
	}
	if err := clone.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Stop() })
	got, err := clone.CollectionCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["docs"] != 1 {
		t.Fatalf("cloned counts=%v", got)
	}
	uri := clone.ConnString()
	if strings.Contains(uri, ":6333") && !strings.Contains(uri, strconv.Itoa(clonePort)) {
		t.Fatalf("clone url: %s", uri)
	}
	if !strings.Contains(uri, "api-key=s3cret-pass") {
		t.Fatalf("api-key missing: %s", uri)
	}

	// Mutate the clone; source must stay at 1 point.
	if err := clone.LocalClient().do(ctx, http.MethodPut, "/collections/docs/points?wait=true", map[string]any{
		"points": []map[string]any{
			{"id": 2, "vector": []float32{0.3, 0.4}, "payload": map[string]any{"sku": "sku-2"}},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	srcCounts, err := src.CollectionCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if srcCounts["docs"] != 1 {
		t.Fatalf("source should be unchanged, got %v", srcCounts)
	}
	cloneCounts, err := clone.CollectionCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cloneCounts["docs"] != 2 {
		t.Fatalf("clone should have 2 points, got %v", cloneCounts)
	}
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
