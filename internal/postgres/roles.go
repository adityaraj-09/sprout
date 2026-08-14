package postgres

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// DBUser is the login role advertised in connection strings (default "sprout").
func DBUser() string {
	if u := strings.TrimSpace(os.Getenv("SPROUT_DB_USER")); u != "" {
		return u
	}
	return "sprout"
}

// PsqlOneLiner is a ready-to-copy shell command for a branch/replica.
func PsqlOneLiner(port int, password, name, from string, owner ...string) string {
	return fmt.Sprintf(`psql "%s"`, FormatConnString(port, "postgres", password, name, from, owner...))
}

// EnsureAppRoles creates the stable app login role (and postgres if missing) for remote clients.
func (i *Instance) EnsureAppRoles() error {
	user := DBUser()
	sql := fmt.Sprintf(`
DO $role$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s WITH LOGIN SUPERUSER;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'postgres') THEN
    CREATE ROLE postgres WITH LOGIN SUPERUSER;
  END IF;
END
$role$;
`, quoteLiteral(user), quoteIdent(user))
	if i.Password != "" {
		sql += fmt.Sprintf("\nALTER ROLE %s WITH LOGIN SUPERUSER PASSWORD %s;\n", quoteIdent(user), quoteLiteral(i.Password))
	}
	_, err := i.ExecSQL("postgres", sql)
	return err
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// ClientMajor returns major version of a postgres binary (postgres/pg_dump/initdb).
func ClientMajor(bin string) (int, error) {
	cmd := exec.Command(bin, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(out))
	for _, f := range fields {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' {
			major := f
			if i := strings.IndexByte(major, '.'); i > 0 {
				major = major[:i]
			}
			n, err := strconv.Atoi(major)
			return n, err
		}
	}
	return 0, fmt.Errorf("cannot parse version: %s", strings.TrimSpace(string(out)))
}
