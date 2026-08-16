package engine

import "testing"

func TestNormalizeAndInfer(t *testing.T) {
	if Normalize("") != Postgres || Normalize("PostgreSQL") != Postgres {
		t.Fatal("postgres default")
	}
	if Normalize("mariadb") != MySQL || !IsMySQL("mysql") {
		t.Fatal("mysql aliases")
	}
	if InferFromURL("mysql://u@h:3306/db") != MySQL {
		t.Fatal("mysql url")
	}
	if InferFromURL("postgresql://u@h/postgres") != Postgres {
		t.Fatal("postgres url")
	}
	if IsKnown("clickhouse") {
		t.Fatal("unknown engines stay unknown")
	}
}
