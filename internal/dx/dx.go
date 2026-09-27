// Package dx holds small developer-experience helpers shared by the CLI and tests.
package dx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StatusMark is a compact ready/idle/error glyph for human lists.
func StatusMark(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "replicating":
		return "●"
	case "idle":
		return "○"
	case "creating", "bootstrapping", "resetting", "deleting":
		return "…"
	case "error", "crashed":
		return "✗"
	default:
		return "·"
	}
}

// RelAge formats a timestamp as a short age ("5m", "2h", "3d"). Zero is "-".
func RelAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return t.UTC().Format("2006-01-02")
	}
}

// EnvLines returns dotenv lines for a branch/connector connection string.
func EnvLines(connString string) []string {
	cs := strings.TrimSpace(connString)
	if cs == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(cs, "mongodb"):
		return []string{"MONGODB_URI=" + cs, "DATABASE_URL=" + cs}
	case strings.HasPrefix(cs, "http://") || strings.HasPrefix(cs, "https://"):
		key := qdrantAPIKey(cs)
		lines := []string{"QDRANT_URL=" + stripQueryUserinfo(cs)}
		if key != "" {
			lines = append(lines, "QDRANT_API_KEY="+key)
		}
		return lines
	default:
		return []string{"DATABASE_URL=" + cs}
	}
}

func qdrantAPIKey(raw string) string {
	for _, prefix := range []string{"api-key=", "api_key=", "apiKey="} {
		if i := strings.Index(strings.ToLower(raw), strings.ToLower(prefix)); i >= 0 {
			rest := raw[i+len(prefix):]
			if j := strings.IndexAny(rest, "&"); j >= 0 {
				rest = rest[:j]
			}
			return rest
		}
	}
	return ""
}

func stripQueryUserinfo(raw string) string {
	return raw
}

// WriteEnvFile writes lines to path (0600), creating parent dirs.
func WriteEnvFile(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

// ReadAtFile expands --flag=@path to file contents; otherwise returns the value.
func ReadAtFile(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "@") {
		return v, nil
	}
	b, err := os.ReadFile(strings.TrimPrefix(v, "@"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
