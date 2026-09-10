package dx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvLinesPostgres(t *testing.T) {
	got := EnvLines("postgresql://sprout:x@feat.example:5432/postgres")
	if len(got) != 1 || !strings.HasPrefix(got[0], "DATABASE_URL=") {
		t.Fatalf("%v", got)
	}
}

func TestEnvLinesMongoAndQdrant(t *testing.T) {
	m := EnvLines("mongodb://sprout:x@h:27017/app")
	if len(m) != 2 || m[0] != "MONGODB_URI=mongodb://sprout:x@h:27017/app" {
		t.Fatalf("mongo %v", m)
	}
	q := EnvLines("https://feat.host:6333?api-key=secret")
	if len(q) != 2 || q[1] != "QDRANT_API_KEY=secret" {
		t.Fatalf("qdrant %v", q)
	}
}

func TestWriteEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", ".env.sprout")
	if err := WriteEnvFile(p, []string{"DATABASE_URL=postgres://x"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "DATABASE_URL=postgres://x\n" {
		t.Fatalf("%q", b)
	}
}

func TestRelAgeAndStatus(t *testing.T) {
	if RelAge(time.Time{}) != "-" {
		t.Fatal("zero")
	}
	if RelAge(time.Now().Add(-3*time.Minute)) != "3m" {
		t.Fatalf("age=%s", RelAge(time.Now().Add(-3*time.Minute)))
	}
	if StatusMark("active") != "●" || StatusMark("idle") != "○" || StatusMark("error") != "✗" {
		t.Fatal("marks")
	}
}

func TestReadAtFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hook.sql")
	if err := os.WriteFile(p, []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAtFile("@" + p)
	if err != nil || got != "SELECT 1;" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = ReadAtFile("inline")
	if err != nil || got != "inline" {
		t.Fatalf("%q %v", got, err)
	}
}
