// Package engine names the database product behind a connector.
// Postgres stays the default; MySQL is dump-import + CoW branches (no binlog).
package engine

import (
	"net/url"
	"strings"
)

const (
	Postgres = "postgres"
	MySQL    = "mysql"
)

func Normalize(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", Postgres, "postgresql", "pg":
		return Postgres
	case MySQL, "mariadb", "maria":
		return MySQL
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func IsMySQL(s string) bool { return Normalize(s) == MySQL }

func IsKnown(s string) bool {
	n := Normalize(s)
	return n == Postgres || n == MySQL
}

// InferFromURL returns mysql for mysql:// and mariadb://, otherwise postgres.
func InferFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Postgres
	}
	switch strings.ToLower(u.Scheme) {
	case "mysql", "mariadb":
		return MySQL
	default:
		return Postgres
	}
}
