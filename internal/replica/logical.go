package replica

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPublication  = "sprout_pub"
	DefaultSubscription = "sprout_sub"
)

// LogicalStatus is subscriber-side sync state (main is a normal primary).
type LogicalStatus struct {
	Subscription string `json:"subscription"`
	Enabled      bool   `json:"enabled"`
	TableTotal   int    `json:"table_total"`
	TableReady   int    `json:"table_ready"`
	ReceivedLSN string `json:"received_lsn"`
	LastMsg      string `json:"last_msg_time,omitempty"`
}

// EnsurePublication creates (or recreates) a publication on the primary.
// If tables is non-empty, publishes only those tables in schema; else TABLES IN SCHEMA.
func (m *Manager) EnsurePublication(ctx context.Context, c Conn, pubName, schema string, tables []string) error {
	if schema == "" {
		schema = "public"
	}
	out, err := m.psqlPrimary(ctx, c, "SHOW wal_level;")
	if err != nil {
		return err
	}
	level := strings.TrimSpace(out)
	if level != "logical" {
		return fmt.Errorf("primary wal_level=%s (need logical for logical replication)", level)
	}

	drop := fmt.Sprintf(`DROP PUBLICATION IF EXISTS %s;`, quoteIdent(pubName))
	_, _ = m.psqlPrimary(ctx, c, drop)

	if len(tables) > 0 {
		parts := make([]string, 0, len(tables))
		for _, t := range tables {
			parts = append(parts, quoteIdent(schema)+"."+quoteIdent(t))
		}
		sql := fmt.Sprintf(`CREATE PUBLICATION %s FOR TABLE %s;`, quoteIdent(pubName), strings.Join(parts, ", "))
		if _, err := m.psqlPrimary(ctx, c, sql); err != nil {
			return fmt.Errorf("create publication (table list): %w", err)
		}
		fmt.Fprintf(os.Stderr, "  publication %s for %d tables\n", pubName, len(tables))
		return nil
	}

	scoped := fmt.Sprintf(`CREATE PUBLICATION %s FOR TABLES IN SCHEMA %s;`, quoteIdent(pubName), quoteIdent(schema))
	if _, err := m.psqlPrimary(ctx, c, scoped); err != nil {
		fallback := fmt.Sprintf(`CREATE PUBLICATION %s FOR ALL TABLES;`, quoteIdent(pubName))
		if _, err2 := m.psqlPrimary(ctx, c, fallback); err2 != nil {
			return fmt.Errorf("create publication: %w (fallback: %v)", err, err2)
		}
		fmt.Fprintf(os.Stderr, "  warning: using FOR ALL TABLES (scoped schema publication unsupported)\n")
	}
	return nil
}

// DumpSchema copies public schema DDL from primary into local subscriber (tables must exist before SUBSCRIPTION).
func (m *Manager) DumpSchema(ctx context.Context, c Conn, localHost string, localPort int, schema string, tables []string) error {
	if schema == "" {
		schema = "public"
	}
	pgDump := findPgDump()
	if pgDump == "" {
		return fmt.Errorf("pg_dump not in PATH (install a client matching the primary major version)")
	}
	fmt.Fprintf(os.Stderr, "→ dumping schema %q from primary with %s\n", schema, pgDump)
	args := []string{
		"-h", c.DialHost(), "-p", strconv.Itoa(c.Port), "-U", c.User, "-d", c.Database,
		"--schema-only",
		"--no-owner",
		"--no-acl",
		"--no-comments",
	}
	if len(tables) > 0 {
		for _, t := range tables {
			args = append(args, "-t", schema+"."+t)
		}
	} else {
		args = append(args, "--schema="+schema)
	}
	dump := exec.CommandContext(ctx, pgDump, args...)
	dump.Env = c.pgEnv()
	ddl, err := dump.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("pg_dump schema: %w (%s)", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return fmt.Errorf("pg_dump schema: %w", err)
	}

	// Filter noisy/harmless statements that conflict with a fresh initdb cluster.
	filtered := filterSchemaDDL(string(ddl))

	apply := exec.CommandContext(ctx, m.Bins.Psql,
		"-h", localHost, "-p", strconv.Itoa(localPort), "-d", "postgres",
		"-v", "ON_ERROR_STOP=1",
	)
	apply.Stdin = strings.NewReader(filtered)
	out, err := apply.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply schema locally: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(os.Stderr, "✓ schema applied on local main\n")
	return nil
}

func filterSchemaDDL(ddl string) string {
	var b strings.Builder
	for _, line := range strings.Split(ddl, "\n") {
		trim := strings.TrimSpace(line)
		upper := strings.ToUpper(trim)
		if strings.HasPrefix(upper, "CREATE SCHEMA") {
			continue
		}
		if strings.HasPrefix(upper, "COMMENT ON SCHEMA") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// findPgDump prefers newer Homebrew clients (needed when primary is PG17+ and PATH has PG14).
func findPgDump() string {
	candidates := []string{
		"/opt/homebrew/opt/postgresql@17/bin/pg_dump",
		"/opt/homebrew/opt/postgresql@16/bin/pg_dump",
		"/opt/homebrew/opt/postgresql@15/bin/pg_dump",
		"/usr/local/opt/postgresql@17/bin/pg_dump",
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	p, err := exec.LookPath("pg_dump")
	if err != nil {
		return ""
	}
	return p
}

// CreateSubscription starts logical replication into local main.
func (m *Manager) CreateSubscription(ctx context.Context, c Conn, localHost string, localPort int, subName, pubName string) error {
	parts := []string{
		fmt.Sprintf("host=%s", c.Host),
		fmt.Sprintf("port=%d", c.Port),
		fmt.Sprintf("user=%s", c.User),
		fmt.Sprintf("dbname=%s", c.Database),
		fmt.Sprintf("sslmode=%s", c.SSLMode),
	}
	if ip := lookupIPv4(c.Host); ip != "" {
		parts = append(parts, fmt.Sprintf("hostaddr=%s", ip))
	}
	if c.Password != "" {
		parts = append(parts, fmt.Sprintf("password=%s", c.Password))
	}
	conninfo := strings.Join(parts, " ")
	conninfoSQL := strings.ReplaceAll(conninfo, "'", "''")

	// DROP/CREATE must NOT share a transaction — create_slot cannot run in one.
	drop := fmt.Sprintf(`DROP SUBSCRIPTION IF EXISTS %s;`, quoteIdent(subName))
	create := fmt.Sprintf(`
CREATE SUBSCRIPTION %s
  CONNECTION '%s'
  PUBLICATION %s
  WITH (copy_data = true, create_slot = true, enabled = true);
`, quoteIdent(subName), conninfoSQL, quoteIdent(pubName))

	run := func(sql string) error {
		cmd := exec.CommandContext(ctx, m.Bins.Psql,
			"-h", localHost, "-p", strconv.Itoa(localPort), "-d", "postgres",
			"-v", "ON_ERROR_STOP=1", "-c", sql,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "→ CREATE SUBSCRIPTION %s (initial copy_data; can take a while)\n", subName)
	_ = run(drop) // ignore if missing
	// Wipe may have destroyed the subscriber without DROP SUBSCRIPTION → slot remains on primary.
	_ = m.DropReplicationSlot(ctx, c, subName)
	if err := run(create); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	return nil
}

func (m *Manager) LogicalSyncStatus(ctx context.Context, localHost string, localPort int, subName string) (LogicalStatus, error) {
	sql := fmt.Sprintf(`
SELECT
  (SELECT count(*) FROM pg_subscription_rel sr
     JOIN pg_subscription s ON s.oid = sr.srsubid
    WHERE s.subname = %s),
  (SELECT count(*) FROM pg_subscription_rel sr
     JOIN pg_subscription s ON s.oid = sr.srsubid
    WHERE s.subname = %s AND sr.srsubstate = 'r'),
  COALESCE((SELECT received_lsn::text FROM pg_stat_subscription WHERE subname = %s LIMIT 1), ''),
  COALESCE((SELECT subenabled::text FROM pg_subscription WHERE subname = %s), 'f');
`, quoteLiteral(subName), quoteLiteral(subName), quoteLiteral(subName), quoteLiteral(subName))

	cmd := exec.CommandContext(ctx, m.Bins.Psql,
		"-h", localHost, "-p", strconv.Itoa(localPort), "-d", "postgres",
		"-t", "-A", "-F", ",", "-c", sql,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return LogicalStatus{}, fmt.Errorf("logical status: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 4 {
		return LogicalStatus{}, fmt.Errorf("unexpected logical status: %q", out)
	}
	total, _ := strconv.Atoi(parts[0])
	ready, _ := strconv.Atoi(parts[1])
	return LogicalStatus{
		Subscription: subName,
		TableTotal:   total,
		TableReady:   ready,
		ReceivedLSN:  parts[2],
		Enabled:      parts[3] == "t",
	}, nil
}

func (m *Manager) WaitLogicalSync(ctx context.Context, localHost string, localPort int, subName string, timeout time.Duration) (LogicalStatus, error) {
	deadline := time.Now().Add(timeout)
	started := time.Now()
	var last LogicalStatus
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		default:
		}
		st, err := m.LogicalSyncStatus(ctx, localHost, localPort, subName)
		if err == nil {
			last = st
			pct := 0
			eta := "?"
			if st.TableTotal > 0 {
				pct = (100 * st.TableReady) / st.TableTotal
				if st.TableReady > 0 && st.TableReady < st.TableTotal {
					elapsed := time.Since(started)
					per := elapsed / time.Duration(st.TableReady)
					left := per * time.Duration(st.TableTotal-st.TableReady)
					eta = left.Round(time.Second).String()
				} else if st.TableReady >= st.TableTotal {
					eta = "0s"
				}
			}
			fmt.Fprintf(os.Stderr, "  sync tables %d/%d (%d%%) eta~%s enabled=%v lsn=%s\n",
				st.TableReady, st.TableTotal, pct, eta, st.Enabled, st.ReceivedLSN)
			if st.TableTotal > 0 && st.TableReady >= st.TableTotal {
				return st, nil
			}
			if st.TableTotal == 0 {
				time.Sleep(2 * time.Second)
				st2, err2 := m.LogicalSyncStatus(ctx, localHost, localPort, subName)
				if err2 == nil {
					last = st2
					if st2.TableTotal == 0 {
						return st2, nil
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last, fmt.Errorf("logical sync not ready within %s (ready=%d/%d)", timeout, last.TableReady, last.TableTotal)
}

func (m *Manager) psqlPrimary(ctx context.Context, c Conn, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, m.Bins.Psql,
		"-h", c.DialHost(), "-p", strconv.Itoa(c.Port), "-U", c.User, "-d", c.Database,
		"-v", "ON_ERROR_STOP=1", "-t", "-A", "-c", sql,
	)
	cmd.Env = c.pgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
