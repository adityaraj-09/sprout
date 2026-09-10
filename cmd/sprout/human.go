package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/cliconfig"
	"github.com/adityaraj/sprout/internal/dx"
	"github.com/adityaraj/sprout/internal/meta"
)

func printConnectorList(list []meta.Connector) {
	if len(list) == 0 {
		fmt.Println("(no connectors)")
		return
	}
	for _, conn := range list {
		eng := conn.Engine
		if eng == "" {
			eng = "postgres"
		}
		lag := ""
		if conn.LastLagBytes > 0 {
			lag = fmt.Sprintf(" lag=%d", conn.LastLagBytes)
		}
		sql := ""
		if strings.TrimSpace(conn.BranchSQL) != "" {
			sql = " hook"
		}
		fmt.Printf("%s %-14s %-8s %-10s %-14s :%-5d%s%s  %s\n",
			dx.StatusMark(conn.Status), conn.Name, eng, conn.Mode, conn.Status, conn.Port, lag, sql, dx.RelAge(conn.UpdatedAt))
		if conn.PrimaryURL != "" {
			fmt.Printf("    %s\n", conn.PrimaryURL)
		}
	}
}

func printBranchList(list []meta.BranchRecord) {
	cur := cliconfig.Load()
	if len(list) == 0 {
		fmt.Println("(no branches)")
		return
	}
	for _, b := range list {
		star := " "
		if b.Name == cur.CurrentBranch && (cur.CurrentFrom == "" || b.SourceConnector == cur.CurrentFrom || b.Role != "branch") {
			star = "*"
		}
		src := b.SourceConnector
		if src == "" {
			src = b.Role
		}
		eng := engineFromConn(b.ConnString)
		used := b.LastUsedAt
		if used.IsZero() {
			used = b.UpdatedAt
		}
		fmt.Printf("%s %s %-16s %-10s %-8s from=%-12s used=%-8s %s\n",
			star, dx.StatusMark(b.Status), b.Name, b.Status, eng, src, dx.RelAge(used), b.ConnString)
	}
}

func engineFromConn(cs string) string {
	switch {
	case strings.HasPrefix(cs, "mongodb"):
		return "mongodb"
	case strings.HasPrefix(cs, "http://") || strings.HasPrefix(cs, "https://"):
		return "qdrant"
	default:
		return "postgres"
	}
}

func currentBranchName(flagName, flagFrom string) (name, from string) {
	if flagName != "" {
		return flagName, flagFrom
	}
	cfg := cliconfig.Load()
	return cfg.CurrentBranch, cfg.CurrentFrom
}

func switchBranch(name, from string) error {
	if name == "" {
		return fmt.Errorf("usage: sprout branch switch <name> [--from=<connector>]")
	}
	_, err := cliconfig.Save(cliconfig.File{CurrentBranch: name, CurrentFrom: from})
	if err != nil {
		return err
	}
	if from != "" {
		fmt.Printf("✓ current branch %s --from=%s (%s)\n", name, from, cliconfig.Path())
	} else {
		fmt.Printf("✓ current branch %s (%s)\n", name, cliconfig.Path())
	}
	return nil
}

func runEnv(c *client, args []string) error {
	write := ""
	name, from := "", ""
	printURL := c.out.printURL
	for _, a := range args {
		if strings.HasPrefix(a, "--write=") {
			write = strings.TrimPrefix(a, "--write=")
			continue
		}
		if a == "--write" {
			write = ".env.sprout"
			continue
		}
		if strings.HasPrefix(a, "--from=") {
			from = strings.TrimPrefix(a, "--from=")
			continue
		}
		if a == "--print-url" {
			printURL = true
			continue
		}
		if name == "" && !strings.HasPrefix(a, "-") {
			name = a
		}
	}
	name, from = currentBranchName(name, from)
	if name == "" {
		return fmt.Errorf("no current branch — sprout branch switch <name> or pass a name")
	}
	var rec meta.BranchRecord
	if err := c.do("GET", branchURL(name, from, ""), nil, &rec); err != nil {
		return err
	}
	if printURL || c.out.printURL {
		emitURL(rec.ConnString)
		return nil
	}
	lines := dx.EnvLines(rec.ConnString)
	if c.out.json() {
		out := map[string]any{
			"name": rec.Name, "from": rec.SourceConnector,
			"connection_string": rec.ConnString, "env": lines,
		}
		emitJSON(out)
		return nil
	}
	if write != "" {
		header := []string{
			fmt.Sprintf("# sprout env — %s from %s  %s", rec.Name, rec.SourceConnector, time.Now().UTC().Format(time.RFC3339)),
		}
		if err := dx.WriteEnvFile(write, append(header, lines...)); err != nil {
			return err
		}
		fmt.Printf("✓ wrote %s\n", write)
		for _, l := range lines {
			fmt.Println(l)
		}
		return nil
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

func printPreflight(rep map[string]any) {
	ok, _ := rep["ok"].(bool)
	if ok {
		fmt.Println("✓ preflight ok — nothing was created")
	} else {
		fmt.Println("✗ preflight found problems — nothing was created")
	}
	fmt.Printf("  engine=%v mode=%v\n", rep["engine"], rep["mode"])
	checks, _ := rep["checks"].([]any)
	for _, raw := range checks {
		ch, _ := raw.(map[string]any)
		mark := "✓"
		if v, _ := ch["ok"].(bool); !v {
			mark = "✗"
		}
		fmt.Printf("%s %-18s %v\n", mark, ch["name"], ch["detail"])
		if hint, _ := ch["hint"].(string); hint != "" {
			fmt.Printf("    hint: %s\n", hint)
		}
		if sql, _ := ch["sql"].(string); sql != "" {
			fmt.Printf("    sql:  %s\n", sql)
		}
	}
	if fix, ok := rep["fix_sql"].([]any); ok && len(fix) > 0 {
		fmt.Println("fix SQL:")
		for _, s := range fix {
			fmt.Printf("  %s\n", s)
		}
	}
	if !ok {
		os.Exit(1)
	}
}
