// Package redact masks secrets before anything derived from a request or
// response reaches logs, the TUI or an error result.
//
// It works two ways (SPEC §5.3, T11):
//
//   - by name: auth headers, cookies, and header or query names that look
//     secret (or are configured as secret) have their values replaced;
//   - by value: every resolved secret is replaced wherever it appears, in its
//     raw, percent-encoded, base64 and JSON-escaped forms. This catches
//     upstream error bodies that echo a key back.
//
// Well-known token formats (GitHub, Stripe, Slack, AWS, JWT, PEM keys, ...)
// are masked as a backstop even when they were never configured.
package redact

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Mask replaces every redacted value.
const Mask = "***"

// Options configures a Redactor.
type Options struct {
	SecretHeaders     []string
	SecretQueryParams []string
	Secrets           []string
}

// Redactor masks secrets. It is immutable after New and safe for concurrent
// use.
type Redactor struct {
	opts     Options
	headers  map[string]bool
	params   map[string]bool
	forms    []string
	replacer *strings.Replacer
}

// New builds a Redactor from opts.
func New(opts Options) *Redactor {
	r := &Redactor{
		opts:    opts,
		headers: make(map[string]bool, len(opts.SecretHeaders)),
		params:  make(map[string]bool, len(opts.SecretQueryParams)),
	}
	for _, h := range opts.SecretHeaders {
		r.headers[normalizeName(h)] = true
	}
	for _, p := range opts.SecretQueryParams {
		r.params[normalizeName(p)] = true
	}

	r.forms = secretForms(opts.Secrets)
	if len(r.forms) > 0 {
		pairs := make([]string, 0, 2*len(r.forms))
		for _, f := range r.forms {
			pairs = append(pairs, f, Mask)
		}
		r.replacer = strings.NewReplacer(pairs...)
	}
	return r
}

// With returns a Redactor that also masks extra secrets, for values only
// known later (an OAuth access token fetched while serving). Empty values
// are ignored; with none left, it returns r itself.
func (r *Redactor) With(extra ...string) *Redactor {
	var add []string
	for _, e := range extra {
		if e != "" {
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return r
	}
	opts := r.opts
	opts.Secrets = append(slices.Clone(opts.Secrets), add...)
	return New(opts)
}

// String returns s with every secret value and known token format masked.
func (r *Redactor) String(s string) string {
	if r.replacer != nil {
		s = r.replacer.Replace(s)
	}
	for _, p := range tokenPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// ContainsSecret reports whether s contains a configured secret in any form
// String masks (raw, percent-encoded, base64 or JSON-escaped). Known token
// formats that were never configured don't count. It is for text that must
// be refused rather than masked, such as an introspection result that
// becomes spec text.
//
// When s is a JSON document with escapes, its canonical spelling
// (CanonicalJSON) is checked too, so every JSON-escape spelling of every
// form counts ("tok\/en", \uXXXX escapes, or a mix): a JSON parser decodes them
// all back to the secret. Text that is not one valid JSON document has each
// complete string literal checked the same way (canonicalStrings).
func (r *Redactor) ContainsSecret(s string) bool {
	if len(r.forms) == 0 {
		return false
	}
	if r.containsForm(s) {
		return true
	}
	if !strings.ContainsRune(s, '\\') {
		return false
	}
	c, ok := CanonicalJSON([]byte(s))
	if !ok {
		c = canonicalStrings([]byte(s))
	}
	return r.containsForm(string(c))
}

func (r *Redactor) containsForm(s string) bool {
	for _, f := range r.forms {
		if strings.Contains(s, f) {
			return true
		}
	}
	return false
}

// Bytes is String for byte slices. It always returns a new slice.
func (r *Redactor) Bytes(b []byte) []byte {
	return []byte(r.String(string(b)))
}

// Header returns a redacted copy of h. Secret-named headers become Mask,
// Cookie keeps cookie names but masks their values, and every other value is
// masked by value and token pattern. h is not modified.
func (r *Redactor) Header(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		masked := make([]string, len(values))
		for i, v := range values {
			switch {
			case http.CanonicalHeaderKey(name) == "Cookie":
				masked[i] = maskCookieValues(v)
			case r.IsSecretHeader(name):
				masked[i] = Mask
			default:
				masked[i] = r.String(v)
			}
		}
		out[name] = masked
	}
	return out
}

// URL returns a redacted string form of u. Userinfo passwords and the values
// of secret-named query parameters are masked; parameter order and encoding
// are kept. The result is then masked by value and token pattern.
func (r *Redactor) URL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	const pwPlaceholder = "PLUGITREDACTEDPASSWORD"
	if c.User != nil {
		if _, ok := c.User.Password(); ok {
			c.User = url.UserPassword(c.User.Username(), pwPlaceholder)
		}
	}
	if c.RawQuery != "" {
		parts := strings.Split(c.RawQuery, "&")
		for i, part := range parts {
			rawKey, _, hasValue := strings.Cut(part, "=")
			key, err := url.QueryUnescape(rawKey)
			if err != nil {
				key = rawKey
			}
			if hasValue && r.isSecretParam(key) {
				parts[i] = rawKey + "=" + Mask
			}
		}
		c.RawQuery = strings.Join(parts, "&")
	}
	s := strings.Replace(c.String(), pwPlaceholder, Mask, 1)
	return r.String(s)
}

// IsSecretHeader reports whether values of the header called name must be
// masked.
func (r *Redactor) IsSecretHeader(name string) bool {
	n := normalizeName(name)
	return r.headers[n] || looksSecret(n)
}

func (r *Redactor) isSecretParam(name string) bool {
	n := normalizeName(name)
	return r.params[n] || looksSecret(n)
}

// normalizeName lowercases and drops '-' and '_' so that X-Api-Key, x_api_key
// and xapikey compare equal.
func normalizeName(name string) string {
	return strings.Map(func(c rune) rune {
		switch c {
		case '-', '_':
			return -1
		}
		if 'A' <= c && c <= 'Z' {
			return c + ('a' - 'A')
		}
		return c
	}, name)
}

var exactSecretNames = map[string]bool{"key": true, "sig": true, "auth": true}

var secretFragments = []string{
	"authorization", "token", "secret", "password", "passwd", "apikey",
	"session", "cookie", "signature", "credential", "privatekey", "accesskey",
}

func looksSecret(normalized string) bool {
	if exactSecretNames[normalized] || strings.HasPrefix(normalized, "xauth") {
		return true
	}
	for _, f := range secretFragments {
		if strings.Contains(normalized, f) {
			return true
		}
	}
	return false
}

// maskCookieValues turns "a=1; b=2" into "a=***; b=***".
func maskCookieValues(v string) string {
	parts := strings.Split(v, ";")
	for i, p := range parts {
		name, _, ok := strings.Cut(p, "=")
		if ok {
			parts[i] = name + "=" + Mask
		} else if strings.TrimSpace(p) != "" {
			parts[i] = Mask
		}
	}
	return strings.Join(parts, ";")
}

// SchemeToken returns the token of an auth header value "<scheme> <token>"
// whose scheme is a single word of ASCII letters ("Bearer", "token"), or ""
// for any other value. Callers list it as a secret of its own next to the
// whole value: an upstream may echo just the token. A short token (as in
// "Bearer x") then masks every occurrence of those bytes, which over-masks
// but never leaks.
func SchemeToken(v string) string {
	scheme, tok, ok := strings.Cut(v, " ")
	if !ok || scheme == "" {
		return ""
	}
	for _, c := range scheme {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return ""
		}
	}
	return strings.TrimSpace(tok)
}

// secretForms expands each secret into every encoding it may appear in,
// deduplicated and ordered longest first so the longest match wins.
func secretForms(secrets []string) []string {
	seen := map[string]bool{}
	var forms []string
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			forms = append(forms, f)
		}
	}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		add(s)
		add(url.QueryEscape(s))
		add(url.PathEscape(s))
		b := []byte(s)
		add(base64.StdEncoding.EncodeToString(b))
		add(base64.URLEncoding.EncodeToString(b))
		add(base64.RawStdEncoding.EncodeToString(b))
		add(base64.RawURLEncoding.EncodeToString(b))
		add(jsonEscaped(s, true))
		add(jsonEscaped(s, false))
	}
	slices.SortStableFunc(forms, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	return forms
}

// jsonEscaped returns s as it appears inside a JSON string literal.
func jsonEscaped(s string, escapeHTML bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(s); err != nil {
		return ""
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	return strings.TrimSuffix(strings.TrimPrefix(out, `"`), `"`)
}

type tokenPattern struct {
	re   *regexp.Regexp
	repl string
}

var tokenPatterns = []tokenPattern{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), Mask},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), Mask},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`), Mask},
	{regexp.MustCompile(`\b[spr]k_(?:live|test)_[A-Za-z0-9]{16,}`), Mask},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), Mask},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), Mask},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), Mask},
	{regexp.MustCompile(`\bsk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}`), Mask},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), Mask},
	{regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} " + Mask},
}

// Args returns a copy of call arguments for the call log: values under
// secret-looking names (at any depth) become Mask, and every string has
// secrets and known token formats masked. args is not changed.
func (r *Redactor) Args(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out, _ := r.walk(args, true).(map[string]any)
	return out
}

// Values is Args without masking by name (log.args: full). Resolved
// secrets and token formats are still masked.
func (r *Redactor) Values(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out, _ := r.walk(args, false).(map[string]any)
	return out
}

func (r *Redactor) walk(v any, byName bool) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if byName && r.isSecretParam(k) {
				out[k] = Mask
				continue
			}
			out[k] = r.walk(x, byName)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = r.walk(x, byName)
		}
		return out
	case string:
		return r.String(v)
	}
	return v
}

// CanonicalJSON re-spells the JSON document b in one canonical form: compact,
// every string (keys included) re-encoded with Go's escapes and no HTML
// escaping, numbers exactly as written, key order and duplicate keys kept.
// A JSON parser decodes "\/" as "/" and a \uXXXX escape as its character,
// so a secret matches
// the canonical spelling however the document escaped it. It reports false,
// with no output, when b isn't exactly one valid JSON document.
//
// It walks the token stream rather than decoding into a map, so a duplicate
// key's earlier value (which a map would drop, and a different parser might
// keep) is still checked. It is pure and allocates in proportion to b;
// callers pass bodies that are already size-capped.
func CanonicalJSON(b []byte) ([]byte, bool) {
	if !json.Valid(b) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out bytes.Buffer
	out.Grow(len(b))
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	type frame struct {
		object bool
		n      int // tokens written in this container
	}
	var stack []frame
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			stack = stack[:len(stack)-1]
			out.WriteString(d.String())
			continue
		}
		if len(stack) > 0 {
			f := &stack[len(stack)-1]
			switch {
			case f.object && f.n%2 == 1:
				out.WriteByte(':')
			case f.n > 0:
				out.WriteByte(',')
			}
			f.n++
		}
		switch v := tok.(type) {
		case json.Delim:
			out.WriteString(v.String())
			stack = append(stack, frame{object: v == '{'})
		case string:
			if enc.Encode(v) != nil {
				return nil, false
			}
			out.Truncate(out.Len() - 1) // Encode's trailing newline
		case json.Number:
			out.WriteString(string(v))
		case bool:
			out.WriteString(strconv.FormatBool(v))
		case nil:
			out.WriteString("null")
		default:
			return nil, false
		}
	}
	return out.Bytes(), true
}

// JSON returns b unchanged, unless b is a JSON document whose canonical
// spelling (CanonicalJSON) holds something String masks: a configured secret
// or a known token format hidden behind JSON escapes ("tok\/en", \uXXXX).
// Then it returns the canonical spelling, so String masks it before the text
// reaches the model, which (like any JSON-parsing client) would decode the
// escapes. A body without a backslash is returned at once: with no escapes,
// its strings already read as they decode.
//
// A body that is not one valid JSON document (cut at the response cap, or
// with trailing junk) gets canonicalStrings instead: each complete string
// literal is re-spelled on its own.
func (r *Redactor) JSON(b []byte) []byte {
	if !bytes.ContainsRune(b, '\\') {
		return b
	}
	c, ok := CanonicalJSON(b)
	if !ok {
		c = canonicalStrings(b)
	}
	if s := string(c); r.String(s) == s {
		return b
	}
	return c
}

// canonicalStrings is the lenient fallback of CanonicalJSON for bytes that
// are not one valid JSON document: it re-spells each complete "..." string
// literal that holds an escape and decodes as a JSON string, with Go's
// escapes and no HTML escaping, and leaves every other byte as it is. A
// literal cut off at the end of b, or with an invalid escape, is left
// alone. It is linear in len(b) and returns b itself when nothing changed.
func canonicalStrings(b []byte) []byte {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	last := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		j, escaped := i+1, false
		for ; j < len(b) && b[j] != '"'; j++ {
			if b[j] == '\\' {
				escaped = true
				j++
			}
		}
		if j >= len(b) {
			break // an unterminated literal
		}
		var s string
		if escaped && json.Unmarshal(b[i:j+1], &s) == nil {
			out.Write(b[last:i])
			if enc.Encode(s) != nil {
				return b
			}
			out.Truncate(out.Len() - 1) // Encode's trailing newline
			last = j + 1
		}
		i = j
	}
	if last == 0 {
		return b
	}
	out.Write(b[last:])
	return out.Bytes()
}
