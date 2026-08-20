package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/adityaraj/sprout/internal/cliconfig"
)

type client struct {
	base, token, org string
	http             *http.Client
}

func peelOrg() string {
	org := ""
	out := []string{os.Args[0]}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--org=") {
			org = strings.TrimPrefix(a, "--org=")
			continue
		}
		if a == "--org" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			org = args[i+1]
			i++
			continue
		}
		out = append(out, a)
	}
	os.Args = out
	return org
}

func resolveOrg(flag string) string {
	if v := strings.TrimSpace(flag); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("SPROUT_ORG")); v != "" {
		return v
	}
	file := cliconfig.Load()
	if v := strings.TrimSpace(file.Org); v != "" {
		return v
	}
	if file.GitHubLogin != "" {
		return "default"
	}
	return ""
}

func (c *client) applyAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.org != "" {
		req.Header.Set("X-Sprout-Org", c.org)
	}
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
	c.applyAuth(req)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("server unreachable (%s): %w\nStart it with: ./bin/sprout-server", c.base, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return httpAPIError(data)
	}
	if out == nil || res.StatusCode == http.StatusNoContent || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *client) doProgress(method, path string, body any, out any) error {
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
	req.Header.Set("Accept", "application/x-ndjson")
	c.applyAuth(req)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("server unreachable (%s): %w\nStart it with: ./bin/sprout-server", c.base, err)
	}
	defer res.Body.Close()
	ct := strings.ToLower(res.Header.Get("Content-Type"))
	if res.StatusCode >= 400 || !strings.Contains(ct, "ndjson") {
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode >= 400 {
			return httpAPIError(data)
		}
		if out == nil || res.StatusCode == http.StatusNoContent || len(data) == 0 {
			return nil
		}
		return json.Unmarshal(data, out)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var result json.RawMessage
	sawResult := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			fmt.Println(string(line))
			continue
		}
		switch fmt.Sprint(ev["type"]) {
		case "step":
			step, _ := ev["step"].(string)
			detail, _ := ev["detail"].(string)
			msg := strings.TrimSpace(strings.TrimSpace(step + " " + detail))
			if msg != "" {
				fmt.Println(msg)
			}
		case "result":
			raw, err := json.Marshal(ev["result"])
			if err != nil {
				return err
			}
			result = raw
			sawResult = true
		case "error":
			code, _ := ev["error"].(string)
			msg, _ := ev["message"].(string)
			if msg == "" {
				msg = string(line)
			}
			if code == "" {
				code = "error"
			}
			return fmt.Errorf("%s: %s", code, msg)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if out == nil || !sawResult {
		return nil
	}
	return json.Unmarshal(result, out)
}

func httpAPIError(data []byte) error {
	var er struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &er)
	if er.Message == "" {
		er.Message = string(data)
	}
	if er.Error == "" {
		er.Error = "error"
	}
	return fmt.Errorf("%s: %s", er.Error, er.Message)
}
