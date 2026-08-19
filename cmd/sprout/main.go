// Command sprout — Phase 2 thin CLI (HTTP client to sprout-server).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/config"
	"github.com/adityaraj/sprout/internal/meta"
)

func main() {
	orgFlag := peelOrg()
	cfg := config.CLIDefaults()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	c := &client{
		base: strings.TrimRight(cfg.ServerURL, "/"), token: cfg.Token, org: resolveOrg(orgFlag),
		http: &http.Client{Timeout: 60 * time.Minute},
	}

	switch os.Args[1] {
	case "login":
		if err := runLogin(c.base); err != nil {
			fatal(err)
		}
	case "logout":
		if err := runLogout(); err != nil {
			fatal(err)
		}
	case "whoami":
		if err := runWhoAmI(c); err != nil {
			fatal(err)
		}
	case "org":
		if err := runOrg(c, os.Args[2:]); err != nil {
			fatal(err)
		}
	case "init":
		var proj meta.Project
		if err := c.do("POST", "/v1/init", nil, &proj); err != nil {
			fatal(err)
		}
		fmt.Printf("✓ project %s (%s)\n", proj.Name, proj.ID)
	case "doctor":
		var out map[string]any
		if err := c.do("GET", "/v1/doctor", nil, &out); err != nil {
			fatal(err)
		}
		ok, _ := out["ok"].(bool)
		if ok {
			fmt.Println("✓ doctor ok")
		} else {
			fmt.Println("✗ doctor found problems")
		}
		if checks, ok := out["checks"].([]any); ok {
			for _, raw := range checks {
				ch, _ := raw.(map[string]any)
				mark := "✓"
				if v, _ := ch["ok"].(bool); !v {
					mark = "✗"
				} else if lvl, _ := ch["level"].(string); lvl == "warn" {
					mark = "!"
				}
				fmt.Printf("%s %-16s %v\n", mark, ch["name"], ch["detail"])
				if hint, _ := ch["hint"].(string); hint != "" {
					fmt.Printf("    hint: %s\n", hint)
				}
			}
		}
		if !ok {
			os.Exit(1)
		}
	case "connect":
		// sprout connect [--name=...] [--engine=postgres|mongodb] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
		mode := ""
		engineName := ""
		name := "primary"
		url := ""
		wipe := true
		dryRun := false
		var tables []string
		for _, a := range os.Args[2:] {
			if strings.HasPrefix(a, "--mode=") {
				mode = strings.TrimPrefix(a, "--mode=")
				continue
			}
			if strings.HasPrefix(a, "--engine=") {
				engineName = strings.TrimPrefix(a, "--engine=")
				continue
			}
			if strings.HasPrefix(a, "--name=") {
				name = strings.TrimPrefix(a, "--name=")
				continue
			}
			if strings.HasPrefix(a, "--tables=") {
				raw := strings.TrimPrefix(a, "--tables=")
				for _, t := range strings.Split(raw, ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						tables = append(tables, t)
					}
				}
				continue
			}
			if a == "--logical" {
				mode = "logical"
				continue
			}
			if a == "--physical" {
				mode = "physical"
				continue
			}
			if a == "--wipe" {
				wipe = true
				continue
			}
			if a == "--no-wipe" {
				wipe = false
				continue
			}
			if a == "--dry-run" {
				dryRun = true
				continue
			}
			if url == "" && !strings.HasPrefix(a, "-") {
				url = a
			}
		}
		if url == "" {
			fatal(fmt.Errorf("usage: sprout connect [--name=<id>] [--engine=postgres|mongodb] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>"))
		}
		body := map[string]any{"url": url, "mode": mode, "name": name, "wipe": wipe, "dry_run": dryRun}
		if engineName != "" {
			body["engine"] = engineName
		}
		if len(tables) > 0 {
			body["tables"] = tables
		}
		var out map[string]any
		if err := c.doProgress("POST", "/v1/projects/default/connect", body, &out); err != nil {
			fatal(err)
		}
		if dry, _ := out["dry_run"].(bool); dry {
			fmt.Println("dry-run estimate (will hit prod once for real bootstrap):")
			b, _ := json.MarshalIndent(out["estimate"], "", "  ")
			fmt.Println(string(b))
			return
		}
		if cs, _ := out["connection_string"].(string); cs != "" {
			fmt.Println("✓ connected")
			fmt.Println(" ", cs)
			if psql, _ := out["psql"].(string); psql != "" {
				fmt.Println(" ", psql)
			}
			if sh, _ := out["mongosh"].(string); sh != "" {
				fmt.Println(" ", sh)
			}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "status":
		path := "/v1/projects/default/replication"
		if len(os.Args) >= 3 && !strings.HasPrefix(os.Args[2], "-") {
			path = "/v1/projects/default/connectors/" + os.Args[2] + "/replication"
		}
		var out map[string]any
		if err := c.do("GET", path, nil, &out); err != nil {
			fatal(err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "connector":
		if len(os.Args) < 3 {
			usage()
			os.Exit(2)
		}
		switch os.Args[2] {
		case "list":
			var list []meta.Connector
			if err := c.do("GET", "/v1/connectors", nil, &list); err != nil {
				fatal(err)
			}
			if len(list) == 0 {
				fmt.Println("(no connectors)")
				return
			}
			for _, conn := range list {
				fmt.Printf("%-12s %-10s %-14s port=%-5d lag_bytes=%d lsn=%s\n  dir=%s\n  %s\n",
					conn.Name, conn.Mode, conn.Status, conn.Port, conn.LastLagBytes, conn.LastLSN, conn.DataDir, conn.PrimaryURL)
			}
		case "delete":
			force := false
			name := ""
			for _, a := range os.Args[3:] {
				if a == "--force" {
					force = true
					continue
				}
				if name == "" && !strings.HasPrefix(a, "-") {
					name = a
				}
			}
			if name == "" {
				fatal(fmt.Errorf("usage: sprout connector delete <name> [--force]"))
			}
			path := "/v1/projects/default/connectors/" + name
			if force {
				path += "?force=true"
			}
			if err := c.do("DELETE", path, nil, nil); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ deleted connector %s\n", name)
		case "suspend":
			need(4)
			var out map[string]any
			if err := c.do("POST", "/v1/projects/default/connectors/"+os.Args[3]+"/suspend", nil, &out); err != nil {
				fatal(err)
			}
			if msg, _ := out["message"].(string); msg != "" {
				fmt.Println("✓", msg)
			} else {
				fmt.Printf("✓ suspended connector %s\n", os.Args[3])
			}
		case "resume":
			need(4)
			var out map[string]any
			if err := c.do("POST", "/v1/projects/default/connectors/"+os.Args[3]+"/resume", nil, &out); err != nil {
				fatal(err)
			}
			if msg, _ := out["message"].(string); msg != "" {
				fmt.Println("✓", msg)
			} else {
				fmt.Printf("✓ resumed connector %s\n", os.Args[3])
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		default:
			usage()
			os.Exit(2)
		}
	case "branch":
		if len(os.Args) < 3 {
			usage()
			os.Exit(2)
		}
		switch os.Args[2] {
		case "create":
			need(4)
			from := ""
			name := ""
			for _, a := range os.Args[3:] {
				if strings.HasPrefix(a, "--from=") {
					from = strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "--from=")), ",;")
					continue
				}
				if name == "" && !strings.HasPrefix(a, "-") {
					name = strings.Trim(strings.TrimSpace(a), ",;")
				}
			}
			if name == "" {
				fatal(fmt.Errorf("usage: sprout branch create <name> [--from=<connector>]"))
			}
			var rec map[string]any
			body := map[string]string{"name": name}
			if from != "" {
				body["from"] = from
			}
			if err := c.doProgress("POST", "/v1/projects/default/branches", body, &rec); err != nil {
				fatal(err)
			}
			src, _ := rec["source_connector"].(string)
			if src == "" {
				src = "main"
			}
			cs, _ := rec["connection_string"].(string)
			psql, _ := rec["psql"].(string)
			mongosh, _ := rec["mongosh"].(string)
			status, _ := rec["status"].(string)
			bname, _ := rec["name"].(string)
			fmt.Printf("✓ %s [%s] from=%s\n  %s\n", bname, status, src, cs)
			if psql != "" {
				fmt.Println(" ", psql)
			}
			if mongosh != "" {
				fmt.Println(" ", mongosh)
			}
		case "list":
			var list []meta.BranchRecord
			if err := c.do("GET", "/v1/projects/default/branches", nil, &list); err != nil {
				fatal(err)
			}
			for _, b := range list {
				src := b.SourceConnector
				if src == "" && b.Role == "branch" {
					src = "-"
				}
				if b.Role == "branch" {
					fmt.Printf("%-16s %-10s port=%-5d from=%-12s %s\n", b.Name, b.Status, b.Port, src, b.ConnString)
				} else {
					fmt.Printf("%-16s %-10s port=%-5d role=%-8s %s\n", b.Name, b.Status, b.Port, b.Role, b.ConnString)
				}
			}
		case "get":
			need(4)
			bname, from := parseNameFrom(os.Args[3:])
			if bname == "" {
				fatal(fmt.Errorf("usage: sprout branch get <name> [--from=<connector>]"))
			}
			var rec meta.BranchRecord
			if err := c.do("GET", branchURL(bname, from, ""), nil, &rec); err != nil {
				fatal(err)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(rec)
		case "diff":
			need(4)
			bname, from := parseNameFrom(os.Args[3:])
			if bname == "" {
				fatal(fmt.Errorf("usage: sprout branch diff <name> [--from=<connector>]"))
			}
			var out map[string]any
			if err := c.do("GET", branchURL(bname, from, "/diff"), nil, &out); err != nil {
				fatal(err)
			}
			if sum, _ := out["summary"].(string); sum != "" {
				fmt.Println(sum)
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		case "reset":
			need(4)
			bname, from := parseNameFrom(os.Args[3:])
			if bname == "" {
				fatal(fmt.Errorf("usage: sprout branch reset <name> [--from=<connector>]"))
			}
			var rec meta.BranchRecord
			if err := c.do("POST", branchURL(bname, from, "/reset"), nil, &rec); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ reset %s\n  %s\n", rec.Name, rec.ConnString)
		case "delete":
			need(4)
			bname, from := parseNameFrom(os.Args[3:])
			if bname == "" {
				fatal(fmt.Errorf("usage: sprout branch delete <name> [--from=<connector>]"))
			}
			if err := c.do("DELETE", branchURL(bname, from, ""), nil, nil); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ deleted %s\n", bname)
		case "suspend":
			need(4)
			bname, from := parseNameFrom(os.Args[3:])
			if bname == "" {
				fatal(fmt.Errorf("usage: sprout branch suspend <name> [--from=<connector>]"))
			}
			var rec meta.BranchRecord
			if err := c.do("POST", branchURL(bname, from, "/suspend"), nil, &rec); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ suspended %s (status=%s)\n", rec.Name, rec.Status)
		case "resume":
			need(4)
			bname, from := parseNameFrom(os.Args[3:])
			if bname == "" {
				fatal(fmt.Errorf("usage: sprout branch resume <name> [--from=<connector>]"))
			}
			var rec meta.BranchRecord
			if err := c.do("POST", branchURL(bname, from, "/resume"), nil, &rec); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ resumed %s\n  %s\n", rec.Name, rec.ConnString)
		default:
			usage()
			os.Exit(2)
		}
	case "health":
		var out map[string]string
		if err := c.do("GET", "/healthz", nil, &out); err != nil {
			fatal(err)
		}
		fmt.Println(out["status"])
	default:
		usage()
		os.Exit(2)
	}
}

func need(n int) {
	if len(os.Args) < n {
		usage()
		os.Exit(2)
	}
}

func parseNameFrom(args []string) (name, from string) {
	for _, a := range args {
		if strings.HasPrefix(a, "--from=") {
			from = strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "--from=")), ",;")
			continue
		}
		if name == "" && !strings.HasPrefix(a, "-") {
			name = strings.Trim(strings.TrimSpace(a), ",;")
		}
	}
	return name, from
}

func branchURL(name, from, extra string) string {
	p := "/v1/projects/default/branches/" + url.PathEscape(name) + extra
	if from != "" {
		return p + "?from=" + url.QueryEscape(from)
	}
	return p
}

func usage() {
	fmt.Fprintf(os.Stderr, `sprout — CLI (talks to sprout-server)

Usage:
  sprout [--org=<name>] <command> ...

  sprout login              GitHub device flow (opens a browser)
  sprout logout
  sprout whoami
  sprout doctor
  sprout org list
  sprout org create <name>
  sprout org use <name-or-id>
  sprout org delete <name-or-id>
  sprout org members list [org]
  sprout org members add <github-login>
  sprout org members remove <github-login>
  sprout init
  sprout connect [--name=<id>] [--engine=postgres|mongodb] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
                                  engine         = infer from URL (mongodb:// / mongodb+srv:// → mongodb)
                                  wipe (default) = destroy local replica and rebootstrap
                                  --no-wipe      = resume existing replica when possible
                                  --dry-run      = estimate tables/rows (logical only)
                                  --tables=...   = allowlist for logical sync (Postgres tables / Mongo collections)
  sprout status [name]
  sprout connector list
  sprout connector delete <name> [--force]
                                  drops local replica + remote publication (logical)
                                  --force also deletes branches created from it
  sprout connector suspend <name> stop connector + all its branches (data kept)
  sprout connector resume <name>  start connector + all its idle branches
  sprout health
  sprout branch create <name> [--from=<connector|main>]
  sprout branch list
  sprout branch get <name> [--from=<connector>]
  sprout branch diff <name> [--from=<connector>]
  sprout branch reset <name> [--from=<connector>]
  sprout branch delete <name> [--from=<connector>]
  sprout branch suspend <name> [--from=<connector>]
  sprout branch resume <name> [--from=<connector>]

  Same branch name is allowed on two connectors (testdb from lab vs testdb from
  supabase). Hosts are testdb-<github>-<connector>.<host>. Pass --from
  when the name is ambiguous.

  Connectors are org-scoped. GitHub login creates a personal org named default.
  Add teammates with sprout org members add <login> — they share the same replica
  dir (no second copy). Owners can connect/wipe/delete connectors; members can
  create/reset/delete only their own branches.

Env:
  SPROUT_SERVER  default http://127.0.0.1:8080 (or apiUrl in ~/.sprout/config.json)
  SPROUT_TOKEN   overrides the token saved by sprout login
  SPROUT_ORG     current org (or org in ~/.sprout/config.json / sprout org use)
  SPROUT_CONFIG  path to config.json (default ~/.sprout/config.json)
  SPROUT_DB_USER default sprout (advertised in connection strings)

GitHub login (server):
  SPROUT_GITHUB_CLIENT_ID   OAuth App client ID (enable Device Flow on the app)
  SPROUT_GITHUB_USERS       optional comma-separated GitHub logins (omit = anyone)
  SPROUT_GITHUB_ORGS        optional orgs (omit = anyone)
  SPROUT_GITHUB_HOST        default https://github.com
  SPROUT_GITHUB_API         default https://api.github.com
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
