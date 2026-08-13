package postgres

import (
	"os"
	"strings"
)

// ReplaceManagedSection rewrites a marked block in path (creating the file if needed).
func ReplaceManagedSection(path, begin, end, body string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := replaceSection(string(data), begin, end, body)
	return os.WriteFile(path, []byte(next), 0o600)
}

// RemoveManagedSection drops a marked block if present.
func RemoveManagedSection(path, begin, end string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	next := removeSection(string(data), begin, end)
	if next == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(next), 0o600)
}

func replaceSection(content, begin, end, body string) string {
	block := begin + "\n" + strings.TrimRight(body, "\n") + "\n" + end + "\n"
	start := strings.Index(content, begin)
	if start < 0 {
		if strings.TrimSpace(content) == "" {
			return block
		}
		return strings.TrimRight(content, "\n") + "\n" + block
	}
	stop := strings.Index(content[start:], end)
	if stop < 0 {
		return content[:start] + block
	}
	stop = start + stop + len(end)
	for stop < len(content) && (content[stop] == '\n' || content[stop] == '\r') {
		stop++
	}
	return content[:start] + block + content[stop:]
}

func removeSection(content, begin, end string) string {
	start := strings.Index(content, begin)
	if start < 0 {
		return content
	}
	stop := strings.Index(content[start:], end)
	if stop < 0 {
		return strings.TrimRight(content[:start], "\n") + "\n"
	}
	stop = start + stop + len(end)
	for stop < len(content) && (content[stop] == '\n' || content[stop] == '\r') {
		stop++
	}
	out := content[:start] + content[stop:]
	return strings.TrimRight(out, "\n") + "\n"
}
