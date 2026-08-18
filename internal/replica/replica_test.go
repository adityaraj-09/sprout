package replica

import (
	"strings"
	"testing"
)

func TestPsqlLocalEnvHasConnectTimeout(t *testing.T) {
	env := psqlLocalEnv()
	found := false
	for _, e := range env {
		if e == "PGCONNECT_TIMEOUT=5" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("PGCONNECT_TIMEOUT=5 missing from psqlLocalEnv")
	}
}

func TestPrimaryKeyIgnoresCredentials(t *testing.T) {
	a := PrimaryKeyFromURL("postgresql://postgres:one@db.example.supabase.co:5432/postgres")
	b := PrimaryKeyFromURL("postgresql://postgres:two@db.example.supabase.co:5432/postgres?sslmode=require")
	if a != b || a != "db.example.supabase.co:5432/postgres" {
		t.Fatalf("keys a=%q b=%q", a, b)
	}
	c := PrimaryKeyFromURL("postgresql://postgres:one@other.supabase.co:5432/postgres")
	if a == c {
		t.Fatal("different hosts must not match")
	}
}

func TestPGBool(t *testing.T) {
	if !pgBool("true") || !pgBool("t") || !pgBool("on") {
		t.Fatal("expected true")
	}
	if pgBool("false") || pgBool("f") || pgBool("") {
		t.Fatal("expected false")
	}
}

func TestInitializingOnly(t *testing.T) {
	if !initializingOnly("i:27") || !initializingOnly("") {
		t.Fatal("expected initializing")
	}
	if initializingOnly("i:1;d:26") || initializingOnly("r:27") {
		t.Fatal("copy in progress is not initializing-only")
	}
}

func TestCreateSubscriptionUsesPrecreatedSlot(t *testing.T) {
	sql := createSubscriptionSQL("sprout_sub_alice", "sprout_pub_alice", "host=db.example")
	for _, want := range []string{
		`CREATE SUBSCRIPTION "sprout_sub_alice"`,
		`PUBLICATION "sprout_pub_alice"`,
		`connect = false`,
		`slot_name = 'sprout_sub_alice'`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
	for _, forbidden := range []string{"create_slot = true", "enabled = true", "copy_data = true"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("offline subscription contains %q:\n%s", forbidden, sql)
		}
	}
}

func TestLogicalStatusCastsInternalCharState(t *testing.T) {
	sql := logicalSyncStatusSQL("sprout_sub_alice")
	if !strings.Contains(sql, "srsubstate::text || ':' || cnt") {
		t.Fatalf("internal char state must be cast before concatenation:\n%s", sql)
	}
}
