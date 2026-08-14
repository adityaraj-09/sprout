package cliconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPROUT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := Save(File{APIUrl: "http://strido.fit:8080/", Token: "gho_x", GitHubLogin: "alice"}); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.APIUrl != "http://strido.fit:8080" || got.Token != "gho_x" || got.GitHubLogin != "alice" {
		t.Fatalf("%+v", got)
	}
	st, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%o", st.Mode().Perm())
	}
}

func TestUnsetToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPROUT_CONFIG", filepath.Join(dir, "config.json"))
	if _, err := Save(File{Token: "gho_x", GitHubLogin: "alice", APIUrl: "http://x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Unset("token", "githubLogin"); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Token != "" || got.GitHubLogin != "" || got.APIUrl != "http://x" {
		t.Fatalf("%+v", got)
	}
}
