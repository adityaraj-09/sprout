package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceCode is GitHub's device-authorization response.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// AccessToken is a GitHub user-to-server token from device flow.
type AccessToken struct {
	Token string `json:"access_token"`
	Type  string `json:"token_type"`
	Scope string `json:"scope"`
}

type deviceError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// DeviceClient talks to GitHub's device-code endpoints.
type DeviceClient struct {
	Settings Settings
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

func (c *DeviceClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *DeviceClient) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RequestCode starts device flow. Enable Device Flow on the GitHub OAuth App.
func (c *DeviceClient) RequestCode(ctx context.Context) (DeviceCode, error) {
	s := c.Settings
	form := url.Values{
		"client_id": {s.ClientID},
		"scope":     {s.Scope()},
	}
	var out DeviceCode
	errBody := deviceError{}
	if err := c.postForm(ctx, s.host()+"/login/device/code", form, &out, &errBody); err != nil {
		return DeviceCode{}, err
	}
	if errBody.Error != "" {
		return DeviceCode{}, fmt.Errorf("github_device: %s (%s)", errBody.Error, errBody.ErrorDescription)
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("github_device: empty device code — enable Device Flow on the OAuth App")
	}
	if out.VerificationURI == "" {
		out.VerificationURI = s.host() + "/login/device"
	}
	if out.Interval < 5 {
		out.Interval = 5
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 900
	}
	return out, nil
}

// WaitForToken polls until the user finishes (or denies/expires) in the browser.
func (c *DeviceClient) WaitForToken(ctx context.Context, dc DeviceCode) (AccessToken, error) {
	s := c.Settings
	interval := time.Duration(dc.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	deadline := c.now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	form := url.Values{
		"client_id":   {s.ClientID},
		"device_code": {dc.DeviceCode},
		"grant_type":  {grantTypeDevice},
	}

	for {
		if err := c.sleep(ctx, interval); err != nil {
			return AccessToken{}, err
		}
		if c.now().After(deadline) {
			return AccessToken{}, fmt.Errorf("github_device: expired_token (open %s and try sprout login again)", dc.VerificationURI)
		}

		var tok AccessToken
		var errBody deviceError
		if err := c.postForm(ctx, s.host()+"/login/oauth/access_token", form, &tok, &errBody); err != nil {
			return AccessToken{}, err
		}
		switch errBody.Error {
		case "":
			if tok.Token == "" {
				return AccessToken{}, fmt.Errorf("github_device: empty access token")
			}
			return tok, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			if interval > 30*time.Second {
				interval = 30 * time.Second
			}
			continue
		case "expired_token":
			return AccessToken{}, fmt.Errorf("github_device: expired_token (code timed out; run sprout login again)")
		case "access_denied":
			return AccessToken{}, fmt.Errorf("github_device: access_denied (you cancelled in the browser)")
		case "incorrect_device_code":
			return AccessToken{}, fmt.Errorf("github_device: incorrect_device_code")
		default:
			desc := errBody.ErrorDescription
			if desc == "" {
				desc = "device flow failed"
			}
			return AccessToken{}, fmt.Errorf("github_device: %s (%s)", errBody.Error, desc)
		}
	}
}

func (c *DeviceClient) postForm(ctx context.Context, endpoint string, form url.Values, dest any, errDest *deviceError) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.Settings.userAgent())

	res, err := c.Settings.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("github_device: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("github_device: read: %w", err)
	}
	if res.StatusCode == http.StatusTooManyRequests {
		if errDest != nil {
			errDest.Error = "slow_down"
			errDest.ErrorDescription = "rate limited"
		}
		return nil
	}
	if res.StatusCode >= 400 && !json.Valid(body) {
		return fmt.Errorf("github_device: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if len(body) == 0 {
		return fmt.Errorf("github_device: empty response (HTTP %d)", res.StatusCode)
	}
	// GitHub returns either a success object or {"error":...} with HTTP 200.
	if errDest != nil {
		_ = json.Unmarshal(body, errDest)
		if errDest.Error != "" {
			return nil
		}
	}
	if dest != nil {
		if err := json.Unmarshal(body, dest); err != nil {
			return fmt.Errorf("github_device: decode: %w", err)
		}
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("github_device: HTTP %d", res.StatusCode)
	}
	return nil
}

// BrowserURL is the page the user should open. Prefer the complete URI (code pre-filled).
func BrowserURL(dc DeviceCode) string {
	if dc.VerificationURIComplete != "" {
		return dc.VerificationURIComplete
	}
	return dc.VerificationURI
}
