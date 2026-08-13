package postgres

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
)

// TrustRemote reports whether non-loopback clients may use trust auth.
// Default is false: remote connections use SCRAM-SHA-256.
func TrustRemote() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SPROUT_TRUST_REMOTE")), "true")
}

// GeneratePassword returns SPROUT_DB_PASSWORD if set, otherwise a random secret.
func GeneratePassword() string {
	if p := strings.TrimSpace(os.Getenv("SPROUT_DB_PASSWORD")); p != "" {
		return p
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "sprout-" + strings.ReplaceAll(base64.RawURLEncoding.EncodeToString(b[:8]), "-", "x")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
