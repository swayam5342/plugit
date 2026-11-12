// Package proxy turns validated tool arguments into an HTTP request and the
// upstream response into a tool result (SPEC §3.7 steps 5-8).
//
// Every argument is routed by the location the spec declares, under its
// upstream name; nothing is guessed. The host always comes from the pinned
// base URL. Path values are percent-encoded and may not be empty, ".", ".."
// or contain a slash (even percent-encoded); header values may not contain
// CR, LF or other control characters. Credentials are attached last, only
// for the pinned origin, and replace anything with the same name.
package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/model"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrBadArgument means an argument value can't be sent safely.
	ErrBadArgument = errors.New("bad argument")
	// ErrBuild means the tool or target can't produce a request.
	ErrBuild = errors.New("cannot build request")
)

// AuthType is how the credential is attached.
type AuthType string

// Auth types (SPEC §3.6).
const (
	AuthNone   AuthType = "none"
	AuthBearer AuthType = "bearer" // Authorization: Bearer <secret>
	AuthHeader AuthType = "header" // <Header>: <secret>
	AuthQuery  AuthType = "query"  // ?<Param>=<secret>
	AuthBasic  AuthType = "basic"  // Authorization: Basic base64(<secret>), secret is "user:pass"
)

// Auth is a resolved credential.
type Auth struct {
	Type   AuthType
	Header string // AuthHeader
	Param  string // AuthQuery
	Secret string
}

// Fixed is a resolved fixed parameter. If the tool declares a parameter with
// this name, the spec's location wins; otherwise In is used (query when
// unset).
type Fixed struct {
	Name   string
	In     model.Location
	Value  string
	Secret bool // from env: or keyring:; redacted by value (SPEC T11)
}

// Target is everything about the upstream that isn't per call.
type Target struct {
	BaseURL   *url.URL // pinned; never from the spec at runtime or the model
	Auth      Auth
	Fixed     []Fixed
	UserAgent string
}

// SecretValues lists every value that must be redacted wherever it appears
// (SPEC T11): the credential, the password part of a Basic credential on its
// own (an upstream may echo just that), and secret fixed values. literal:
// fixed values are not secrets.
func (tg Target) SecretValues() []string {
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	if tg.Auth.Type != AuthNone && tg.Auth.Type != "" {
		add(tg.Auth.Secret)
		if tg.Auth.Type == AuthBasic {
			if _, pass, ok := strings.Cut(tg.Auth.Secret, ":"); ok {
				add(pass)
			}
		}
	}
	for _, f := range tg.Fixed {
		if f.Secret {
			add(f.Value)
		}
	}
	return out
}

var placeholderRe = regexp.MustCompile(`\{[^{}/]*\}`)

// Build creates the request for one call. args are decoded arguments (see
// schema.DecodeArgs) and should already have passed schema validation; Build
// re-checks everything it relies on.
func Build(ctx context.Context, t model.Tool, args map[string]any, tg Target) (*http.Request, error) {
	if err := checkBase(tg.BaseURL); err != nil {
		return nil, err
	}

	byName := make(map[string]*model.Param, len(t.Params))
	for i := range t.Params {
		p := &t.Params[i]
		if !p.Hidden {
			byName[p.Name] = p
		}
	}
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	slices.Sort(names) // deterministic errors and query order

	path := t.Path
	query := url.Values{}
	header := http.Header{}
	bodyFields := map[string]any{}
	var bodyRoot any
	hasBodyRoot := false

	for _, name := range names {
		v := args[name]
		p, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: unknown argument %q", ErrBadArgument, name)
		}
		if v == nil {
			continue
		}
		switch p.In {
		case model.InPath:
			seg, err := pathSegment(name, v)
			if err != nil {
				return nil, err
			}
			path = strings.ReplaceAll(path, "{"+p.UpstreamName+"}", seg)
		case model.InQuery:
			if err := addQuery(query, p.UpstreamName, v); err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrBadArgument, name, err)
			}
		case model.InHeader:
			s, err := headerValue(v)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrBadArgument, name, err)
			}
			header.Set(p.UpstreamName, s)
		case model.InBody:
			bodyFields[p.UpstreamName] = v
		case model.InBodyRoot:
			bodyRoot, hasBodyRoot = v, true
		default:
			return nil, fmt.Errorf("%w: argument %q has unsupported location %s", ErrBuild, name, p.In)
		}
	}

	// Fixed params: hidden from the model, injected where the spec declares
	// them, else at the configured location.
	for _, f := range tg.Fixed {
		in := f.In
		upstream := f.Name
		if p := declaredFixed(t, f.Name); p != nil {
			in, upstream = p.In, p.UpstreamName
		} else if in != model.InHeader {
			in = model.InQuery
		}
		switch in {
		case model.InQuery:
			query.Set(upstream, f.Value)
		case model.InHeader:
			s, err := headerValue(f.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: fixed header %q has an invalid value", ErrBuild, upstream)
			}
			header.Set(upstream, s)
		case model.InBody:
			bodyFields[upstream] = f.Value
		case model.InPath:
			seg, err := pathSegment(upstream, f.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: fixed path parameter %q has an invalid value", ErrBuild, upstream)
			}
			path = strings.ReplaceAll(path, "{"+upstream+"}", seg)
		default:
			return nil, fmt.Errorf("%w: fixed parameter %q has unsupported location %s", ErrBuild, upstream, in)
		}
	}

	if ph := placeholderRe.FindString(path); ph != "" {
		return nil, fmt.Errorf("%w: path parameter %s has no value", ErrBuild, ph)
	}

	body, contentType, err := encodeBody(t.Body, bodyFields, bodyRoot, hasBodyRoot)
	if err != nil {
		return nil, err
	}

	u := joinURL(tg.BaseURL, path)
	if tg.Auth.Type == AuthQuery && tg.Auth.Param != "" {
		query.Del(tg.Auth.Param) // the credential replaces anything with its name
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(t.Method), u.String(), bytesReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBuild, err)
	}
	maps.Copy(req.Header, header)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, */*;q=0.8")
	if tg.UserAgent != "" {
		req.Header.Set("User-Agent", tg.UserAgent)
	}
	if err := attachAuth(req, tg); err != nil {
		return nil, err
	}
	return req, nil
}

func checkBase(b *url.URL) error {
	switch {
	case b == nil:
		return fmt.Errorf("%w: no base URL", ErrBuild)
	case b.Scheme != "http" && b.Scheme != "https":
		return fmt.Errorf("%w: base URL must be http or https", ErrBuild)
	case b.Host == "":
		return fmt.Errorf("%w: base URL has no host", ErrBuild)
	case b.User != nil:
		return fmt.Errorf("%w: base URL must not contain credentials", ErrBuild)
	case b.RawQuery != "" || b.Fragment != "":
		return fmt.Errorf("%w: base URL must not have a query or fragment", ErrBuild)
	}
	return nil
}

func declaredFixed(t model.Tool, name string) *model.Param {
	for i := range t.Params {
		p := &t.Params[i]
		if !p.Hidden {
			continue
		}
		if p.UpstreamName == name || (p.In == model.InHeader && strings.EqualFold(p.UpstreamName, name)) {
			return p
		}
	}
	return nil
}

// joinURL appends an escaped path to the base URL's path.
func joinURL(b *url.URL, escapedPath string) *url.URL {
	u := &url.URL{Scheme: b.Scheme, Host: b.Host}
	full := strings.TrimRight(b.EscapedPath(), "/") + escapedPath
	if p, err := url.PathUnescape(full); err == nil {
		u.Path = p
		u.RawPath = full
	} else {
		u.Path = full
	}
	return u
}

// pathSegment formats and checks one path parameter value.
func pathSegment(name string, v any) (string, error) {
	s, ok := scalar(v)
	if !ok {
		return "", fmt.Errorf("%w: path parameter %q must be a string, number or boolean", ErrBadArgument, name)
	}
	decoded, err := url.PathUnescape(s)
	if err != nil {
		decoded = s
	}
	for _, c := range []string{s, decoded} {
		if c == "" || c == "." || c == ".." || strings.ContainsAny(c, `/\`) || hasControl(c) {
			return "", fmt.Errorf("%w: path parameter %q must not be empty, '.', '..', contain '/', '\\' or control characters (also when percent-decoded)", ErrBadArgument, name)
		}
	}
	return url.PathEscape(s), nil
}

func addQuery(q url.Values, name string, v any) error {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if e == nil {
				continue
			}
			s, ok := scalar(e)
			if !ok {
				return errors.New("array items must be strings, numbers or booleans")
			}
			q.Add(name, s)
		}
		return nil
	case map[string]any:
		// OpenAPI's default (style: form, explode: true): one pair per property.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if x[k] == nil {
				continue
			}
			s, ok := scalar(x[k])
			if !ok {
				return errors.New("object properties must be strings, numbers or booleans")
			}
			q.Add(k, s)
		}
		return nil
	}
	s, ok := scalar(v)
	if !ok {
		return errors.New("unsupported value")
	}
	q.Add(name, s)
	return nil
}

func headerValue(v any) (string, error) {
	var s string
	if arr, ok := v.([]any); ok {
		parts := make([]string, 0, len(arr))
		for _, e := range arr {
			p, ok := scalar(e)
			if !ok {
				return "", errors.New("array items must be strings, numbers or booleans")
			}
			parts = append(parts, p)
		}
		s = strings.Join(parts, ",")
	} else {
		var ok bool
		if s, ok = scalar(v); !ok {
			return "", errors.New("header values must be strings, numbers or booleans")
		}
	}
	if hasControl(s) {
		return "", errors.New("header values must not contain CR, LF or other control characters")
	}
	return s, nil
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	}
	return "", false
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func attachAuth(req *http.Request, tg Target) error {
	a := tg.Auth
	if a.Type == "" || a.Type == AuthNone {
		return nil
	}
	// Bound to the pinned origin (SPEC T8). Redirects are same-origin only
	// in httpx, so this holds for the whole exchange.
	if !httpx.SameOrigin(req.URL, tg.BaseURL) {
		return fmt.Errorf("%w: request origin differs from the pinned base URL", ErrBuild)
	}
	if hasControl(a.Secret) {
		return fmt.Errorf("%w: the credential contains control characters", ErrBuild)
	}
	switch a.Type {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+a.Secret)
	case AuthHeader:
		if a.Header == "" {
			return fmt.Errorf("%w: auth.header is required for header auth", ErrBuild)
		}
		req.Header.Del(a.Header)
		req.Header.Set(a.Header, a.Secret)
	case AuthQuery:
		if a.Param == "" {
			return fmt.Errorf("%w: auth.param is required for query auth", ErrBuild)
		}
		q := req.URL.Query()
		q.Set(a.Param, a.Secret)
		req.URL.RawQuery = q.Encode()
	case AuthBasic:
		if !strings.Contains(a.Secret, ":") {
			return fmt.Errorf("%w: basic auth secrets must look like user:password", ErrBuild)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.Secret)))
	default:
		return fmt.Errorf("%w: unknown auth type %q", ErrBuild, a.Type)
	}
	return nil
}

// bytesReader returns a nil io.Reader (not a typed-nil *bytes.Reader, which
// http.NewRequest would dereference) when there is no body.
func bytesReader(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}
