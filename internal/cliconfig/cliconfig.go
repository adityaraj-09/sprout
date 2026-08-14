package cliconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// File is ~/.sprout/config.json — shared by the Go CLI and npm sproutdb-cli.
type File struct {
	APIUrl      string `json:"apiUrl,omitempty"`
	Token       string `json:"token,omitempty"`
	Project     string `json:"project,omitempty"`
	GitHubLogin string `json:"githubLogin,omitempty"`
}

func Path() string {
	if p := strings.TrimSpace(os.Getenv("SPROUT_CONFIG")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".sprout", "config.json")
}

func Load() File {
	b, err := os.ReadFile(Path())
	if err != nil {
		return File{}
	}
	var f File
	if json.Unmarshal(b, &f) != nil {
		return File{}
	}
	return f
}

func Save(patch File) (File, error) {
	next := merge(Load(), patch)
	if err := write(next); err != nil {
		return File{}, err
	}
	return next, nil
}

func Unset(keys ...string) (File, error) {
	cur := Load()
	for _, k := range keys {
		switch k {
		case "apiUrl", "api-url", "server":
			cur.APIUrl = ""
		case "token":
			cur.Token = ""
		case "project":
			cur.Project = ""
		case "githubLogin", "github-login":
			cur.GitHubLogin = ""
		}
	}
	if err := write(cur); err != nil {
		return File{}, err
	}
	return cur, nil
}

func write(f File) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		return err
	}
	return os.Chmod(Path(), 0o600)
}

func merge(base, patch File) File {
	if patch.APIUrl != "" {
		base.APIUrl = strings.TrimRight(patch.APIUrl, "/")
	}
	if patch.Token != "" {
		base.Token = patch.Token
	}
	if patch.Project != "" {
		base.Project = patch.Project
	}
	if patch.GitHubLogin != "" {
		base.GitHubLogin = patch.GitHubLogin
	}
	return base
}
