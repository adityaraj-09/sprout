package engine

import "testing"

func TestNormalizeAndInfer(t *testing.T) {
	if Normalize("") != Postgres || Normalize("pg") != Postgres {
		t.Fatal("postgres aliases")
	}
	if Normalize("mongo") != Mongo || Normalize("mongodb+srv") != Mongo {
		t.Fatal("mongo aliases")
	}
	if !IsMongo("mongodb") || IsMongo("postgres") {
		t.Fatal("IsMongo")
	}
	if InferFromURL("mongodb://u@h:27017/shop") != Mongo {
		t.Fatal("mongodb url")
	}
	if InferFromURL("mongodb+srv://u@cluster.mongodb.net/shop") != Mongo {
		t.Fatal("srv url")
	}
	if InferFromURL("postgresql://u@h/postgres") != Postgres {
		t.Fatal("postgres url")
	}
	if IsKnown("clickhouse") {
		t.Fatal("unknown engines stay unknown")
	}
	if !IsKnown("mongodb") || !IsKnown("") {
		t.Fatal("known")
	}
}
