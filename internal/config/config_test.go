package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCLIDefaultsRemoteNoDevToken(t *testing.T) {
	t.Setenv("SPROUT_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SPROUT_SERVER", "http://strido.fit:8080")
	t.Setenv("SPROUT_TOKEN", "")
	cfg := CLIDefaults()
	if cfg.Token != "" {
		t.Fatalf("remote logout must not fall back to dev-token, got %q", cfg.Token)
	}
	if cfg.ServerURL != "http://strido.fit:8080" {
		t.Fatalf("server=%q", cfg.ServerURL)
	}
}

func TestParseSyncInterval(t *testing.T) {
	if d := ParseSyncInterval(""); d != time.Hour {
		t.Fatalf("empty default=%s", d)
	}
	if d := ParseSyncInterval("30m"); d != 30*time.Minute {
		t.Fatalf("30m=%s", d)
	}
	for _, off := range []string{"0", "off", "false", "none", "disabled"} {
		if d := ParseSyncInterval(off); d != 0 {
			t.Fatalf("%s should disable, got %s", off, d)
		}
	}
	if d := ParseSyncInterval("bogus"); d != time.Hour {
		t.Fatalf("invalid fallback=%s", d)
	}
}

func TestCLIDefaultsLoopbackDevToken(t *testing.T) {
	t.Setenv("SPROUT_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SPROUT_SERVER", "http://127.0.0.1:8080")
	t.Setenv("SPROUT_TOKEN", "")
	cfg := CLIDefaults()
	if cfg.Token != "dev-token" {
		t.Fatalf("loopback still defaults to dev-token, got %q", cfg.Token)
	}
}
