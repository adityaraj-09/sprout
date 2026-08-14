package auth

import (
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultHost      = "https://github.com"
	defaultAPI       = "https://api.github.com"
	defaultUserAgent = "sprout (https://github.com/adityaraj-09/sprout)"
	defaultScopeUser = "read:user"
	defaultScopeOrg  = "read:user read:org"
	grantTypeDevice  = "urn:ietf:params:oauth:grant-type:device_code"
)

// Settings is GitHub OAuth device-flow config (server + CLI).
type Settings struct {
	ClientID  string
	Users     []string // GitHub logins allowed to call the API
	Orgs      []string // membership in any of these orgs is enough
	Host      string   // https://github.com (or GitHub Enterprise)
	API       string   // https://api.github.com
	UserAgent string
	HTTP      *http.Client
	// RequestedScope overrides Scope() when the CLI copies the server's advertised scope.
	RequestedScope string
}

func FromEnv() Settings {
	return Settings{
		ClientID:  strings.TrimSpace(os.Getenv("SPROUT_GITHUB_CLIENT_ID")),
		Users:     splitList(os.Getenv("SPROUT_GITHUB_USERS")),
		Orgs:      splitList(os.Getenv("SPROUT_GITHUB_ORGS")),
		Host:      trimSlash(envOr("SPROUT_GITHUB_HOST", defaultHost)),
		API:       trimSlash(envOr("SPROUT_GITHUB_API", defaultAPI)),
		UserAgent: envOr("SPROUT_GITHUB_USER_AGENT", defaultUserAgent),
	}
}

func (s Settings) Enabled() bool {
	return s.ClientID != ""
}

// Ready is true when GitHub login is configured AND an allowlist exists.
// A public API with only a client id would let any GitHub user in.
func (s Settings) Ready() bool {
	return s.Enabled() && (len(s.Users) > 0 || len(s.Orgs) > 0)
}

func (s Settings) Scope() string {
	if strings.TrimSpace(s.RequestedScope) != "" {
		return strings.TrimSpace(s.RequestedScope)
	}
	if len(s.Orgs) > 0 {
		return defaultScopeOrg
	}
	return defaultScopeUser
}

func (s Settings) HostURL() string { return s.host() }

func (s Settings) APIURL() string { return s.api() }

func (s Settings) httpClient() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (s Settings) host() string {
	if s.Host != "" {
		return trimSlash(s.Host)
	}
	return defaultHost
}

func (s Settings) api() string {
	if s.API != "" {
		return trimSlash(s.API)
	}
	return defaultAPI
}

func (s Settings) userAgent() string {
	if s.UserAgent != "" {
		return s.UserAgent
	}
	return defaultUserAgent
}

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func trimSlash(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func containsFold(list []string, got string) bool {
	got = strings.ToLower(strings.TrimSpace(got))
	for _, x := range list {
		if strings.ToLower(strings.TrimSpace(x)) == got {
			return true
		}
	}
	return false
}
