package qdrant

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adityaraj/sprout/internal/pgproxy"
)

func tlsDataRoot() string {
	if d := strings.TrimSpace(os.Getenv("SPROUT_DATA")); d != "" {
		return d
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "data")
}

func writeTLSFiles(certDest, keyDest string) error {
	if err := os.MkdirAll(filepath.Dir(certDest), 0o700); err != nil {
		return err
	}
	certFile := strings.TrimSpace(os.Getenv("SPROUT_TLS_CERT"))
	keyFile := strings.TrimSpace(os.Getenv("SPROUT_TLS_KEY"))
	if certFile == "" || keyFile == "" {
		root := tlsDataRoot()
		if _, err := pgproxy.LoadTLSConfig(root); err != nil {
			return fmt.Errorf("qdrant tls: %w", err)
		}
		certFile = filepath.Join(root, "tls", "server.crt")
		keyFile = filepath.Join(root, "tls", "server.key")
	}
	cert, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("qdrant tls cert: %w", err)
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("qdrant tls key: %w", err)
	}
	if err := os.WriteFile(certDest, cert, 0o600); err != nil {
		return err
	}
	return os.WriteFile(keyDest, key, 0o600)
}
