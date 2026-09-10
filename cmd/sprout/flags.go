package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type output struct {
	format   string // text | json
	printURL bool
	quiet    bool
}

func (o output) json() bool     { return o.format == "json" }
func (o output) human() bool    { return !o.json() && !o.printURL }
func (o output) hideProg() bool { return o.quiet || o.json() || o.printURL }

func peelGlobals() (org string, out output) {
	out.format = "text"
	args := os.Args[1:]
	kept := []string{os.Args[0]}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--org="):
			org = strings.TrimPrefix(a, "--org=")
		case a == "--org" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-"):
			org = args[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			out.format = normalizeFormat(strings.TrimPrefix(a, "--format="))
		case a == "--format" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-"):
			out.format = normalizeFormat(args[i+1])
			i++
		case a == "--print-url" || a == "--print_url":
			out.printURL = true
		case a == "--quiet" || a == "-q":
			out.quiet = true
		default:
			kept = append(kept, a)
		}
	}
	os.Args = kept
	return org, out
}

func normalizeFormat(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json", "jsonl":
		return "json"
	default:
		return "text"
	}
}

func emitJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(b))
}

func emitURL(cs string) {
	fmt.Println(strings.TrimSpace(cs))
}

func connStringOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, _ := t["connection_string"].(string); s != "" {
			return s
		}
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			return ""
		}
		if s, _ := m["connection_string"].(string); s != "" {
			return s
		}
	}
	return ""
}

func writeJSONOr(o output, v any, human func()) {
	if o.printURL {
		if cs := connStringOf(v); cs != "" {
			emitURL(cs)
			return
		}
	}
	if o.json() {
		emitJSON(v)
		return
	}
	human()
}

func progressWriter(o output) io.Writer {
	if o.hideProg() {
		return io.Discard
	}
	return os.Stdout
}
