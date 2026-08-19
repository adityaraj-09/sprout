package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/progress"
)

func (s *Server) withOrg(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		if s.Service == nil {
			next.ServeHTTP(w, r)
			return
		}
		want := strings.TrimSpace(r.Header.Get("X-Sprout-Org"))
		if want == "" {
			want = strings.TrimSpace(r.URL.Query().Get("org"))
		}
		if auth.IsUser(ctx) {
			if _, err := s.Service.EnsureDefaultOrg(ctx); err != nil {
				writeErr(w, http.StatusInternalServerError, "org_failed", err.Error())
				return
			}
			if want == "" {
				want = meta.DefaultOrg
			}
			org, err := s.Service.ResolveOrg(ctx, want)
			if err != nil {
				code, status := mapErr(err)
				writeErr(w, status, code, err.Error())
				return
			}
			ctx = auth.WithOrg(ctx, org.ID, org.Name)
		} else if want != "" {
			org, err := s.Service.ResolveOrg(ctx, want)
			if err == nil {
				ctx = auth.WithOrg(ctx, org.ID, org.Name)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	list, err := s.Service.ListOrgs(r.Context())
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	if list == nil {
		list = []meta.Org{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orgs":        list,
		"current_org": auth.OrgNameFrom(r.Context()),
		"current_id":  auth.OrgIDFrom(r.Context()),
	})
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_body", `JSON {"name":"..."} required`)
		return
	}
	org, err := s.Service.CreateOrg(r.Context(), body.Name)
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	if err := s.Service.DeleteOrg(r.Context(), r.PathValue("org")); err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Service.ListOrgMembers(r.Context(), r.PathValue("org"))
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Login) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_body", `JSON {"login":"github-user"} required`)
		return
	}
	m, err := s.Service.AddOrgMember(r.Context(), r.PathValue("org"), body.Login)
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	if err := s.Service.RemoveOrgMember(r.Context(), r.PathValue("org"), r.PathValue("login")); err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func wantsProgress(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "ndjson") || r.URL.Query().Get("progress") == "1"
}

func withProgress(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if !wantsProgress(r) {
		return r, false
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	ctx := progress.With(r.Context(), func(ev progress.Event) {
		_ = enc.Encode(ev)
		if flusher != nil {
			flusher.Flush()
		}
	})
	return r.WithContext(ctx), true
}

func writeProgressResult(w http.ResponseWriter, streamed bool, status int, v any) {
	if !streamed {
		writeJSON(w, status, v)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "result", "result": v})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeProgressErr(w http.ResponseWriter, streamed bool, err error) {
	code, status := mapErr(err)
	if !streamed {
		writeErr(w, status, code, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": code, "message": err.Error()})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
