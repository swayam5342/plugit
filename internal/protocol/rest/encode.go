// Package rest is the REST Protocol (SPEC §3.5 P4/§3.7 P5): it encodes a
// validated call into an HTTP request and decodes the response into a neutral
// Result. It never attaches a credential, opens a connection or calls httpx —
// invoke owns every network step (T17).
//
// Every argument is routed by the location the spec declares, under its
// upstream name; nothing is guessed. The host always comes from the pinned
// base URL. Path values are percent-encoded and may not be empty, ".", ".."
// or contain a slash (even percent-encoded); header values may not contain
// CR, LF or other control characters.
package rest

import (
	"bytes"
	"context"
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

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrBadArgument = errors.New("bad argument")
	ErrBuild       = errors.New("cannot build request")
)

// Fixed is a resolved fixed parameter. If the tool declares a parameter with
// this name, the spec's location wins; otherwise In is used (query when
// unset).
type Fixed struct {
	Name   string
	In     model.Location
	Value  string
	Secret bool
}

// Target is everything about the upstream that isn't per call.
type Target struct {
	BaseURL   *url.URL
	Fixed     []Fixed
	UserAgent string
	Extra     []string
}

// SecretValues lists the target's values that must be redacted wherever they
// appear (SPEC T11): secret fixed values and Extra. The credential's own values
// come from invoke.Credential.SecretValues. literal:
// fixed values are not secrets.
func (tg Target) SecretValues() []string {
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	for _, f := range tg.Fixed {
		if f.Secret {
			add(f.Value)
		}
	}
	for _, e := range tg.Extra {
		add(e)
	}
	return out
}

var placeholderRe = regexp.MustCompile(`\{[^{}/]*\}`)

// Encode routes arguments and fixed params into an *http.Request WITHOUT any
// credential (SPEC §3.7 P5: a protocol's Encode never attaches credentials;
// invoke calls AttachAuth afterwards).
func Encode(ctx context.Context, t model.Tool, args map[string]any, tg Target) (*http.Request, error) {
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
	slices.Sort(names)

	path := t.HTTP.Path
	query := url.Values{}
	header := http.Header{}
	bodyFields := map[string]any{}
	cookies := map[string]string{}
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
		switch p.Bind.In {
		case model.InPath:
			seg, err := pathSegment(name, v)
			if err != nil {
				return nil, err
			}
			path = strings.ReplaceAll(path, "{"+p.Bind.Upstream+"}", seg)
		case model.InQuery:
			if err := addQuery(query, p.Bind.Upstream, v); err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrBadArgument, name, err)
			}
		case model.InHeader:
			if schema.IsForbiddenHeader(p.Bind.Upstream) {
				return nil, fmt.Errorf("%w: %q: the %s header can't be set by a tool", ErrBadArgument, name, p.Bind.Upstream)
			}
			s, err := headerValue(v)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrBadArgument, name, err)
			}
			header.Set(p.Bind.Upstream, s)
		case model.InCookie:
			s, err := cookieValue(p.Bind.Upstream, v)
			if err != nil {
				return nil, fmt.Errorf("%w: %q: %w", ErrBadArgument, name, err)
			}
			cookies[p.Bind.Upstream] = s
		case model.InBody:
			bodyFields[p.Bind.Upstream] = v
		case model.InBodyRoot:
			bodyRoot, hasBodyRoot = v, true
		default:
			return nil, fmt.Errorf("%w: argument %q has unsupported location %s", ErrBuild, name, p.Bind.In)
		}
	}

	for _, f := range tg.Fixed {
		in := f.In
		upstream := f.Name
		if p := declaredFixed(t, f.Name); p != nil {
			in, upstream = p.Bind.In, p.Bind.Upstream
		} else if in != model.InHeader && in != model.InCookie {
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
		case model.InCookie:
			s, err := cookieValue(upstream, f.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: fixed cookie %q has an invalid value", ErrBuild, upstream)
			}
			cookies[upstream] = s
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

	declared := map[string]bool{}
	for _, p := range t.Params {
		if p.Bind.In == model.InBody {
			declared[p.Bind.Upstream] = true
		}
	}
	body, contentType, err := encodeBody(t.HTTP.Body, bodyFields, bodyRoot, hasBodyRoot, declared)
	if err != nil {
		return nil, err
	}

	u := joinURL(tg.BaseURL, path)
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(t.HTTP.Method), u.String(), bytesReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBuild, err)
	}
	maps.Copy(req.Header, header)
	if len(cookies) > 0 {
		parts := make([]string, 0, len(cookies))
		for _, k := range slices.Sorted(maps.Keys(cookies)) {
			parts = append(parts, k+"="+cookies[k])
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, */*;q=0.8")
	if tg.UserAgent != "" {
		req.Header.Set("User-Agent", tg.UserAgent)
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
		if p.Bind.Upstream == name || (p.Bind.In == model.InHeader && strings.EqualFold(p.Bind.Upstream, name)) {
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

var cookieNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")

// cookieValue formats a scalar as a cookie value and refuses anything that
// could end the value or start another cookie (RFC 6265 cookie-octets).
func cookieValue(name string, v any) (string, error) {
	if !cookieNameRe.MatchString(name) {
		return "", fmt.Errorf("%q isn't a valid cookie name", name)
	}
	s, ok := formScalar(v)
	if !ok {
		return "", errors.New("a cookie value must be a string, number or boolean")
	}
	for _, r := range s {
		if r <= ' ' || r >= 0x7f || r == '"' || r == ',' || r == ';' || r == '\\' {
			return "", errors.New("cookie values can't contain spaces, quotes, commas, semicolons, backslashes, control or non-ASCII characters")
		}
	}
	return s, nil
}

// bytesReader returns a nil io.Reader (not a typed-nil *bytes.Reader, which
// http.NewRequest would dereference) when there is no body.
func bytesReader(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}
