package qdrant

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/adityaraj/sprout/internal/postgres"
)

const defaultProxyPort = 6333

// ProxyBackendHost is the loopback address the SNI passthrough dials.
func ProxyBackendHost() string {
	if h := strings.TrimSpace(os.Getenv("SPROUT_QDRANT_PROXY_BACKEND")); h != "" {
		return h
	}
	return "127.0.0.1"
}

// ProxyPort is the public Qdrant HTTP port advertised when the SNI proxy is on.
func ProxyPort() int {
	raw := strings.TrimSpace(os.Getenv("SPROUT_QDRANT_PROXY_PORT"))
	if raw == "" {
		return defaultProxyPort
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p <= 0 || p > 65535 {
		return defaultProxyPort
	}
	return p
}

// ProxyEnabled is true when branch subdomains are on and the Qdrant SNI proxy is not disabled.
func ProxyEnabled() bool {
	if !postgres.BranchSubdomain() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SPROUT_QDRANT_PROXY"))) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// AdvertisePort is the TCP port written into http(s):// URLs.
func AdvertisePort(instancePort int) int {
	if ProxyEnabled() {
		return ProxyPort()
	}
	return instancePort
}

// ListenAddr is ":6333" unless SPROUT_QDRANT_PROXY_LISTEN or SPROUT_QDRANT_PROXY_PORT is set.
func ListenAddr() string {
	if a := strings.TrimSpace(os.Getenv("SPROUT_QDRANT_PROXY_LISTEN")); a != "" {
		return a
	}
	return fmt.Sprintf(":%d", ProxyPort())
}

func bindIP() string {
	if ProxyEnabled() {
		return "127.0.0.1"
	}
	h := postgres.PublicHost()
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

func useTLS() bool { return ProxyEnabled() }
