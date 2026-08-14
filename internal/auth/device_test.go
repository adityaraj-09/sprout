package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForTokenPendingThenSuccess(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/oauth/access_token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept=%s", r.Header.Get("Accept"))
		}
		n := polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"gho_secret","token_type":"bearer","scope":"read:user"}`)
	}))
	t.Cleanup(srv.Close)

	c := &DeviceClient{
		Settings: Settings{ClientID: "iv1.test", Host: srv.URL, HTTP: srv.Client()},
		Sleep:    func(ctx context.Context, d time.Duration) error { return nil },
		Now:      time.Now,
	}
	tok, err := c.WaitForToken(context.Background(), DeviceCode{
		DeviceCode: "dev", UserCode: "ABCD-EFGH", VerificationURI: srv.URL + "/login/device",
		ExpiresIn: 900, Interval: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "gho_secret" {
		t.Fatalf("token=%q", tok.Token)
	}
	if polls.Load() != 3 {
		t.Fatalf("polls=%d", polls.Load())
	}
}

func TestWaitForTokenSlowDownIncreasesInterval(t *testing.T) {
	var sleeps []time.Duration
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"gho_ok"}`)
	}))
	t.Cleanup(srv.Close)

	c := &DeviceClient{
		Settings: Settings{ClientID: "iv1.test", Host: srv.URL, HTTP: srv.Client()},
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
		Now: time.Now,
	}
	if _, err := c.WaitForToken(context.Background(), DeviceCode{
		DeviceCode: "dev", UserCode: "X", VerificationURI: "http://x",
		ExpiresIn: 900, Interval: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) < 2 {
		t.Fatalf("sleeps=%v", sleeps)
	}
	if sleeps[0] != 5*time.Second || sleeps[1] != 10*time.Second {
		t.Fatalf("expected 5s then 10s, got %v", sleeps)
	}
}

func TestWaitForTokenAccessDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"access_denied","error_description":"cancelled"}`)
	}))
	t.Cleanup(srv.Close)
	c := &DeviceClient{
		Settings: Settings{ClientID: "iv1.test", Host: srv.URL, HTTP: srv.Client()},
		Sleep:    func(context.Context, time.Duration) error { return nil },
		Now:      time.Now,
	}
	_, err := c.WaitForToken(context.Background(), DeviceCode{
		DeviceCode: "dev", UserCode: "X", VerificationURI: "http://x", ExpiresIn: 900, Interval: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitForTokenExpiredDeadline(t *testing.T) {
	now := time.Now()
	c := &DeviceClient{
		Settings: Settings{ClientID: "iv1.test", Host: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Millisecond}},
		Sleep:    func(context.Context, time.Duration) error { return nil },
		Now: func() time.Time {
			now = now.Add(time.Hour)
			return now
		},
	}
	_, err := c.WaitForToken(context.Background(), DeviceCode{
		DeviceCode: "dev", UserCode: "X", VerificationURI: "https://github.com/login/device",
		ExpiresIn: 1, Interval: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "expired_token") {
		t.Fatalf("got %v", err)
	}
}

func TestRequestCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/device/code" {
			t.Fatalf("path %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "iv1.app" {
			t.Fatalf("client_id=%s", r.Form.Get("client_id"))
		}
		if r.Form.Get("scope") != "read:user" {
			t.Fatalf("scope=%s", r.Form.Get("scope"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"device_code":"dc","user_code":"WDJB-MJHT",
			"verification_uri":"https://github.com/login/device",
			"verification_uri_complete":"https://github.com/login/device?user_code=WDJB-MJHT",
			"expires_in":899,"interval":5
		}`)
	}))
	t.Cleanup(srv.Close)
	c := &DeviceClient{Settings: Settings{ClientID: "iv1.app", Host: srv.URL, HTTP: srv.Client()}}
	dc, err := c.RequestCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "WDJB-MJHT" || BrowserURL(dc) == "" {
		t.Fatalf("%+v", dc)
	}
}

func TestPostForm429IsSlowDown(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"gho_ok"}`)
	}))
	t.Cleanup(srv.Close)
	c := &DeviceClient{
		Settings: Settings{ClientID: "iv1.test", Host: srv.URL, HTTP: srv.Client()},
		Sleep:    func(context.Context, time.Duration) error { return nil },
		Now:      time.Now,
	}
	tok, err := c.WaitForToken(context.Background(), DeviceCode{
		DeviceCode: "dev", UserCode: "X", VerificationURI: "http://x", ExpiresIn: 900, Interval: 5,
	})
	if err != nil || tok.Token != "gho_ok" {
		t.Fatalf("tok=%v err=%v", tok, err)
	}
}

func TestBrowserURLPrefersComplete(t *testing.T) {
	u := BrowserURL(DeviceCode{VerificationURI: "https://github.com/login/device", VerificationURIComplete: "https://github.com/login/device?user_code=A"})
	if u != "https://github.com/login/device?user_code=A" {
		t.Fatal(u)
	}
}

func TestRequestCodeFormEncoding(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form
		_, _ = io.WriteString(w, `{"device_code":"d","user_code":"U","verification_uri":"https://github.com/login/device","expires_in":15,"interval":5}`)
	}))
	t.Cleanup(srv.Close)
	c := &DeviceClient{Settings: Settings{ClientID: "abc", Host: srv.URL, Orgs: []string{"acme"}, HTTP: srv.Client()}}
	if _, err := c.RequestCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Get("scope") != "read:user read:org" {
		t.Fatalf("scope=%s", got.Get("scope"))
	}
}
