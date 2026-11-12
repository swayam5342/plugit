package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/tui"
)

// maxShownBody caps how much of a request body try prints.
const maxShownBody = 4 << 10

type tryFlags struct {
	args []string // k=v
	yes  bool     // confirm tools whose policy is confirm
}

type tryDeps struct {
	runDeps
	out io.Writer
	// confirm asks the user a yes/no question. nil means no terminal.
	confirm func(question string) (bool, error)
	// form opens the Try it screen when no --arg is given; nil means no
	// terminal (the call is made straight away).
	form func(ctx context.Context, in tui.TryInput) error
}

func newTryCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var f tryFlags
	var toolset string
	cmd := &cobra.Command{
		Use:   "try <name> <tool>",
		Short: "Call one tool once and print the request and response (secrets redacted)",
		Long: `Call one tool of a saved API, as an MCP client would, and print the
outgoing request and the tool result. Secrets are redacted.

<tool> is a tool name or an operation key such as "GET /pets/{id}". Arguments
are --arg name=value; values for non-string parameters are read as JSON
(--arg limit=2, --arg tags='["a","b"]').

On a terminal, leaving out --arg opens the Try it screen: a form with one
field per parameter, which shows the request and response for each call.

The API's policy applies: denied tools are refused, and tools that need
confirmation ask for it on the terminal (or pass --yes).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			d := tryDeps{
				runDeps: runDeps{
					stderr:   cmd.ErrOrStderr(),
					resolver: secrets.NewResolver(),
					parse:    isolate.Parse,
					info:     info,
				},
				out: cmd.OutOrStdout(),
			}
			d.confirm = terminalConfirm(cmd)
			if len(f.args) == 0 && d.confirm != nil {
				if out, ok := cmd.OutOrStdout().(*os.File); ok && term.IsTerminal(int(out.Fd())) { // #nosec G115 -- a file descriptor fits in int
					d.form = func(ctx context.Context, in tui.TryInput) error { return tui.RunTryIt(ctx, in, cmd.InOrStdin(), out) }
				}
			}
			return tryTool(cmd.Context(), serveSettings{paths: p, name: args[0], toolset: toolset}, args[1], f, d)
		},
	}
	cmd.Flags().StringArrayVar(&f.args, "arg", nil, "tool argument name=value (repeatable)")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "confirm the call if the tool's policy asks for confirmation (never overrides deny)")
	cmd.Flags().StringVar(&toolset, "toolset", defaultToolset, "toolset the tool belongs to")
	return cmd
}

// tryTool makes one call. It returns an error when the call is refused,
// fails, or the result is an error, so the exit status reflects it.
func tryTool(ctx context.Context, s serveSettings, toolName string, f tryFlags, d tryDeps) error {
	logger := slog.New(slog.NewTextHandler(d.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	a, err := loadActive(ctx, s, d.runDeps, logger)
	if err != nil {
		return err
	}
	st, err := findTool(a.tools, toolName, a.toolset)
	if err != nil {
		return err
	}
	t := st.Tool

	dec := policy.Check(t.Safety, a.cfg.PolicyMode(), st.Override)
	if dec.Decision == policy.Deny {
		return fmt.Errorf("%s: denied by policy: %s (see policy.mode and the toolset's policy overrides in plugit.yaml)", t.Name, dec.Reason)
	}
	if d.form != nil && len(f.args) == 0 {
		confirm := ""
		if dec.Decision == policy.Confirm && !f.yes {
			confirm = "This tool needs confirmation"
			if r := dec.Reason; r != "" {
				confirm = strings.ToUpper(r[:1]) + r[1:]
			}
		}
		return d.form(ctx, tui.TryInput{API: a.cfg.Name, Tool: t, Mode: a.cfg.PolicyMode(), Confirm: confirm,
			Send: func(ctx context.Context, values map[string]string) tui.TryResult { return formSend(ctx, a, st, values) }})
	}

	switch dec.Decision {
	case policy.Allow:
	case policy.Confirm:
		ok := f.yes
		if !ok && d.confirm == nil {
			return fmt.Errorf("%s: %s and needs confirmation: run it from a terminal, or pass --yes", t.Name, dec.Reason)
		}
		if !ok {
			if ok, err = d.confirm(fmt.Sprintf("%s (%s %s) %s. Make this call?", t.Name, t.Method, t.Path, dec.Reason)); err != nil {
				return err
			}
		}
		if !ok {
			return fmt.Errorf("%s: not confirmed; no call made", t.Name)
		}
	}

	r, err := callTool(ctx, a, st, f.args, d.out)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(d.out, "←\n%s\n", r.Text); err != nil {
		return err
	}
	if r.IsError {
		return fmt.Errorf("%s returned an error result", t.Name)
	}
	return nil
}

// callTool validates name=value arguments and makes the call, writing the
// redacted request to reqOut. Policy has already been checked.
func callTool(ctx context.Context, a *active, st mcpserver.Tool, kv []string, reqOut io.Writer) (proxy.Result, error) {
	t := st.Tool
	args, err := parseTryArgs(t, kv)
	if err != nil {
		return proxy.Result{}, fmt.Errorf("%s: %w", t.Name, err)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return proxy.Result{}, fmt.Errorf("%s: %w", t.Name, err)
	}
	if err := schema.NewValidator(t).Validate(raw); err != nil {
		return proxy.Result{}, errors.New(a.red.String(t.Name + ": " + err.Error()))
	}
	decoded, err := schema.DecodeArgs(raw)
	if err != nil {
		return proxy.Result{}, fmt.Errorf("%s: %w", t.Name, err)
	}
	rec := &printingDoer{next: a.doer, out: reqOut, red: a.red}
	r := proxy.Call(ctx, rec, t, decoded, a.target, a.red, st.Fields)
	if !rec.printed {
		if _, err := fmt.Fprintln(reqOut, "→ (no request sent)"); err != nil {
			return r, err
		}
	}
	return r, nil
}

// formSend is the Try it screen's Send: field values as typed become
// name=value arguments, exactly as with --arg.
func formSend(ctx context.Context, a *active, st mcpserver.Tool, values map[string]string) tui.TryResult {
	kv := make([]string, 0, len(values))
	for _, k := range slices.Sorted(maps.Keys(values)) {
		kv = append(kv, k+"="+values[k])
	}
	var req bytes.Buffer
	r, err := callTool(ctx, a, st, kv, &req)
	if err != nil {
		return tui.TryResult{Request: a.red.String(req.String()), Err: a.red.String(err.Error())}
	}
	return tui.TryResult{Request: req.String(), Status: r.Status, Response: r.Text, IsError: r.IsError}
}

func findTool(tools []mcpserver.Tool, name, toolset string) (mcpserver.Tool, error) {
	for _, st := range tools {
		if st.Tool.Name == name || string(st.Tool.Key) == name {
			return st, nil
		}
	}
	names := make([]string, 0, len(tools))
	for _, st := range tools {
		names = append(names, st.Tool.Name)
	}
	slices.Sort(names)
	shown := names[:min(len(names), 20)]
	more := ""
	if len(names) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(names)-len(shown))
	}
	return mcpserver.Tool{}, fmt.Errorf("no tool %q in toolset %s (tools: %s%s)", name, toolset, strings.Join(shown, ", "), more)
}

// parseTryArgs turns --arg name=value into arguments. Values for parameters
// whose schema isn't plainly a string are read as JSON when they parse, so
// --arg limit=2 is a number but --arg id=007 for a string id stays "007".
func parseTryArgs(t model.Tool, in []string) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for _, kv := range in {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--arg: expected name=value, got %q", safeName(kv))
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("--arg %s given twice", k)
		}
		out[k] = v
		if isStringParam(t, k) {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(v))
		dec.UseNumber()
		var j any
		if err := dec.Decode(&j); err == nil && !dec.More() {
			out[k] = j
		}
	}
	return out, nil
}

func isStringParam(t model.Tool, name string) bool {
	for _, p := range t.Params {
		if p.Name != name || p.Hidden {
			continue
		}
		var s struct {
			Type any `json:"type"`
		}
		if json.Unmarshal(p.Schema, &s) != nil {
			return false
		}
		return s.Type == "string"
	}
	return false
}

// printingDoer prints each outgoing request, redacted, before sending it.
type printingDoer struct {
	next    httpx.Doer
	out     io.Writer
	red     *redact.Redactor
	printed bool
}

func (p *printingDoer) Do(req *http.Request) (*http.Response, error) {
	p.printed = true
	var b strings.Builder
	fmt.Fprintf(&b, "→ %s %s\n", req.Method, p.red.URL(req.URL))
	h := p.red.Header(req.Header)
	for _, k := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[k] {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
	}
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			data, _ := io.ReadAll(io.LimitReader(body, maxShownBody+1))
			_ = body.Close()
			if len(data) > 0 {
				shown := p.red.Bytes(data[:min(len(data), maxShownBody)])
				fmt.Fprintf(&b, "\n%s\n", bytes.TrimRight(shown, "\n"))
				if len(data) > maxShownBody {
					b.WriteString("[body truncated]\n")
				}
			}
		}
	}
	if _, err := io.WriteString(p.out, b.String()); err != nil {
		return nil, err
	}
	return p.next.Do(req)
}
