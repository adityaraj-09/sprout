package mongo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	c, err := ParseURL("mongodb://alice:s3cret@db.example.com:27018/shop?tls=true")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "db.example.com" || c.Port != 27018 || c.User != "alice" || c.Database != "shop" || !c.TLS {
		t.Fatalf("%+v", c)
	}
	if _, err := ParseURL("postgresql://u@h/db"); err == nil {
		t.Fatal("postgres scheme must fail")
	}
	c, err = ParseURL("mongodb+srv://user:pass@cluster0.mongodb.net/app")
	if err != nil || !c.SRV || c.Host != "cluster0.mongodb.net" || c.Database != "app" {
		t.Fatalf("srv: %+v %v", c, err)
	}
}

func TestFormatConnStringUniqueTLSPort(t *testing.T) {
	t.Setenv("SPROUT_DB_USER", "sprout")
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	t.Setenv("SPROUT_MONGO_PROXY", "")
	local := FormatConnString(55461, "shop", "secret", "feat", "atlas")
	if !strings.Contains(local, "localhost:55461") {
		t.Fatalf("local: %s", local)
	}
	if !strings.Contains(local, "tls=true") || !strings.Contains(local, "authSource=admin") {
		t.Fatalf("tls missing: %s", local)
	}

	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	hosted := FormatConnString(55461, "shop", "secret", "feat", "atlas")
	if !strings.Contains(hosted, "feat-atlas.strido.fit:27017") {
		t.Fatalf("expected SNI proxy port, got %s", hosted)
	}

	t.Setenv("SPROUT_MONGO_PROXY", "false")
	direct := FormatConnString(55461, "shop", "secret", "feat", "atlas")
	if !strings.Contains(direct, "feat-atlas.strido.fit:55461") {
		t.Fatalf("proxy off should keep unique port, got %s", direct)
	}
}

func TestPrepareCloneRewritesPortAndDropsLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPROUT_DATA", dir)
	if err := os.WriteFile(filepath.Join(dir, "mongod.lock"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "WiredTiger.lock"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mongod.pid"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{DataDir: dir, Port: 55461, LogFile: filepath.Join(dir, "mongod.log")}
	if err := inst.PrepareClone(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mongod.lock")); !os.IsNotExist(err) {
		t.Fatal("mongod.lock should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "WiredTiger.lock")); !os.IsNotExist(err) {
		t.Fatal("WiredTiger.lock should be removed")
	}
	body, err := os.ReadFile(filepath.Join(dir, "mongod.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "port: 55461") {
		t.Fatalf("mongod.conf:\n%s", body)
	}
	if !strings.Contains(string(body), "authorization: enabled") {
		t.Fatalf("auth missing:\n%s", body)
	}
	if !strings.Contains(string(body), "requireTLS") {
		t.Fatalf("tls missing:\n%s", body)
	}
	if !strings.Contains(string(body), "CAFile:") {
		t.Fatalf("CAFile missing (MongoDB 7+):\n%s", body)
	}
}

func TestLocalRestoreURI(t *testing.T) {
	u := localRestoreURI(55433)
	if !strings.Contains(u, "127.0.0.1:55433") || !strings.Contains(u, "tls=true") {
		t.Fatalf("%s", u)
	}
}

func TestToolTLSFlagsPreferSSLOnDatabaseTools(t *testing.T) {
	p, err := exec.LookPath("mongorestore")
	if err != nil {
		t.Skip("mongorestore not installed")
	}
	flags := toolTLSFlags(p)
	if len(flags) == 0 {
		t.Fatal("no tls flags")
	}
}

func TestHasDataDir(t *testing.T) {
	dir := t.TempDir()
	if HasDataDir(dir) {
		t.Fatal("empty")
	}
	if err := os.WriteFile(filepath.Join(dir, "WiredTiger.wt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasDataDir(dir) {
		t.Fatal("WiredTiger.wt")
	}
	pg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pg, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasDataDir(pg) {
		t.Fatal("postgres dir is not mongo")
	}
}
