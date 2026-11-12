package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
)

const secret = "test-token-123"

func tool(key model.OpKey, name string, params ...model.Param) model.Tool {
	return schema.Prepare(model.Tool{
		Key: key, Name: name, Method: key.Method(), Path: key.Path(),
		Description: "desc of " + name, Safety: model.SafetyForMethod(key.Method()), Params: params,
	}, schema.Options{})
}

func qparam(name string, required bool, s string) model.Param {
	return model.Param{Name: name, UpstreamName: name, In: model.InQuery, Required: required, Schema: json.RawMessage(s)}
}

var (
	listPets  = tool("GET /pets", "listPets", qparam("limit", false, `{"type":"integer","maximum":100}`))
	createPet = tool("POST /pets", "createPet")
	deletePet = tool("DELETE /pets/{id}", "deletePet", model.Param{Name: "id", UpstreamName: "id", In: model.InPath, Required: true, Schema: json.RawMessage(`{"type":"string"}`)})
	headPets  = tool("HEAD /pets", "headPets")
)

type rig struct {
	cs      *mcp.ClientSession
	hits    *atomic.Int32
	lastReq *atomic.Value
	logs    *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func setup(t *testing.T, cfg Config, upstream http.HandlerFunc) *rig {
	t.Helper()
	return setupClient(t, cfg, upstream, nil)
}

// setupClient is setup with client options (for example an elicitation
// handler, which makes the client advertise elicitation).
func setupClient(t *testing.T, cfg Config, upstream http.HandlerFunc, opts *mcp.ClientOptions) *rig {
	t.Helper()
	return setupFull(t, cfg, upstream, opts, "")
}

// setupClientVersion connects at a given MCP protocol version.
func setupClientVersion(t *testing.T, cfg Config, opts *mcp.ClientOptions, version string) *rig {
	t.Helper()
	return setupFull(t, cfg, nil, opts, version)
}

func setupFull(t *testing.T, cfg Config, upstream http.HandlerFunc, opts *mcp.ClientOptions, version string) *rig {
	t.Helper()
	r := &rig{hits: &atomic.Int32{}, lastReq: &atomic.Value{}, logs: &syncBuffer{}}
	if upstream == nil {
		upstream = func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"path":"` + req.URL.Path + `"}`))
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		r.lastReq.Store(req.URL.String())
		upstream(w, req)
	}))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL)
	cfg.Target.BaseURL = u
	if cfg.Doer == nil {
		cfg.Doer = httpx.NewClient(httpx.Options{AllowPrivate: true})
	}
	if cfg.Redactor == nil {
		cfg.Redactor = redact.New(redact.Options{Secrets: cfg.Target.SecretValues()})
	}
	cfg.Logger = slog.New(slog.NewTextHandler(r.logs, nil))
	if cfg.Name == "" {
		cfg.Name = "plugit-test"
	}

	server := New(cfg)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, opts)
	var sopts *mcp.ClientSessionOptions
	if version != "" {
		sopts = &mcp.ClientSessionOptions{ProtocolVersion: version}
	}
	cs, err := client.Connect(t.Context(), ct, sopts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	r.cs = cs
	return r
}

func served(tools ...model.Tool) []Tool {
	out := make([]Tool, len(tools))
	for i, tl := range tools {
		out[i] = Tool{Tool: tl}
	}
	return out
}

func (r *rig) list(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	res, err := r.cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

func (r *rig) call(t *testing.T, name string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	return r.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func names(m map[string]*mcp.Tool) []string {
	var out []string
	for n := range m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func TestListToolsRespectsMode(t *testing.T) {
	all := served(listPets, createPet, deletePet, headPets)
	for mode, want := range map[policy.Mode][]string{
		policy.ReadOnly:      {"headPets", "listPets"},
		policy.ConfirmWrites: {"createPet", "deletePet", "headPets", "listPets"},
		policy.Full:          {"createPet", "deletePet", "headPets", "listPets"},
	} {
		t.Run(string(mode), func(t *testing.T) {
			r := setup(t, Config{Tools: all, Mode: mode}, nil)
			if got := names(r.list(t)); !slices.Equal(got, want) {
				t.Errorf("listed %v, want %v", got, want)
			}
		})
	}
}

func TestDeniedToolsAreNotListed(t *testing.T) {
	tools := served(listPets, createPet)
	tools[0].Override = policy.OverrideDeny
	r := setup(t, Config{Tools: tools, Mode: policy.Full}, nil)
	if got := names(r.list(t)); !slices.Equal(got, []string{"createPet"}) {
		t.Errorf("listed %v, want [createPet]", got)
	}
}

func TestToolMetadataAndAnnotations(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets, createPet, deletePet, headPets), Mode: policy.Full}, nil)
	l := r.list(t)

	lp := l["listPets"]
	if lp.Description != "desc of listPets" {
		t.Errorf("description = %q", lp.Description)
	}
	is, _ := json.Marshal(lp.InputSchema)
	if !strings.Contains(string(is), `"additionalProperties":false`) || !strings.Contains(string(is), `"limit"`) {
		t.Errorf("input schema = %s", is)
	}
	type hints struct{ ro, destructive, idempotent, openWorld bool }
	get := func(n string) hints {
		a := l[n].Annotations
		if a == nil {
			t.Fatalf("%s has no annotations", n)
		}
		return hints{a.ReadOnlyHint, a.DestructiveHint != nil && *a.DestructiveHint, a.IdempotentHint, a.OpenWorldHint != nil && *a.OpenWorldHint}
	}
	for name, want := range map[string]hints{
		"listPets":  {ro: true, idempotent: true, openWorld: true},
		"headPets":  {ro: true, idempotent: true, openWorld: true},
		"createPet": {openWorld: true},
		"deletePet": {destructive: true, idempotent: true, openWorld: true},
	} {
		if got := get(name); got != want {
			t.Errorf("%s hints = %+v, want %+v", name, got, want)
		}
	}
	// Non-destructive tools must say so explicitly: MCP defaults destructiveHint to true.
	if a := l["createPet"].Annotations; a.DestructiveHint == nil {
		t.Error("createPet must set destructiveHint explicitly to false")
	}
}

func TestCallHappyPath(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly}, nil)
	res, err := r.call(t, "listPets", map[string]any{"limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(text(res), `"ok": true`) {
		t.Errorf("isError=%v text=%s", res.IsError, text(res))
	}
	if got := r.lastReq.Load().(string); got != "/pets?limit=5" {
		t.Errorf("upstream request = %s", got)
	}
}

func TestCallUnlistedToolIsProtocolError(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets, createPet), Mode: policy.ReadOnly}, nil)
	if _, err := r.call(t, "createPet", map[string]any{}); err == nil {
		t.Error("calling a tool that read-only mode didn't list must be a JSON-RPC error")
	}
	if _, err := r.call(t, "noSuchTool", map[string]any{}); err == nil {
		t.Error("unknown tool must be a JSON-RPC error")
	}
	if r.hits.Load() != 0 {
		t.Errorf("upstream hit %d times", r.hits.Load())
	}
}

func TestConfirmWithoutElicitationIsDenied(t *testing.T) {
	r := setup(t, Config{Tools: served(createPet), Mode: policy.ConfirmWrites}, nil)
	res, err := r.call(t, "createPet", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "confirm") || r.hits.Load() != 0 {
		t.Errorf("isError=%v hits=%d text=%s", res.IsError, r.hits.Load(), text(res))
	}
}

func TestInvalidArgumentsAreToolErrors(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly}, nil)
	for _, args := range []any{
		map[string]any{"limit": 1000},
		map[string]any{"limit": "ten"},
		map[string]any{"url": "http://169.254.169.254/"},
	} {
		res, err := r.call(t, "listPets", args)
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.HasPrefix(text(res), "listPets: ") {
			t.Errorf("args %v: isError=%v text=%s", args, res.IsError, text(res))
		}
	}
	if r.hits.Load() != 0 {
		t.Errorf("upstream hit %d times by invalid calls", r.hits.Load())
	}
}

type panicDoer struct{ n atomic.Int32 }

func (p *panicDoer) Do(req *http.Request) (*http.Response, error) {
	if p.n.Add(1) == 1 {
		panic("boom " + secret)
	}
	return httpx.NewClient(httpx.Options{AllowPrivate: true}).Do(req)
}

func TestPanicIsRecoveredAsToolError(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly, Doer: &panicDoer{},
		Target: proxy.Target{Auth: proxy.Auth{Type: proxy.AuthBearer, Secret: secret}}}, nil)
	res, err := r.call(t, "listPets", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "internal error") {
		t.Errorf("isError=%v text=%s", res.IsError, text(res))
	}
	if strings.Contains(text(res), secret) || strings.Contains(r.logs.String(), secret) {
		t.Error("panic value leaked the secret")
	}
	// The server keeps working.
	res, err = r.call(t, "listPets", map[string]any{})
	if err != nil || res.IsError {
		t.Errorf("second call: err=%v isError=%v text=%s", err, res != nil && res.IsError, text(res))
	}
}

func TestSecretsNeverReachResultsOrLogs(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token ` + r.Header.Get("Authorization") + `"}`))
	}
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly,
		Target: proxy.Target{Auth: proxy.Auth{Type: proxy.AuthBearer, Secret: secret}}}, echo)
	res, err := r.call(t, "listPets", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || strings.Contains(text(res), secret) || !strings.Contains(text(res), redact.Mask) {
		t.Errorf("isError=%v text=%s", res.IsError, text(res))
	}
	logs := r.logs.String()
	if strings.Contains(logs, secret) {
		t.Errorf("logs leak the secret:\n%s", logs)
	}
	if !strings.Contains(logs, "listPets") || !strings.Contains(logs, "status=401") {
		t.Errorf("expected a call log line, got:\n%s", logs)
	}
}

func TestServingNeverWritesToStdout(t *testing.T) {
	rd, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	r := setup(t, Config{Tools: served(listPets, createPet), Mode: policy.ConfirmWrites, Doer: &panicDoer{}}, nil)
	r.list(t)
	_, _ = r.call(t, "listPets", map[string]any{})
	_, _ = r.call(t, "createPet", map[string]any{})
	_, _ = r.call(t, "listPets", map[string]any{"limit": "bad"})
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(rd)
	if len(out) != 0 {
		t.Errorf("wrote to stdout: %q", out)
	}
}
