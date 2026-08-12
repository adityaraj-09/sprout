// Package postgres is Layer 4 — compute.
//
// Each branch gets its OWN postmaster process and PGDATA directory.
// Storage gave us the files; this package starts/stops Postgres against them.
package postgres

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Paths to Homebrew / system binaries (resolved at runtime).
type Binaries struct {
	InitDB       string
	Postgres     string
	PgCtl        string
	PgIsReady    string
	Createdb     string
	Psql         string
	PgBaseBackup string
}

func LookBinaries() (Binaries, error) {
	need := []string{"initdb", "postgres", "pg_ctl", "pg_isready", "createdb", "psql", "pg_basebackup"}
	found := map[string]string{}
	for _, n := range need {
		p, err := exec.LookPath(n)
		if err != nil {
			return Binaries{}, fmt.Errorf("missing %s in PATH (install PostgreSQL)", n)
		}
		found[n] = p
	}
	return Binaries{
		InitDB:       found["initdb"],
		Postgres:     found["postgres"],
		PgCtl:        found["pg_ctl"],
		PgIsReady:    found["pg_isready"],
		Createdb:     found["createdb"],
		Psql:         found["psql"],
		PgBaseBackup: found["pg_basebackup"],
	}, nil
}

// Instance is one Postgres process bound to one data directory + port.
type Instance struct {
	Name    string
	DataDir string
	Port    int
	LogFile string
	Bins    Binaries
}

func (i *Instance) ConnString(db string) string {
	return FormatConnString(i.Port, db)
}

// Init creates a brand-new cluster in DataDir (only for MAIN, never for branches).
func (i *Instance) Init() error {
	if err := os.MkdirAll(i.DataDir, 0o700); err != nil {
		return err
	}
	// initdb refuses non-empty dirs
	cmd := exec.Command(i.Bins.InitDB, "-D", i.DataDir, "--auth=trust", "--no-sync")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("initdb: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return ApplyNetworkSettings(i.DataDir, i.Port)
}

func (i *Instance) writeConfig() error {
	return ApplyNetworkSettings(i.DataDir, i.Port)
}

// PrepareClone makes a CoW-cloned PGDATA safe to start as an independent primary.
//
// Teaching notes — this is the "Postgres problem" from the architecture writeup:
//   1. postmaster.pid may be leftover from the parent snapshot → must remove on CLONE only
//   2. port must differ from parent
//   3. standby.signal would make it try to be a replica → remove if present
//   4. sockets/logs from parent are irrelevant
func (i *Instance) PrepareClone() error {
	pid := filepath.Join(i.DataDir, "postmaster.pid")
	_ = os.Remove(pid)

	_ = os.Remove(filepath.Join(i.DataDir, "standby.signal"))
	_ = os.Remove(filepath.Join(i.DataDir, "recovery.signal"))

	return ApplyNetworkSettings(i.DataDir, i.Port)
}

func (i *Instance) Start() error {
	if err := os.MkdirAll(filepath.Dir(i.LogFile), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(i.Bins.PgCtl, "-D", i.DataDir, "-l", i.LogFile, "start", "-o", fmt.Sprintf("-p %d", i.Port))
	out, err := cmd.CombinedOutput()
	if err != nil {
		logTail, _ := os.ReadFile(i.LogFile)
		return fmt.Errorf("pg_ctl start: %w (%s)\nlog:\n%s", err, strings.TrimSpace(string(out)), string(logTail))
	}
	return i.WaitReady(30 * time.Second)
}

func (i *Instance) Stop() error {
	cmd := exec.Command(i.Bins.PgCtl, "-D", i.DataDir, "-m", "fast", "stop")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Already stopped is OK for idempotent cleanup.
		if strings.Contains(string(out), "Is server running?") || strings.Contains(string(out), "does not exist") {
			return nil
		}
		return fmt.Errorf("pg_ctl stop: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (i *Instance) IsRunning() bool {
	cmd := exec.Command(i.Bins.PgIsReady, "-h", "127.0.0.1", "-p", strconv.Itoa(i.Port))
	return cmd.Run() == nil
}

func (i *Instance) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if i.IsRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	logTail, _ := os.ReadFile(i.LogFile)
	return fmt.Errorf("postgres on port %d not ready after %s\nlog:\n%s", i.Port, timeout, string(logTail))
}

// Checkpoint asks a running instance to flush dirty pages.
// Used before snapshot so the frozen image needs less WAL replay.
func (i *Instance) Checkpoint() error {
	cmd := exec.Command(i.Bins.Psql, "-h", "127.0.0.1", "-p", strconv.Itoa(i.Port), "-d", "postgres", "-c", "CHECKPOINT;")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("CHECKPOINT: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExecSQL runs SQL as the local superuser (trust).
func (i *Instance) ExecSQL(db, sql string) (string, error) {
	cmd := exec.Command(i.Bins.Psql, "-h", "127.0.0.1", "-p", strconv.Itoa(i.Port), "-d", db, "-v", "ON_ERROR_STOP=1", "-c", sql)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// SeedDemo loads a small but non-trivial table so branch experiments are visible.
func (i *Instance) SeedDemo() error {
	sql := `
CREATE TABLE IF NOT EXISTS users (
  id   bigserial PRIMARY KEY,
  email text NOT NULL,
  note  text
);
INSERT INTO users (email, note)
SELECT 'user' || g || '@example.com', 'seed row ' || g
FROM generate_series(1, 10000) g
WHERE NOT EXISTS (SELECT 1 FROM users LIMIT 1);
`
	_, err := i.ExecSQL("postgres", sql)
	return err
}
