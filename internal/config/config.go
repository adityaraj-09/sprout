package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	DataRoot   string
	Listen     string
	Token      string
	Compute    string // local|docker|auto
	ColdSnap   bool
	MetaDB     string
	ServerURL  string // CLI only
}

func ServerDefaults() Config {
	root := envOr("ARDENT_DATA", "")
	if root == "" {
		wd, _ := os.Getwd()
		root = filepath.Join(wd, "data")
	}
	return Config{
		DataRoot: root,
		Listen:   envOr("ARDENT_LISTEN", "127.0.0.1:8080"),
		Token:    envOr("ARDENT_TOKEN", "dev-token"),
		Compute:  envOr("ARDENT_COMPUTE", "auto"),
		ColdSnap: envOr("ARDENT_COLD_SNAP", "true") != "false",
		MetaDB:   "", // filled as DataRoot/control.db
	}
}

func (c Config) MetaPath() string {
	if c.MetaDB != "" {
		return c.MetaDB
	}
	// Phase 2: JSON control plane notebook (Store interface → swap to SQLite later).
	return filepath.Join(c.DataRoot, "control.json")
}

func CLIDefaults() Config {
	return Config{
		ServerURL: envOr("ARDENT_SERVER", "http://127.0.0.1:8080"),
		Token:     envOr("ARDENT_TOKEN", "dev-token"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
