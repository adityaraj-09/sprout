package branch

import (
	"strings"
	"testing"

	"github.com/adityaraj/sprout/internal/engine"
)

func TestReplicaIdentitySQLAllowlist(t *testing.T) {
	sql := replicaIdentitySQL([]string{"users", "orders"})
	if !strings.Contains(sql, "'users'") || !strings.Contains(sql, "'orders'") {
		t.Fatalf("allowlist missing: %s", sql)
	}
	if !strings.Contains(sql, "relreplident") {
		t.Fatal("expected replica identity predicate")
	}
}

func TestPreflightInfersEngine(t *testing.T) {
	if engine.InferFromURL("mongodb+srv://a:b@c.net/db") != engine.Mongo {
		t.Fatal("mongo")
	}
	if engine.InferFromURL("https://x.cloud.qdrant.io:6333") != engine.Qdrant {
		t.Fatal("qdrant")
	}
	if engine.InferFromURL("postgresql://u@h/db") != engine.Postgres {
		t.Fatal("pg")
	}
}

func TestPreflightRejectsEmptyURL(t *testing.T) {
	svc, _ := testService(t)
	_, err := svc.Preflight(t.Context(), PreflightOpts{})
	if err == nil || !strings.Contains(err.Error(), "url required") {
		t.Fatalf("err=%v", err)
	}
}
