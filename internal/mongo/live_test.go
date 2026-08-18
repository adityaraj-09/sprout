package mongo

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireMongoTools(t *testing.T) Binaries {
	t.Helper()
	b, err := LookBinaries()
	if err != nil {
		t.Skip(err.Error())
	}
	return b
}

func waitTCP(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s", addr)
}

func startPlainMongod(t *testing.T, bins Binaries, dir string, port int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "mongod.log")
	cmd := exec.Command(bins.Mongod, "--dbpath", dir, "--port", strconv.Itoa(port),
		"--bind_ip", "127.0.0.1", "--fork", "--logpath", log)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source mongod: %v (%s)", err, out)
	}
	waitTCP(t, port, 15*time.Second)
	t.Cleanup(func() {
		_ = exec.Command(bins.Mongosh, "--quiet", "--host", "127.0.0.1", "--port", strconv.Itoa(port),
			"--eval", "db.getSiblingDB('admin').shutdownServer()").Run()
	})
}

func mongoshEval(t *testing.T, bins Binaries, args []string, js string) string {
	t.Helper()
	cmd := exec.Command(bins.Mongosh, append(append([]string{"--quiet"}, args...), "--eval", js)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mongosh %v: %v (%s)", args, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

func TestLiveDumpRestoreCloneAndAuth(t *testing.T) {
	bins := requireMongoTools(t)
	t.Setenv("SPROUT_DATA", t.TempDir())
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_DB_USER", "sprout")

	srcPort := 27098
	dstPort := 27099
	clonePort := 27100
	srcDir := t.TempDir()
	startPlainMongod(t, bins, srcDir, srcPort)

	mongoshEval(t, bins, []string{"--host", "127.0.0.1", "--port", strconv.Itoa(srcPort)},
		`db.getSiblingDB('shop').orders.insertOne({sku:'sku-1', n: 42})`)

	dstDir := t.TempDir()
	dst := &Instance{
		Name: "atlas", DataDir: dstDir, Port: dstPort,
		LogFile: filepath.Join(dstDir, "mongod.log"), Bins: bins, Password: "s3cret-pass",
	}
	if err := dst.Init(); err != nil {
		t.Fatal(err)
	}
	if err := dst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Stop() })

	conn, err := ParseURL(fmt.Sprintf("mongodb://127.0.0.1:%d/shop", srcPort))
	if err != nil {
		t.Fatal(err)
	}
	if err := bins.DumpImport(context.Background(), conn, dst, nil); err != nil {
		t.Fatal(err)
	}
	if err := dst.EnsureAppRoles(); err != nil {
		t.Fatal(err)
	}

	got := mongoshEval(t, bins, []string{
		"--tls", "--tlsAllowInvalidCertificates",
		"--host", "127.0.0.1", "--port", strconv.Itoa(dstPort),
		"-u", "sprout", "-p", "s3cret-pass", "--authenticationDatabase", "admin",
	}, `db.getSiblingDB('shop').orders.findOne({sku:'sku-1'}).n`)
	if !strings.Contains(got, "42") {
		t.Fatalf("restored doc missing, mongosh said %q", got)
	}

	// Reconcile/resume path: Start without password after auth is on.
	if err := dst.Stop(); err != nil {
		t.Fatal(err)
	}
	resumed := &Instance{DataDir: dstDir, Port: dstPort, LogFile: dst.LogFile, Bins: bins}
	if err := resumed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resumed.Stop() })

	cloneDir := t.TempDir()
	// Simulate CoW: copy files while source is fsyncLocked.
	dst.Password = "s3cret-pass"
	if err := dst.LockForSnapshot(); err != nil {
		t.Fatal(err)
	}
	cp := exec.Command("cp", "-a", dstDir+"/.", cloneDir)
	if out, err := cp.CombinedOutput(); err != nil {
		_ = dst.UnlockForSnapshot()
		t.Fatalf("cp: %v (%s)", err, out)
	}
	if err := dst.UnlockForSnapshot(); err != nil {
		t.Fatal(err)
	}

	clone := &Instance{
		Name: "feat", Source: "atlas", DataDir: cloneDir, Port: clonePort,
		LogFile: filepath.Join(cloneDir, "mongod.log"), Bins: bins, Password: "s3cret-pass",
	}
	if err := clone.PrepareClone(); err != nil {
		t.Fatal(err)
	}
	if err := clone.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Stop() })
	if err := clone.EnsureAppRoles(); err != nil {
		t.Fatal(err)
	}
	got = mongoshEval(t, bins, []string{
		"--tls", "--tlsAllowInvalidCertificates",
		"--host", "127.0.0.1", "--port", strconv.Itoa(clonePort),
		"-u", "sprout", "-p", "s3cret-pass", "--authenticationDatabase", "admin",
	}, `db.getSiblingDB('shop').orders.findOne({sku:'sku-1'}).n`)
	if !strings.Contains(got, "42") {
		t.Fatalf("cloned doc missing, mongosh said %q", got)
	}
	uri := clone.ConnString("shop")
	if strings.Contains(uri, ":27017") || !strings.Contains(uri, "tls=true") {
		t.Fatalf("clone url: %s", uri)
	}
}
