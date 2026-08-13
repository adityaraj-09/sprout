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
	if strings.Count(string(hba), "0.0.0.0/0") != 1 {
		t.Fatalf("expected one remote hba block:\n%s", hba)
	}
	if !strings.Contains(string(hba), "scram-sha-256") {
		t.Fatalf("expected scram, got:\n%s", hba)
	}
	if strings.Contains(string(hba), "trust") && strings.Contains(string(hba), "0.0.0.0/0") {
		// stock local trust is fine; remote must not be trust
		for _, line := range strings.Split(string(hba), "\n") {
			if strings.Contains(line, "0.0.0.0/0") && strings.Contains(line, "trust") {
				t.Fatalf("remote trust line: %s", line)
			}
		}
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
	got := FormatConnString(55433, "postgres", "p@ss:word/x")
	if !strings.Contains(got, "p%40ss") && !strings.Contains(got, "p%40ss%3Aword") {
		if !strings.Contains(got, "sprout:") {
			t.Fatalf("missing userinfo: %s", got)
		}
	}
	if !strings.Contains(got, "55433") || !strings.Contains(got, "sprout") {
		t.Fatalf("unexpected url: %s", got)
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
