package cli

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/isolate"
)

// serve --http: the Streamable HTTP transport on loopback with a bearer
// token kept in an owner-only file, reused across restarts (SPEC T15).
func TestServeHTTP(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	stderr := &syncBuf{}
	start := func(newToken bool) (string, context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(t.Context())
		urls := make(chan string, 1)
		done := make(chan error, 1)
		d := serveDeps(isolate.Parse)
		d.stderr = stderr
		go func() {
			done <- serveAPI(ctx, serveSettings{paths: p, name: "pets", http: "127.0.0.1:0", newToken: newToken,
				ready: func(u string) { urls <- u }}, d)
		}()
		select {
		case u := <-urls:
			return u, cancel, done
		case err := <-done:
			cancel()
			t.Fatalf("serve --http: %v\n%s", err, stderr)
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("serve --http didn't start")
		}
		return "", cancel, done
	}

	endpoint, stop, done := start(false)
	tokenPath, _ := p.HTTPToken("pets")
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 40 {
		t.Fatalf("token too short: %d characters", len(token))
	}
	if fi, _ := os.Stat(tokenPath); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v", fi.Mode().Perm())
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearerRT{token}}}
	cs, err := client.Connect(t.Context(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pp_list_pets", Arguments: map[string]any{}}); err != nil || r.IsError {
		t.Fatalf("call over HTTP: %v %s", err, resultText(r))
	}
	_ = cs.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %v %v", resp, err)
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v", err)
	}

	_, stop, done = start(false)
	if again, _ := os.ReadFile(tokenPath); strings.TrimSpace(string(again)) != token {
		t.Error("token changed on restart")
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v", err)
	}
	_, stop, done = start(true)
	if rotated, _ := os.ReadFile(tokenPath); strings.TrimSpace(string(rotated)) == token {
		t.Error("--new-token kept the old token")
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v", err)
	}

	if log := stderr.String(); strings.Contains(log, token) || !strings.Contains(log, tokenPath) {
		t.Errorf("stderr must name the token file and never show the token:\n%s", log)
	}
}

func TestServeHTTPRefusesNonLoopback(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	d := serveDeps(isolate.Parse)
	d.stderr = &bytes.Buffer{}
	err := serveAPI(t.Context(), serveSettings{paths: p, name: "pets", http: "0.0.0.0:8931"}, d)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("err = %v", err)
	}
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
