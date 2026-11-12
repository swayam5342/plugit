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
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
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

	switch dec := policy.Check(t.Safety, a.cfg.PolicyMode(), st.Override); dec.Decision {
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
	default:
		return fmt.Errorf("%s: denied by policy: %s (see policy.mode and the toolset's policy overrides in plugit.yaml)", t.Name, dec.Reason)
	}

	args, err := parseTryArgs(t, f.args)
	if err != nil {
		return fmt.Errorf("%s: %w", t.Name, err)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("%s: %w", t.Name, err)
	}
	if err := schema.NewValidator(t).Validate(raw); err != nil {
		return errors.New(a.red.String(t.Name + ": " + err.Error()))
	}
	decoded, err := schema.DecodeArgs(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", t.Name, err)
	}

	rec := &printingDoer{next: a.doer, out: d.out, red: a.red}
	r := proxy.Call(ctx, rec, t, decoded, a.target, a.red)
	if !rec.printed {
		if _, err := fmt.Fprintln(d.out, "→ (no request sent)"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(d.out, "←\n%s\n", r.Text); err != nil {
		return err
	}
	if r.IsError {
		return fmt.Errorf("%s returned an error result", t.Name)
	}
	return nil
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
