package engine

import "testing"

func TestNormalizeAndInfer(t *testing.T) {
	if Normalize("") != Postgres || Normalize("pg") != Postgres {
		t.Fatal("postgres aliases")
	}
	if Normalize("mongo") != Mongo || Normalize("mongodb+srv") != Mongo {
		t.Fatal("mongo aliases")
	}
	if Normalize("qdrants") != Qdrant || Normalize("Qdrant") != Qdrant {
		t.Fatal("qdrant aliases")
	}
	if !IsMongo("mongodb") || IsMongo("postgres") {
		t.Fatal("IsMongo")
	}
	if !IsQdrant("qdrant") || IsQdrant("mongodb") {
		t.Fatal("IsQdrant")
	}
	if !IsDumpSnapshot("mongodb") || !IsDumpSnapshot("qdrant") || IsDumpSnapshot("postgres") {
		t.Fatal("IsDumpSnapshot")
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
	if InferFromURL("qdrant://:key@host:6333") != Qdrant {
		t.Fatal("qdrant url")
	}
	if InferFromURL("https://xyz.us-east.aws.cloud.qdrant.io") != Qdrant {
		t.Fatal("qdrant cloud host")
	}
	if InferFromURL("http://127.0.0.1:6333") != Qdrant {
		t.Fatal("qdrant default port")
	}
	if IsKnown("clickhouse") {
		t.Fatal("unknown engines stay unknown")
	}
	if !IsKnown("mongodb") || !IsKnown("") || !IsKnown("qdrant") {
		t.Fatal("known")
	}
}
