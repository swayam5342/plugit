package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/secrets"
)

const (
	oauthClientSecret = "test-client-secret-123"
	oauthAccess       = "test-access-token-xyz-1"
	oauthRefresh      = "test-refresh-token-1"
)

func (k *fakeKeyring) Get(service, key string) (string, error) {
	v, ok := k.items[service+"/"+key]
	if !ok {
		return "", secrets.ErrKeyringItemNotFound
	}
	return v, nil
}

const oauthYAML = `version: 4
name: pets
source: {url: https://example.test/openapi.yaml}
base_url: %s
prefix: pp
network: {allow_private: true}
auth: %s
toolsets:
  default:
    tools:
      "GET /pets": {}
`

// oauthWorld is a fake authorization server and an API that only accepts
// the access token it issues.
type oauthWorld struct {
	auth, api *httptest.Server
	tokens    atomic.Int32
}

func newOAuthWorld(t *testing.T) *oauthWorld {
	t.Helper()
	w := &oauthWorld{}
	reply := func(rw http.ResponseWriter, status int, v any) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_ = json.NewEncoder(rw).Encode(v)
	}
	w.auth = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case r.URL.Path == "/device":
			reply(rw, 200, map[string]any{"device_code": "dev-1", "user_code": "ABCD-EFGH", "verification_uri": "https://auth.example.test/activate", "expires_in": 600, "interval": 1})
		case r.PostForm.Get("grant_type") == "client_credentials":
			w.tokens.Add(1)
			if id, s, ok := r.BasicAuth(); !ok || id != "pets-client" || s != oauthClientSecret {
				reply(rw, 401, map[string]string{"error": "invalid_client"})
				return
			}
			reply(rw, 200, map[string]any{"access_token": oauthAccess, "token_type": "Bearer", "expires_in": 3600})
		case r.PostForm.Get("grant_type") == "urn:ietf:params:oauth:grant-type:device_code":
			reply(rw, 200, map[string]any{"access_token": "first-access", "refresh_token": oauthRefresh, "expires_in": 3600})
		case r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") == oauthRefresh:
			w.tokens.Add(1)
			reply(rw, 200, map[string]any{"access_token": oauthAccess, "expires_in": 3600})
		default:
			reply(rw, 400, map[string]string{"error": "invalid_grant"})
		}
	}))
	w.api = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+oauthAccess {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte(`{"error":"bad token ` + r.Header.Get("Authorization") + `"}`))
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`[{"id":1,"seen":"` + r.Header.Get("Authorization") + `"}]`))
	}))
	t.Cleanup(w.auth.Close)
	t.Cleanup(w.api.Close)
	return w
}

func (w *oauthWorld) setup(t *testing.T, auth string) config.Paths {
	t.Helper()
	p := setupAPI(t, w.api.URL)
	cfg, _ := p.APIConfig("pets")
	if err := config.WriteFileAtomic(cfg, fmt.Appendf(nil, oauthYAML, w.api.URL, auth)); err != nil {
		t.Fatal(err)
	}
	return p
}

func callListPets(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pp_listPets", Arguments: map[string]any{}})
	if err != nil || r.IsError {
		t.Fatalf("call: %v %s", err, resultText(r))
	}
	return resultText(r)
}

// Client credentials: serve fetches a token with the client secret (only
// sent to token_url), caches it, attaches it to the API, and redacts it.
func TestServeOAuthClientCredentials(t *testing.T) {
	w := newOAuthWorld(t)
	p := w.setup(t, fmt.Sprintf(`{type: oauth2, flow: client_credentials, token_url: %q, client_id: pets-client, secret: "env:PETS_CLIENT_SECRET"}`, w.auth.URL+"/token"))
	d := deps(nil, nil, map[string]string{"PETS_CLIENT_SECRET": oauthClientSecret})
	cs, stderr := startServe(t, serveSettings{paths: p, name: "pets"}, d)
	first := callListPets(t, cs)
	callListPets(t, cs)
	if w.tokens.Load() != 1 {
		t.Errorf("%d token requests for two calls (the token should be cached)", w.tokens.Load())
	}
	all := first + stderr.String()
	if strings.Contains(all, oauthAccess) || strings.Contains(all, oauthClientSecret) {
		t.Errorf("a token or the client secret leaked:\n%s", all)
	}
}

// Device flow: plugit auth shows the code, stores the refresh token in the
// keyring (never in plugit.yaml), and serve exchanges it for access tokens.
func TestOAuthDeviceFlow(t *testing.T) {
	w := newOAuthWorld(t)
	p := w.setup(t, fmt.Sprintf(`{type: oauth2, flow: device_code, token_url: %q, device_url: %q, client_id: pets-cli, scopes: [read]}`, w.auth.URL+"/token", w.auth.URL+"/device"))
	kr := &fakeKeyring{}
	var out bytes.Buffer
	err := authAPI(t.Context(), p, "pets", authDeps{in: strings.NewReader(""), out: &out, errOut: &bytes.Buffer{}, keyring: kr,
		resolver: &secrets.Resolver{Keyring: kr}, sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatalf("auth: %v\n%s", err, out.String())
	}
	if o := out.String(); !strings.Contains(o, "ABCD-EFGH") || !strings.Contains(o, "https://auth.example.test/activate") || strings.Contains(o, oauthRefresh) {
		t.Errorf("auth output:\n%s", o)
	}
	if kr.items["plugit/oauth-refresh-pets"] != oauthRefresh {
		t.Fatalf("refresh token not stored: %v", kr.items)
	}
	cfgPath, _ := p.APIConfig("pets")
	if y, _ := os.ReadFile(cfgPath); strings.Contains(string(y), oauthRefresh) {
		t.Error("refresh token written to plugit.yaml")
	}

	d := deps(nil, nil, nil)
	d.resolver.Keyring = kr
	d.keyring = kr
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, d)
	if got := callListPets(t, cs); strings.Contains(got, oauthAccess) {
		t.Errorf("access token not redacted: %s", got)
	}
}

// Without a stored refresh token, the call explains what to run.
func TestServeOAuthNeedsSignIn(t *testing.T) {
	w := newOAuthWorld(t)
	p := w.setup(t, fmt.Sprintf(`{type: oauth2, flow: device_code, token_url: %q, device_url: %q, client_id: pets-cli}`, w.auth.URL+"/token", w.auth.URL+"/device"))
	d := deps(nil, nil, nil)
	kr := &fakeKeyring{}
	d.resolver.Keyring, d.keyring = kr, kr
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, d)
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pp_listPets", Arguments: map[string]any{}})
	if err != nil || !r.IsError || !strings.Contains(resultText(r), "plugit auth") {
		t.Errorf("err=%v text=%s", err, resultText(r))
	}
}

// plugit auth for client credentials stores the secret and checks it by
// fetching a token.
func TestAuthClientCredentialsVerifies(t *testing.T) {
	w := newOAuthWorld(t)
	p := w.setup(t, fmt.Sprintf(`{type: oauth2, flow: client_credentials, token_url: %q, client_id: pets-client, secret: "keyring:plugit/pets-client"}`, w.auth.URL+"/token"))
	kr := &fakeKeyring{}
	var out bytes.Buffer
	err := authAPI(t.Context(), p, "pets", authDeps{in: strings.NewReader(oauthClientSecret + "\n"), out: &out, errOut: &bytes.Buffer{}, keyring: kr,
		resolver: &secrets.Resolver{Keyring: kr}})
	if err != nil || kr.items["plugit/pets-client"] != oauthClientSecret || !strings.Contains(out.String(), "access token") {
		t.Errorf("err=%v items=%v out=%s", err, kr.items, out.String())
	}

	// A wrong secret is stored (it's what the user typed) but reported.
	kr = &fakeKeyring{}
	err = authAPI(t.Context(), p, "pets", authDeps{in: strings.NewReader("wrong-secret\n"), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, keyring: kr,
		resolver: &secrets.Resolver{Keyring: kr}})
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "wrong-secret") {
		t.Errorf("err = %v", err)
	}
}

func TestDoctorOAuth(t *testing.T) {
	w := newOAuthWorld(t)
	p := w.setup(t, fmt.Sprintf(`{type: oauth2, flow: device_code, token_url: %q, device_url: %q, client_id: pets-cli}`, w.auth.URL+"/token", w.auth.URL+"/device"))
	var out bytes.Buffer
	n := runDoctor(doctorDeps{paths: p, out: &out, env: clients.Env{GOOS: "linux", Home: t.TempDir(), ConfigHome: t.TempDir()},
		lookupEnv: func(string) (string, bool) { return "", false },
		keyring:   &fakeKeyring{}, exe: func() (string, error) { return "plugit", nil },
		lookPath: func(string) (string, error) { return "", errors.New("not found") }, stat: func(string) error { return nil }})
	if n == 0 || !strings.Contains(out.String(), "plugit auth pets") {
		t.Errorf("problems=%d\n%s", n, out.String())
	}
}
