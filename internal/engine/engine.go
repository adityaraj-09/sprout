// Package engine names the database product behind a connector.
// Postgres stays the default; MongoDB is dump-restore + CoW branches (no oplog).
package engine

import (
	"net/url"
	"strings"
)

const (
	Postgres = "postgres"
	Mongo    = "mongodb"
)

func Normalize(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", Postgres, "postgresql", "pg":
		return Postgres
	case Mongo, "mongo", "mongodb+srv":
		return Mongo
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func IsMongo(s string) bool { return Normalize(s) == Mongo }

func IsKnown(s string) bool {
	n := Normalize(s)
	return n == Postgres || n == Mongo
}

// InferFromURL returns mongodb for mongodb:// and mongodb+srv://, otherwise postgres.
func InferFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Postgres
	}
	switch strings.ToLower(u.Scheme) {
	case "mongodb", "mongodb+srv":
		return Mongo
	default:
		return Postgres
	}
}
