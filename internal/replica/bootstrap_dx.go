package replica

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/adityaraj/sprout/internal/postgres"
)

// CheckToolsForPrimary ensures local pg_dump/postgres majors are >= primary major.
func (m *Manager) CheckToolsForPrimary(ctx context.Context, c Conn) error {
	out, err := m.psqlPrimary(ctx, c, `SHOW server_version_num;`)
	if err != nil {
		return fmt.Errorf("cannot read primary version: %w", err)
	}
	num, _ := strconv.Atoi(strings.TrimSpace(out))
	if num <= 0 {
		return fmt.Errorf("cannot parse primary server_version_num %q", strings.TrimSpace(out))
	}
	primaryMajor := num / 10000

	pgDump := findPgDump()
	if pgDump == "" {
		return fmt.Errorf("version_mismatch: pg_dump not found — install postgresql-client-%d (primary is PG%d)", primaryMajor, primaryMajor)
	}
	dumpMajor, err := postgres.ClientMajor(pgDump)
	if err != nil {
		return err
	}
	if dumpMajor < primaryMajor {
		return fmt.Errorf("version_mismatch: pg_dump is %d but primary is %d — install postgresql-client-%d and put it first on PATH", dumpMajor, primaryMajor, primaryMajor)
	}
	pgMajor, err := postgres.ClientMajor(m.Bins.Postgres)
	if err != nil {
		return err
	}
	if pgMajor < primaryMajor {
		return fmt.Errorf("version_mismatch: local postgres is %d but primary is %d — install postgresql-%d for initdb/subscriber", pgMajor, primaryMajor, primaryMajor)
	}
	fmt.Fprintf(os.Stderr, "  tools ok: primary=PG%d pg_dump=%d postgres=%d\n", primaryMajor, dumpMajor, pgMajor)
	return nil
}

// EstimateLogicalBootstrap returns table counts for dry-run.
func (m *Manager) EstimateLogicalBootstrap(ctx context.Context, c Conn, schema string, tables []string) (map[string]any, error) {
	if schema == "" {
		schema = "public"
	}
	var sql string
	if len(tables) > 0 {
		quoted := make([]string, 0, len(tables))
		for _, t := range tables {
			quoted = append(quoted, quoteLiteral(t))
		}
		sql = fmt.Sprintf(`
SELECT c.relname::text || E'\t' || COALESCE(s.n_live_tup, 0)::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE n.nspname = %s AND c.relkind = 'r'
  AND c.relname IN (%s)
ORDER BY 1;
`, quoteLiteral(schema), strings.Join(quoted, ","))
	} else {
		sql = fmt.Sprintf(`
SELECT c.relname::text || E'\t' || COALESCE(s.n_live_tup, 0)::text
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
WHERE n.nspname = %s AND c.relkind = 'r'
ORDER BY 1;
`, quoteLiteral(schema))
	}
	out, err := m.psqlPrimary(ctx, c, sql)
	if err != nil {
		return nil, err
	}
	type row struct {
		Name string `json:"name"`
		Rows int64  `json:"approx_rows"`
	}
	var list []row
	var total int64
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		list = append(list, row{Name: strings.TrimSpace(parts[0]), Rows: n})
		total += n
	}
	return map[string]any{
		"schema":        schema,
		"table_count":   len(list),
		"approx_rows":   total,
		"tables":        list,
		"will_hit_prod": true,
		"note":          "Initial logical copy streams these tables from prod once; ongoing load is WAL decode only.",
	}, nil
}

// DropPublication removes a publication on the primary (best-effort cleanup).
func (m *Manager) DropPublication(ctx context.Context, c Conn, pubName string) error {
	_, err := m.psqlPrimary(ctx, c, fmt.Sprintf(`DROP PUBLICATION IF EXISTS %s;`, quoteIdent(pubName)))
	return err
}

// DropReplicationSlot removes a logical slot on the primary left behind after a wiped subscriber.
func (m *Manager) DropReplicationSlot(ctx context.Context, c Conn, slotName string) error {
	sql := fmt.Sprintf(`
SELECT pg_drop_replication_slot(slot_name)
FROM pg_replication_slots
WHERE slot_name = %s;
`, quoteLiteral(slotName))
	_, err := m.psqlPrimary(ctx, c, sql)
	if err != nil {
		return fmt.Errorf("drop replication slot %s: %w", slotName, err)
	}
	fmt.Fprintf(os.Stderr, "  dropped replication slot %s on primary (if it existed)\n", slotName)
	return nil
}

// DropSubscriptionLocal drops a subscription on the local subscriber.
func (m *Manager) DropSubscriptionLocal(ctx context.Context, localHost string, localPort int, subName string) error {
	sql := fmt.Sprintf(`DROP SUBSCRIPTION IF EXISTS %s;`, quoteIdent(subName))
	cmd := exec.CommandContext(ctx, m.Bins.Psql,
		"-h", localHost, "-p", strconv.Itoa(localPort), "-d", "postgres",
		"-v", "ON_ERROR_STOP=1", "-c", sql,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("drop subscription: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
