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
	h := postgres.PublicHost()
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

// writeTLSPEM writes a combined certificate+key PEM for mongod.
func writeTLSPEM(dest string) error {
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
	out := append(append([]byte{}, cert...), key...)
	return os.WriteFile(dest, out, 0o600)
}
