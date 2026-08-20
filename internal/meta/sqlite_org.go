package meta

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *SQLiteStore) ensureOrgSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS orgs (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(created_by, name)
);
CREATE TABLE IF NOT EXISTS org_members (
  org_id     TEXT NOT NULL,
  login      TEXT NOT NULL,
  role       TEXT NOT NULL,
  added_by   TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY(org_id, login)
);
`)
	if err != nil {
		return err
	}
	if err := s.ensureColumn("connectors", "org_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("branches", "org_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return s.backfillDefaultOrgs()
}

func (s *SQLiteStore) backfillDefaultOrgs() error {
	logins := map[string]struct{}{}
	rows, err := s.db.Query(`SELECT DISTINCT created_by FROM connectors WHERE created_by != '' UNION SELECT DISTINCT created_by FROM branches WHERE created_by != ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var login string
		if err := rows.Scan(&login); err != nil {
			return err
		}
		login = strings.ToLower(strings.TrimSpace(login))
		if login != "" {
			logins[login] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ctx := context.Background()
	for login := range logins {
		org, err := s.EnsureDefaultOrg(ctx, login)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE connectors SET org_id = ? WHERE created_by = ? AND (org_id = '' OR org_id IS NULL)`, org.ID, login); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE branches SET org_id = ? WHERE created_by = ? AND (org_id = '' OR org_id IS NULL)`, org.ID, login); err != nil {
			return err
		}
	}
	return nil
}

func normalizeOrgName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func (s *SQLiteStore) EnsureDefaultOrg(ctx context.Context, login string) (Org, error) {
	login = normalizeLogin(login)
	if login == "" {
		return Org{}, fmt.Errorf("invalid_name: org owner required")
	}
	return s.ensureOrg(ctx, login, DefaultOrg)
}

func (s *SQLiteStore) ensureOrg(ctx context.Context, login, name string) (Org, error) {
	name = normalizeOrgName(name)
	var o Org
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_by, created_at FROM orgs WHERE created_by = ? AND name = ?`, login, name).
		Scan(&o.ID, &o.Name, &o.CreatedBy, &created)
	if err == nil {
		o.CreatedAt = parseTime(created)
		o.Role = OrgRoleOwner
		_ = s.AddOrgMember(ctx, OrgMember{OrgID: o.ID, Login: login, Role: OrgRoleOwner, AddedBy: login})
		return o, nil
	}
	if err != sql.ErrNoRows {
		return Org{}, err
	}
	o = Org{ID: uuid.NewString(), Name: name, CreatedBy: login, CreatedAt: time.Now().UTC(), Role: OrgRoleOwner}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO orgs(id, name, created_by, created_at) VALUES(?,?,?,?)`,
		o.ID, o.Name, o.CreatedBy, formatTime(o.CreatedAt)); err != nil {
		return Org{}, err
	}
	if err := s.AddOrgMember(ctx, OrgMember{OrgID: o.ID, Login: login, Role: OrgRoleOwner, AddedBy: login}); err != nil {
		return Org{}, err
	}
	return o, nil
}

func (s *SQLiteStore) CreateOrg(ctx context.Context, login, name string) (Org, error) {
	login = normalizeLogin(login)
	name = normalizeOrgName(name)
	if login == "" || name == "" {
		return Org{}, fmt.Errorf("invalid_name: org name and owner required")
	}
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM orgs WHERE created_by = ? AND name = ?`, login, name).Scan(&existing)
	if err == nil {
		return Org{}, fmt.Errorf("org_exists: %q", name)
	}
	if err != sql.ErrNoRows {
		return Org{}, err
	}
	return s.ensureOrg(ctx, login, name)
}

func (s *SQLiteStore) GetOrg(ctx context.Context, id string) (Org, error) {
	var o Org
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, created_by, created_at FROM orgs WHERE id = ?`, id).
		Scan(&o.ID, &o.Name, &o.CreatedBy, &created)
	if err == sql.ErrNoRows {
		return Org{}, fmt.Errorf("org_not_found")
	}
	if err != nil {
		return Org{}, err
	}
	o.CreatedAt = parseTime(created)
	return o, nil
}

func (s *SQLiteStore) ListOrgs(ctx context.Context, login string) ([]Org, error) {
	login = normalizeLogin(login)
	q := `SELECT o.id, o.name, o.created_by, o.created_at, COALESCE(m.role, '')
FROM orgs o LEFT JOIN org_members m ON m.org_id = o.id AND m.login = ?
ORDER BY o.created_by, o.name`
	args := []any{login}
	if login != "" {
		q = `SELECT o.id, o.name, o.created_by, o.created_at, m.role
FROM orgs o INNER JOIN org_members m ON m.org_id = o.id
WHERE m.login = ?
ORDER BY o.name`
		args = []any{login}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		var o Org
		var created string
		if err := rows.Scan(&o.ID, &o.Name, &o.CreatedBy, &created, &o.Role); err != nil {
			return nil, err
		}
		o.CreatedAt = parseTime(created)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteOrg(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM org_members WHERE org_id = ?`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM orgs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("org_not_found")
	}
	return nil
}

func (s *SQLiteStore) AddOrgMember(ctx context.Context, m OrgMember) error {
	m.Login = normalizeLogin(m.Login)
	m.AddedBy = normalizeLogin(m.AddedBy)
	if m.Login == "" || m.OrgID == "" {
		return fmt.Errorf("invalid_name: member login required")
	}
	if m.Role != OrgRoleOwner && m.Role != OrgRoleMember {
		m.Role = OrgRoleMember
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO org_members(org_id, login, role, added_by, created_at) VALUES(?,?,?,?,?)
ON CONFLICT(org_id, login) DO UPDATE SET role=excluded.role
`, m.OrgID, m.Login, m.Role, m.AddedBy, formatTime(m.CreatedAt))
	return err
}

func (s *SQLiteStore) RemoveOrgMember(ctx context.Context, orgID, login string) error {
	login = normalizeLogin(login)
	res, err := s.db.ExecContext(ctx, `DELETE FROM org_members WHERE org_id = ? AND login = ?`, orgID, login)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("member_not_found")
	}
	return nil
}

func (s *SQLiteStore) ListOrgMembers(ctx context.Context, orgID string) ([]OrgMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org_id, login, role, added_by, created_at FROM org_members WHERE org_id = ? ORDER BY login`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgMember
	for rows.Next() {
		var m OrgMember
		var created string
		if err := rows.Scan(&m.OrgID, &m.Login, &m.Role, &m.AddedBy, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetOrgMember(ctx context.Context, orgID, login string) (OrgMember, error) {
	login = normalizeLogin(login)
	var m OrgMember
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT org_id, login, role, added_by, created_at FROM org_members WHERE org_id = ? AND login = ?`, orgID, login).
		Scan(&m.OrgID, &m.Login, &m.Role, &m.AddedBy, &created)
	if err == sql.ErrNoRows {
		return OrgMember{}, fmt.Errorf("member_not_found")
	}
	if err != nil {
		return OrgMember{}, err
	}
	m.CreatedAt = parseTime(created)
	return m, nil
}

func (s *SQLiteStore) GetConnectorByNameInOrg(ctx context.Context, projectID, name, orgID string) (Connector, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+connectorCols+` FROM connectors WHERE project_id = ? AND name = ? AND org_id = ?`, projectID, name, orgID)
	c, err := scanConnector(row)
	if err == sql.ErrNoRows {
		return Connector{}, fmt.Errorf("connector not found: %s", name)
	}
	return c, err
}
