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
	case "connect":
		// sprout connect [--name=...] [--mode=logical|physical] <url>
		mode := "physical"
		name := "primary"
		url := ""
		for _, a := range os.Args[2:] {
			if strings.HasPrefix(a, "--mode=") {
				mode = strings.TrimPrefix(a, "--mode=")
				continue
			}
			if strings.HasPrefix(a, "--name=") {
				name = strings.TrimPrefix(a, "--name=")
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
			if url == "" && !strings.HasPrefix(a, "-") {
				url = a
			}
		}
		if url == "" {
			fatal(fmt.Errorf("usage: sprout connect [--name=<id>] [--mode=logical|physical] <postgresql-url>"))
		}
		var out map[string]any
		if err := c.do("POST", "/v1/projects/default/connect", map[string]string{"url": url, "mode": mode, "name": name}, &out); err != nil {
			fatal(err)
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
			var rec meta.BranchRecord
			body := map[string]string{"name": name}
			if from != "" {
				body["from"] = from
			}
			if err := c.do("POST", "/v1/projects/default/branches", body, &rec); err != nil {
				fatal(err)
			}
			src := rec.SourceConnector
			if src == "" {
				src = "main"
			}
			fmt.Printf("✓ %s [%s] from=%s\n  %s\n", rec.Name, rec.Status, src, rec.ConnString)
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
  sprout init
  sprout connect [--name=<id>] [--mode=logical|physical] <url>
                                  Each --name gets data/replicas/<name>/ + its own port
                                  physical = pg_basebackup (full PGDATA twin)
                                  logical  = publication/subscription (table sync)
  sprout status [name]              Replication lag (optional connector name)
  sprout connector list             List connectors (name, mode, port, dir)
  sprout health
  sprout branch create <name> [--from=<connector|main>]
  sprout branch list
  sprout branch get <name>
  sprout branch reset <name>
  sprout branch delete <name>
  sprout branch suspend <name>
  sprout branch resume <name>

Env:
  SPROUT_SERVER  default http://127.0.0.1:8080
  SPROUT_TOKEN   default dev-token
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
