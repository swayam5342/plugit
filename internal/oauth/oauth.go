// Package oauth gets OAuth 2.0 access tokens for an API (SPEC M6 design
// note: OAuth2): the client credentials grant, and the device
// authorization grant (RFC 8628) whose refresh token is kept by a Store
// (the OS keyring). Access tokens are only ever held in memory.
//
// Token endpoints are reached through the Doer the caller passes (plugit's
// hardened httpx client), so the SSRF guard and redirect policy apply.
// Errors carry the OAuth error code and a cleaned description, never a
// raw response body or a secret.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/naming"
)

// Flow is an OAuth grant plugit supports.
type Flow string

// Flows.
const (
	ClientCredentials Flow = "client_credentials"
	DeviceCode        Flow = "device_code"
)

// Limits and timing.
const (
	maxResponse   = 1 << 20
	refreshEarly  = 60 * time.Second
	slowDownExtra = 5 * time.Second
	grantDevice   = "urn:ietf:params:oauth:grant-type:device_code"
)

// ErrNoRefreshToken means the device flow hasn't been run for this API.
var ErrNoRefreshToken = errors.New("no OAuth refresh token stored: run `plugit auth` for this API to sign in")

// Error is an error response from the authorization server.
type Error struct {
	Status      int
	Code        string // the OAuth "error" field
	Description string // cleaned and capped
}

func (e *Error) Error() string {
	msg := "OAuth token request failed"
	if e.Code != "" {
		msg += ": " + e.Code
	} else {
		msg += fmt.Sprintf(": HTTP %d", e.Status)
	}
	if e.Description != "" {
		msg += " (" + e.Description + ")"
	}
	return msg
}

// Config is an API's OAuth settings, with the client secret resolved.
type Config struct {
	Flow         Flow
	TokenURL     string
	DeviceURL    string // device flow only
	ClientID     string
	ClientSecret string // may be empty for a public device-flow client
	Scopes       []string
}

// Store keeps the device flow's refresh token.
type Store interface {
	Load() (string, error) // ErrNoRefreshToken when there is none
	Save(token string) error
}

// Token is an authorization server's answer.
type Token struct {
	Access  string
	Refresh string
	Expiry  time.Time // zero when the server gave no expires_in
}

// Source hands out a valid access token, fetching a new one when the
// cached one is about to expire. It is safe for concurrent use.
type Source struct {
	cfg   Config
	doer  httpx.Doer
	store Store
	now   func() time.Time

	mu      sync.Mutex
	access  string
	expiry  time.Time
	refresh string // last refresh token seen, for redaction
}

// NewSource builds a Source. store is used by the device flow; now is the
// clock (time.Now if nil).
func NewSource(cfg Config, doer httpx.Doer, store Store, now func() time.Time) *Source {
	if now == nil {
		now = time.Now
	}
	return &Source{cfg: cfg, doer: doer, store: store, now: now}
}

// Token returns an access token, from cache when it has more than a minute
// left. Concurrent callers wait for one fetch.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access != "" && (s.expiry.IsZero() || s.now().Before(s.expiry.Add(-refreshEarly))) {
		return s.access, nil
	}
	form := url.Values{}
	switch s.cfg.Flow {
	case ClientCredentials:
		form.Set("grant_type", "client_credentials")
		if len(s.cfg.Scopes) > 0 {
			form.Set("scope", strings.Join(s.cfg.Scopes, " "))
		}
	case DeviceCode:
		if s.store == nil {
			return "", ErrNoRefreshToken
		}
		rt, err := s.store.Load()
		if err != nil {
			return "", err
		}
		s.refresh = rt
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", rt)
	default:
		return "", fmt.Errorf("unknown OAuth flow %q", s.cfg.Flow)
	}
	tok, err := request(ctx, s.cfg, s.doer, s.cfg.TokenURL, form, s.secrets())
	if err != nil {
		return "", err
	}
	if tok.Refresh != "" && s.cfg.Flow == DeviceCode && tok.Refresh != s.refresh {
		if err := s.store.Save(tok.Refresh); err != nil {
			return "", fmt.Errorf("saving the rotated OAuth refresh token: %w", err)
		}
		s.refresh = tok.Refresh
	}
	s.access, s.expiry = tok.Access, time.Time{}
	if !tok.Expiry.IsZero() {
		s.expiry = s.now().Add(tok.Expiry.Sub(time.Time{}))
	}
	return s.access, nil
}

// Invalidate drops the cached access token (after the API answered 401).
func (s *Source) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = ""
}

// Secrets are the values to redact: the client secret, the refresh token
// and the current access token.
func (s *Source) Secrets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(s.secrets(), s.access)
}

func (s *Source) secrets() []string {
	return []string{s.cfg.ClientSecret, s.refresh}
}

// Device is a started device authorization (RFC 8628 §3.2).
type Device struct {
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               time.Duration
	Interval                time.Duration
	deviceCode              string
}

// StartDevice asks the authorization server for a device and user code.
func StartDevice(ctx context.Context, cfg Config, doer httpx.Doer) (*Device, error) {
	form := url.Values{}
	if len(cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURL         string `json:"verification_url"` // used by some servers
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := post(ctx, cfg, doer, cfg.DeviceURL, form, []string{cfg.ClientSecret}, &out); err != nil {
		return nil, err
	}
	uri := out.VerificationURI
	if uri == "" {
		uri = out.VerificationURL
	}
	if out.DeviceCode == "" || out.UserCode == "" || uri == "" {
		return nil, errors.New("the device authorization response is missing device_code, user_code or verification_uri")
	}
	d := &Device{
		UserCode:                naming.CleanText(out.UserCode, 64),
		VerificationURI:         cleanURL(uri),
		VerificationURIComplete: cleanURL(out.VerificationURIComplete),
		ExpiresIn:               time.Duration(max(out.ExpiresIn, 0)) * time.Second,
		Interval:                time.Duration(max(out.Interval, 5)) * time.Second,
		deviceCode:              out.DeviceCode,
	}
	if out.Interval > 0 {
		d.Interval = time.Duration(out.Interval) * time.Second
	}
	if d.ExpiresIn == 0 {
		d.ExpiresIn = 15 * time.Minute
	}
	if d.VerificationURI == "" {
		return nil, errors.New("the device authorization response has an unusable verification_uri")
	}
	return d, nil
}

// cleanURL keeps only http(s) URLs, cleaned of control characters.
func cleanURL(s string) string {
	u, err := url.Parse(naming.CleanText(s, 512))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.String()
}

// Wait polls until the user approves, denies, or the code expires. sleep
// waits between polls (it should return ctx.Err() when ctx is done).
func (d *Device) Wait(ctx context.Context, cfg Config, doer httpx.Doer, sleep func(context.Context, time.Duration) error) (*Token, error) {
	interval := d.Interval
	var waited time.Duration
	form := url.Values{"grant_type": {grantDevice}, "device_code": {d.deviceCode}}
	for {
		if waited >= d.ExpiresIn {
			return nil, errors.New("the device code expired before sign-in finished; run plugit auth again")
		}
		if err := sleep(ctx, interval); err != nil {
			return nil, err
		}
		waited += interval
		tok, err := request(ctx, cfg, doer, cfg.TokenURL, form, []string{cfg.ClientSecret})
		var oe *Error
		switch {
		case err == nil:
			return tok, nil
		case errors.As(err, &oe) && oe.Code == "authorization_pending":
		case errors.As(err, &oe) && oe.Code == "slow_down":
			interval += slowDownExtra
		default:
			return nil, err
		}
	}
}

// request posts a token request and decodes a token.
func request(ctx context.Context, cfg Config, doer httpx.Doer, endpoint string, form url.Values, secrets []string) (*Token, error) {
	var out struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := post(ctx, cfg, doer, endpoint, form, secrets, &out); err != nil {
		return nil, err
	}
	switch {
	case out.AccessToken == "":
		return nil, errors.New("the token response has no access_token")
	case out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer"):
		return nil, fmt.Errorf("unsupported token type %q (plugit sends bearer tokens)", naming.CleanText(out.TokenType, 20))
	case invalidToken(out.AccessToken) || invalidToken(out.RefreshToken):
		return nil, errors.New("the token response has an invalid token (control characters or spaces)")
	}
	tok := &Token{Access: out.AccessToken, Refresh: out.RefreshToken}
	if out.ExpiresIn > 0 {
		tok.Expiry = time.Time{}.Add(time.Duration(out.ExpiresIn) * time.Second) // a duration from now
	}
	return tok, nil
}

func invalidToken(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// post sends a form to the authorization server and decodes the JSON
// answer into out. The client authenticates with HTTP Basic when it has a
// secret (RFC 6749 §2.3.1), else by client_id in the form.
func post(ctx context.Context, cfg Config, doer httpx.Doer, endpoint string, form url.Values, secrets []string, out any) error {
	if cfg.ClientSecret == "" {
		form.Set("client_id", cfg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("OAuth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if cfg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	}
	resp, err := doer.Do(req)
	if err != nil {
		return fmt.Errorf("OAuth request to %s failed: %w", hostOf(endpoint), unwrapURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("reading the OAuth response: %w", err)
	}
	if len(body) > maxResponse {
		return errors.New("the OAuth response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		desc := naming.CleanText(e.Description, 200)
		for _, s := range secrets {
			if s != "" {
				desc = strings.ReplaceAll(desc, s, "***")
			}
		}
		return &Error{Status: resp.StatusCode, Code: naming.CleanText(e.Error, 64), Description: desc}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("the OAuth response isn't valid JSON")
	}
	return nil
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Host
	}
	return "the token endpoint"
}

// unwrapURL drops *url.Error, whose message repeats the URL.
func unwrapURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}
