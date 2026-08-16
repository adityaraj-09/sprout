package mysql

import (
	"os"
	"strconv"
	"strings"

	"github.com/adityaraj/sprout/internal/postgres"
)

const defaultProxyPort = 3306

// ProxyBackendHost is the loopback address the MySQL hostname proxy dials.
// mysqld is bound to 127.0.0.1; the proxy re-authenticates rather than splicing auth.
func ProxyBackendHost() string {
	if h := strings.TrimSpace(os.Getenv("SPROUT_MYSQL_PROXY_BACKEND")); h != "" {
		return h
	}
	return "127.0.0.1"
}

// ProxyPort is the public MySQL port advertised when the hostname proxy is on.
func ProxyPort() int {
	raw := strings.TrimSpace(os.Getenv("SPROUT_MYSQL_PROXY_PORT"))
	if raw == "" {
		return defaultProxyPort
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p <= 0 || p > 65535 {
		return defaultProxyPort
	}
	return p
}

// ProxyEnabled is true when branch subdomains are on and the MySQL proxy is not disabled.
// Clients then get host:3306; the proxy picks the backend from TLS SNI after SSL upgrade.
func ProxyEnabled() bool {
	if !postgres.BranchSubdomain() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SPROUT_MYSQL_PROXY"))) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// AdvertisePort is the TCP port written into MySQL connection URLs.
func AdvertisePort(instancePort int) int {
	if ProxyEnabled() {
		return ProxyPort()
	}
	return instancePort
}
