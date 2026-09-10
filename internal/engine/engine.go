// Package engine names the database product behind a connector.
// Postgres stays the default; MongoDB and Qdrant are dump-snapshot + CoW
// branches (no incremental follow).
package engine

import (
	"net"
	"net/url"
	"strings"
)

const (
	Postgres = "postgres"
	Mongo    = "mongodb"
	Qdrant   = "qdrant"
)

func Normalize(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", Postgres, "postgresql", "pg":
		return Postgres
	case Mongo, "mongo", "mongodb+srv":
		return Mongo
	case Qdrant, "qdrants":
		return Qdrant
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func IsMongo(s string) bool  { return Normalize(s) == Mongo }
func IsQdrant(s string) bool { return Normalize(s) == Qdrant }

// IsDumpSnapshot is true for engines that bootstrap with a point-in-time copy
// and have no incremental apply (Mongo dump, Qdrant snapshot).
func IsDumpSnapshot(s string) bool {
	n := Normalize(s)
	return n == Mongo || n == Qdrant
}

func IsKnown(s string) bool {
	n := Normalize(s)
	return n == Postgres || n == Mongo || n == Qdrant
}

func KnownList() string { return "postgres, mongodb, or qdrant" }

// InferFromURL returns mongodb for mongodb:// and mongodb+srv://, qdrant for
// qdrant:// / qdrants:// (and http(s) URLs that look like Qdrant Cloud or :6333),
// otherwise postgres.
func InferFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Postgres
	}
	switch strings.ToLower(u.Scheme) {
	case "mongodb", "mongodb+srv":
		return Mongo
	case "qdrant", "qdrants":
		return Qdrant
	case "http", "https":
		if looksLikeQdrantHost(u.Hostname(), u.Port()) {
			return Qdrant
		}
	}
	return Postgres
}

func looksLikeQdrantHost(host, port string) bool {
	h := strings.ToLower(host)
	if strings.Contains(h, "qdrant") {
		return true
	}
	if port == "6333" || port == "6334" {
		return true
	}
	_, p, err := net.SplitHostPort(host)
	if err == nil && (p == "6333" || p == "6334") {
		return true
	}
	return false
}
