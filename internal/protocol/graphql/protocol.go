package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/utils"
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
	declared := make(map[string]*model.Param, len(c.Tool.Params))
	for i := range c.Tool.Params {
		p := &c.Tool.Params[i]
		if p.Bind.In == model.InVariable {
			declared[p.Bind.Upstream] = p
		}
		if !p.Hidden {
			byName[p.Name] = p
		}
	}

	vars := map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(c.Args)) {
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
			// Sent only if the document declares the variable; serve warns at
			// load time about fixed variables a tool doesn't declare.
			if p := declared[f.Name]; p != nil {
				v, err := FixedVariableValue(p.Schema, f.Value)
				if err != nil {
					return nil, fmt.Errorf("%w: fixed variable %q: %w", ErrBuild, f.Name, err)
				}
				vars[f.Name] = v
			}
		case model.InHeader:
			if schema.IsForbiddenHeader(f.Name) {
				return nil, fmt.Errorf("%w: fixed header %q can't be set", ErrBuild, f.Name)
			}
			if utils.HasControl(f.Value) {
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

// FixedVariableValue converts a fixed variable's config string to the JSON
// type of the variable's param schema: integer, number and boolean are
// parsed, anything else (string, enum, ID, custom scalar) is sent as the
// string. An error never repeats the value, which may be a secret.
func FixedVariableValue(paramSchema json.RawMessage, value string) (any, error) {
	switch t := SchemaType(paramSchema); t {
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, errors.New("the value is not an integer")
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("the value is not a finite number")
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, errors.New("the value is not true or false")
		}
		return b, nil
	default:
		return value, nil
	}
}

// SchemaType is the "type" of a param's JSON Schema, or "" when it has none
// (an enum, or a degraded {}) or isn't a single string.
func SchemaType(paramSchema json.RawMessage) string {
	var s struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(paramSchema, &s) != nil {
		return ""
	}
	t, _ := s.Type.(string)
	return t
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
	case protocol.IsBinary(ct, body):
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
// an object nor null, errors not a list of objects with string messages, or
// a data object without the root field and no errors (a nonconforming
// server; an explicit null field is a conforming "not found").
// fieldValue is data.<field> byte-for-byte, or null.
func parseResponse(body []byte, field string) (data, fieldValue json.RawMessage, errs []cleanError, ok bool) {
	var r response
	if json.Unmarshal(body, &r) != nil || (r.Data == nil && r.Errors == nil) {
		return nil, nil, nil, false
	}
	fieldValue = json.RawMessage("null")
	missing := false
	if !isNull(r.Data) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(r.Data, &obj) != nil || obj == nil {
			return nil, nil, nil, false
		}
		v, found := obj[field]
		if found {
			fieldValue = v
		}
		missing = !found
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
	if missing && len(errs) == 0 {
		return nil, nil, nil, false
	}
	return r.Data, fieldValue, errs, true
}

// cleanPath keeps an error path only when it is a list, as the GraphQL spec
// defines it, and keeps only the segments the spec allows: field names (the
// GraphQL Name grammar) and non-negative integer list indices. Any other
// segment is dropped, so upstream text can't reach the model through a
// path. A path left with no segment is dropped.
func cleanPath(raw json.RawMessage) json.RawMessage {
	if isNull(raw) {
		return nil
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return nil
	}
	kept := make([]any, 0, len(elems))
	for _, e := range elems {
		var s string
		var n json.Number
		switch {
		case json.Unmarshal(e, &s) == nil:
			if isName(s) {
				kept = append(kept, s)
			}
		case json.Unmarshal(e, &n) == nil:
			if i, err := strconv.ParseUint(n.String(), 10, 63); err == nil {
				kept = append(kept, i)
			}
		}
	}
	if len(kept) == 0 {
		return nil
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil
	}
	return b
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
