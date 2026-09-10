package qdrant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	c, err := ParseURL("qdrants://:s3cret@xxx.us-east.aws.cloud.qdrant.io:6333")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "xxx.us-east.aws.cloud.qdrant.io" || c.Port != 6333 || c.APIKey != "s3cret" || !c.TLS {
		t.Fatalf("%+v", c)
	}
	c, err = ParseURL("http://127.0.0.1:6333?api-key=abc")
	if err != nil || c.APIKey != "abc" || c.TLS || c.Port != 6333 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ParseURL("postgresql://u@h/db"); err == nil {
		t.Fatal("postgres scheme must fail")
	}
	c, err = ParseURL("https://user:pass@qdrant.example.com")
	if err != nil || c.APIKey != "pass" || !c.TLS || c.Port != 6333 {
		t.Fatalf("https: %+v %v", c, err)
	}
}

func TestFormatConnStringProxyPort(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	t.Setenv("SPROUT_QDRANT_PROXY", "")
	local := FormatConnString(55480, "secret", "feat", "vectors")
	if !strings.Contains(local, "localhost:55480") {
		t.Fatalf("local: %s", local)
	}
	if !strings.Contains(local, "api-key=secret") {
		t.Fatalf("api-key missing: %s", local)
	}
	if strings.HasPrefix(local, "https://") {
		t.Fatalf("local should be http: %s", local)
	}

	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	hosted := FormatConnString(55480, "secret", "feat", "vectors")
	if !strings.Contains(hosted, "feat-vectors.strido.fit:6333") {
		t.Fatalf("expected SNI proxy port, got %s", hosted)
	}
	if !strings.HasPrefix(hosted, "https://") {
		t.Fatalf("proxy should advertise https: %s", hosted)
	}

	t.Setenv("SPROUT_QDRANT_PROXY", "false")
	direct := FormatConnString(55480, "secret", "feat", "vectors")
	if !strings.Contains(direct, "feat-vectors.strido.fit:55480") {
		t.Fatalf("proxy off should keep unique port, got %s", direct)
	}
}

func TestPrepareCloneRewritesPort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPROUT_DATA", dir)
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	t.Setenv("SPROUT_QDRANT_PROXY", "")
	if err := os.MkdirAll(filepath.Join(dir, "storage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qdrant.pid"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "storage", ".lock"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{DataDir: dir, Port: 55480, LogFile: filepath.Join(dir, "qdrant.log"), Password: "s3cret"}
	if err := inst.PrepareClone(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "qdrant.pid")); !os.IsNotExist(err) {
		t.Fatal("qdrant.pid should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", ".lock")); !os.IsNotExist(err) {
		t.Fatal(".lock should be removed")
	}
	body, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "http_port: 55480") {
		t.Fatalf("config.yaml:\n%s", body)
	}
	if !strings.Contains(string(body), "api_key:") {
		t.Fatalf("api_key missing:\n%s", body)
	}
	if !strings.Contains(string(body), "s3cret") {
		t.Fatalf("password missing:\n%s", body)
	}
}

func TestHasDataDir(t *testing.T) {
	dir := t.TempDir()
	if HasDataDir(dir) {
		t.Fatal("empty")
	}
	if err := os.WriteFile(filepath.Join(dir, engineMarker), []byte("qdrant\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasDataDir(dir) {
		t.Fatal("marker")
	}
	pg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pg, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasDataDir(pg) {
		t.Fatal("postgres dir is not qdrant")
	}
}

func TestStopReturnsImmediatelyWhenNotRunning(t *testing.T) {
	inst := &Instance{DataDir: t.TempDir(), Port: 1}
	if err := inst.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestCurlOneLiner(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	line := CurlOneLiner(55480, "s3cret", "feat", "vectors")
	if !strings.Contains(line, "curl") || !strings.Contains(line, "api-key: s3cret") || !strings.Contains(line, "/collections") {
		t.Fatalf("%s", line)
	}
}
