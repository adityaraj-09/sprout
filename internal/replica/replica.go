package replica

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/postgres"
)

// Conn is a parsed Postgres URL for physical replication bootstrap.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
	Raw      string
}

func ParseURL(raw string) (Conn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Conn{}, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return Conn{}, fmt.Errorf("url scheme must be postgres/postgresql")
	}
	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := 5432
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	user := "postgres"
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		db = "postgres"
	}
	ssl := u.Query().Get("sslmode")
	if ssl == "" {
		if host == "127.0.0.1" || host == "localhost" {
			ssl = "disable"
		} else {
			ssl = "require" // Supabase / cloud primaries
		}
	}
	return Conn{Host: host, Port: port, User: user, Password: pass, Database: db, SSLMode: ssl, Raw: raw}, nil
}

func (c Conn) PrimaryConnInfo() string {
	parts := []string{
		fmt.Sprintf("host=%s", c.Host),
		fmt.Sprintf("port=%d", c.Port),
		fmt.Sprintf("user=%s", c.User),
		fmt.Sprintf("sslmode=%s", c.SSLMode),
		"application_name=sprout-replica",
	}
	// Prefer IPv4 — many VMs (Azure) have broken IPv6 routes while DNS returns AAAA first.
	if ip := lookupIPv4(c.Host); ip != "" {
		parts = append(parts, fmt.Sprintf("hostaddr=%s", ip))
	}
	if c.Password != "" {
		parts = append(parts, fmt.Sprintf("password=%s", c.Password))
	}
	return strings.Join(parts, " ")
}

func (c Conn) pgEnv() []string {
	return append(os.Environ(),
		"PGPASSWORD="+c.Password,
		"PGSSLMODE="+c.SSLMode,
	)
}

// DialHost returns an IPv4 address when available so clients skip broken IPv6 routes.
func (c Conn) DialHost() string {
	if ip := lookupIPv4(c.Host); ip != "" {
		return ip
	}
	return c.Host
}

// Manager performs full physical bootstrap + standby helpers.
type Manager struct {
	Bins postgres.Binaries
}

func (m *Manager) Ping(ctx context.Context, c Conn) error {
	args := []string{
		"-h", c.DialHost(), "-p", strconv.Itoa(c.Port), "-U", c.User, "-d", c.Database,
		"-v", "ON_ERROR_STOP=1", "-c", "SELECT 1;",
	}
	cmd := exec.CommandContext(ctx, m.Bins.Psql, args...)
	cmd.Env = c.pgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		hint := ""
		if strings.Contains(msg, "Network is unreachable") || strings.Contains(err.Error(), "network is unreachable") {
			hint = " — tip: prefer IPv4 (hostaddr) or fix broken IPv6 on this VM; Sprout already tries tcp4 first"
		}
		return fmt.Errorf("cannot connect to primary: %w (%s)%s", err, msg, hint)
	}
	return nil
}

// BaseBackup copies the entire primary cluster into destDir (SLOW — full data copy).
// Uses pg_basebackup -R so standby.signal + primary_conninfo are written.
func (m *Manager) BaseBackup(ctx context.Context, c Conn, destDir string) error {
	if err := os.RemoveAll(destDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "→ pg_basebackup from %s:%d → %s (full copy; can take a long time)\n", c.Host, c.Port, destDir)
	start := time.Now()

	args := []string{
		"-h", c.DialHost(),
		"-p", strconv.Itoa(c.Port),
		"-U", c.User,
		"-D", destDir,
		"-R",          // write recovery config
		"-X", "stream", // stream WAL while copying
		"-c", "fast",
		"--no-password",
	}
	cmd := exec.CommandContext(ctx, m.Bins.PgBaseBackup, args...)
	cmd.Env = c.pgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(destDir)
		return fmt.Errorf("pg_basebackup: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(os.Stderr, "✓ basebackup finished in %s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

// PrepareStandbyDataDir forces our listen port / socket after basebackup.
func (m *Manager) PrepareStandbyDataDir(dataDir string, port int) error {
	_ = os.Remove(filepath.Join(dataDir, "postmaster.pid"))
	if err := postgres.ApplyNetworkSettings(dataDir, port); err != nil {
		return err
	}
	conf := filepath.Join(dataDir, "postgresql.conf")
	f, err := os.OpenFile(conf, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(`
# --- sprout replica ---
hot_standby = on
`)
	return err
}

type Lag struct {
	IsStandby     bool   `json:"is_standby"`
	ReceiveLSN   string `json:"receive_lsn"`
	ReplayLSN    string `json:"replay_lsn"`
	ReplayPause   bool   `json:"replay_paused"`
	LagBytes      int64  `json:"lag_bytes"`
	InRecovery    bool   `json:"in_recovery"`
}

func (m *Manager) Status(ctx context.Context, host string, port int) (Lag, error) {
	sql := `
SELECT pg_is_in_recovery(),
       COALESCE(pg_last_wal_receive_lsn()::text, ''),
       COALESCE(pg_last_wal_replay_lsn()::text, ''),
       pg_is_wal_replay_paused(),
       COALESCE(pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()), 0);
`
	cmd := exec.CommandContext(ctx, m.Bins.Psql,
		"-h", host, "-p", strconv.Itoa(port), "-d", "postgres",
		"-v", "ON_ERROR_STOP=1", "-t", "-A", "-F", ",",
		"-c", sql,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Lag{}, fmt.Errorf("replica status: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	line := strings.TrimSpace(string(out))
	parts := strings.Split(line, ",")
	if len(parts) < 5 {
		return Lag{}, fmt.Errorf("unexpected status output: %q", line)
	}
	lagBytes, _ := strconv.ParseInt(parts[4], 10, 64)
	return Lag{
		InRecovery:  parts[0] == "t",
		IsStandby:   parts[0] == "t",
		ReceiveLSN:  parts[1],
		ReplayLSN:   parts[2],
		ReplayPause: parts[3] == "t",
		LagBytes:    lagBytes,
	}, nil
}

func (m *Manager) PauseReplay(ctx context.Context, host string, port int) error {
	return m.execSQL(ctx, host, port, "SELECT pg_wal_replay_pause();")
}

func (m *Manager) ResumeReplay(ctx context.Context, host string, port int) error {
	return m.execSQL(ctx, host, port, "SELECT pg_wal_replay_resume();")
}

func (m *Manager) Checkpoint(ctx context.Context, host string, port int) error {
	return m.execSQL(ctx, host, port, "CHECKPOINT;")
}

func (m *Manager) WaitCaughtUp(ctx context.Context, host string, port int, maxLagBytes int64, timeout time.Duration) (Lag, error) {
	deadline := time.Now().Add(timeout)
	var last Lag
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		default:
		}
		st, err := m.Status(ctx, host, port)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		last = st
		if st.IsStandby && st.LagBytes <= maxLagBytes && st.ReceiveLSN != "" {
			return st, nil
		}
		// Primary mode (not a standby) — treat as caught up for local init demos.
		if !st.IsStandby {
			return st, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return last, fmt.Errorf("replica not caught up within %s (lag_bytes=%d)", timeout, last.LagBytes)
}

func (m *Manager) execSQL(ctx context.Context, host string, port int, sql string) error {
	cmd := exec.CommandContext(ctx, m.Bins.Psql,
		"-h", host, "-p", strconv.Itoa(port), "-d", "postgres",
		"-v", "ON_ERROR_STOP=1", "-c", sql,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", sql, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsurePrimaryReachable is a quick TCP check before bootstrap.
// Tries IPv4 first — dual-stack dial often picks unreachable AAAA on cloud VMs.
func EnsurePrimaryReachable(host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var errs []string
	for _, network := range []string{"tcp4", "tcp6"} {
		conn, err := net.DialTimeout(network, addr, timeout)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", network, err))
	}
	hint := ""
	if strings.Contains(strings.Join(errs, " "), "network is unreachable") || strings.Contains(strings.Join(errs, " "), "no such host") {
		hint = " — tip: host may be IPv6-only or DNS has no A record; enable IPv4 on the upstream, use a pooler with IPv4, or fix VM IPv6 routing"
	}
	return fmt.Errorf("primary %s not reachable (%s)%s", addr, strings.Join(errs, "; "), hint)
}

func lookupIPv4(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
		return ""
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}
