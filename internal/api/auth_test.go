package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adityaraj/sprout/internal/auth"
)

func TestAuthHealthzOpen(t *testing.T) {
	s := &Server{Token: "secret", Mux: http.NewServeMux()}
	s.routes()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAuthSharedToken(t *testing.T) {
	s := &Server{Token: "secret", Mux: http.NewServeMux()}
	s.Mux.HandleFunc("GET /v1/whoami", s.handleWhoAmI)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/whoami", nil)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	_ = res.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var a auth.Actor
	_ = json.NewDecoder(res.Body).Decode(&a)
	if a.Kind != auth.KindToken {
		t.Fatalf("%+v", a)
	}
}

func TestAuthGitHubAllowAndDeny(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.Path == "/user" {
			if tok != "gho_alice" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"login":"alice","id":7}`)
			return
		}
		if r.URL.Path == "/user/orgs" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(ghSrv.Close)

	s := &Server{
		Token: "shared",
		GitHub: auth.Settings{
			ClientID: "iv1", Users: []string{"alice"}, API: ghSrv.URL, HTTP: ghSrv.Client(),
		},
		Mux: http.NewServeMux(),
	}
	s.Verifier = auth.NewVerifier(s.GitHub)
	s.Mux.HandleFunc("GET /v1/whoami", s.handleWhoAmI)
	s.Mux.HandleFunc("GET /v1/auth/github", s.handleAuthGitHub)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/v1/auth/github")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("auth meta %d", res.StatusCode)
	}
	_ = res.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer gho_alice")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("alice %d", res.StatusCode)
	}
	var a auth.Actor
	_ = json.NewDecoder(res.Body).Decode(&a)
	if a.Kind != auth.KindGitHub || a.Login != "alice" {
		t.Fatalf("%+v", a)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer gho_bob")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 401 {
		t.Fatalf("bob want 401 got %d", res.StatusCode)
	}
	_ = res.Body.Close()
}

func TestAuthGitHubNotAllowedIs403(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = io.WriteString(w, `{"login":"mallory","id":9}`)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(ghSrv.Close)
	s := &Server{
		GitHub: auth.Settings{ClientID: "iv1", Users: []string{"alice"}, API: ghSrv.URL, HTTP: ghSrv.Client()},
		Mux:    http.NewServeMux(),
	}
	s.Verifier = auth.NewVerifier(s.GitHub)
	s.Mux.HandleFunc("GET /v1/whoami", s.handleWhoAmI)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer gho_m")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("status %d", res.StatusCode)
	}
}
