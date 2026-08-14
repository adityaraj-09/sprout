package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/cliconfig"
)

type githubAuthMeta struct {
	Enabled  bool   `json:"enabled"`
	Ready    bool   `json:"ready"`
	ClientID string `json:"client_id"`
	Host     string `json:"host"`
	API      string `json:"api"`
	Scope    string `json:"scope"`
}

func runLogin(serverURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	meta, err := fetchGitHubAuthMeta(ctx, serverURL)
	if err != nil {
		return err
	}
	if !meta.Enabled {
		return fmt.Errorf("github login is not enabled on %s — set SPROUT_GITHUB_CLIENT_ID on the server", serverURL)
	}
	if !meta.Ready {
		return fmt.Errorf("github login is not ready on the server — set SPROUT_GITHUB_USERS or SPROUT_GITHUB_ORGS")
	}

	dcClient := &auth.DeviceClient{Settings: auth.Settings{
		ClientID:       meta.ClientID,
		Host:           meta.Host,
		API:            meta.API,
		RequestedScope: meta.Scope,
	}}
	dc, err := dcClient.RequestCode(ctx)
	if err != nil {
		return err
	}

	page := auth.BrowserURL(dc)
	fmt.Println("GitHub device login")
	fmt.Println()
	fmt.Printf("  1. Open  %s\n", page)
	fmt.Printf("  2. Enter code  %s\n", dc.UserCode)
	fmt.Println()
	if err := auth.OpenBrowser(page); err != nil {
		fmt.Fprintf(os.Stderr, "  (could not open a browser: %v)\n", err)
	}
	fmt.Println("Waiting for GitHub…")

	tok, err := dcClient.WaitForToken(ctx, dc)
	if err != nil {
		return err
	}

	who, err := fetchWhoAmI(ctx, serverURL, tok.Token)
	if err != nil {
		return fmt.Errorf("logged into GitHub, but sprout-server rejected the token: %w", err)
	}
	login := who.Login
	if login == "" {
		login = who.Kind
	}

	if _, err := cliconfig.Save(cliconfig.File{
		APIUrl:      strings.TrimRight(serverURL, "/"),
		Token:       tok.Token,
		GitHubLogin: login,
	}); err != nil {
		return err
	}
	fmt.Printf("✓ logged in as %s (%s)\n", login, cliconfig.Path())
	return nil
}

func runLogout() error {
	if _, err := cliconfig.Unset("token", "githubLogin"); err != nil {
		return err
	}
	fmt.Printf("✓ logged out (%s)\n", cliconfig.Path())
	return nil
}

func runWhoAmI(c *client) error {
	file := cliconfig.Load()
	var who struct {
		Kind  string `json:"kind"`
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := c.do("GET", "/v1/whoami", nil, &who); err != nil {
		if file.GitHubLogin != "" {
			fmt.Printf("github %s (saved; server: %v)\n", file.GitHubLogin, err)
			return nil
		}
		return err
	}
	fmt.Printf("%s %s\n", who.Kind, who.Login)
	return nil
}

func fetchGitHubAuthMeta(ctx context.Context, serverURL string) (githubAuthMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/v1/auth/github", nil)
	if err != nil {
		return githubAuthMeta{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubAuthMeta{}, fmt.Errorf("server unreachable (%s): %w", serverURL, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound {
		return githubAuthMeta{}, fmt.Errorf("github login is not enabled on %s", serverURL)
	}
	if res.StatusCode >= 400 {
		return githubAuthMeta{}, fmt.Errorf("GET /v1/auth/github: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var meta githubAuthMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return githubAuthMeta{}, fmt.Errorf("decode /v1/auth/github: %w", err)
	}
	return meta, nil
}

func fetchWhoAmI(ctx context.Context, serverURL, token string) (auth.Actor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/v1/whoami", nil)
	if err != nil {
		return auth.Actor{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return auth.Actor{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		var er struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &er)
		if er.Message == "" {
			er.Message = strings.TrimSpace(string(body))
		}
		return auth.Actor{}, fmt.Errorf("%s: %s", er.Error, er.Message)
	}
	var a auth.Actor
	if err := json.Unmarshal(body, &a); err != nil {
		return auth.Actor{}, err
	}
	return a, nil
}
