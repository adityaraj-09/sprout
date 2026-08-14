package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/adityaraj/sprout/internal/auth"
)

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		got, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || got == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or bad token")
			return
		}

		if s.Token != "" && secureEqual(got, s.Token) {
			r = r.WithContext(auth.WithActor(r.Context(), auth.Actor{Kind: auth.KindToken, Login: "token"}))
			next.ServeHTTP(w, r)
			return
		}

		if s.Verifier != nil {
			user, err := s.Verifier.Lookup(r.Context(), got)
			switch {
			case err == nil:
				r = r.WithContext(auth.WithActor(r.Context(), auth.Actor{
					Kind: auth.KindGitHub, Login: user.Login, ID: user.ID,
				}))
				next.ServeHTTP(w, r)
				return
			case errors.Is(err, auth.ErrNotAllowed):
				writeErr(w, http.StatusForbidden, "forbidden", err.Error())
				return
			case errors.Is(err, auth.ErrUnavailable), errors.Is(err, auth.ErrNotReady):
				writeErr(w, http.StatusServiceUnavailable, "github_unavailable", err.Error())
				return
			default:
				writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or bad token")
				return
			}
		}

		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or bad token")
	})
}

func publicPath(p string) bool {
	return p == "/healthz" || p == "/v1/auth/github"
}

func bearerToken(h string) (string, bool) {
	const p = "Bearer "
	if len(h) < len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(p):])
	return tok, tok != ""
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) handleAuthGitHub(w http.ResponseWriter, r *http.Request) {
	gh := s.GitHub
	if !gh.Enabled() {
		writeErr(w, http.StatusNotFound, "github_auth_disabled", "set SPROUT_GITHUB_CLIENT_ID on the server")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   true,
		"ready":     true,
		"public":    !gh.Restricted(),
		"client_id": gh.ClientID,
		"host":      gh.HostURL(),
		"api":       gh.APIURL(),
		"scope":     gh.Scope(),
	})
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	a := auth.ActorFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":  a.Kind,
		"login": a.Login,
		"id":    a.ID,
	})
}
