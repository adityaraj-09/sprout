package mongo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func (c Conn) dumpArgs() []string {
	return []string{"--uri=" + c.dumpURI()}
}

func (m Binaries) Ping(ctx context.Context, c Conn) error {
	if m.Mongosh != "" || m.Mongo != "" {
		shell := m.Mongosh
		if shell == "" {
			shell = m.Mongo
		}
		cmd := exec.CommandContext(ctx, shell, "--quiet", c.dumpURI(), "--eval", "db.runCommand({ping:1})")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("mongo ping: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, m.Mongodump, append(c.dumpArgs(), "--archive=-", "--quiet")...)
	cmd.Stdout = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongo ping: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m Binaries) Estimate(ctx context.Context, c Conn, collections []string) (map[string]any, error) {
	note := "mongodb connect is a mongodump snapshot into a local mongod (no oplog follow)"
	out := map[string]any{"engine": "mongodb", "note": note}
	if c.Database != "" {
		out["database"] = c.Database
	}
	if len(collections) > 0 {
		out["collections"] = collections
	}
	return out, nil
}

func (m Binaries) DumpImport(ctx context.Context, c Conn, local *Instance, collections []string) error {
	if local == nil {
		return fmt.Errorf("local mongod instance required")
	}
	dir, err := os.MkdirTemp("", "sprout-mongodump-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	args := append(c.dumpArgs(), "--out="+dir)
	if c.Database != "" {
		args = append(args, "--db="+c.Database)
	}
	if len(collections) > 0 {
		if c.Database == "" {
			return fmt.Errorf("invalid_body: --tables requires a database in the mongodb URL")
		}
		for _, col := range collections {
			one := append([]string{}, args...)
			one = append(one, "--collection="+col)
			cmd := exec.CommandContext(ctx, m.Mongodump, one...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("mongodump %s: %w (%s)", col, err, strings.TrimSpace(string(out)))
			}
		}
	} else {
		cmd := exec.CommandContext(ctx, m.Mongodump, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("mongodump: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	}

	// Drop dumped system DBs so Atlas admin users never land on a standalone branch.
	for _, skip := range []string{"local", "config", "admin"} {
		_ = os.RemoveAll(filepath.Join(dir, skip))
	}

	restore := []string{
		"--host=127.0.0.1",
		"--port=" + strconv.Itoa(local.Port),
	}
	restore = append(restore, toolTLSFlags(m.Mongorestore)...)
	restore = append(restore, "--drop", dir)
	cmd := exec.CommandContext(ctx, m.Mongorestore, restore...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongorestore: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// toolTLSFlags returns database-tools TLS flags. MongoDB Database Tools 100.x
// expose --ssl/--tlsInsecure; newer builds use --tls/--tlsAllowInvalidCertificates.
func toolTLSFlags(bin string) []string {
	out, _ := exec.Command(bin, "--help").CombinedOutput()
	help := string(out)
	if strings.Contains(help, "--tlsAllowInvalidCertificates") || (strings.Contains(help, "--tls") && !strings.Contains(help, "--tlsInsecure")) {
		return []string{"--tls", "--tlsAllowInvalidCertificates"}
	}
	if strings.Contains(help, "--ssl") {
		if strings.Contains(help, "--tlsInsecure") {
			return []string{"--ssl", "--tlsInsecure"}
		}
		if strings.Contains(help, "--sslAllowInvalidCertificates") {
			return []string{"--ssl", "--sslAllowInvalidCertificates"}
		}
		return []string{"--ssl"}
	}
	return []string{"--tls", "--tlsAllowInvalidCertificates"}
}
