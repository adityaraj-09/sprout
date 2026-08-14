package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func githubAPI(t *testing.T, userJSON string, orgsJSON string, userStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("missing User-Agent")
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/user":
			w.WriteHeader(userStatus)
			_, _ = io.WriteString(w, userJSON)
		case r.URL.Path == "/user/orgs":
			_, _ = io.WriteString(w, orgsJSON)
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
}

func TestLookupAllowlistUser(t *testing.T) {
	srv := githubAPI(t, `{"login":"Alice","id":1}`, `[]`, 200)
	t.Cleanup(srv.Close)
	v := NewVerifier(Settings{
		ClientID: "iv1", Users: []string{"alice"}, API: srv.URL, HTTP: srv.Client(),
	})
	u, err := v.Lookup(context.Background(), "gho_a")
	if err != nil || u.Login != "Alice" {
		t.Fatalf("u=%+v err=%v", u, err)
	}
	// cache: second call must not require HTTP if we close the server
	srv.Close()
	u2, err := v.Lookup(context.Background(), "gho_a")
	if err != nil || u2.Login != "Alice" {
		t.Fatalf("cache u=%+v err=%v", u2, err)
	}
}

func TestLookupNotAllowed(t *testing.T) {
	srv := githubAPI(t, `{"login":"mallory","id":2}`, `[]`, 200)
	t.Cleanup(srv.Close)
	v := NewVerifier(Settings{
		ClientID: "iv1", Users: []string{"alice"}, API: srv.URL, HTTP: srv.Client(),
	})
	_, err := v.Lookup(context.Background(), "gho_b")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err=%v", err)
	}
}

func TestLookupOrgMembership(t *testing.T) {
	srv := githubAPI(t, `{"login":"bob","id":3}`, `[{"login":"acme"},{"login":"other"}]`, 200)
	t.Cleanup(srv.Close)
	v := NewVerifier(Settings{
		ClientID: "iv1", Orgs: []string{"Acme"}, API: srv.URL, HTTP: srv.Client(),
	})
	u, err := v.Lookup(context.Background(), "gho_c")
	if err != nil || u.Login != "bob" {
		t.Fatalf("u=%+v err=%v", u, err)
	}
}

func TestLookupInvalidToken(t *testing.T) {
	srv := githubAPI(t, `{"message":"bad"}`, `[]`, 401)
	t.Cleanup(srv.Close)
	v := NewVerifier(Settings{
		ClientID: "iv1", Users: []string{"alice"}, API: srv.URL, HTTP: srv.Client(),
	})
	_, err := v.Lookup(context.Background(), "nope")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err=%v", err)
	}
}

func TestLookupPublicAnyGitHubUser(t *testing.T) {
	srv := githubAPI(t, `{"login":"anyone","id":42}`, `[]`, 200)
	t.Cleanup(srv.Close)
	v := NewVerifier(Settings{ClientID: "iv1", API: srv.URL, HTTP: srv.Client()})
	u, err := v.Lookup(context.Background(), "gho")
	if err != nil || u.Login != "anyone" {
		t.Fatalf("u=%+v err=%v", u, err)
	}
}

func TestHashTokenDoesNotLeak(t *testing.T) {
	h := hashToken("gho_secret_value")
	if strings.Contains(h, "gho_") || strings.Contains(h, "secret") {
		t.Fatal(h)
	}
	if len(h) != 64 {
		t.Fatalf("len=%d", len(h))
	}
}

func TestSettingsReady(t *testing.T) {
	if (Settings{}).Ready() {
		t.Fatal("empty settings should not be ready")
	}
	if !(Settings{ClientID: "x"}).Ready() {
		t.Fatal("client id alone should be public/ready")
	}
	if (Settings{ClientID: "x"}).Restricted() {
		t.Fatal("no allowlist is public")
	}
	if !(Settings{ClientID: "x", Users: []string{"a"}}).Restricted() {
		t.Fatal("users should restrict")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" Alice, bob,, ")
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("%v", got)
	}
}

func TestUnavailableNotCached(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "alice", "id": 1})
	}))
	t.Cleanup(srv.Close)
	v := NewVerifier(Settings{
		ClientID: "iv1", Users: []string{"alice"}, API: srv.URL, HTTP: srv.Client(),
	})
	_, err := v.Lookup(context.Background(), "gho")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	u, err := v.Lookup(context.Background(), "gho")
	if err != nil || u.Login != "alice" {
		t.Fatalf("retry u=%+v err=%v", u, err)
	}
}
