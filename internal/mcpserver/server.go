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
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
)

// Tool is one tool to serve: a prepared model.Tool (see schema.Prepare) and
// its per-tool policy.
type Tool struct {
	Tool     model.Tool
	Override policy.Override
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
	Logger   *slog.Logger // must not write to stdout
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
	for _, st := range cfg.Tools {
		if !policy.Lists(st.Tool.Safety, cfg.Mode) || st.Override == policy.OverrideDeny {
			continue
		}
		h := &handler{cfg: &cfg, tool: st}
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

	once      sync.Once
	validator *schema.Validator
}

func (h *handler) handle(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, _ error) {
	name := h.tool.Tool.Name
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			h.cfg.Logger.Error("tool handler panicked",
				"tool", name, "panic", h.cfg.Redactor.String(fmt.Sprint(r)),
				"stack", h.cfg.Redactor.String(string(debug.Stack())))
			res = errorResult(name + ": internal error (see the plugit log)")
		}
	}()

	decision := policy.Check(h.tool.Tool.Safety, h.cfg.Mode, h.tool.Override)
	switch decision.Decision {
	case policy.Deny:
		h.log(name, start, 0, true, "denied")
		return errorResult(name + ": denied by policy: " + decision.Reason), nil
	case policy.Confirm:
		// Elicitation-based confirmation arrives in M5 (SPEC §7); until then,
		// deny and say how to proceed.
		h.log(name, start, 0, true, "needs confirmation")
		return errorResult(name + ": " + decision.Reason + ", which requires user confirmation; " +
			"this server can't ask for it yet, so the call was not made. Use `plugit edit` to allow this tool or change policy.mode"), nil
	}

	h.once.Do(func() {
		h.validator = schema.NewValidator(h.tool.Tool)
		if h.validator.Degraded() {
			h.cfg.Logger.Warn("input schema could not be compiled; validating argument names and required arguments only", "tool", name)
		}
	})
	var raw json.RawMessage
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	if err := h.validator.Validate(raw); err != nil {
		h.log(name, start, 0, true, "invalid arguments")
		return errorResult(h.cfg.Redactor.String(name + ": " + err.Error())), nil
	}
	args, err := schema.DecodeArgs(raw)
	if err != nil {
		h.log(name, start, 0, true, "invalid arguments")
		return errorResult(name + ": " + err.Error()), nil
	}

	r := proxy.Call(ctx, h.cfg.Doer, h.tool.Tool, args, h.cfg.Target, h.cfg.Redactor)
	h.log(name, start, r.Status, r.IsError, "", "bytes", r.Bytes, "truncated", r.Truncated)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: r.Text}}, IsError: r.IsError}, nil
}

// log writes one line per call. Arguments and bodies are never logged here
// (SPEC §5.4: log.args defaults to redacted; the call log is M4).
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
