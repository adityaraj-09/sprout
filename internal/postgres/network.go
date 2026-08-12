package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PublicHost is the hostname advertised in connection strings.
// Set SPROUT_PUBLIC_HOST=db.example.com for remote clients.
func PublicHost() string {
	if h := strings.TrimSpace(os.Getenv("SPROUT_PUBLIC_HOST")); h != "" {
		return h
	}
	return "localhost"
}

// ListenAddresses is written into postgresql.conf.
// Default: 127.0.0.1 for local lab; "*" when SPROUT_PUBLIC_HOST is a real hostname.
// Override with SPROUT_PG_LISTEN (e.g. "*", "0.0.0.0", "10.0.0.5").
func ListenAddresses() string {
	if a := strings.TrimSpace(os.Getenv("SPROUT_PG_LISTEN")); a != "" {
		return a
	}
	h := PublicHost()
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return "127.0.0.1"
	}
	return "*"
}

// RemoteAccess reports whether Postgres is configured to accept non-loopback clients.
func RemoteAccess() bool {
	a := ListenAddresses()
	return a == "*" || a == "0.0.0.0" || a == "::" || (a != "127.0.0.1" && a != "localhost" && a != "::1")
}

// FormatConnString builds a libpq URL for a Sprout-managed instance.
func FormatConnString(port int, db string) string {
	if db == "" {
		db = "postgres"
	}
	return fmt.Sprintf("postgresql://%s@%s:%d/%s", DBUser(), PublicHost(), port, db)
}

// ApplyNetworkSettings appends listen/port overrides and opens pg_hba for TCP clients.
func ApplyNetworkSettings(dataDir string, port int) error {
	listen := ListenAddresses()
	conf := filepath.Join(dataDir, "postgresql.conf")
	f, err := os.OpenFile(conf, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, `
# --- sprout network ---
port = %d
listen_addresses = '%s'
unix_socket_directories = '%s'
`, port, listen, dataDir)
	_ = f.Close()
	if err != nil {
		return err
	}

	// Fast local demos (disable with SPROUT_SAFE=true).
	if os.Getenv("SPROUT_SAFE") != "true" {
		cf, err := os.OpenFile(conf, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = cf.WriteString(`
# --- sprout lab perf (SPROUT_SAFE=true to skip) ---
logging_collector = off
fsync = off
synchronous_commit = off
full_page_writes = off
`)
		_ = cf.Close()
		if err != nil {
			return err
		}
	}

	if !RemoteAccess() {
		return nil
	}
	// Lab / early product: trust over TCP when exposing publicly.
	// Replace with scram + roles before any real multi-tenant deploy.
	hba := filepath.Join(dataDir, "pg_hba.conf")
	hf, err := os.OpenFile(hba, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer hf.Close()
	_, err = hf.WriteString(`
# --- sprout remote (SPROUT_PUBLIC_HOST / SPROUT_PG_LISTEN) ---
host all all 0.0.0.0/0 trust
host all all ::/0 trust
`)
	return err
}
