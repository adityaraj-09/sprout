package config

import (
	"path/filepath"
	"testing"
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

func TestCLIDefaultsLoopbackDevToken(t *testing.T) {
	t.Setenv("SPROUT_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SPROUT_SERVER", "http://127.0.0.1:8080")
	t.Setenv("SPROUT_TOKEN", "")
	cfg := CLIDefaults()
	if cfg.Token != "dev-token" {
		t.Fatalf("loopback still defaults to dev-token, got %q", cfg.Token)
	}
}
