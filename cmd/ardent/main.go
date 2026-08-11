// Command ardent — Phase 2 thin CLI (HTTP client to ardent-server).
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

	"github.com/adityaraj/ardent-clone/internal/config"
	"github.com/adityaraj/ardent-clone/internal/meta"
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
		// ardent connect [--mode=logical|physical] <url>
		mode := "physical"
		url := ""
		for _, a := range os.Args[2:] {
			if strings.HasPrefix(a, "--mode=") {
				mode = strings.TrimPrefix(a, "--mode=")
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
			fatal(fmt.Errorf("usage: ardent connect [--mode=logical|physical] <postgresql-url>"))
		}
		var out map[string]any
		if err := c.do("POST", "/v1/projects/default/connect", map[string]string{"url": url, "mode": mode}, &out); err != nil {
			fatal(err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "status":
		var out map[string]any
		if err := c.do("GET", "/v1/projects/default/replication", nil, &out); err != nil {
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
				fmt.Printf("%-12s %-10s %-14s project=%s lag_bytes=%d lsn=%s\n  %s\n",
					conn.Name, conn.Mode, conn.Status, conn.ProjectID, conn.LastLagBytes, conn.LastLSN, conn.PrimaryURL)
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
			var rec meta.BranchRecord
			if err := c.do("POST", "/v1/projects/default/branches", map[string]string{"name": os.Args[3]}, &rec); err != nil {
				fatal(err)
			}
			fmt.Printf("✓ %s [%s]\n  %s\n", rec.Name, rec.Status, rec.ConnString)
		case "list":
			var list []meta.BranchRecord
			if err := c.do("GET", "/v1/projects/default/branches", nil, &list); err != nil {
				fatal(err)
			}
			for _, b := range list {
				fmt.Printf("%-16s %-10s port=%-5d %s\n", b.Name, b.Status, b.Port, b.ConnString)
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
		return fmt.Errorf("server unreachable (%s): %w\nStart it with: ./bin/ardent-server", c.base, err)
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
	fmt.Fprintf(os.Stderr, `ardent — Phase 2 CLI (talks to ardent-server)

Usage:
  ardent init
  ardent connect [--mode=logical|physical] <url>
                                  physical = pg_basebackup (full PGDATA twin)
                                  logical  = publication/subscription (table sync)
  ardent status                     Replication lag / connector state
  ardent connector list             List all connectors
  ardent health
  ardent branch create <name>
  ardent branch list
  ardent branch get <name>
  ardent branch reset <name>
  ardent branch delete <name>
  ardent branch suspend <name>
  ardent branch resume <name>

Env:
  ARDENT_SERVER  default http://127.0.0.1:8080
  ARDENT_TOKEN   default dev-token
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
