// Package invoke is the one call pipeline (SPEC §3.7 P5), shared by the MCP
// server, `plugit try` and the TUI Try-it screen. It owns every network step
// around a protocol's Encode/Decode: credential attachment (origin-bound,
// T8), the single httpx call (T9/T12), response-field filtering and
// redaction (T11). A protocol package never does any of this (T17).
package invoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/redact"
)

// Deps is everything one serve process needs to make calls. It is built once
// and reused across calls.
type Deps struct {
	Proto    protocol.Protocol
	Doer     httpx.Doer
	Cred     Credential // attached only to requests for Endpoint's origin
	Endpoint *url.URL   // pinned
	Redactor *redact.Redactor
	Timeout  time.Duration
}

// Result is the shaped, redacted outcome of one call.
type Result struct {
	Text      string
	IsError   bool
	Code      string // protocol-neutral call-log code, e.g. "HTTP 404"
	Status    int    // HTTP status (0 for a failure before any response)
	Bytes     int
	Truncated bool
}

// Perform runs steps 5–10 of the call lifecycle for one validated call: inject
// nothing more (args are already validated and fixed values live in the protocol),
// Encode (no credential), AttachAuth, httpx.Do, Decode, then shape + redact.
// It never returns a Go error; every failure is an isError Result whose text
// starts with the tool name.
func Perform(ctx context.Context, d Deps, t model.Tool, args map[string]any, pick *jsonpick.Picker) Result {
	red := d.Redactor
	cred := d.Cred
	if cred.Token != nil {
		tok, err := cred.Token(ctx)
		if err != nil {
			return failure(t, red, transportCode, "couldn't get an access token: "+err.Error())
		}
		cred.Secret = tok
		red = red.With(tok)
	}

	req, err := d.Proto.Encode(ctx, protocol.Call{Tool: t, Args: args, Endpoint: d.Endpoint, Timeout: d.Timeout})
	if err != nil {
		return failure(t, red, "", "invalid arguments: "+err.Error())
	}
	if err := attachAuth(req, cred, d.Endpoint); err != nil {
		return failure(t, red, transportCode, describeTransportError(ctx, err))
	}

	resp, err := d.Doer.Do(req)
	if err != nil {
		return failure(t, red, transportCode, describeTransportError(ctx, err))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	truncated := errors.Is(err, httpx.ErrResponseTruncated)
	if err != nil && !truncated {
		return failure(t, red, transportCode, describeTransportError(ctx, fmt.Errorf("reading response: %w", err)))
	}
	if resp.StatusCode == http.StatusUnauthorized && cred.Unauthorized != nil {
		cred.Unauthorized()
	}

	res := d.Proto.Decode(protocol.Call{Tool: t}, resp, body, truncated)
	// A JSON body may spell a secret with escapes ("tok\/en", or \uXXXX for
	// any character) that match no secret form as bytes, yet the model
	// decodes them. When its canonical spelling holds something to mask,
	// show that spelling so red.String masks it (T11). A body that is not
	// valid JSON (truncated at the cap, or with trailing junk) has each
	// complete string literal re-spelled instead. Other bodies are shown
	// exactly as sent.
	res.Value = red.JSON(res.Value)
	if res.Value == nil && res.Binary == nil {
		res.Text = string(red.JSON([]byte(res.Text)))
	}
	text := shape(res, pick, truncated, len(body))
	if res.IsError {
		text = fmt.Sprintf("%s: upstream returned %s\n\n%s", t.Name, res.Status, text)
	}
	return Result{Text: red.String(text), IsError: res.IsError, Code: res.Code, Status: resp.StatusCode, Bytes: len(body), Truncated: truncated}
}

// transportCode is the call-log code for a failure before any response
// (timeout, SSRF refusal, connection error), the same across protocols.
const transportCode = "transport error"

func failure(t model.Tool, red *redact.Redactor, code, msg string) Result {
	return Result{Text: red.String(t.Name + ": " + msg), IsError: true, Code: code}
}

func shape(res protocol.Result, pick *jsonpick.Picker, truncated bool, bodyLen int) string {
	var b strings.Builder
	b.WriteString(res.Status)
	b.WriteByte('\n')
	val := res.Value
	if val != nil && pick != nil && !res.IsError && !truncated {
		if filtered, ok := applyPick(val, pick); ok {
			val = filtered
			fmt.Fprintf(&b, "Filtered to fields: %s (the tool's response_fields; other fields were left out)\n", strings.Join(pick.Paths(), ", "))
		}
	}
	b.WriteByte('\n')
	switch {
	case res.Binary != nil:
		b.WriteString(res.Text)
	case val != nil:
		b.Write(prettyJSON(val))
	default:
		b.WriteString(res.Text)
	}
	if truncated {
		fmt.Fprintf(&b, "\n[truncated: response exceeded %d bytes]", bodyLen)
	}
	return b.String()
}

// applyPick keeps only the tool's response_fields of a complete JSON value.
func applyPick(raw json.RawMessage, pick *jsonpick.Picker) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.More() {
		return nil, false
	}
	switch v.(type) {
	case map[string]any, []any:
	default:
		return nil, false
	}
	out, err := json.Marshal(pick.Apply(v))
	if err != nil {
		return nil, false
	}
	return out, true
}

func prettyJSON(raw json.RawMessage) []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return raw
	}
	return out.Bytes()
}
