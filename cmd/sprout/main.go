// Command sprout — Phase 2 thin CLI (HTTP client to sprout-server).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/config"
	"github.com/adityaraj/sprout/internal/meta"
)

func main() {
	cfg := config.CLIDefaults()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	c := &client{base: strings.TrimRight(cfg.ServerURL, "/"), token: cfg.Token, http: &http.Client{Timeout: 60 * time.Minute}}

	switch os.Args[1] {
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
		// sprout connect [--name=...] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
		mode := "physical"
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
			fatal(fmt.Errorf("usage: sprout connect [--name=<id>] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <postgresql-url>"))
		}
		body := map[string]any{"url": url, "mode": mode, "name": name, "wipe": wipe, "dry_run": dryRun}
		if len(tables) > 0 {
			body["tables"] = tables
		}
		var out map[string]any
		if err := c.do("POST", "/v1/projects/default/connect", body, &out); err != nil {
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
			need(4)
			if err := c.do("DELETE", "/v1/projects/default/connectors/"+os.Args[3], nil, nil); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ deleted connector %s\n", os.Args[3])
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
					from = strings.TrimPrefix(a, "--from=")
					continue
				}
				if name == "" && !strings.HasPrefix(a, "-") {
					name = a
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
			if err := c.do("POST", "/v1/projects/default/branches", body, &rec); err != nil {
				fatal(err)
			}
			src, _ := rec["source_connector"].(string)
			if src == "" {
				src = "main"
			}
			cs, _ := rec["connection_string"].(string)
			psql, _ := rec["psql"].(string)
			status, _ := rec["status"].(string)
			bname, _ := rec["name"].(string)
			fmt.Printf("✓ %s [%s] from=%s\n  %s\n", bname, status, src, cs)
			if psql != "" {
				fmt.Println(" ", psql)
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
			var rec meta.BranchRecord
			if err := c.do("GET", "/v1/projects/default/branches/"+os.Args[3], nil, &rec); err != nil {
				fatal(err)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(rec)
		case "diff":
			need(4)
			var out map[string]any
			if err := c.do("GET", "/v1/projects/default/branches/"+os.Args[3]+"/diff", nil, &out); err != nil {
				fatal(err)
			}
			if sum, _ := out["summary"].(string); sum != "" {
				fmt.Println(sum)
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		case "reset":
			need(4)
			var rec meta.BranchRecord
			if err := c.do("POST", "/v1/projects/default/branches/"+os.Args[3]+"/reset", nil, &rec); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ reset %s\n  %s\n", rec.Name, rec.ConnString)
		case "delete":
			need(4)
			if err := c.do("DELETE", "/v1/projects/default/branches/"+os.Args[3], nil, nil); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ deleted %s\n", os.Args[3])
		case "suspend":
			need(4)
			var rec meta.BranchRecord
			if err := c.do("POST", "/v1/projects/default/branches/"+os.Args[3]+"/suspend", nil, &rec); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ suspended %s (status=%s)\n", rec.Name, rec.Status)
		case "resume":
			need(4)
			var rec meta.BranchRecord
			if err := c.do("POST", "/v1/projects/default/branches/"+os.Args[3]+"/resume", nil, &rec); err != nil {
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

type client struct {
	base, token string
	http        *http.Client
}

func (c *client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("server unreachable (%s): %w\nStart it with: ./bin/sprout-server", c.base, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		var er struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &er)
		if er.Message == "" {
			er.Message = string(data)
		}
		return fmt.Errorf("%s: %s", er.Error, er.Message)
	}
	if out == nil || res.StatusCode == http.StatusNoContent || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func need(n int) {
	if len(os.Args) < n {
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `sprout — Phase 2/3 CLI (talks to sprout-server)

Usage:
  sprout doctor
  sprout init
  sprout connect [--name=<id>] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
                                  wipe (default) = destroy local replica and rebootstrap
                                  --no-wipe      = resume existing replica when possible
                                  --dry-run      = estimate tables/rows (logical only)
                                  --tables=...   = allowlist for logical sync
  sprout status [name]
  sprout connector list
  sprout connector delete <name>  drops local replica + remote publication (logical)
  sprout health
  sprout branch create <name> [--from=<connector|main>]
  sprout branch list
  sprout branch get <name>
  sprout branch diff <name>       schema + row counts vs parent
  sprout branch reset <name>
  sprout branch delete <name>
  sprout branch suspend <name>
  sprout branch resume <name>

Env:
  SPROUT_SERVER  default http://127.0.0.1:8080
  SPROUT_TOKEN   default dev-token
  SPROUT_DB_USER default sprout (advertised in connection strings)
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
