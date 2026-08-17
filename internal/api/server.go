package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/branch"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
)

type Server struct {
	Service  *branch.Service
	Token    string
	GitHub   auth.Settings
	Verifier *auth.Verifier
	Mux      *http.ServeMux
}

func New(svc *branch.Service, token string) *Server {
	gh := auth.FromEnv()
	s := &Server{Service: svc, Token: token, GitHub: gh, Mux: http.NewServeMux()}
	if gh.Enabled() {
		s.Verifier = auth.NewVerifier(gh)
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.auth(s.Mux)
}

func (s *Server) routes() {
	s.Mux.HandleFunc("GET /healthz", s.handleHealth)
	s.Mux.HandleFunc("GET /v1/auth/github", s.handleAuthGitHub)
	s.Mux.HandleFunc("GET /v1/whoami", s.handleWhoAmI)
	s.Mux.HandleFunc("GET /v1/doctor", s.handleDoctor)
	s.Mux.HandleFunc("POST /v1/init", s.handleInit)
	s.Mux.HandleFunc("GET /v1/connectors", s.handleListConnectors)
	s.Mux.HandleFunc("POST /v1/projects/{project}/connect", s.handleConnect)
	s.Mux.HandleFunc("DELETE /v1/projects/{project}/connectors/{name}", s.handleDeleteConnector)
	s.Mux.HandleFunc("POST /v1/projects/{project}/connectors/{name}/suspend", s.handleSuspendConnector)
	s.Mux.HandleFunc("POST /v1/projects/{project}/connectors/{name}/resume", s.handleResumeConnector)
	s.Mux.HandleFunc("GET /v1/projects/{project}/replication", s.handleReplication)
	s.Mux.HandleFunc("GET /v1/projects/{project}/connectors/{name}/replication", s.handleReplicationNamed)
	s.Mux.HandleFunc("GET /v1/projects", s.handleListProjects)
	s.Mux.HandleFunc("POST /v1/projects/{project}/branches", s.handleCreateBranch)
	s.Mux.HandleFunc("GET /v1/projects/{project}/branches", s.handleListBranches)
	s.Mux.HandleFunc("GET /v1/projects/{project}/branches/{name}", s.handleGetBranch)
	s.Mux.HandleFunc("GET /v1/projects/{project}/branches/{name}/diff", s.handleDiffBranch)
	s.Mux.HandleFunc("DELETE /v1/projects/{project}/branches/{name}", s.handleDeleteBranch)
	s.Mux.HandleFunc("POST /v1/projects/{project}/branches/{name}/reset", s.handleResetBranch)
	s.Mux.HandleFunc("POST /v1/projects/{project}/branches/{name}/suspend", s.handleSuspendBranch)
	s.Mux.HandleFunc("POST /v1/projects/{project}/branches/{name}/resume", s.handleResumeBranch)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	rep := s.Service.Doctor(r.Context())
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	list, err := s.Service.ListConnectors(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	out := make([]meta.Connector, 0, len(list))
	for _, c := range list {
		c.PrimaryURL = redactURL(c.PrimaryURL)
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

func redactURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			userinfo := rest[:at]
			hostpart := rest[at:]
			if colon := strings.Index(userinfo, ":"); colon >= 0 {
				return raw[:i+3] + userinfo[:colon] + ":***" + hostpart
			}
		}
	}
	return raw
}

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	if auth.IsUser(r.Context()) {
		writeErr(w, http.StatusForbidden, "forbidden", "sprout init is machine-token only; connect your own replica with sprout connect")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	proj, err := s.Service.InitMain(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "init_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, proj)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	var body struct {
		URL    string   `json:"url"`
		Mode   string   `json:"mode"`
		Name   string   `json:"name"`
		Wipe   *bool    `json:"wipe"`
		DryRun bool     `json:"dry_run"`
		Tables []string `json:"tables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		writeErr(w, http.StatusBadRequest, "invalid_body",
			`JSON {"url":"postgresql://...","mode":"logical|physical","name":"...","wipe":true,"dry_run":false,"tables":["t"]} required`)
		return
	}
	wipe := true
	if body.Wipe != nil {
		wipe = *body.Wipe
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()
	res, err := s.Service.Connect(ctx, proj.ID, branch.ConnectOpts{
		Name: body.Name, URL: body.URL, Mode: body.Mode,
		Wipe: wipe, DryRun: body.DryRun, Tables: body.Tables,
	})
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	if res.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"dry_run":  true,
			"estimate": res.Estimate,
			"project":  proj,
		})
		return
	}
	if res.Connector != nil {
		res.Connector.PrimaryURL = redactURL(res.Connector.PrimaryURL)
	}
	out := map[string]any{
		"connector": res.Connector,
		"lag":       res.Lag,
		"project":   proj,
	}
	if res.Connector != nil {
		out["connection_string"] = postgres.FormatConnString(res.Connector.Port, "postgres", res.Connector.Password, res.Connector.Name, "", res.Connector.CreatedBy)
		out["psql"] = postgres.PsqlOneLiner(res.Connector.Port, res.Connector.Password, res.Connector.Name, "", res.Connector.CreatedBy)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteConnector(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	force := r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1"
	if err := s.Service.DeleteConnector(ctx, proj.ID, r.PathValue("name"), force); err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSuspendConnector(w http.ResponseWriter, r *http.Request) {
	s.mutateConnector(w, r, s.Service.SuspendConnector)
}

func (s *Server) handleResumeConnector(w http.ResponseWriter, r *http.Request) {
	s.mutateConnector(w, r, s.Service.ResumeConnector)
}

type connectorMutator func(ctx context.Context, projectID, name string) (branch.ConnectorLifecycleResult, error)

func (s *Server) mutateConnector(w http.ResponseWriter, r *http.Request, fn connectorMutator) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := fn(ctx, proj.ID, r.PathValue("name"))
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	if res.Connector.PrimaryURL != "" {
		res.Connector.PrimaryURL = redactURL(res.Connector.PrimaryURL)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleReplication(w http.ResponseWriter, r *http.Request) {
	s.writeReplication(w, r, r.URL.Query().Get("name"))
}

func (s *Server) handleReplicationNamed(w http.ResponseWriter, r *http.Request) {
	s.writeReplication(w, r, r.PathValue("name"))
}

func (s *Server) writeReplication(w http.ResponseWriter, r *http.Request, name string) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	conn, lag, err := s.Service.ReplicationStatus(r.Context(), proj.ID, name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no_connector", err.Error())
		return
	}
	conn.PrimaryURL = redactURL(conn.PrimaryURL)
	writeJSON(w, http.StatusOK, map[string]any{"connector": conn, "lag": lag})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Service.Store.ListProjects(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) resolveProject(r *http.Request) (meta.Project, error) {
	idOrName := r.PathValue("project")
	if idOrName == "" || idOrName == "default" {
		return s.Service.Store.EnsureProject(r.Context(), branch.DefaultProject)
	}
	return s.Service.Store.GetProject(r.Context(), idOrName)
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	var body struct {
		Name string `json:"name"`
		From string `json:"from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_body", `JSON {"name":"...","from":"connector-name"} required`)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	rec, err := s.Service.Create(ctx, proj.ID, body.Name, body.From)
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":                  rec.ID,
		"project_id":          rec.ProjectID,
		"name":                rec.Name,
		"role":                rec.Role,
		"status":              rec.Status,
		"port":                rec.Port,
		"data_dir":            rec.DataDir,
		"snapshot_ref":        rec.SnapshotRef,
		"container_id":        rec.ContainerID,
		"compute":             rec.Compute,
		"connection_string":   rec.ConnString,
		"psql":                postgres.PsqlOneLiner(rec.Port, rec.Password, rec.Name, rec.SourceConnector, rec.CreatedBy),
		"error_message":       rec.ErrorMessage,
		"source_lsn":          rec.SourceLSN,
		"source_connector":    rec.SourceConnector,
		"source_connector_id": rec.SourceConnectorID,
		"created_by":          rec.CreatedBy,
		"created_at":          rec.CreatedAt,
		"updated_at":          rec.UpdatedAt,
		"last_used_at":        rec.LastUsedAt,
	})
}

func (s *Server) handleDiffBranch(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	diff, err := s.Service.DiffBranch(ctx, proj.ID, r.PathValue("name"), branchFrom(r))
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	list, err := s.Service.List(r.Context(), proj.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetBranch(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	rec, err := s.Service.Get(r.Context(), proj.ID, r.PathValue("name"), branchFrom(r))
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	if err := s.Service.Delete(r.Context(), proj.ID, r.PathValue("name"), branchFrom(r)); err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetBranch(w http.ResponseWriter, r *http.Request) {
	s.mutateBranch(w, r, s.Service.Reset)
}

func (s *Server) handleSuspendBranch(w http.ResponseWriter, r *http.Request) {
	s.mutateBranch(w, r, s.Service.Suspend)
}

func (s *Server) handleResumeBranch(w http.ResponseWriter, r *http.Request) {
	s.mutateBranch(w, r, s.Service.Resume)
}

type branchMutator func(ctx context.Context, projectID, name, from string) (meta.BranchRecord, error)

func branchFrom(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("from"))
}

func (s *Server) mutateBranch(w http.ResponseWriter, r *http.Request, fn branchMutator) {
	proj, err := s.resolveProject(r)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project_not_found", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	rec, err := fn(ctx, proj.ID, r.PathValue("name"), branchFrom(r))
	if err != nil {
		code, status := mapErr(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func mapErr(err error) (code string, status int) {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "forbidden"):
		return "forbidden", http.StatusForbidden
	case strings.HasPrefix(msg, "ambiguous_connector"):
		return "ambiguous_connector", http.StatusConflict
	case strings.HasPrefix(msg, "branch_exists"):
		return "branch_exists", http.StatusConflict
	case strings.HasPrefix(msg, "ambiguous_branch"):
		return "ambiguous_branch", http.StatusConflict
	case strings.HasPrefix(msg, "branch_not_found"):
		return "branch_not_found", http.StatusNotFound
	case strings.HasPrefix(msg, "invalid_name"):
		return "invalid_name", http.StatusBadRequest
	case strings.HasPrefix(msg, "invalid_state"):
		return "invalid_state", http.StatusConflict
	case strings.HasPrefix(msg, "main_not_ready"), strings.HasPrefix(msg, "source_not_ready"):
		return "source_not_ready", http.StatusServiceUnavailable
	case strings.HasPrefix(msg, "connector_not_found"), strings.HasPrefix(msg, "no source"), strings.HasPrefix(msg, "multiple connectors"):
		return "connector_required", http.StatusBadRequest
	case strings.HasPrefix(msg, "connector_has_branches"):
		return "connector_has_branches", http.StatusConflict
	case strings.HasPrefix(msg, "connector_exists"):
		return "connector_exists", http.StatusConflict
	case strings.HasPrefix(msg, "invalid_mode"):
		return "invalid_mode", http.StatusBadRequest
	case strings.HasPrefix(msg, "version_mismatch"):
		return "version_mismatch", http.StatusBadRequest
	case strings.HasPrefix(msg, "dry_run"):
		return "invalid_body", http.StatusBadRequest
	case strings.HasPrefix(msg, "logical_sync_stuck"):
		return "logical_sync_stuck", http.StatusConflict
	case strings.HasPrefix(msg, "replica_lag"):
		return "replica_lag", http.StatusConflict
	case strings.HasPrefix(msg, "compute_failed"):
		return "compute_failed", http.StatusInternalServerError
	case strings.HasPrefix(msg, "storage_failed"):
		return "storage_failed", http.StatusInternalServerError
	default:
		return "internal", http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
