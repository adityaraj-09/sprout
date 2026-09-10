package postgres

import "testing"

func TestLookBinariesOptionalNeverErrors(t *testing.T) {
	b := LookBinariesOptional()
	if b.Complete() {
		if b.Psql == "" || b.PgCtl == "" {
			t.Fatal("complete binaries must include psql and pg_ctl")
		}
		return
	}
	// Incomplete is valid on a Mongo/Qdrant-only host.
	if b.Psql != "" && b.InitDB != "" && b.Postgres != "" && b.PgCtl != "" {
		t.Fatal("Complete() should be true when core tools exist")
	}
}
