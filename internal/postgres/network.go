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
	cloneBegin  = "# --- sprout clone guard begin ---"
	cloneEnd    = "# --- sprout clone guard end ---"
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
// Default: 127.0.0.1 for local lab; "*" when SPROUT_PUBLIC_HOST is a real hostname
// without the SNI proxy. When the proxy is on, Postgres stays on loopback
// (127.0.0.1 trust for the control plane, 127.0.0.2 SCRAM for the proxy).
// Override with SPROUT_PG_LISTEN (e.g. "*", "0.0.0.0", "10.0.0.5").
func ListenAddresses() string {
	if a := strings.TrimSpace(os.Getenv("SPROUT_PG_LISTEN")); a != "" {
		return a
	}
	if ProxyEnabled() {
		return "127.0.0.1," + ProxyBackendHost()
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
	if a == "*" || a == "0.0.0.0" || a == "::" {
		return true
	}
	for _, part := range strings.Split(a, ",") {
		p := strings.TrimSpace(part)
		if p == "" || p == "127.0.0.1" || p == "localhost" || p == "::1" || strings.HasPrefix(p, "127.") {
			continue
		}
		return true
	}
	return false
}

// FormatConnString builds a libpq URL for a Sprout-managed instance.
// name is the branch/connector/main id; from is the source connector (if any).
// When subdomains are on, branches become <name>-<from>.<public host> so the
// same branch label from two connectors cannot share a hostname.
func FormatConnString(port int, db, password, name, from string, owner ...string) string {
	if db == "" {
		db = "postgres"
	}
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	u := url.URL{
		Scheme: "postgresql",
		Host:   net.JoinHostPort(AdvertiseHost(name, from, own), strconv.Itoa(AdvertisePort(port))),
		Path:   "/" + db,
	}
	if password != "" {
		u.User = url.UserPassword(DBUser(), password)
	} else {
		u.User = url.User(DBUser())
	}
	return u.String()
}

// DNSLabel turns an instance name into a single DNS label.
func DNSLabel(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "replica-")
	name = strings.ToLower(name)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// HostLabel is the DNS label for one instance.
// Connectors/main (from empty): "lab" / "main".
// Branches: "<name>-<connector>". With a GitHub owner: "<name>-<owner>-<connector>"
// so alice/testdb and bob/testdb do not share a hostname or data dir.
func HostLabel(name, from string, owner ...string) string {
	n, f := DNSLabel(name), DNSLabel(from)
	o := ""
	if len(owner) > 0 {
		o = DNSLabel(owner[0])
	}
	if f == "main" {
		f = ""
	}
	parts := make([]string, 0, 3)
	if n != "" {
		parts = append(parts, n)
	}
	if o != "" {
		parts = append(parts, o)
	}
	if f != "" {
		parts = append(parts, f)
	}
	return joinDNSParts(parts)
}

// ReplicaComputeName is the compute handle for a connector replica.
func ReplicaComputeName(name, owner string) string {
	return "replica-" + HostLabel(name, "", owner)
}

func joinDNSParts(parts []string) string {
	joined := strings.Join(parts, "-")
	if len(joined) <= 63 || len(parts) <= 1 {
		if len(joined) > 63 {
			return joined[:63]
		}
		return joined
	}
	suffix := strings.Join(parts[1:], "-")
	keep := 63 - 1 - len(suffix)
	if keep < 1 {
		if len(joined) > 63 {
			return joined[:63]
		}
		return joined
	}
	head := parts[0]
	if keep > len(head) {
		keep = len(head)
	}
	return head[:keep] + "-" + suffix
}

// BranchSubdomain reports whether advertised hosts should be <label>.<public host>.
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
func AdvertiseHost(name, from string, owner ...string) string {
	base := PublicHost()
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	label := HostLabel(name, from, own)
	if label == "" || !BranchSubdomain() {
		return base
	}
	return label + "." + base
}

const defaultProxyPort = 5432

// ProxyBackendHost is the loopback address the SNI proxy dials so pg_hba can
// require SCRAM (stock initdb trusts only 127.0.0.1).
func ProxyBackendHost() string {
	if h := strings.TrimSpace(os.Getenv("SPROUT_PG_PROXY_BACKEND")); h != "" {
		return h
	}
	return "127.0.0.2"
}

// ProxyPort is the public Postgres port advertised when the SNI proxy is on.
func ProxyPort() int {
	raw := strings.TrimSpace(os.Getenv("SPROUT_PG_PROXY_PORT"))
	if raw == "" {
		return defaultProxyPort
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p <= 0 || p > 65535 {
		return defaultProxyPort
	}
	return p
}

// ProxyEnabled is true when branch subdomains are on and the SNI proxy is not disabled.
// Clients then get host:5432; the proxy picks the backend from TLS SNI.
func ProxyEnabled() bool {
	if !BranchSubdomain() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SPROUT_PG_PROXY"))) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// AdvertisePort is the TCP port written into connection URLs.
func AdvertisePort(instancePort int) int {
	if ProxyEnabled() {
		return ProxyPort()
	}
	return instancePort
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
	var lines []string
	if ProxyEnabled() {
		// Proxy splices to 127.0.0.2 so it does not inherit 127.0.0.1 trust.
		lines = append(lines, fmt.Sprintf("host all all %s/32 scram-sha-256", ProxyBackendHost()))
	}
	if RemoteAccess() {
		auth := "scram-sha-256"
		if TrustRemote() {
			auth = "trust"
		}
		lines = append(lines, fmt.Sprintf("host all all 0.0.0.0/0 %s", auth), fmt.Sprintf("host all all ::/0 %s", auth))
	}
	if len(lines) == 0 {
		return RemoveManagedSection(hba, remoteBegin, remoteEnd)
	}
	return ReplaceManagedSection(hba, remoteBegin, remoteEnd, strings.Join(lines, "\n")+"\n")
}

// SetLogicalReplicationWorkers writes max_logical_replication_workers for a clone start.
// n < 0 removes the override so workers return to Postgres defaults.
func SetLogicalReplicationWorkers(dataDir string, n int) error {
	conf := filepath.Join(dataDir, "postgresql.conf")
	if n < 0 {
		return RemoveManagedSection(conf, cloneBegin, cloneEnd)
	}
	return ReplaceManagedSection(conf, cloneBegin, cloneEnd, fmt.Sprintf("max_logical_replication_workers = %d\n", n))
}
