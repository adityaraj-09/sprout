package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid_github_token")
	ErrNotAllowed   = errors.New("github_user_not_allowed")
	ErrUnavailable  = errors.New("github_unavailable")
	ErrNotReady     = errors.New("github_auth_not_ready")
)

// User is a GitHub account that passed verification + allowlist.
type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Name  string `json:"name,omitempty"`
}

type cacheEntry struct {
	user  User
	err   error
	until time.Time
}

// Verifier maps a GitHub bearer token to a User. Results are cached by token hash.
type Verifier struct {
	Settings Settings
	TTL      time.Duration
	NegTTL   time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

func NewVerifier(s Settings) *Verifier {
	return &Verifier{
		Settings: s,
		TTL:      5 * time.Minute,
		NegTTL:   20 * time.Second,
		cache:    map[string]cacheEntry{},
	}
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) Lookup(ctx context.Context, token string) (User, error) {
	if !v.Settings.Ready() {
		return User{}, ErrNotReady
	}
	token = trimSlash(token) // trim space; slash-safe no-op for tokens
	if token == "" {
		return User{}, ErrInvalidToken
	}
	key := hashToken(token)
	if u, err, ok := v.get(key); ok {
		return u, err
	}

	u, err := v.lookupUncached(ctx, token)
	ttl := v.TTL
	if err != nil {
		ttl = v.NegTTL
		if errors.Is(err, ErrUnavailable) {
			// Don't cache transport failures — GitHub blips should retry.
			return User{}, err
		}
	}
	v.put(key, u, err, ttl)
	return u, err
}

func (v *Verifier) lookupUncached(ctx context.Context, token string) (User, error) {
	u, err := v.fetchUser(ctx, token)
	if err != nil {
		return User{}, err
	}
	if containsFold(v.Settings.Users, u.Login) {
		return u, nil
	}
	if len(v.Settings.Orgs) > 0 {
		ok, err := v.inAllowedOrg(ctx, token)
		if err != nil {
			return User{}, err
		}
		if ok {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("%w: %s", ErrNotAllowed, u.Login)
}

func (v *Verifier) fetchUser(ctx context.Context, token string) (User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.Settings.api()+"/user", nil)
	if err != nil {
		return User{}, err
	}
	v.githubHeaders(req, token)
	res, err := v.Settings.httpClient().Do(req)
	if err != nil {
		return User{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch res.StatusCode {
	case http.StatusOK:
		var u User
		if err := json.Unmarshal(body, &u); err != nil || u.Login == "" {
			return User{}, fmt.Errorf("%w: decode user", ErrUnavailable)
		}
		return u, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return User{}, ErrInvalidToken
	case http.StatusTooManyRequests:
		return User{}, fmt.Errorf("%w: rate limited", ErrUnavailable)
	default:
		if res.StatusCode >= 500 {
			return User{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, res.StatusCode)
		}
		return User{}, ErrInvalidToken
	}
}

type ghOrg struct {
	Login string `json:"login"`
}

func (v *Verifier) inAllowedOrg(ctx context.Context, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.Settings.api()+"/user/orgs?per_page=100", nil)
	if err != nil {
		return false, err
	}
	v.githubHeaders(req, token)
	res, err := v.Settings.httpClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return false, ErrInvalidToken
	}
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		return false, fmt.Errorf("%w: HTTP %d", ErrUnavailable, res.StatusCode)
	}
	if res.StatusCode >= 400 {
		return false, nil
	}
	var orgs []ghOrg
	if err := json.Unmarshal(body, &orgs); err != nil {
		return false, fmt.Errorf("%w: decode orgs", ErrUnavailable)
	}
	for _, o := range orgs {
		if containsFold(v.Settings.Orgs, o.Login) {
			return true, nil
		}
	}
	return false, nil
}

func (v *Verifier) githubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", v.Settings.userAgent())
}

func (v *Verifier) get(key string) (User, error, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ent, ok := v.cache[key]
	if !ok || v.now().After(ent.until) {
		if ok {
			delete(v.cache, key)
		}
		return User{}, nil, false
	}
	return ent.user, ent.err, true
}

func (v *Verifier) put(key string, u User, err error, ttl time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cache == nil {
		v.cache = map[string]cacheEntry{}
	}
	v.cache[key] = cacheEntry{user: u, err: err, until: v.now().Add(ttl)}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
