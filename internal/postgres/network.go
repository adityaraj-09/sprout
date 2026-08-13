package postgres

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	netBegin    = "# --- sprout network begin ---"
	netEnd      = "# --- sprout network end ---"
	perfBegin   = "# --- sprout lab perf begin ---"
	perfEnd     = "# --- sprout lab perf end ---"
	remoteBegin = "# --- sprout remote begin ---"
	remoteEnd   = "# --- sprout remote end ---"
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
// name is the branch/connector/main id: used as DNS label when subdomains
// are on, and as application_name (valid libpq param; does not change the DB).
func FormatConnString(port int, db, password, name string) string {
	if db == "" {
		db = "postgres"
	}
	label := DNSLabel(name)
	u := url.URL{
		Scheme: "postgresql",
		Host:   net.JoinHostPort(AdvertiseHost(label), strconv.Itoa(port)),
		Path:   "/" + db,
	}
	if password != "" {
		u.User = url.UserPassword(DBUser(), password)
	} else {
		u.User = url.User(DBUser())
	}
	if label != "" {
		q := url.Values{}
		q.Set("application_name", label)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// DNSLabel turns an instance name into a single DNS / application_name label.
func DNSLabel(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "replica-")
	name = strings.ToLower(name)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// BranchSubdomain reports whether advertised hosts should be <name>.<public host>.
// Auto-on when SPROUT_PUBLIC_HOST is a DNS name (not localhost / IP).
// Override with SPROUT_BRANCH_SUBDOMAIN=true|false.
func BranchSubdomain() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SPROUT_BRANCH_SUBDOMAIN"))) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	h := PublicHost()
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return false
	}
	return strings.Contains(h, ".")
}

// AdvertiseHost is the hostname put in connection strings for one instance.
func AdvertiseHost(name string) string {
	base := PublicHost()
	label := DNSLabel(name)
	if label == "" || !BranchSubdomain() {
		return base
	}
	return label + "." + base
}

// ApplyNetworkSettings rewrites managed listen/port/auth sections (idempotent).
func ApplyNetworkSettings(dataDir string, port int) error {
	listen := ListenAddresses()
	conf := filepath.Join(dataDir, "postgresql.conf")
	netBody := fmt.Sprintf("port = %d\nlisten_addresses = '%s'\nunix_socket_directories = '%s'\npassword_encryption = scram-sha-256\n",
		port, listen, dataDir)
	if err := ReplaceManagedSection(conf, netBegin, netEnd, netBody); err != nil {
		return err
	}

	if os.Getenv("SPROUT_SAFE") == "true" {
		if err := RemoveManagedSection(conf, perfBegin, perfEnd); err != nil {
			return err
		}
	} else {
		perfBody := "logging_collector = off\nfsync = off\nsynchronous_commit = off\nfull_page_writes = off\n"
		if err := ReplaceManagedSection(conf, perfBegin, perfEnd, perfBody); err != nil {
			return err
		}
	}

	hba := filepath.Join(dataDir, "pg_hba.conf")
	if !RemoteAccess() {
		return RemoveManagedSection(hba, remoteBegin, remoteEnd)
	}
	auth := "scram-sha-256"
	if TrustRemote() {
		auth = "trust"
	}
	hbaBody := fmt.Sprintf("host all all 0.0.0.0/0 %s\nhost all all ::/0 %s\n", auth, auth)
	return ReplaceManagedSection(hba, remoteBegin, remoteEnd, hbaBody)
}
