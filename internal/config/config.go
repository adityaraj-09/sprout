package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/adityaraj/sprout/internal/cliconfig"
)

type Config struct {
	DataRoot   string
	Listen     string
	Token      string
	Compute    string // local|docker|auto
	ColdSnap   bool
	AutoResume bool
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
		AutoResume: envOr("SPROUT_AUTO_RESUME", "") == "true",
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
	file := cliconfig.Load()
	server := strings.TrimSpace(os.Getenv("SPROUT_SERVER"))
	if server == "" {
		server = file.APIUrl
	}
	if server == "" {
		server = "http://127.0.0.1:8080"
	}
	token := strings.TrimSpace(os.Getenv("SPROUT_TOKEN"))
	if token == "" {
		token = file.Token
	}
	if token == "" && isLoopbackAPI(server) {
		token = "dev-token"
	}
	return Config{
		ServerURL: strings.TrimRight(server, "/"),
		Token:     token,
	}
}

func isLoopbackAPI(server string) bool {
	s := strings.ToLower(strings.TrimSpace(server))
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	host := s
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
