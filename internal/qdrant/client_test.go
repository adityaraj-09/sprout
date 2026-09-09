package qdrant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientListAndScrollCopy(t *testing.T) {
	var destCreated bool
	var destPoints int
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/collections" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"result":{"collections":[{"name":"docs"}]},"status":"ok"}`))
		case r.URL.Path == "/collections/docs" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"result":{"status":"green","points_count":1,"config":{"params":{"vectors":{"size":2,"distance":"Cosine"}}}},"status":"ok"}`))
		case strings.HasSuffix(r.URL.Path, "/snapshots") && r.Method == http.MethodPost:
			http.Error(w, "snapshots disabled", http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, "/points/scroll") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"result":{"points":[{"id":1,"vector":[0.1,0.2],"payload":{"t":"a"}}],"next_page_offset":null},"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer src.Close()
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/collections/docs" && r.Method == http.MethodPut:
			destCreated = true
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case strings.HasSuffix(r.URL.Path, "/points") && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			var body struct {
				Points []json.RawMessage `json:"points"`
			}
			_ = json.Unmarshal(b, &body)
			destPoints = len(body.Points)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer dst.Close()

	remote := &Client{BaseURL: src.URL, HTTP: src.Client()}
	dest := &Client{BaseURL: dst.URL, HTTP: dst.Client()}
	names, err := remote.ListCollections(context.Background())
	if err != nil || len(names) != 1 || names[0] != "docs" {
		t.Fatalf("list: %v %v", names, err)
	}
	if err := remote.ScrollCopy(context.Background(), dest, "docs"); err != nil {
		t.Fatal(err)
	}
	if !destCreated || destPoints != 1 {
		t.Fatalf("created=%v points=%d", destCreated, destPoints)
	}
}
