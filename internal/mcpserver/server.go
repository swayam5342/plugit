// Package mcpserver serves tools over MCP (SPEC §3.7).
//
// Each call runs: policy check, argument validation, then proxy.Call. Every
// failure after the tool lookup is an isError result; only malformed
// JSON-RPC and unknown tools are protocol errors (the SDK handles those).
// Handlers recover panics. Nothing here writes to stdout: in serve and run,
// stdout carries JSON-RPC, and logs go to the configured (stderr) logger.
package mcpserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/store"
)

// Tool is one tool to serve: a prepared model.Tool (see schema.Prepare) and
// its per-tool policy.
type Tool struct {
	Tool     model.Tool
	Override policy.Override
	Limit    *policy.Limiter  // per-tool rate limit; nil for none
	Fields   *jsonpick.Picker // response_fields; nil keeps the whole response
}

// Config configures a server.
type Config struct {
	Name     string // implementation name reported to clients
	Version  string
	Tools    []Tool
	Mode     policy.Mode
	Target   proxy.Target
	Doer     httpx.Doer
	Redactor *redact.Redactor
	Logger   *slog.Logger    // must not write to stdout
	Log      *CallLog        // nil: calls aren't recorded
	Limit    *policy.Limiter // policy.rate_limit, shared by every tool; nil for none
}

// CallLog sends one redacted record per call to the call log (SPEC §3.7
// step 9).
type CallLog struct {
	Record  func(store.Call) // must not block (see store.Recorder)
	API     string
	Toolset string
	Args    config.LogArgs // none, redacted or full (secrets are masked either way)
	Preview bool           // keep a redacted response preview
}

// New builds the MCP server. Tools the mode can never run, and tools set to
// deny, are not listed at all.
func New(cfg Config) *mcp.Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Redactor == nil {
		cfg.Redactor = redact.New(redact.Options{Secrets: cfg.Target.SecretValues()})
	}
	srv := mcp.NewServer(
		&mcp.Implementation{Name: cfg.Name, Version: cfg.Version},
		&mcp.ServerOptions{Logger: cfg.Logger},
	)
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand.Read never fails (Go 1.24+)
	for _, st := range cfg.Tools {
		if !policy.Lists(st.Tool.Safety, cfg.Mode) || st.Override == policy.OverrideDeny {
			continue
		}
		h := &handler{cfg: &cfg, tool: st, key: key}
		srv.AddTool(&mcp.Tool{
			Name:        st.Tool.Name,
			Description: st.Tool.Description,
			InputSchema: schema.InputSchema(st.Tool),
			Annotations: annotations(st.Tool),
		}, h.handle)
	}
	return srv
}

// Serve runs the server on t until ctx is done or the client disconnects.
func Serve(ctx context.Context, cfg Config, t mcp.Transport) error {
	return New(cfg).Run(ctx, t)
}

func annotations(t model.Tool) *mcp.ToolAnnotations {
	f, tr := false, true
	a := &mcp.ToolAnnotations{
		ReadOnlyHint:    t.Safety == model.ReadOnly,
		DestructiveHint: &f, // MCP defaults this to true, so always set it
		OpenWorldHint:   &tr,
	}
	if t.Safety == model.Destructive {
		a.DestructiveHint = &tr
	}
	switch strings.ToUpper(t.Method) {
	case "GET", "HEAD", "OPTIONS", "PUT", "DELETE":
		a.IdempotentHint = true
	}
	return a
}

type handler struct {
	cfg  *Config
	tool Tool
	key  []byte // signs confirmation state (see confirmState)

	once      sync.Once
	validator *schema.Validator
}

func (h *handler) handle(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, _ error) {
	name := h.tool.Tool.Name
	start := time.Now()
	var raw json.RawMessage
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	// Recorded last, after the panic recovery below has set res.
	rec := store.Call{Outcome: store.OK}
	defer func() { h.record(start, raw, &rec, res) }()
	defer func() {
		if r := recover(); r != nil {
			h.cfg.Logger.Error("tool handler panicked",
				"tool", name, "panic", h.cfg.Redactor.String(fmt.Sprint(r)),
				"stack", h.cfg.Redactor.String(string(debug.Stack())))
			res = errorResult(name + ": internal error (see the plugit log)")
			rec.Outcome = store.Failed
		}
	}()

	decision := policy.Check(h.tool.Tool.Safety, h.cfg.Mode, h.tool.Override)
	if decision.Decision == policy.Deny {
		h.log(name, start, 0, true, "denied")
		rec.Outcome = store.Denied
		return errorResult(name + ": denied by policy: " + decision.Reason), nil
	}
	// The retry that carries a confirmation answer is the same call: it
	// doesn't spend the budget again (and only a call that passed here got
	// a signed state to retry with).
	if retry := decision.Decision == policy.Confirm && req != nil && req.Params != nil && req.Params.InputResponses != nil; !retry {
		if why := h.limited(); why != "" {
			h.log(name, start, 0, true, "rate limited")
			rec.Outcome = store.RateLimited
			return errorResult(name + ": " + why), nil
		}
	}

	h.once.Do(func() {
		h.validator = schema.NewValidator(h.tool.Tool)
		if h.validator.Degraded() {
			h.cfg.Logger.Warn("input schema could not be compiled; validating argument names and required arguments only", "tool", name)
		}
	})
	if err := h.validator.Validate(raw); err != nil {
		h.log(name, start, 0, true, "invalid arguments")
		rec.Outcome = store.Invalid
		return errorResult(h.cfg.Redactor.String(name + ": " + err.Error())), nil
	}
	args, err := schema.DecodeArgs(raw)
	if err != nil {
		h.log(name, start, 0, true, "invalid arguments")
		rec.Outcome = store.Invalid
		return errorResult(name + ": " + err.Error()), nil
	}

	// Confirmation comes after validation, so the user is only asked about
	// calls that would really be made.
	if decision.Decision == policy.Confirm {
		ask, ok, why := h.confirm(req, args, decision.Reason)
		if ask != nil {
			rec.Outcome = "" // not a call yet: the retry with the answer is recorded
			return ask, nil
		}
		if !ok {
			h.log(name, start, 0, true, "not confirmed")
			rec.Outcome = store.Denied
			return errorResult(h.cfg.Redactor.String(name + ": " + why)), nil
		}
	}

	r := proxy.Call(ctx, h.cfg.Doer, h.tool.Tool, args, h.cfg.Target, h.cfg.Redactor, h.tool.Fields)
	h.log(name, start, r.Status, r.IsError, "", "bytes", r.Bytes, "truncated", r.Truncated)
	rec.Status, rec.Bytes = r.Status, r.Bytes
	switch {
	case r.Status >= 400:
		rec.Outcome = store.HTTPError
	case r.IsError:
		rec.Outcome = store.Failed
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: r.Text}}, IsError: r.IsError}, nil
}

// limited takes one call from the tool's and the API's budgets (SPEC §3.7
// step 3). It returns why the call is refused, or "".
func (h *handler) limited() string {
	for _, l := range []struct {
		lim   *policy.Limiter
		scope string
	}{{h.tool.Limit, "this tool"}, {h.cfg.Limit, "this API"}} {
		if ok, wait := l.lim.Allow(); !ok {
			return fmt.Sprintf("rate limit reached (%s for %s); retry in %s, and the call was not made",
				l.lim.Rate(), l.scope, max(wait.Round(time.Second), time.Second))
		}
	}
	return ""
}

// confirmSchema is the elicitation form: one pre-ticked checkbox. It isn't
// required, so a client that only offers accept/decline still works.
var confirmSchema = json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean","title":"Make this call","default":true}}}`)

// ConfirmTimeout is how long an answer to a confirmation stays valid.
const ConfirmTimeout = 5 * time.Minute

// maxShownArgs caps the arguments shown in a confirmation.
const maxShownArgs = 2000

// confirmID names plugit's input request.
const confirmID = "plugit_confirm"

// now is the clock for confirmation state (tests replace it).
var now = time.Now

// confirm asks the user, through the MCP client, whether to make this call
// (SPEC §3.7 step 2). It uses multi round-trip requests (SEP-2322): the
// first call returns ask, an input-required result carrying an elicitation
// form; the client fulfils it and calls again with the answer, which this
// call then judges. The SDK turns the same result into a plain
// elicitation/create request for clients on older protocol versions.
//
// The answer is bound to the exact arguments: requestState is an HMAC of
// the tool, the arguments and the time, so a retry with different
// arguments or a stale answer is refused. Without elicitation support, the
// answer is no (§10 Q3).
func (h *handler) confirm(req *mcp.CallToolRequest, args map[string]any, reason string) (ask *mcp.CallToolResult, ok bool, why string) {
	var params *mcp.CallToolParamsRaw
	var ss *mcp.ServerSession
	if req != nil {
		params, ss = req.Params, req.Session
	}
	if params != nil && params.InputResponses != nil {
		ans, _ := params.InputResponses[confirmID].(*mcp.ElicitResult)
		if ans == nil {
			return nil, false, "the confirmation answer is missing; the call was not made"
		}
		if err := h.checkState(params.RequestState, args); err != nil {
			return nil, false, err.Error() + "; the call was not made"
		}
		switch ans.Action {
		case "accept":
			if v, set := ans.Content["confirm"]; set && v != true {
				return nil, false, "declined by the user; the call was not made"
			}
			return nil, true, ""
		case "decline":
			return nil, false, "declined by the user; the call was not made"
		}
		return nil, false, "confirmation cancelled; the call was not made"
	}

	if ss == nil || ss.InitializeParams() == nil || ss.InitializeParams().Capabilities == nil || ss.InitializeParams().Capabilities.Elicitation == nil {
		return nil, false, reason + ", which requires user confirmation; this MCP client can't ask for it (it doesn't support elicitation), " +
			"so the call was not made. Use `plugit edit` to allow this tool or change policy.mode"
	}
	t := h.tool.Tool
	shown := "(none)"
	if len(args) > 0 {
		if b, err := json.MarshalIndent(h.cfg.Redactor.Args(args), "", "  "); err == nil {
			shown = visible(string(b))
		}
	}
	if len(shown) > maxShownArgs {
		shown = shown[:maxShownArgs] + "\n… (truncated)"
	}
	msg := fmt.Sprintf("%s wants to call %s (%s %s, a %s tool).\n\nArguments:\n%s\n\nAllow this call?",
		h.cfg.Name, t.Name, t.Method, t.Path, t.Safety, shown)
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{confirmID: &mcp.ElicitParams{Mode: "form", Message: msg, RequestedSchema: confirmSchema}},
		RequestState:  h.state(now(), args),
	}, false, ""
}

// state signs the tool, the arguments and the time.
func (h *handler) state(at time.Time, args map[string]any) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	return ts + "." + base64.RawURLEncoding.EncodeToString(h.mac(ts, args))
}

func (h *handler) mac(ts string, args map[string]any) []byte {
	canon, _ := json.Marshal(args) // maps encode with sorted keys
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(h.tool.Tool.Name + "\x00" + ts + "\x00"))
	m.Write(canon)
	return m.Sum(nil)
}

// checkState verifies requestState against the arguments of this call.
func (h *handler) checkState(state string, args map[string]any) error {
	ts, sig, found := strings.Cut(state, ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if !found || err != nil || !hmac.Equal(got, h.mac(ts, args)) {
		return errors.New("the confirmation doesn't match these arguments")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now().Sub(time.Unix(sec, 0)) > ConfirmTimeout {
		return errors.New("the confirmation expired")
	}
	return nil
}

// visible makes invisible characters visible as \uXXXX, so arguments
// written by the model can't hide or reorder text in a confirmation (JSON
// encoding already escapes control characters but not bidi overrides).
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n') {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// record sends one call to the call log, if there is one. Arguments follow
// log.args; the error and preview come from the result text, which is
// already redacted, and are redacted again to be sure.
func (h *handler) record(start time.Time, raw json.RawMessage, c *store.Call, res *mcp.CallToolResult) {
	l := h.cfg.Log
	if l == nil || l.Record == nil || c.Outcome == "" {
		return
	}
	c.Time, c.Duration = start.UTC(), time.Since(start)
	c.API, c.Toolset, c.Tool, c.Op = l.API, l.Toolset, h.tool.Tool.Name, string(h.tool.Tool.Key)
	red := h.cfg.Redactor
	if l.Args != config.LogArgsNone {
		var args map[string]any
		if json.Unmarshal(raw, &args) == nil && args != nil {
			if l.Args == config.LogArgsFull {
				args = red.Values(args)
			} else {
				args = red.Args(args)
			}
			if b, err := json.Marshal(args); err == nil {
				c.Args = b
			}
		}
	}
	text := ""
	if res != nil {
		for _, ct := range res.Content {
			if tc, ok := ct.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
	}
	text = red.String(text)
	if c.Outcome != store.OK {
		first, _, _ := strings.Cut(text, "\n")
		c.Error = first
	}
	if l.Preview {
		c.Preview = text
	}
	l.Record(*c)
}

// log writes one line per call to the process log. Arguments and bodies
// never go here; the call log (record) has them, redacted.
func (h *handler) log(name string, start time.Time, status int, isError bool, outcome string, extra ...any) {
	attrs := append([]any{"tool", name, "status", status, "is_error", isError, "ms", time.Since(start).Milliseconds()}, extra...)
	if outcome != "" {
		attrs = append(attrs, "outcome", outcome)
	}
	h.cfg.Logger.Info("tool call", attrs...)
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: true}
}
