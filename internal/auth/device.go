package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Device flow terminal errors.
var (
	ErrExpired = errors.New("code expired, start again")
	ErrDenied  = errors.New("authorization denied")
)

// DeviceCode is GitHub's answer to a device authorization request.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceClient runs GitHub's OAuth device flow.
type DeviceClient struct {
	BaseURL  string // default https://github.com
	ClientID string
	HTTP     *http.Client
	Sleep    func(context.Context, time.Duration) error
	Now      func() time.Time
}

// Start requests a device and user code.
func (d *DeviceClient) Start(ctx context.Context) (DeviceCode, error) {
	if d.ClientID == "" {
		return DeviceCode{}, fmt.Errorf("auth: no OAuth client ID configured; set %s or add a classic token", ClientIDEnv)
	}
	var resp struct {
		DeviceCode
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := d.post(ctx, "login/device/code", url.Values{"client_id": {d.ClientID}, "scope": {Scopes}}, &resp); err != nil {
		return DeviceCode{}, err
	}
	if resp.Error != "" {
		return DeviceCode{}, fmt.Errorf("auth: device flow unavailable: %s %s", resp.Error, resp.ErrorDescription)
	}
	if resp.DeviceCode.DeviceCode == "" || resp.UserCode == "" {
		return DeviceCode{}, errors.New("auth: incomplete device code response")
	}
	if resp.Interval <= 0 {
		resp.Interval = 5
	}
	return resp.DeviceCode, nil
}

// tokenResponse is GitHub's answer on login/oauth/access_token. The expiry
// fields are present only for GitHub App user tokens with expiration enabled.
type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

func (r tokenResponse) credential(now time.Time) Credential {
	c := Credential{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken}
	if r.ExpiresIn > 0 {
		c.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if r.RefreshTokenExpiresIn > 0 {
		c.RefreshExpiresAt = now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)
	}
	return c
}

// Refresh renews an expiring GitHub App user token. Tokens issued through the
// device flow refresh without a client secret. Any error answer from GitHub
// means the grant is gone; transport and HTTP failures are worth retrying.
func (d *DeviceClient) Refresh(ctx context.Context, refreshToken string) (Credential, error) {
	form := url.Values{"client_id": {d.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	var resp tokenResponse
	if err := d.post(ctx, "login/oauth/access_token", form, &resp); err != nil {
		return Credential{}, err
	}
	if resp.Error != "" {
		return Credential{}, fmt.Errorf("%w: %s", ErrRefreshRejected, resp.Error)
	}
	if resp.AccessToken == "" {
		return Credential{}, errors.New("auth: empty refresh response")
	}
	return resp.credential(d.now()), nil
}

// Wait polls for the access token until it is granted, denied, or expired.
func (d *DeviceClient) Wait(ctx context.Context, dc DeviceCode) (Credential, error) {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := d.now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	form := url.Values{
		"client_id":   {d.ClientID},
		"device_code": {dc.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	for {
		if err := d.sleep(ctx, interval); err != nil {
			return Credential{}, err
		}
		if dc.ExpiresIn > 0 && !d.now().Before(deadline) {
			return Credential{}, ErrExpired
		}
		var resp tokenResponse
		if err := d.post(ctx, "login/oauth/access_token", form, &resp); err != nil {
			return Credential{}, err
		}
		switch resp.Error {
		case "":
			if resp.AccessToken == "" {
				return Credential{}, errors.New("auth: empty token response")
			}
			return resp.credential(d.now()), nil
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "expired_token":
			return Credential{}, ErrExpired
		case "access_denied":
			return Credential{}, ErrDenied
		default:
			return Credential{}, fmt.Errorf("auth: device flow failed: %s %s", resp.Error, resp.ErrorDescription)
		}
	}
}

func (d *DeviceClient) post(ctx context.Context, path string, form url.Values, out any) error {
	base := strings.TrimSuffix(d.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("auth: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("auth: %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: %s: HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("auth: %s: invalid JSON: %w", path, err)
	}
	return nil
}

func (d *DeviceClient) sleep(ctx context.Context, wait time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, wait)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d *DeviceClient) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
