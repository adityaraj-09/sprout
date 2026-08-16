package mysql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	c, err := ParseURL("mysql://alice:s3cret@db.example.com:3307/app?ssl-mode=REQUIRED")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "db.example.com" || c.Port != 3307 || c.User != "alice" || c.Database != "app" {
		t.Fatalf("%+v", c)
	}
	if _, err := ParseURL("postgresql://u@h/db"); err == nil {
		t.Fatal("postgres scheme must fail")
	}
	c, err = ParseURL("mariadb://root@127.0.0.1/shop")
	if err != nil || c.Port != 3306 || c.SSLMode != "DISABLED" {
		t.Fatalf("mariadb local: %+v %v", c, err)
	}
}

func TestPrepareCloneRewritesPortAndDropsUUID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auto.cnf"), []byte("[auto]\nserver-uuid=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mysqld.pid"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{DataDir: dir, Port: 33061, LogFile: filepath.Join(dir, "err.log")}
	if err := inst.PrepareClone(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auto.cnf")); !os.IsNotExist(err) {
		t.Fatal("auto.cnf should be removed so mysqld mints a new server-uuid")
	}
	body, err := os.ReadFile(filepath.Join(dir, "my.cnf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "port=33061") {
		t.Fatalf("my.cnf:\n%s", body)
	}
	if !strings.Contains(string(body), "default_authentication_plugin=mysql_native_password") {
		t.Fatalf("native password plugin missing:\n%s", body)
	}
}

func TestFormatConnStringProxyPort(t *testing.T) {
	t.Setenv("SPROUT_DB_USER", "sprout")
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	local := FormatConnString(33061, "shop", "secret", "feat", "shop")
	if !strings.Contains(local, "localhost:33061") {
		t.Fatalf("local: %s", local)
	}

	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	proxied := FormatConnString(33061, "shop", "secret", "feat", "shop")
	if !strings.Contains(proxied, "feat-shop.strido.fit:3306") {
		t.Fatalf("expected :3306 host, got %s", proxied)
	}
	if !strings.Contains(proxied, "ssl-mode=REQUIRED") {
		t.Fatalf("ssl-mode missing: %s", proxied)
	}
	line := MysqlOneLiner(33061, "secret", "feat", "shop")
	if !strings.Contains(line, "--ssl-mode=REQUIRED") || !strings.Contains(line, "-P 3306") {
		t.Fatalf("one-liner: %s", line)
	}

	t.Setenv("SPROUT_MYSQL_PROXY", "false")
	direct := FormatConnString(33061, "shop", "secret", "feat", "shop")
	if !strings.Contains(direct, "feat-shop.strido.fit:33061") {
		t.Fatalf("proxy off: %s", direct)
	}
}

func TestHasDataDir(t *testing.T) {
	dir := t.TempDir()
	if HasDataDir(dir) {
		t.Fatal("empty")
	}
	if err := os.WriteFile(filepath.Join(dir, "ibdata1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasDataDir(dir) {
		t.Fatal("ibdata1")
	}
	pg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pg, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasDataDir(pg) {
		t.Fatal("postgres dir is not mysql")
	}
}
