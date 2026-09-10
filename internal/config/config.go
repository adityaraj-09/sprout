package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/cliconfig"
)

type Config struct {
	DataRoot     string
	Listen       string
	Token        string
	Compute      string // local|docker|auto
	ColdSnap     bool
	AutoResume   bool
	PublicHost   string
	MetaDB       string
	ServerURL    string        // CLI only
	SyncInterval time.Duration // logical apply cadence; 0 disables the ticker
	IdleSuspend  time.Duration // auto-suspend idle branches; 0 disables
}

func ServerDefaults() Config {
	root := envOr("SPROUT_DATA", "")
	if root == "" {
		wd, _ := os.Getwd()
		root = filepath.Join(wd, "data")
	}
	return Config{
		DataRoot:     root,
		Listen:       envOr("SPROUT_LISTEN", "127.0.0.1:8080"),
		Token:        envOr("SPROUT_TOKEN", "dev-token"),
		Compute:      envOr("SPROUT_COMPUTE", "auto"),
		ColdSnap:     envOr("SPROUT_COLD_SNAP", "true") != "false",
		AutoResume:   envOr("SPROUT_AUTO_RESUME", "") == "true",
		PublicHost:   envOr("SPROUT_PUBLIC_HOST", "localhost"),
		MetaDB:       "",
		SyncInterval: ParseSyncInterval(os.Getenv("SPROUT_SYNC_INTERVAL")),
		IdleSuspend:  ParseIdleSuspend(os.Getenv("SPROUT_IDLE_SUSPEND")),
	}
}

// ParseSyncInterval reads SPROUT_SYNC_INTERVAL.
// Empty defaults to 1h. 0 / off / false / none disables scheduled apply.
func ParseSyncInterval(raw string) time.Duration {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return time.Hour
	}
	switch s {
	case "0", "off", "false", "none", "disable", "disabled":
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Hour
	}
	return d
}

// ParseIdleSuspend reads SPROUT_IDLE_SUSPEND.
// Empty defaults to 15m. 0 / off / false / none disables auto-suspend.
func ParseIdleSuspend(raw string) time.Duration {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 15 * time.Minute
	}
	switch s {
	case "0", "off", "false", "none", "disable", "disabled":
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 15 * time.Minute
	}
	return d
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
