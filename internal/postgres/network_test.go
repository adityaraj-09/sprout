package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceSectionIdempotent(t *testing.T) {
	const begin, end = "# --- begin ---", "# --- end ---"
	first := replaceSection("listen_addresses = 'localhost'\n", begin, end, "port = 1\n")
	second := replaceSection(first, begin, end, "port = 2\n")
	if strings.Count(second, begin) != 1 || strings.Count(second, end) != 1 {
		t.Fatalf("expected one managed block, got:\n%s", second)
	}
	if !strings.Contains(second, "port = 2") || strings.Contains(second, "port = 1") {
		t.Fatalf("body not replaced:\n%s", second)
	}
}

func TestApplyNetworkSettingsRemoteSCRAM(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "postgresql.conf"), []byte("# stock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pg_hba.conf"), []byte("local all all trust\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPROUT_PUBLIC_HOST", "db.example.com")
	t.Setenv("SPROUT_TRUST_REMOTE", "")
	t.Setenv("SPROUT_SAFE", "true")
	t.Setenv("SPROUT_PG_PROXY", "")
	if err := ApplyNetworkSettings(dir, 55440); err != nil {
		t.Fatal(err)
	}
	if err := ApplyNetworkSettings(dir, 55441); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	hba, _ := os.ReadFile(filepath.Join(dir, "pg_hba.conf"))
	if strings.Count(string(conf), netBegin) != 1 {
		t.Fatalf("network section not unique:\n%s", conf)
	}
	if !strings.Contains(string(conf), "port = 55441") {
		t.Fatalf("port not updated:\n%s", conf)
	}
	if !strings.Contains(string(conf), "127.0.0.2") {
		t.Fatalf("proxy backend listen missing:\n%s", conf)
	}
	if !strings.Contains(string(hba), "127.0.0.2/32 scram-sha-256") {
		t.Fatalf("expected proxy backend scram, got:\n%s", hba)
	}
	if strings.Contains(string(hba), "0.0.0.0/0") {
		t.Fatalf("unique ports should not be public when SNI proxy is on:\n%s", hba)
	}
}

func TestApplyNetworkSettingsTrustRemote(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "postgresql.conf"), []byte(""), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pg_hba.conf"), []byte(""), 0o600)
	t.Setenv("SPROUT_PUBLIC_HOST", "10.0.0.5")
	t.Setenv("SPROUT_TRUST_REMOTE", "true")
	if err := ApplyNetworkSettings(dir, 1); err != nil {
		t.Fatal(err)
	}
	hba, _ := os.ReadFile(filepath.Join(dir, "pg_hba.conf"))
	if !strings.Contains(string(hba), "0.0.0.0/0 trust") {
		t.Fatalf("expected remote trust:\n%s", hba)
	}
}

func TestFormatConnStringPasswordEncoding(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_DB_USER", "sprout")
	got := FormatConnString(55433, "postgres", "p@ss:word/x", "", "")
	if !strings.Contains(got, "p%40ss") && !strings.Contains(got, "p%40ss%3Aword") {
		if !strings.Contains(got, "sprout:") {
			t.Fatalf("missing userinfo: %s", got)
		}
	}
	if !strings.Contains(got, "55433") || !strings.Contains(got, "sprout") {
		t.Fatalf("unexpected url: %s", got)
	}
}

func TestFormatConnStringBranchSubdomain(t *testing.T) {
	t.Setenv("SPROUT_DB_USER", "sprout")
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	local := FormatConnString(55440, "postgres", "secret", "testdb", "lab")
	if !strings.Contains(local, "localhost:55440") {
		t.Fatalf("local host: %s", local)
	}
	if strings.Contains(local, "testdb.") || strings.Contains(local, "application_name") {
		t.Fatalf("localhost should be a plain host with no app name: %s", local)
	}

	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	fromLab := FormatConnString(55440, "postgres", "secret", "testdb", "lab")
	if !strings.Contains(fromLab, "testdb-lab.strido.fit:5432") {
		t.Fatalf("expected testdb-lab.strido.fit:5432, got %s", fromLab)
	}
	owned := FormatConnString(55440, "postgres", "secret", "testdb", "supabase", "alice")
	if !strings.Contains(owned, "testdb-alice-supabase.strido.fit:5432") {
		t.Fatalf("owned branch host: %s", owned)
	}
	fromSupa := FormatConnString(55441, "postgres", "secret", "testdb", "supabase")
	if !strings.Contains(fromSupa, "testdb-supabase.strido.fit:5432") {
		t.Fatalf("same branch name from another connector: %s", fromSupa)
	}
	namedLikeConn := FormatConnString(55442, "postgres", "secret", "lab", "lab")
	if !strings.Contains(namedLikeConn, "lab-lab.strido.fit:5432") {
		t.Fatalf("branch named like connector must not collide: %s", namedLikeConn)
	}
	if !strings.Contains(fromLab, "/postgres") || strings.Contains(fromLab, "application_name") {
		t.Fatalf("path should stay /postgres without application_name: %s", fromLab)
	}

	conn := FormatConnString(55434, "postgres", "secret", "lab", "")
	if !strings.Contains(conn, "lab.strido.fit:5432") {
		t.Fatalf("connector host: %s", conn)
	}

	t.Setenv("SPROUT_PG_PROXY", "false")
	direct := FormatConnString(55440, "postgres", "secret", "testdb", "lab")
	if !strings.Contains(direct, "testdb-lab.strido.fit:55440") {
		t.Fatalf("proxy off should keep instance port: %s", direct)
	}
	t.Setenv("SPROUT_PG_PROXY", "")

	t.Setenv("SPROUT_PUBLIC_HOST", "20.244.18.205")
	ip := FormatConnString(55440, "postgres", "secret", "testdb", "lab")
	if strings.Contains(ip, "testdb") && strings.Contains(ip, "20.244") {
		t.Fatalf("IP should not get a subdomain: %s", ip)
	}

	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "false")
	off := FormatConnString(55440, "postgres", "secret", "testdb", "lab")
	if strings.Contains(off, "testdb-lab.strido.fit") {
		t.Fatalf("subdomain disabled: %s", off)
	}
}

func TestAdvertiseHostReplicaPrefix(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "true")
	if got := AdvertiseHost("replica-sup", ""); got != "sup.strido.fit" {
		t.Fatalf("got %s", got)
	}
	if got := HostLabel("testdb", "lab"); got != "testdb-lab" {
		t.Fatalf("host label: %s", got)
	}
	if got := HostLabel("lab", "lab"); got != "lab-lab" {
		t.Fatalf("branch named like its connector: %s", got)
	}
	if got := HostLabel("testdb", "main"); got != "testdb" {
		t.Fatalf("main-sourced branch stays unsuffixed: %s", got)
	}
	if got := HostLabel("testdb", "supabase", "alice"); got != "testdb-alice-supabase" {
		t.Fatalf("owned branch: %s", got)
	}
	if got := HostLabel("supabase", "", "alice"); got != "supabase-alice" {
		t.Fatalf("owned connector: %s", got)
	}
	if got := ReplicaComputeName("supabase", "alice"); got != "replica-supabase-alice" {
		t.Fatalf("replica compute: %s", got)
	}
}

func TestTrustRemote(t *testing.T) {
	t.Setenv("SPROUT_TRUST_REMOTE", "")
	if TrustRemote() {
		t.Fatal("default must not trust remote")
	}
	t.Setenv("SPROUT_TRUST_REMOTE", "true")
	if !TrustRemote() {
		t.Fatal("expected trust remote")
	}
}
