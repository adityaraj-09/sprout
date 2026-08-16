package mysql

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func (c Conn) clientArgs() []string {
	args := []string{
		"-h", c.Host,
		"-P", strconv.Itoa(c.Port),
		"-u", c.User,
		"--protocol=TCP",
		c.sslArg(),
	}
	return args
}

func (m Binaries) Ping(ctx context.Context, c Conn) error {
	args := append(c.clientArgs(), "-e", "SELECT 1")
	cmd := exec.CommandContext(ctx, m.Mysql, args...)
	cmd.Env = c.pgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mysql ping: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m Binaries) ListUserDatabases(ctx context.Context, c Conn) ([]string, error) {
	if c.Database != "" && c.Database != "*" {
		return []string{c.Database}, nil
	}
	args := append(c.clientArgs(), "-N", "-e", "SHOW DATABASES")
	cmd := exec.CommandContext(ctx, m.Mysql, args...)
	cmd.Env = c.pgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("SHOW DATABASES: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	var dbs []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		switch name {
		case "", "information_schema", "performance_schema", "sys", "mysql":
			continue
		}
		dbs = append(dbs, name)
	}
	return dbs, nil
}

func (m Binaries) Estimate(ctx context.Context, c Conn, tables []string) (map[string]any, error) {
	dbs, err := m.ListUserDatabases(ctx, c)
	if err != nil {
		return nil, err
	}
	filter := ""
	if len(tables) > 0 {
		quoted := make([]string, 0, len(tables))
		for _, t := range tables {
			quoted = append(quoted, "'"+strings.ReplaceAll(t, "'", "''")+"'")
		}
		filter = " AND table_name IN (" + strings.Join(quoted, ",") + ")"
	}
	in := make([]string, 0, len(dbs))
	for _, d := range dbs {
		in = append(in, "'"+strings.ReplaceAll(d, "'", "''")+"'")
	}
	if len(in) == 0 {
		return map[string]any{"databases": dbs, "tables": 0, "rows": 0}, nil
	}
	sql := `SELECT COUNT(*), COALESCE(SUM(table_rows),0) FROM information_schema.tables WHERE table_schema IN (` + strings.Join(in, ",") + `) AND table_type='BASE TABLE'` + filter
	args := append(c.clientArgs(), "-N", "-e", sql)
	cmd := exec.CommandContext(ctx, m.Mysql, args...)
	cmd.Env = c.pgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("estimate: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	tablesN, rowsN := 0, 0
	if len(fields) >= 1 {
		tablesN, _ = strconv.Atoi(fields[0])
	}
	if len(fields) >= 2 {
		rowsN, _ = strconv.Atoi(fields[1])
	}
	return map[string]any{
		"engine":    "mysql",
		"databases": dbs,
		"tables":    tablesN,
		"rows":      rowsN,
		"note":      "MySQL connect is a snapshot import (mysqldump). It does not follow binlog.",
	}, nil
}

func (m Binaries) DumpImport(ctx context.Context, c Conn, local *Instance, tables []string) error {
	dbs, err := m.ListUserDatabases(ctx, c)
	if err != nil {
		return err
	}
	if len(dbs) == 0 {
		return fmt.Errorf("no user databases to dump on %s:%d", c.Host, c.Port)
	}
	args := append(c.clientArgs(),
		"--single-transaction",
		"--routines",
		"--triggers",
		"--set-gtid-purged=OFF",
		"--databases",
	)
	args = append(args, dbs...)
	if len(tables) > 0 {
		// Per-table dump of the URL database only.
		args = c.clientArgs()
		args = append(args, "--single-transaction", "--routines", "--triggers", "--set-gtid-purged=OFF")
		if c.Database != "" {
			args = append(args, c.Database)
		} else {
			args = append(args, dbs[0])
		}
		for _, t := range tables {
			args = append(args, t)
		}
	}
	fmt.Fprintf(os.Stderr, "→ dumping %d MySQL database(s) with %s\n", len(dbs), m.Mysqldump)
	dump := exec.CommandContext(ctx, m.Mysqldump, args...)
	dump.Env = c.pgEnv()
	pipe, err := dump.StdoutPipe()
	if err != nil {
		return err
	}
	dump.Stderr = os.Stderr
	apply := exec.CommandContext(ctx, local.Bins.Mysql, "--socket="+local.socket(), "-uroot", "--protocol=SOCKET")
	apply.Stdin = pipe
	if err := dump.Start(); err != nil {
		return fmt.Errorf("mysqldump: %w", err)
	}
	out, err := apply.CombinedOutput()
	waitErr := dump.Wait()
	if err != nil {
		return fmt.Errorf("apply dump: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if waitErr != nil {
		return fmt.Errorf("mysqldump: %w", waitErr)
	}
	fmt.Fprintf(os.Stderr, "✓ dump applied on local mysqld\n")
	return nil
}
