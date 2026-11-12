package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	clientSecret = "test-client-secret-123"
	refresh1     = "test-refresh-token-1"
	refresh2     = "test-refresh-token-2"
)

type memStore struct {
	mu sync.Mutex
	v  string
}

func (m *memStore) Load() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.v == "" {
		return "", ErrNoRefreshToken
	}
	return m.v, nil
}

func (m *memStore) Save(v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.v = v
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// authServer is a fake authorization server.
type authServer struct {
	*httptest.Server
	tokens atomic.Int32
	handle func(w http.ResponseWriter, r *http.Request)
}

func newAuthServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *authServer {
	t.Helper()
	a := &authServer{handle: handle}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			a.tokens.Add(1)
		}
		a.handle(w, r)
	}))
	t.Cleanup(a.Close)
	return a
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ccConfig(a *authServer) Config {
	return Config{Flow: ClientCredentials, TokenURL: a.URL + "/token", ClientID: "my-client", ClientSecret: clientSecret, Scopes: []string{"read", "write"}}
}

func TestClientCredentials(t *testing.T) {
	a := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || id != "my-client" || secret != clientSecret || r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("scope") != "read write" || r.PostForm.Has("client_secret") {
			reply(w, 400, map[string]string{"error": "invalid_request"})
			return
		}
		reply(w, 200, map[string]any{"access_token": "access-" + time.Now().Format("150405.000000000"), "token_type": "Bearer", "expires_in": 3600})
	})
	c := &clock{t: time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)}
	s := NewSource(ccConfig(a), http.DefaultClient, nil, c.now)

	tok, err := s.Token(t.Context())
	if err != nil || !strings.HasPrefix(tok, "access-") {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if again, _ := s.Token(t.Context()); again != tok || a.tokens.Load() != 1 {
		t.Errorf("not cached: %d fetches", a.tokens.Load())
	}
	c.t = c.t.Add(3550 * time.Second) // within a minute of expiry
	if fresh, _ := s.Token(t.Context()); fresh == tok || a.tokens.Load() != 2 {
		t.Errorf("not refreshed before expiry: %d fetches", a.tokens.Load())
	}
	s.Invalidate()
	if _, _ = s.Token(t.Context()); a.tokens.Load() != 3 {
		t.Errorf("Invalidate didn't force a fetch: %d", a.tokens.Load())
	}
	if !slices.Contains(s.Secrets(), clientSecret) || len(s.Secrets()) < 2 {
		t.Errorf("Secrets = %d values, want the client secret and the access token", len(s.Secrets()))
	}
}

// Concurrent calls share one fetch.
func TestTokenSingleFetch(t *testing.T) {
	a := newAuthServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		reply(w, 200, map[string]any{"access_token": "shared", "expires_in": 3600})
	})
	s := NewSource(ccConfig(a), http.DefaultClient, nil, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if tok, err := s.Token(t.Context()); err != nil || tok != "shared" {
				t.Errorf("Token = %q, %v", tok, err)
			}
		})
	}
	wg.Wait()
	if a.tokens.Load() != 1 {
		t.Errorf("%d fetches for 8 concurrent calls", a.tokens.Load())
	}
}

// Token endpoint errors give the OAuth error code and a cleaned
// description, never the raw body, and never a secret.
func TestTokenErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"oauth error":   {401, `{"error":"invalid_client","error_description":"client secret ` + clientSecret + ` is wrong\u001b[2J"}`, "invalid_client"},
		"not json":      {502, `<html>` + clientSecret + `</html>`, "HTTP 502"},
		"no token":      {200, `{"token_type":"Bearer"}`, "no access_token"},
		"bad type":      {200, `{"access_token":"x","token_type":"mac"}`, "token type"},
		"control chars": {200, `{"access_token":"a\nb"}`, "invalid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := newAuthServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := NewSource(ccConfig(a), http.DefaultClient, nil, nil).Token(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), clientSecret) || strings.Contains(err.Error(), "\x1b") || strings.Contains(err.Error(), "<html>") {
				t.Errorf("error leaks: %v", err)
			}
		})
	}
}

// The device flow's refresh token is exchanged for access tokens, and a
// rotated refresh token is saved.
func TestRefreshToken(t *testing.T) {
	a := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != refresh1 || r.PostForm.Get("client_id") != "public-client" {
			reply(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		reply(w, 200, map[string]any{"access_token": "from-refresh", "refresh_token": refresh2, "expires_in": 600})
	})
	cfg := Config{Flow: DeviceCode, TokenURL: a.URL + "/token", DeviceURL: a.URL + "/device", ClientID: "public-client"}
	store := &memStore{v: refresh1}
	s := NewSource(cfg, http.DefaultClient, store, nil)
	if tok, err := s.Token(t.Context()); err != nil || tok != "from-refresh" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if store.v != refresh2 {
		t.Errorf("rotated refresh token not saved: %q", store.v)
	}
	if !slices.Contains(s.Secrets(), refresh2) {
		t.Error("refresh token not in Secrets")
	}

	_, err := NewSource(cfg, http.DefaultClient, &memStore{}, nil).Token(t.Context())
	if !errors.Is(err, ErrNoRefreshToken) || !strings.Contains(err.Error(), "plugit auth") {
		t.Errorf("no refresh token: %v", err)
	}
}

func TestDeviceFlow(t *testing.T) {
	var polls atomic.Int32
	a := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/device":
			if r.PostForm.Get("client_id") != "public-client" || r.PostForm.Get("scope") != "repo" {
				reply(w, 400, map[string]string{"error": "invalid_request"})
				return
			}
			reply(w, 200, map[string]any{"device_code": "dev-123", "user_code": "WDJB-MJHT", "verification_uri": "https://auth.example.test/device",
				"verification_uri_complete": "https://auth.example.test/device?user_code=WDJB-MJHT", "expires_in": 900, "interval": 5})
		case "/token":
			if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.PostForm.Get("device_code") != "dev-123" {
				reply(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			switch polls.Add(1) {
			case 1:
				reply(w, 400, map[string]string{"error": "authorization_pending"})
			case 2:
				reply(w, 400, map[string]string{"error": "slow_down"})
			default:
				reply(w, 200, map[string]any{"access_token": "dev-access", "refresh_token": refresh1, "expires_in": 3600})
			}
		}
	})
	cfg := Config{Flow: DeviceCode, TokenURL: a.URL + "/token", DeviceURL: a.URL + "/device", ClientID: "public-client", Scopes: []string{"repo"}}
	d, err := StartDevice(t.Context(), cfg, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if d.UserCode != "WDJB-MJHT" || d.VerificationURI != "https://auth.example.test/device" || d.Interval != 5*time.Second {
		t.Errorf("device = %+v", d)
	}
	var waits []time.Duration
	sleep := func(_ context.Context, dur time.Duration) error { waits = append(waits, dur); return nil }
	tok, err := d.Wait(t.Context(), cfg, http.DefaultClient, sleep)
	if err != nil || tok.Access != "dev-access" || tok.Refresh != refresh1 {
		t.Fatalf("Wait = %+v, %v", tok, err)
	}
	// slow_down adds 5 seconds to the interval (RFC 8628 §3.5).
	if !slices.Equal(waits, []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}) {
		t.Errorf("waits = %v", waits)
	}
}

func TestDeviceFlowStops(t *testing.T) {
	for _, code := range []string{"access_denied", "expired_token"} {
		t.Run(code, func(t *testing.T) {
			a := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/device" {
					reply(w, 200, map[string]any{"device_code": "d", "user_code": "U", "verification_uri": "https://a.example.test/d", "expires_in": 60, "interval": 1})
					return
				}
				reply(w, 400, map[string]string{"error": code})
			})
			cfg := Config{Flow: DeviceCode, TokenURL: a.URL + "/token", DeviceURL: a.URL + "/device", ClientID: "c"}
			d, err := StartDevice(t.Context(), cfg, http.DefaultClient)
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.Wait(t.Context(), cfg, http.DefaultClient, func(context.Context, time.Duration) error { return nil })
			if err == nil || !strings.Contains(err.Error(), code) {
				t.Errorf("err = %v", err)
			}
		})
	}
	// The device code's lifetime bounds the polling.
	a := newAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device" {
			reply(w, 200, map[string]any{"device_code": "d", "user_code": "U", "verification_uri": "https://a.example.test/d", "expires_in": 10, "interval": 5})
			return
		}
		reply(w, 400, map[string]string{"error": "authorization_pending"})
	})
	cfg := Config{Flow: DeviceCode, TokenURL: a.URL + "/token", DeviceURL: a.URL + "/device", ClientID: "c"}
	d, _ := StartDevice(t.Context(), cfg, http.DefaultClient)
	n := 0
	_, err := d.Wait(t.Context(), cfg, http.DefaultClient, func(context.Context, time.Duration) error { n++; return nil })
	if err == nil || !strings.Contains(err.Error(), "expired") || n > 3 {
		t.Errorf("err = %v after %d polls", err, n)
	}
}
