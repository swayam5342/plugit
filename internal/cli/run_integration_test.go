package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/secrets"
)

// The test binary doubles as the spec-parse child, so these tests use the
// real isolate.Parse path (re-executing os.Executable()).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == isolate.ChildArg {
		os.Exit(isolate.ChildMain(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

const runSecret = "test-token-123"

const petSpec = `openapi: 3.0.3
info: {title: Pets, version: '1'}
servers:
  - url: %s
paths:
  /pets:
    get:
      operationId: listPets
      parameters:
        - {name: X-Api-Key, in: header, schema: {type: string}}
        - {name: limit, in: query, schema: {type: integer}}
      responses: {'200': {description: ok}}
    post:
      operationId: createPet
      responses: {'201': {description: created}}
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses: {'200': {description: ok}}
`

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func writeSpec(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type upstream struct {
	*httptest.Server
	hits    atomic.Int32
	lastURL atomic.Value
	lastKey atomic.Value
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.lastURL.Store(r.URL.String())
		u.lastKey.Store(r.Header.Get("X-Api-Key"))
		w.Header().Set("Content-Type", "application/json")
		// Echo the credential back, as a misbehaving API might.
		_, _ = fmt.Fprintf(w, `{"path":%q,"echo":%q}`, r.URL.Path, r.Header.Get("X-Api-Key"))
	}))
	t.Cleanup(u.Close)
	return u
}

func settings(specPath string, mut func(*runSettings)) runSettings {
	s := runSettings{spec: specPath, mode: policy.ReadOnly, maxTools: defaultMaxTools, timeout: 5 * time.Second}
	if mut != nil {
		mut(&s)
	}
	return s
}

func deps(transport mcp.Transport, stderr io.Writer, env map[string]string) runDeps {
	return runDeps{
		transport: transport,
		stderr:    stderr,
		resolver:  &secrets.Resolver{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }},
		parse:     isolate.Parse,
		info:      BuildInfo{Version: "v0.0.0-test"},
	}
}

// startRun serves in the background and returns a connected client.
func startRun(t *testing.T, s runSettings, env map[string]string) (*mcp.ClientSession, *syncBuf) {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	stderr := &syncBuf{}
	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- runServe(ctx, s, deps(st, stderr, env)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case serveErr := <-errCh:
			if serveErr != nil && !errors.Is(serveErr, context.Canceled) && !strings.Contains(serveErr.Error(), "closed") {
				t.Errorf("runServe ended with: %v", serveErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("runServe did not stop")
		}
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		select {
		case serveErr := <-errCh:
			t.Fatalf("runServe failed: %v (stderr: %s)", serveErr, stderr.String())
		default:
			t.Fatalf("connect: %v", err)
		}
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, stderr
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	slices.Sort(out)
	return out
}

func resultText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestRunEndToEnd(t *testing.T) {
	up := newUpstream(t)
	specPath := writeSpec(t, fmt.Sprintf(petSpec, "https://attacker.example.test"))
	cs, stderr := startRun(t, settings(specPath, func(s *runSettings) {
		s.baseURL = up.URL
		s.allowPrivate = true
		s.headers = []string{"X-Api-Key=env:PETS_KEY"}
	}), map[string]string{"PETS_KEY": runSecret})

	// Read-only mode: write tools aren't offered.
	if got := toolNames(t, cs); !slices.Equal(got, []string{"getPet", "listPets"}) {
		t.Fatalf("tools = %v, want [getPet listPets]", got)
	}
	// The credential header is hidden from the model's schema.
	res, _ := cs.ListTools(t.Context(), nil)
	for _, tl := range res.Tools {
		if s := fmt.Sprint(tl.InputSchema); strings.Contains(s, "X-Api-Key") {
			t.Errorf("%s exposes the credential header in its schema: %s", tl.Name, s)
		}
	}

	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "listPets", Arguments: map[string]any{"limit": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("call failed: %s", resultText(r))
	}
	if got := up.lastURL.Load(); got != "/pets?limit=2" {
		t.Errorf("upstream URL = %v", got)
	}
	if up.lastKey.Load() != runSecret {
		t.Error("credential header not sent upstream")
	}
	if strings.Contains(resultText(r), runSecret) || !strings.Contains(resultText(r), "***") {
		t.Errorf("echoed credential not redacted: %s", resultText(r))
	}

	// The model can't supply the credential header itself.
	r, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "listPets", Arguments: map[string]any{"X-Api-Key": "attacker"}})
	if err != nil || !r.IsError {
		t.Errorf("model-supplied credential header accepted: err=%v", err)
	}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "createPet"}); err == nil {
		t.Error("write tool callable in read-only mode")
	}

	if strings.Contains(stderr.String(), runSecret) {
		t.Errorf("stderr leaks the credential:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "plugit run serving") {
		t.Errorf("missing startup log line:\n%s", stderr.String())
	}
}

func TestRunAdoptsSingleServerWithoutCredentials(t *testing.T) {
	up := newUpstream(t)
	specPath := writeSpec(t, fmt.Sprintf(petSpec, up.URL))
	cs, _ := startRun(t, settings(specPath, func(s *runSettings) { s.allowPrivate = true }), nil)
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "getPet", Arguments: map[string]any{"id": "7"}})
	if err != nil || r.IsError {
		t.Fatalf("err=%v text=%s", err, resultText(r))
	}
	if up.lastURL.Load() != "/pets/7" {
		t.Errorf("upstream URL = %v", up.lastURL.Load())
	}
}

func TestRunSetupFailures(t *testing.T) {
	var huge strings.Builder
	huge.WriteString("openapi: 3.0.3\ninfo: {title: big, version: '1'}\nservers: [{url: 'https://api.example.test'}]\npaths:\n")
	for i := range 1000 {
		fmt.Fprintf(&huge, "  /r%d:\n    get:\n      tags: [t%d]\n      responses: {'200': {description: ok}}\n", i, i%7)
	}
	attackerSpec := fmt.Sprintf(petSpec, "https://attacker.example.test")

	cases := map[string]struct {
		spec string
		mut  func(*runSettings)
		env  map[string]string
		want []string
		is   error
	}{
		"1000-op spec without filters": {huge.String(), nil, nil, []string{"1000 tools", "--max-tools 40", "--tags"}, errTooManyTools},
		"credentials need --base-url": {attackerSpec, func(s *runSettings) { s.headers = []string{"X-Api-Key=env:K"} },
			map[string]string{"K": runSecret}, []string{"--base-url", "attacker.example.test"}, nil},
		"missing env secret": {attackerSpec, func(s *runSettings) {
			s.baseURL = "https://api.example.test"
			s.headers = []string{"X-Api-Key=env:NOT_SET"}
		}, nil, []string{"NOT_SET", "not set"}, secrets.ErrNotFound},
		"literal secret on the command line": {attackerSpec, func(s *runSettings) {
			s.baseURL = "https://api.example.test"
			s.headers = []string{"X-Api-Key=literal:" + runSecret}
		}, nil, []string{"env: or keyring:"}, secrets.ErrLiteralSecret},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := mcp.NewInMemoryTransports()
			stderr := &syncBuf{}
			start := time.Now()
			err := runServe(t.Context(), settings(writeSpec(t, tc.spec), tc.mut), deps(st, stderr, tc.env))
			if err == nil {
				t.Fatal("expected a setup error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("err = %v, want %v", err, tc.is)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), runSecret) || strings.Contains(stderr.String(), runSecret) {
				t.Errorf("secret leaked: err=%v stderr=%s", err, stderr.String())
			}
			if time.Since(start) > 20*time.Second {
				t.Errorf("setup failure took %v", time.Since(start))
			}
		})
	}
}

func TestRunNeverWritesToStdout(t *testing.T) {
	up := newUpstream(t)
	specPath := writeSpec(t, fmt.Sprintf(petSpec, up.URL))
	rd, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	func() {
		defer func() { os.Stdout = orig }()
		cs, _ := startRun(t, settings(specPath, func(s *runSettings) { s.allowPrivate = true }), nil)
		toolNames(t, cs)
		_, _ = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "listPets"})
		_, _ = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "getPet", Arguments: map[string]any{"id": ".."}})
	}()
	_ = w.Close()
	out, _ := io.ReadAll(rd)
	if len(out) != 0 {
		t.Errorf("plugit run wrote to stdout: %q", out)
	}
}
