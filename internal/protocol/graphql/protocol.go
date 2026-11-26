package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/schema"
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrBadArgument = errors.New("bad argument")
	ErrBuild       = errors.New("cannot build request")
)

// Result codes for a response that is a GraphQL response (SPEC §3.7 P3/P7).
// A response that isn't one is coded "HTTP <status>", as for REST.
const (
	CodeOK      = "ok"
	CodeErrors  = "errors"
	CodePartial = "partial"
)

// partialNote ends the status line of a partial result, so the model knows
// that some fields failed even though data came back.
const partialNote = "; GraphQL partial result (errors present)"

// Fixed is a resolved fixed parameter: In is model.InVariable or
// model.InHeader (SPEC §3.4 P3).
type Fixed struct {
	Name   string
	In     model.Location
	Value  string
	Secret bool
}

// Target is everything about the upstream that isn't per call. The endpoint
// comes from the Call; the credential is attached by invoke, never here (T17).
type Target struct {
	Fixed     []Fixed
	UserAgent string
}

// SecretValues lists the secret fixed values, which must be redacted
// wherever they appear (SPEC T11).
func (tg Target) SecretValues() []string {
	var out []string
	for _, f := range tg.Fixed {
		if f.Secret && f.Value != "" {
			out = append(out, f.Value)
		}
	}
	return out
}

// GraphQL serves GraphQL tools (SPEC §3.5 P7). It is constructed directly by
// the CLI (cli.resolveProto), like every protocol.
type GraphQL struct {
	Target Target
}

// Name reports the protocol.
func (GraphQL) Name() model.Protocol { return model.GraphQL }

// Describe returns the operation type and root field for the TUI and `try`.
func (GraphQL) Describe(t model.Tool) (badge, target string) {
	if t.GraphQL != nil {
		return t.GraphQL.Operation, t.GraphQL.Field
	}
	return "", string(t.Key)
}

type requestBody struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName"`
}

// Encode builds POST <endpoint> carrying the tool's fixed document and the
// validated arguments as variables, keyed by their upstream names. It attaches
// no credential (SPEC §3.7 P5 step 6).
func (g GraphQL) Encode(ctx context.Context, c protocol.Call) (*http.Request, error) {
	op := c.Tool.GraphQL
	if op == nil {
		return nil, fmt.Errorf("%w: tool %q has no GraphQL operation", ErrBuild, c.Tool.Key)
	}
	if err := checkEndpoint(c.Endpoint); err != nil {
		return nil, err
	}

	byName := make(map[string]*model.Param, len(c.Tool.Params))
	declared := make(map[string]bool, len(c.Tool.Params))
	for i := range c.Tool.Params {
		p := &c.Tool.Params[i]
		if p.Bind.In == model.InVariable {
			declared[p.Bind.Upstream] = true
		}
		if !p.Hidden {
			byName[p.Name] = p
		}
	}

	vars := map[string]any{}
	for _, name := range sortedKeys(c.Args) {
		p, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown argument %q", ErrBadArgument, name)
		}
		if p.Bind.In != model.InVariable {
			return nil, fmt.Errorf("%w: argument %q has unsupported location %s", ErrBuild, name, p.Bind.In)
		}
		if v := c.Args[name]; v != nil {
			vars[p.Bind.Upstream] = v
		}
	}

	header := http.Header{}
	for _, f := range g.Target.Fixed {
		switch f.In {
		case model.InVariable:
			// Sent only if the document declares the variable; Task 11 warns at
			// load time about fixed variables a tool doesn't declare.
			if declared[f.Name] {
				vars[f.Name] = f.Value
			}
		case model.InHeader:
			if schema.IsForbiddenHeader(f.Name) {
				return nil, fmt.Errorf("%w: fixed header %q can't be set", ErrBuild, f.Name)
			}
			if hasControl(f.Value) {
				return nil, fmt.Errorf("%w: fixed header %q has an invalid value", ErrBuild, f.Name)
			}
			header.Set(f.Name, f.Value)
		default:
			return nil, fmt.Errorf("%w: fixed parameter %q has unsupported location %s", ErrBuild, f.Name, f.In)
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(requestBody{Query: op.Document, Variables: vars, OperationName: operationPrefix + op.Field}); err != nil {
		return nil, fmt.Errorf("%w: encoding the request body: %w", ErrBadArgument, err)
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBuild, err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/graphql-response+json, application/json")
	if g.Target.UserAgent != "" {
		req.Header.Set("User-Agent", g.Target.UserAgent)
	}
	return req, nil
}

func checkEndpoint(u *url.URL) error {
	switch {
	case u == nil:
		return fmt.Errorf("%w: no endpoint", ErrBuild)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("%w: endpoint must be http or https", ErrBuild)
	case u.Host == "":
		return fmt.Errorf("%w: endpoint has no host", ErrBuild)
	case u.User != nil:
		return fmt.Errorf("%w: endpoint must not contain credentials", ErrBuild)
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// response is a GraphQL response envelope. Both fields stay raw so the data
// keeps its number formatting, key order and escaping; a nil field was absent.
type response struct {
	Data   json.RawMessage `json:"data"`
	Errors json.RawMessage `json:"errors"`
}

// upstreamError is the part of an errors[] entry that reaches the model:
// extensions (arbitrary upstream payload) and locations (line numbers in
// PlugIt's own document) are dropped.
type upstreamError struct {
	Message *string         `json:"message"`
	Path    json.RawMessage `json:"path"`
}

// cleanError is an errors[] entry as shown to the model.
type cleanError struct {
	Message string          `json:"message"`
	Path    json.RawMessage `json:"path,omitempty"`
}

// Decode applies the P7 result rules. A body that is a GraphQL response is
// judged by its data and errors whatever the HTTP status; anything else
// (empty, binary, non-JSON, truncated, or JSON without data or errors) is
// coded by its HTTP status. Redaction happens later in invoke.
func (GraphQL) Decode(c protocol.Call, resp *http.Response, body []byte, truncated bool) protocol.Result {
	ct := resp.Header.Get("Content-Type")
	status := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if ct != "" {
		status += " " + ct
	}
	httpRes := protocol.Result{Code: fmt.Sprintf("HTTP %d", resp.StatusCode), IsError: true, Status: status}

	switch {
	case truncated:
		httpRes.IsError = resp.StatusCode >= 400
		httpRes.Text = string(body)
		return httpRes
	case len(body) == 0:
		httpRes.Text = fmt.Sprintf("(empty response, status %d)", resp.StatusCode)
		return httpRes
	case isBinary(ct, body):
		typ := ct
		if typ == "" {
			typ = "unknown"
		}
		httpRes.Binary = &protocol.BinaryInfo{Bytes: len(body), Type: typ}
		httpRes.Text = fmt.Sprintf("[binary %d bytes, type %s]", len(body), typ)
		return httpRes
	}

	field := ""
	if c.Tool.GraphQL != nil {
		field = c.Tool.GraphQL.Field
	}
	data, fieldValue, errs, ok := parseResponse(body, field)
	if !ok {
		httpRes.Text = string(body)
		return httpRes
	}

	res := protocol.Result{Status: status}
	switch {
	case len(errs) == 0:
		res.Code = CodeOK
		res.Value = fieldValue
	case !isNull(fieldValue):
		res.Code = CodePartial
		res.Status += partialNote
		res.Value = partialValue(data, errs)
	default:
		res.Code = CodeErrors
		res.IsError = true
		res.Text = errorText(errs)
	}
	return res
}

// parseResponse reads a GraphQL response envelope. ok is false when body is
// not one: not a JSON object, neither data nor errors present, data neither
// an object nor null, or errors not a list of objects with string messages.
// fieldValue is data.<field> byte-for-byte, or null.
func parseResponse(body []byte, field string) (data, fieldValue json.RawMessage, errs []cleanError, ok bool) {
	var r response
	if json.Unmarshal(body, &r) != nil || (r.Data == nil && r.Errors == nil) {
		return nil, nil, nil, false
	}
	fieldValue = json.RawMessage("null")
	if !isNull(r.Data) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(r.Data, &obj) != nil || obj == nil {
			return nil, nil, nil, false
		}
		if v, found := obj[field]; found {
			fieldValue = v
		}
	}
	if !isNull(r.Errors) {
		var raw []upstreamError
		if json.Unmarshal(r.Errors, &raw) != nil {
			return nil, nil, nil, false
		}
		for _, e := range raw {
			if e.Message == nil {
				return nil, nil, nil, false
			}
			errs = append(errs, cleanError{Message: naming.CleanText(*e.Message, 0), Path: cleanPath(e.Path)})
		}
	}
	return r.Data, fieldValue, errs, true
}

// cleanPath keeps an error path only when it is a list of strings and
// integers, as the GraphQL spec defines it, compacted.
func cleanPath(raw json.RawMessage) json.RawMessage {
	if isNull(raw) {
		return nil
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return nil
	}
	for _, e := range elems {
		var s string
		var n json.Number
		if json.Unmarshal(e, &s) != nil && json.Unmarshal(e, &n) != nil {
			return nil
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return nil
	}
	return buf.Bytes()
}

// partialValue renders {"data": <data byte-for-byte>, "errors": <cleaned>}.
func partialValue(data json.RawMessage, errs []cleanError) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteString(`{"data":`)
	buf.Write(data)
	buf.WriteString(`,"errors":`)
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(errs) // cleanError holds a string and validated raw JSON; it cannot fail
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return append(out, '}')
}

// errorText renders one cleaned error message per line, each followed by its
// path when it has one.
func errorText(errs []cleanError) string {
	lines := make([]string, 0, len(errs))
	for _, e := range errs {
		line := strings.Join(strings.Fields(e.Message), " ") // one line per error
		if line == "" {
			line = "(no message)"
		}
		if e.Path != nil {
			line += " (path: " + string(e.Path) + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

func isBinary(ct string, body []byte) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"),
		strings.HasPrefix(mt, "font/"), mt == "application/octet-stream", mt == "application/pdf",
		mt == "application/zip", mt == "application/gzip":
		return true
	}
	return !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0
}
