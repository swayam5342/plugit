package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/policy"
)

const httpToken = "test-token-123-http-not-real"

// bearer adds the token to every request, as an MCP client configured
// with the header would.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func httpRig(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := Config{Name: "plugit-test", Tools: served(listPets), Mode: policy.ReadOnly}
	srv := httptest.NewServer(HTTPHandler(cfg, httpToken, nil))
	t.Cleanup(srv.Close)
	return srv
}

func connectHTTP(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: url + "/mcp", HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}}
	return client.Connect(t.Context(), tr, nil)
}

// With the token, it is an ordinary MCP server.
func TestHTTPWithToken(t *testing.T) {
	srv := httpRig(t)
	cs, err := connectHTTP(t, srv.URL, httpToken)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(res.Tools) != 1 || res.Tools[0].Name != "listPets" {
		t.Fatalf("tools = %+v, %v", res, err)
	}
}

func post(t *testing.T, url string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

// SPEC T15: a random bearer token, Origin validation, loopback only.
func TestHTTPRefusals(t *testing.T) {
	srv := httpRig(t)
	auth := "Bearer " + httpToken
	cases := map[string]struct {
		header map[string]string
		want   int
	}{
		"no token":           {nil, http.StatusUnauthorized},
		"wrong token":        {map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		"token prefix":       {map[string]string{"Authorization": "Bearer " + httpToken[:10]}, http.StatusUnauthorized},
		"not bearer":         {map[string]string{"Authorization": "Basic " + httpToken}, http.StatusUnauthorized},
		"evil origin":        {map[string]string{"Authorization": auth, "Origin": "https://evil.example.test"}, http.StatusForbidden},
		"null origin":        {map[string]string{"Authorization": auth, "Origin": "null"}, http.StatusForbidden},
		"DNS rebinding host": {map[string]string{"Authorization": auth, "Host": "evil.example.test"}, http.StatusForbidden},
		"cross-site fetch":   {map[string]string{"Authorization": auth, "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if resp := post(t, srv.URL, tc.header); resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	// A local web page (loopback origin) with the token is allowed.
	if resp := post(t, srv.URL, map[string]string{"Authorization": auth, "Origin": "http://localhost:3000"}); resp.StatusCode != http.StatusOK {
		t.Errorf("loopback origin: status %d", resp.StatusCode)
	}
	// 401 says how to authenticate, without echoing anything.
	if resp := post(t, srv.URL, nil); resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestListenAddr(t *testing.T) {
	good := map[string]string{
		":8931":          "127.0.0.1:8931",
		"127.0.0.1:0":    "127.0.0.1:0",
		"localhost:8931": "127.0.0.1:8931",
		"[::1]:8931":     "[::1]:8931",
		"8931":           "127.0.0.1:8931",
	}
	for in, want := range good {
		if got, err := ListenAddr(in); err != nil || got != want {
			t.Errorf("ListenAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"0.0.0.0:8931", "[::]:8931", "192.168.1.5:8931", "example.test:8931", "127.0.0.1:99999", "127.0.0.1:x", ""} {
		if got, err := ListenAddr(bad); err == nil {
			t.Errorf("ListenAddr(%q) = %q, accepted", bad, got)
		}
	}
}
