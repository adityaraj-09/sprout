package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const defaultNextPort = 55433

// SQLiteStore is the control-plane notebook backed by SQLite (WAL).
type SQLiteStore struct {
	db   *sql.DB
	path string
}

// Open opens (or creates) the SQLite control DB at path.
// If the DB is empty and a sibling control.json exists, it imports that once.
func Open(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writes on one connection
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &SQLiteStore{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.importJSONIfNeeded(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("import control.json: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS branches (
  id                   TEXT PRIMARY KEY,
  project_id           TEXT NOT NULL,
  name                 TEXT NOT NULL,
  role                 TEXT NOT NULL DEFAULT 'branch',
  status               TEXT NOT NULL,
  port                 INTEGER NOT NULL DEFAULT 0,
  data_dir             TEXT NOT NULL DEFAULT '',
  snapshot_ref         TEXT NOT NULL DEFAULT '',
  container_id         TEXT NOT NULL DEFAULT '',
  compute              TEXT NOT NULL DEFAULT '',
  conn_string          TEXT NOT NULL DEFAULT '',
  error_message        TEXT NOT NULL DEFAULT '',
  source_lsn           TEXT NOT NULL DEFAULT '',
  source_connector     TEXT NOT NULL DEFAULT '',
  source_connector_id  TEXT NOT NULL DEFAULT '',
  password             TEXT NOT NULL DEFAULT '',
  created_by           TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  last_used_at         TEXT NOT NULL,
  UNIQUE(project_id, source_connector, name, created_by)
);
CREATE TABLE IF NOT EXISTS connectors (
  id              TEXT PRIMARY KEY,
  project_id      TEXT NOT NULL,
  name            TEXT NOT NULL,
  primary_url     TEXT NOT NULL DEFAULT '',
  mode            TEXT NOT NULL DEFAULT 'physical',
  status          TEXT NOT NULL,
  data_dir        TEXT NOT NULL DEFAULT '',
  port            INTEGER NOT NULL DEFAULT 0,
  error_message   TEXT NOT NULL DEFAULT '',
  last_lsn        TEXT NOT NULL DEFAULT '',
  last_lag_bytes  INTEGER NOT NULL DEFAULT 0,
  password        TEXT NOT NULL DEFAULT '',
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  UNIQUE(project_id, name, created_by)
);
`)
	if err != nil {
		return err
	}
	if err := s.ensureColumn("branches", "password", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("connectors", "password", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("branches", "created_by", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("connectors", "created_by", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("connectors", "engine", "TEXT NOT NULL DEFAULT 'postgres'"); err != nil {
		return err
	}
	if err := s.ensureOwnedUniques(); err != nil {
		return err
	}
	if err := s.ensureOrgSchema(); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'next_port'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err = s.db.Exec(`INSERT INTO meta(key, value) VALUES('next_port', ?)`, fmt.Sprintf("%d", defaultNextPort))
		return err
	}
	return nil
}

func (s *SQLiteStore) ensureColumn(table, column, decl string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	return err
}

// ensureOwnedUniques rebuilds older DBs so two GitHub users can share a label.
func (s *SQLiteStore) ensureOwnedUniques() error {
	if err := s.rebuildTableIfUniqueMissing("branches", "UNIQUE(project_id, source_connector, name, created_by)", `
CREATE TABLE branches_new (
  id                   TEXT PRIMARY KEY,
  project_id           TEXT NOT NULL,
  name                 TEXT NOT NULL,
  role                 TEXT NOT NULL DEFAULT 'branch',
  status               TEXT NOT NULL,
  port                 INTEGER NOT NULL DEFAULT 0,
  data_dir             TEXT NOT NULL DEFAULT '',
  snapshot_ref         TEXT NOT NULL DEFAULT '',
  container_id         TEXT NOT NULL DEFAULT '',
  compute              TEXT NOT NULL DEFAULT '',
  conn_string          TEXT NOT NULL DEFAULT '',
  error_message        TEXT NOT NULL DEFAULT '',
  source_lsn           TEXT NOT NULL DEFAULT '',
  source_connector     TEXT NOT NULL DEFAULT '',
  source_connector_id  TEXT NOT NULL DEFAULT '',
  password             TEXT NOT NULL DEFAULT '',
  created_by           TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  last_used_at         TEXT NOT NULL,
  UNIQUE(project_id, source_connector, name, created_by)
)`, `INSERT INTO branches_new(
  id, project_id, name, role, status, port, data_dir, snapshot_ref, container_id, compute,
  conn_string, error_message, source_lsn, source_connector, source_connector_id, password,
  created_by, created_at, updated_at, last_used_at
)
SELECT
  id, project_id, name, role, status, port, data_dir, snapshot_ref, container_id, compute,
  conn_string, error_message, source_lsn, source_connector, source_connector_id, password,
  created_by, created_at, updated_at, last_used_at
FROM branches`); err != nil {
		return err
	}
	return s.rebuildTableIfUniqueMissing("connectors", "UNIQUE(project_id, name, created_by)", `
CREATE TABLE connectors_new (
  id              TEXT PRIMARY KEY,
  project_id      TEXT NOT NULL,
  name            TEXT NOT NULL,
  primary_url     TEXT NOT NULL DEFAULT '',
  mode            TEXT NOT NULL DEFAULT 'physical',
  status          TEXT NOT NULL,
  data_dir        TEXT NOT NULL DEFAULT '',
  port            INTEGER NOT NULL DEFAULT 0,
  error_message   TEXT NOT NULL DEFAULT '',
  last_lsn        TEXT NOT NULL DEFAULT '',
  last_lag_bytes  INTEGER NOT NULL DEFAULT 0,
  password        TEXT NOT NULL DEFAULT '',
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  UNIQUE(project_id, name, created_by)
)`, `INSERT INTO connectors_new(
  id, project_id, name, primary_url, mode, status, data_dir, port, error_message,
  last_lsn, last_lag_bytes, password, created_by, created_at, updated_at
)
SELECT
  id, project_id, name, primary_url, mode, status, data_dir, port, error_message,
  last_lsn, last_lag_bytes, password, created_by, created_at, updated_at
FROM connectors`)
}

func (s *SQLiteStore) rebuildTableIfUniqueMissing(table, uniqueNeedle, createSQL, copySQL string) error {
	var ddl string
	err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl)
	if err != nil {
		return err
	}
	if strings.Contains(ddl, uniqueNeedle) {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(createSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE ` + table); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + `_new RENAME TO ` + table); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) importJSONIfNeeded() error {
	var projects, branches, connectors int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&projects)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM branches`).Scan(&branches)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM connectors`).Scan(&connectors)
	if projects+branches+connectors > 0 {
		return nil
	}
	jsonPath := filepath.Join(filepath.Dir(s.path), "control.json")
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var data fileData
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	next := data.NextPort
	if next == 0 {
		next = defaultNextPort
	}
	if _, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'next_port'`, fmt.Sprintf("%d", next)); err != nil {
		return err
	}
	for _, p := range data.Projects {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO projects(id, name, created_at) VALUES(?,?,?)`,
			p.ID, p.Name, formatTime(p.CreatedAt)); err != nil {
			return err
		}
	}
	for _, br := range data.Branches {
		if err := putBranchTx(tx, br); err != nil {
			return err
		}
	}
	for key, c := range data.Connectors {
		if c.ID == "" {
			c.ID = key
		}
		if c.Name == "" {
			c.Name = "primary"
		}
		if c.Port == 0 {
			c.Port = 55432
		}
		if err := putConnectorTx(tx, c); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "→ imported control plane from %s → %s\n", jsonPath, s.path)
	return nil
}

func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *SQLiteStore) EnsureProject(ctx context.Context, name string) (Project, error) {
	var p Project
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM projects WHERE name = ?`, name).
		Scan(&p.ID, &p.Name, &created)
	if err == nil {
		p.CreatedAt = parseTime(created)
		return p, nil
	}
	if err != sql.ErrNoRows {
		return Project{}, err
	}
	p = Project{ID: uuid.NewString(), Name: name, CreatedAt: time.Now().UTC()}
	_, err = s.db.ExecContext(ctx, `INSERT INTO projects(id, name, created_at) VALUES(?,?,?)`,
		p.ID, p.Name, formatTime(p.CreatedAt))
	return p, err
}

func (s *SQLiteStore) GetProject(ctx context.Context, idOrName string) (Project, error) {
	var p Project
	var created string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, created_at FROM projects WHERE id = ? OR name = ? LIMIT 1`, idOrName, idOrName).
		Scan(&p.ID, &p.Name, &created)
	if err == sql.ErrNoRows {
		return Project{}, fmt.Errorf("project not found: %s", idOrName)
	}
	if err != nil {
		return Project{}, err
	}
	p.CreatedAt = parseTime(created)
	return p, nil
}

func (s *SQLiteStore) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var created string
		if err := rows.Scan(&p.ID, &p.Name, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) AllocPort(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var val string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'next_port'`).Scan(&val); err != nil {
		return 0, err
	}
	var port int
	if _, err := fmt.Sscanf(val, "%d", &port); err != nil || port == 0 {
		port = defaultNextPort
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = 'next_port'`, fmt.Sprintf("%d", port+1)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return port, nil
}

func putBranchTx(tx *sql.Tx, b BranchRecord) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = now
	}
	if b.LastUsedAt.IsZero() {
		b.LastUsedAt = now
	}
	_, err := tx.Exec(`
INSERT INTO branches(
  id, project_id, name, role, status, port, data_dir, snapshot_ref, container_id, compute,
  conn_string, error_message, source_lsn, source_connector, source_connector_id, password,
  created_by, org_id, created_at, updated_at, last_used_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  project_id=excluded.project_id, name=excluded.name, role=excluded.role, status=excluded.status,
  port=excluded.port, data_dir=excluded.data_dir, snapshot_ref=excluded.snapshot_ref,
  container_id=excluded.container_id, compute=excluded.compute, conn_string=excluded.conn_string,
  error_message=excluded.error_message, source_lsn=excluded.source_lsn,
  source_connector=excluded.source_connector, source_connector_id=excluded.source_connector_id,
  password=excluded.password, created_by=excluded.created_by, org_id=excluded.org_id,
  updated_at=excluded.updated_at, last_used_at=excluded.last_used_at
`, b.ID, b.ProjectID, b.Name, b.Role, b.Status, b.Port, b.DataDir, b.SnapshotRef, b.ContainerID, b.Compute,
		b.ConnString, b.ErrorMessage, b.SourceLSN, b.SourceConnector, b.SourceConnectorID, b.Password,
		b.CreatedBy, b.OrgID, formatTime(b.CreatedAt), formatTime(b.UpdatedAt), formatTime(b.LastUsedAt))
	return err
}

func (s *SQLiteStore) PutBranch(ctx context.Context, b BranchRecord) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	if b.LastUsedAt.IsZero() {
		b.LastUsedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO branches(
  id, project_id, name, role, status, port, data_dir, snapshot_ref, container_id, compute,
  conn_string, error_message, source_lsn, source_connector, source_connector_id, password,
  created_by, org_id, created_at, updated_at, last_used_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  project_id=excluded.project_id, name=excluded.name, role=excluded.role, status=excluded.status,
  port=excluded.port, data_dir=excluded.data_dir, snapshot_ref=excluded.snapshot_ref,
  container_id=excluded.container_id, compute=excluded.compute, conn_string=excluded.conn_string,
  error_message=excluded.error_message, source_lsn=excluded.source_lsn,
  source_connector=excluded.source_connector, source_connector_id=excluded.source_connector_id,
  password=excluded.password, created_by=excluded.created_by, org_id=excluded.org_id,
  updated_at=excluded.updated_at, last_used_at=excluded.last_used_at
`, b.ID, b.ProjectID, b.Name, b.Role, b.Status, b.Port, b.DataDir, b.SnapshotRef, b.ContainerID, b.Compute,
		b.ConnString, b.ErrorMessage, b.SourceLSN, b.SourceConnector, b.SourceConnectorID, b.Password,
		b.CreatedBy, b.OrgID, formatTime(b.CreatedAt), formatTime(b.UpdatedAt), formatTime(b.LastUsedAt))
	return err
}

func (s *SQLiteStore) UpdateBranch(ctx context.Context, b BranchRecord) error {
	b.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE branches SET
  project_id=?, name=?, role=?, status=?, port=?, data_dir=?, snapshot_ref=?, container_id=?, compute=?,
  conn_string=?, error_message=?, source_lsn=?, source_connector=?, source_connector_id=?, password=?,
  created_by=?, org_id=?, updated_at=?, last_used_at=?
WHERE id=?`,
		b.ProjectID, b.Name, b.Role, b.Status, b.Port, b.DataDir, b.SnapshotRef, b.ContainerID, b.Compute,
		b.ConnString, b.ErrorMessage, b.SourceLSN, b.SourceConnector, b.SourceConnectorID, b.Password,
		b.CreatedBy, b.OrgID, formatTime(b.UpdatedAt), formatTime(b.LastUsedAt), b.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("branch not found: %s", b.ID)
	}
	return nil
}

const branchCols = `id, project_id, name, role, status, port, data_dir, snapshot_ref, container_id, compute,
  conn_string, error_message, source_lsn, source_connector, source_connector_id, password, created_by, org_id,
  created_at, updated_at, last_used_at`

func scanBranch(scanner interface {
	Scan(dest ...any) error
}) (BranchRecord, error) {
	var b BranchRecord
	var created, updated, lastUsed string
	err := scanner.Scan(
		&b.ID, &b.ProjectID, &b.Name, &b.Role, &b.Status, &b.Port, &b.DataDir, &b.SnapshotRef, &b.ContainerID, &b.Compute,
		&b.ConnString, &b.ErrorMessage, &b.SourceLSN, &b.SourceConnector, &b.SourceConnectorID, &b.Password, &b.CreatedBy, &b.OrgID,
		&created, &updated, &lastUsed,
	)
	if err != nil {
		return BranchRecord{}, err
	}
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	b.LastUsedAt = parseTime(lastUsed)
	return b, nil
}

func (s *SQLiteStore) GetBranch(ctx context.Context, projectID, name string) (BranchRecord, error) {
	return s.FindBranch(ctx, projectID, name, "", "")
}

func (s *SQLiteStore) FindBranch(ctx context.Context, projectID, name, from, owner string) (BranchRecord, error) {
	list, err := s.listBranchesQuery(ctx, `SELECT `+branchCols+` FROM branches WHERE project_id = ? AND name = ? ORDER BY source_connector`, projectID, name)
	if err != nil {
		return BranchRecord{}, err
	}
	return ResolveBranch(name, from, FilterBranchesByOwner(owner, list))
}

func (s *SQLiteStore) GetBranchByID(ctx context.Context, id string) (BranchRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+branchCols+` FROM branches WHERE id = ?`, id)
	b, err := scanBranch(row)
	if err == sql.ErrNoRows {
		return BranchRecord{}, fmt.Errorf("branch not found")
	}
	return b, err
}

func (s *SQLiteStore) listBranchesQuery(ctx context.Context, query string, args ...any) ([]BranchRecord, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BranchRecord
	for rows.Next() {
		b, err := scanBranch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListBranches(ctx context.Context, projectID string) ([]BranchRecord, error) {
	return s.listBranchesQuery(ctx, `SELECT `+branchCols+` FROM branches WHERE project_id = ? ORDER BY name`, projectID)
}

func (s *SQLiteStore) ListAllBranches(ctx context.Context) ([]BranchRecord, error) {
	return s.listBranchesQuery(ctx, `SELECT `+branchCols+` FROM branches ORDER BY name`)
}

func (s *SQLiteStore) DeleteBranch(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM branches WHERE id = ?`, id)
	return err
}

func putConnectorTx(tx *sql.Tx, c Connector) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := tx.Exec(`
INSERT INTO connectors(
  id, project_id, name, primary_url, engine, mode, status, data_dir, port, error_message, last_lsn, last_lag_bytes, password, created_by, org_id, created_at, updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  project_id=excluded.project_id, name=excluded.name, primary_url=excluded.primary_url, engine=excluded.engine, mode=excluded.mode,
  status=excluded.status, data_dir=excluded.data_dir, port=excluded.port, error_message=excluded.error_message,
  last_lsn=excluded.last_lsn, last_lag_bytes=excluded.last_lag_bytes, password=excluded.password,
  created_by=excluded.created_by, org_id=excluded.org_id, updated_at=excluded.updated_at
`, c.ID, c.ProjectID, c.Name, c.PrimaryURL, connectorEngine(c), c.Mode, c.Status, c.DataDir, c.Port, c.ErrorMessage, c.LastLSN, c.LastLagBytes, c.Password,
		c.CreatedBy, c.OrgID, formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	return err
}

func (s *SQLiteStore) PutConnector(ctx context.Context, c Connector) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	var existingID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM connectors WHERE project_id = ? AND name = ? AND org_id = ? AND created_by = ? AND id != ?`,
		c.ProjectID, c.Name, c.OrgID, c.CreatedBy, c.ID).
		Scan(&existingID)
	if err == nil {
		return fmt.Errorf("connector_exists: %q", c.Name)
	}
	if err != sql.ErrNoRows {
		return err
	}
	if c.OrgID != "" {
		err = s.db.QueryRowContext(ctx,
			`SELECT id FROM connectors WHERE project_id = ? AND name = ? AND org_id = ? AND id != ?`,
			c.ProjectID, c.Name, c.OrgID, c.ID).Scan(&existingID)
		if err == nil {
			return fmt.Errorf("connector_exists: %q", c.Name)
		}
		if err != sql.ErrNoRows {
			return err
		}
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO connectors(
  id, project_id, name, primary_url, engine, mode, status, data_dir, port, error_message, last_lsn, last_lag_bytes, password, created_by, org_id, created_at, updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  project_id=excluded.project_id, name=excluded.name, primary_url=excluded.primary_url, engine=excluded.engine, mode=excluded.mode,
  status=excluded.status, data_dir=excluded.data_dir, port=excluded.port, error_message=excluded.error_message,
  last_lsn=excluded.last_lsn, last_lag_bytes=excluded.last_lag_bytes, password=excluded.password,
  created_by=excluded.created_by, org_id=excluded.org_id, updated_at=excluded.updated_at
`, c.ID, c.ProjectID, c.Name, c.PrimaryURL, connectorEngine(c), c.Mode, c.Status, c.DataDir, c.Port, c.ErrorMessage, c.LastLSN, c.LastLagBytes, c.Password,
		c.CreatedBy, c.OrgID, formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	return err
}

func connectorEngine(c Connector) string {
	if strings.TrimSpace(c.Engine) == "" {
		return "postgres"
	}
	return c.Engine
}

const connectorCols = `id, project_id, name, primary_url, engine, mode, status, data_dir, port, error_message, last_lsn, last_lag_bytes, password, created_by, org_id, created_at, updated_at`

func scanConnector(scanner interface {
	Scan(dest ...any) error
}) (Connector, error) {
	var c Connector
	var created, updated string
	err := scanner.Scan(
		&c.ID, &c.ProjectID, &c.Name, &c.PrimaryURL, &c.Engine, &c.Mode, &c.Status, &c.DataDir, &c.Port,
		&c.ErrorMessage, &c.LastLSN, &c.LastLagBytes, &c.Password, &c.CreatedBy, &c.OrgID, &created, &updated,
	)
	if err != nil {
		return Connector{}, err
	}
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return c, nil
}

func (s *SQLiteStore) GetConnectorByID(ctx context.Context, id string) (Connector, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+connectorCols+` FROM connectors WHERE id = ?`, id)
	c, err := scanConnector(row)
	if err == sql.ErrNoRows {
		return Connector{}, fmt.Errorf("connector not found")
	}
	return c, err
}

func (s *SQLiteStore) GetConnectorByName(ctx context.Context, projectID, name, owner string) (Connector, error) {
	list, err := s.listConnectorsQuery(ctx, `SELECT `+connectorCols+` FROM connectors WHERE project_id = ? AND name = ? ORDER BY created_by`, projectID, name)
	if err != nil {
		return Connector{}, err
	}
	return resolveConnector(name, owner, list)
}

func (s *SQLiteStore) listConnectorsQuery(ctx context.Context, query string, args ...any) ([]Connector, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connector
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListConnectors(ctx context.Context) ([]Connector, error) {
	return s.listConnectorsQuery(ctx, `SELECT `+connectorCols+` FROM connectors ORDER BY name`)
}

func (s *SQLiteStore) ListConnectorsByProject(ctx context.Context, projectID string) ([]Connector, error) {
	return s.listConnectorsQuery(ctx, `SELECT `+connectorCols+` FROM connectors WHERE project_id = ? ORDER BY name`, projectID)
}

func (s *SQLiteStore) UpdateConnector(ctx context.Context, c Connector) error {
	c.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
UPDATE connectors SET
  project_id=?, name=?, primary_url=?, engine=?, mode=?, status=?, data_dir=?, port=?, error_message=?, last_lsn=?, last_lag_bytes=?, password=?, created_by=?, org_id=?, updated_at=?
WHERE id=?`,
		c.ProjectID, c.Name, c.PrimaryURL, connectorEngine(c), c.Mode, c.Status, c.DataDir, c.Port, c.ErrorMessage, c.LastLSN, c.LastLagBytes, c.Password,
		c.CreatedBy, c.OrgID, formatTime(c.UpdatedAt), c.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connector not found")
	}
	return nil
}

func (s *SQLiteStore) DeleteConnector(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connectors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}
	res, err = s.db.ExecContext(ctx, `DELETE FROM connectors WHERE name = ?`, id)
	if err != nil {
		return err
	}
	n, _ = res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connector not found")
	}
	return nil
}
