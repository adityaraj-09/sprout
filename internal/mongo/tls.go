package mongo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adityaraj/sprout/internal/pgproxy"
	"github.com/adityaraj/sprout/internal/postgres"
)

func tlsDataRoot() string {
	if d := strings.TrimSpace(os.Getenv("SPROUT_DATA")); d != "" {
		return d
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "data")
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

// writeTLSPEM writes a combined certificate+key PEM and a CA file for mongod.
func writeTLSPEM(dest, caDest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	certFile := strings.TrimSpace(os.Getenv("SPROUT_TLS_CERT"))
	keyFile := strings.TrimSpace(os.Getenv("SPROUT_TLS_KEY"))
	if certFile == "" || keyFile == "" {
		root := tlsDataRoot()
		if _, err := pgproxy.LoadTLSConfig(root); err != nil {
			return fmt.Errorf("mongo tls: %w", err)
		}
		certFile = filepath.Join(root, "tls", "server.crt")
		keyFile = filepath.Join(root, "tls", "server.key")
	}
	cert, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("mongo tls cert: %w", err)
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("mongo tls key: %w", err)
	}
	if len(cert) > 0 && cert[len(cert)-1] != '\n' {
		cert = append(cert, '\n')
	}
	out := append(append([]byte{}, cert...), key...)
	if err := os.WriteFile(dest, out, 0o600); err != nil {
		return err
	}
	// MongoDB 7+ requires a CA chain of trust (SERVER-72839). Self-signed: the cert is the CA.
	return os.WriteFile(caDest, cert, 0o600)
}
