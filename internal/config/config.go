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
	PublicHost string
	MetaDB     string
	ServerURL  string // CLI only
}

func ServerDefaults() Config {
	root := envOr("SPROUT_DATA", "")
	if root == "" {
		wd, _ := os.Getwd()
		root = filepath.Join(wd, "data")
	}
	return Config{
		DataRoot:   root,
		Listen:     envOr("SPROUT_LISTEN", "127.0.0.1:8080"),
		Token:      envOr("SPROUT_TOKEN", "dev-token"),
		Compute:    envOr("SPROUT_COMPUTE", "auto"),
		ColdSnap:   envOr("SPROUT_COLD_SNAP", "true") != "false",
		PublicHost: envOr("SPROUT_PUBLIC_HOST", "localhost"),
		MetaDB:     "",
	}
}

func (c Config) MetaPath() string {
	if c.MetaDB != "" {
		return c.MetaDB
	}
	return filepath.Join(c.DataRoot, "control.db")
}

func CLIDefaults() Config {
	return Config{
		ServerURL: envOr("SPROUT_SERVER", "http://127.0.0.1:8080"),
		Token:     envOr("SPROUT_TOKEN", "dev-token"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
