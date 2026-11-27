package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/swayam5342/plugit/internal/formats"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
)

// knownAuthTypes is every auth.type this plugit understands, under any
// protocol.
var knownAuthTypes = []AuthType{AuthNone, AuthBearer, AuthHeader, AuthQuery, AuthBasic, AuthOAuth2}

// checkAuthType says whether auth.type t may be used with proto: a known
// type the protocol's P3 row forbids, or a type no protocol knows (a newer
// plugit's, SPEC §8.1.2 rule 4).
func checkAuthType(proto model.Protocol, t string) error {
	if model.AuthTypeAllowed(proto, t) {
		return nil
	}
	if slices.Contains(knownAuthTypes, AuthType(t)) {
		return fmt.Errorf("auth.type %q isn't available for protocol %q", t, proto)
	}
	return fmt.Errorf("auth.type %q: needs a newer plugit (this one knows none, bearer, header, query, basic and oauth2)",
		naming.CleanText(t, 40))
}

var (
	prefixRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{0,32}$`)
	headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,256}$")
	sha256Re     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	retentionRe  = regexp.MustCompile(`^([0-9]{1,5})d$`)
	lintRuleRe   = regexp.MustCompile(`^L[0-9]{3}$`)
)

var toolMethods = map[string]bool{
	"GET": true, "HEAD": true, "OPTIONS": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// Validate checks a config with defaults applied. Errors name the field and
// never echo secret values.
func (c *API) Validate() error {
	var errs []error
	bad := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: %s: %s", ErrInvalidConfig, field, fmt.Sprintf(format, args...)))
	}
	wrap := func(field string, err error) {
		errs = append(errs, fmt.Errorf("%w: %s: %w", ErrInvalidConfig, field, err))
	}

	if c.Version == 0 {
		bad("version", "missing (this plugit writes version %d)", CurrentVersion)
	}
	proto := model.Protocol(c.Protocol)
	if !model.KnownProtocol(proto) {
		bad("protocol", "%q is not a protocol this plugit serves (needs a newer plugit)", c.Protocol)
		proto = model.REST // validate the rest of the file against REST rules
	}
	if err := ValidateAPIName(c.Name); err != nil {
		wrap("name", err)
	}
	if strings.TrimSpace(c.Source.URL) == "" {
		bad("source.url", "missing")
	}
	if c.Source.SHA256 != "" && !sha256Re.MatchString(c.Source.SHA256) {
		bad("source.sha256", "must be 64 lowercase hex characters")
	}
	if c.Source.Format != "" {
		if names := formats.NamesFor(proto); !slices.Contains(names, c.Source.Format) {
			bad("source.format", "%q is not a %s format (use %s)",
				naming.CleanText(c.Source.Format, 40), proto, strings.Join(names, " or "))
		}
	}
	if err := validateEndpoint(c.Endpoint); err != nil {
		bad("endpoint", "%s", err)
	}
	if !prefixRe.MatchString(c.Prefix) {
		bad("prefix", "use at most 32 letters, digits, '_' or '-'")
	}

	seen := map[string]bool{}
	for i, f := range c.Fixed {
		field := fmt.Sprintf("fixed[%d]", i)
		switch {
		case f.Name == "" || strings.ContainsFunc(f.Name, isSpaceOrControl):
			bad(field+".name", "must be a non-empty name without spaces or control characters")
		case seen[strings.ToLower(f.Name)]:
			bad(field+".name", "%q is listed twice", f.Name)
		case f.In == "header" && (!headerNameRe.MatchString(f.Name) || schema.IsForbiddenHeader(f.Name)):
			bad(field+".name", "%q can't be a fixed header (auth and hop-by-hop headers are set by plugit)", f.Name)
		}
		seen[strings.ToLower(f.Name)] = true
		if f.In != "" && !model.FixedInAllowed(proto, f.In) {
			bad(field+".in", "%q is not a fixed-param location for %s (or leave it out to use the spec's location)", f.In, proto)
		}
		if _, err := secrets.ParseRef(f.Value); err != nil {
			wrap(field+".value", err)
		}
	}

	if c.Network.Timeout <= 0 || c.Network.Timeout > MaxTimeout {
		bad("network.timeout", "must be between 1ms and %v", MaxTimeout)
	}
	if c.Network.MaxResponseBytes <= 0 || c.Network.MaxResponseBytes > MaxMaxResponseBytes {
		bad("network.max_response_bytes", "must be between 1 and %d", MaxMaxResponseBytes)
	}

	c.validateSections(proto, bad)
	c.validateAuth(proto, bad, wrap)
	if c.hasCredential() {
		if err := CheckCredentialTransport(c.Endpoint, c.Network.AllowInsecure); err != nil {
			bad("endpoint", "%s; to send it over http anyway, set network.allow_insecure: true", err)
		}
	}

	if _, err := policy.ParseMode(c.Policy.Mode); err != nil {
		wrap("policy.mode", err)
	}
	if c.Policy.RateLimit != "" {
		if _, err := policy.ParseRate(c.Policy.RateLimit); err != nil {
			bad("policy.rate_limit", "%q: %v", naming.CleanText(c.Policy.RateLimit, 40), err)
		}
	}

	switch c.Log.Args {
	case LogArgsNone, LogArgsRedacted, LogArgsFull:
	default:
		bad("log.args", "%q: use none, redacted or full", naming.CleanText(string(c.Log.Args), 40))
	}
	if c.Log.Retention != "" && !validRetention(c.Log.Retention) {
		bad("log.retention", "%q: use days (30d) or a duration (72h)", naming.CleanText(c.Log.Retention, 40))
	}
	if msg := checkLintRules(c.Lint.Ignore); msg != "" {
		bad("lint.ignore", "%s", msg)
	}
	for _, r := range c.Lint.Ignore {
		if alwaysErrorRules[r] {
			bad("lint.ignore", "%s is always an error and can't be ignored for the whole API", r)
		}
	}

	if len(c.Toolsets) == 0 {
		bad("toolsets", "define at least one toolset (usually \"default\")")
	}
	for name, ts := range c.Toolsets {
		c.validateToolset(name, ts, bad, wrap)
	}
	return errors.Join(errs...)
}

// validateSections allows only the protocol config section matching proto
// (SPEC §3.6) and range-checks it.
func (c *API) validateSections(proto model.Protocol, bad func(string, string, ...any)) {
	if c.GraphQL == nil {
		return
	}
	if model.ConfigSection(proto) != "graphql" {
		bad("graphql", "only allowed with protocol: graphql")
		return
	}
	if d := c.GraphQL.SelectionDepth; d < 0 || d > MaxSelectionDepth {
		bad("graphql.selection_depth", "must be between 1 and %d (default %d)", MaxSelectionDepth, DefaultSelectionDepth)
	}
	if n := c.GraphQL.MaxFields; n < 0 || n > MaxMaxFields {
		bad("graphql.max_fields", "must be between 1 and %d (default %d)", MaxMaxFields, DefaultMaxFields)
	}
}

func (c *API) validateAuth(proto model.Protocol, bad func(string, string, ...any), wrap func(string, error)) {
	a := c.Auth
	if err := checkAuthType(proto, string(a.Type)); err != nil {
		bad("auth.type", "%s", strings.TrimPrefix(err.Error(), "auth.type "))
		return
	}
	needSecret := true
	if a.Type != AuthOAuth2 && (a.Flow != "" || a.TokenURL != "" || a.DeviceURL != "" || a.ClientID != "" || len(a.Scopes) > 0) {
		bad("auth", "flow, token_url, device_url, client_id and scopes are only used with type oauth2")
	}
	switch a.Type {
	case AuthNone:
		needSecret = false
		if a.Secret != "" || a.Header != "" || a.Param != "" {
			bad("auth", "type none takes no secret, header or param")
		}
	case AuthBearer, AuthBasic:
		if a.Header != "" {
			bad("auth.header", "only used with type header")
		}
		if a.Param != "" {
			bad("auth.param", "only used with type query")
		}
	case AuthHeader:
		if !headerNameRe.MatchString(a.Header) ||
			(schema.IsForbiddenHeader(a.Header) && !strings.EqualFold(a.Header, "Authorization")) {
			bad("auth.header", "must be a valid header name other than an auth or hop-by-hop header like Host or Cookie")
		}
		if a.Param != "" {
			bad("auth.param", "only used with type query")
		}
	case AuthQuery:
		if a.Param == "" || strings.ContainsFunc(a.Param, isSpaceOrControl) {
			bad("auth.param", "type query needs the query parameter name")
		}
		if a.Header != "" {
			bad("auth.header", "only used with type header")
		}
	case AuthOAuth2:
		needSecret = c.validateOAuth(bad, wrap)
	}
	if !needSecret {
		return
	}
	if a.Secret == "" {
		bad("auth.secret", "type %s needs a secret (env:VAR or keyring:service/key)", a.Type)
		return
	}
	if _, err := secrets.ParseSecretRef(a.Secret); err != nil {
		wrap("auth.secret", err)
	}
}

func (c *API) validateToolset(name string, ts Toolset, bad func(string, string, ...any), wrap func(string, error)) {
	field := "toolsets." + naming.CleanText(name, 64)
	proto := model.Protocol(c.Protocol)
	if !model.KnownProtocol(proto) {
		proto = model.REST
	}
	if !apiNameRe.MatchString(name) {
		bad("toolsets", "toolset name %q: use lowercase letters, digits, '-' or '_'", naming.CleanText(name, 64))
	}
	for key, o := range ts.Tools {
		k := model.OpKey(key)
		kf := field + ".tools[" + naming.CleanText(key, 120) + "]"
		if !model.KeyValid(proto, k) || (proto == model.REST && key != k.Method()+" "+k.Path()) {
			bad(kf, "not a valid %s tool key (REST keys look like \"GET /pets/{id}\")", proto)
		}
		if o.Name != "" && !naming.ValidToolName(o.Name) {
			bad(kf+".name", "use 1-64 letters, digits, '_' or '-'")
		}
		if _, err := policy.ParseOverride(o.Policy); err != nil {
			wrap(kf+".policy", err)
		}
		if o.RateLimit != "" {
			if _, err := policy.ParseRate(o.RateLimit); err != nil {
				bad(kf+".rate_limit", "%q: %v", naming.CleanText(o.RateLimit, 40), err)
			}
		}
		if _, err := jsonpick.Parse(o.ResponseFields); err != nil {
			wrap(kf+".response_fields", err)
		}
		if msg := checkLintRules(o.LintIgnore); msg != "" {
			bad(kf+".lint_ignore", "%s", msg)
		}
	}
	if len(ts.Methods) > 0 && !model.MethodsFilterAllowed(proto) {
		bad(field+".methods", "the methods: filter is REST-only; use safety: for %s", proto)
	}
	for _, m := range ts.Methods {
		if !toolMethods[strings.ToUpper(m)] {
			bad(field+".methods", "%q is not an HTTP method plugit serves", naming.CleanText(m, 20))
		}
	}
	for _, s := range ts.Safety {
		if _, ok := model.ParseSafety(s); !ok {
			bad(field+".safety", "%q: use read-only, write or destructive", naming.CleanText(s, 20))
		}
	}
	for _, tag := range ts.IncludeTags {
		if strings.TrimSpace(tag) == "" {
			bad(field+".include_tags", "empty tag")
		}
	}
}

// validateOAuth checks type oauth2 and reports whether a client secret is
// required (client credentials) or optional (device flow).
func (c *API) validateOAuth(bad func(string, string, ...any), wrap func(string, error)) bool {
	a := c.Auth
	if a.Header != "" || a.Param != "" {
		bad("auth", "type oauth2 takes no header or param: the token is sent as Authorization: Bearer")
	}
	if err := validateOAuthURL(a.TokenURL); err != nil {
		wrap("auth.token_url", err)
	}
	if a.ClientID == "" || len(a.ClientID) > 256 || strings.ContainsFunc(a.ClientID, isSpaceOrControl) {
		bad("auth.client_id", "type oauth2 needs the client_id (no spaces, at most 256 characters)")
	}
	for _, s := range a.Scopes {
		if s == "" || strings.ContainsFunc(s, isSpaceOrControl) {
			bad("auth.scopes", "scopes are single words without spaces")
			break
		}
	}
	switch a.Flow {
	case FlowClientCredentials:
		if a.DeviceURL != "" {
			bad("auth.device_url", "only used with flow device_code")
		}
		return true
	case FlowDeviceCode:
		if err := validateOAuthURL(a.DeviceURL); err != nil {
			wrap("auth.device_url", err)
		}
		if a.Secret != "" {
			if _, err := secrets.ParseSecretRef(a.Secret); err != nil {
				wrap("auth.secret", err)
			}
		}
		return false
	default:
		bad("auth.flow", "%q: use client_credentials or device_code", naming.CleanText(a.Flow, 40))
		return false
	}
}

// validateOAuthURL accepts an absolute https URL (http only to a loopback
// host, for local authorization servers) with no userinfo, query or
// fragment.
func validateOAuthURL(raw string) error {
	if err := validateEndpoint(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.Scheme == "http" {
		host := u.Hostname()
		if ip := net.ParseIP(host); !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("must be https (plain http is only allowed to localhost)")
		}
	}
	return nil
}

// validateEndpoint accepts an absolute http(s) URL with a host and no
// userinfo, query or fragment. The value is never echoed: it may carry
// credentials.
func validateEndpoint(raw string) error {
	if raw == "" {
		return errors.New("missing")
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("must be an absolute http(s) URL")
	case u.Host == "":
		return errors.New("has no host")
	case u.User != nil:
		return errors.New("must not contain credentials; use auth.secret")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return errors.New("must not have a query or fragment; use fixed for query parameters")
	}
	return nil
}

func (c *API) hasCredential() bool {
	if c.Auth.Type != AuthNone && c.Auth.Type != "" {
		return true
	}
	for _, f := range c.Fixed {
		if ref, err := secrets.ParseRef(f.Value); err == nil && ref.IsSecret() {
			return true
		}
	}
	return false
}

// CheckCredentialTransport refuses plain http to a remote host when a
// credential is sent (SPEC T8), unless allowInsecure is on. The URL is never
// echoed.
func CheckCredentialTransport(baseURL string, allowInsecure bool) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" || allowInsecure {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("must be https when a credential is configured: plain http would send it unencrypted " +
		"(without a switch, http is only allowed to localhost)")
}

var alwaysErrorRules = map[string]bool{"L003": true, "L009": true, "L011": true}

// checkLintRules says what is wrong with a list of lint rule IDs, or "".
func checkLintRules(rules []string) string {
	seen := map[string]bool{}
	for _, r := range rules {
		switch {
		case !lintRuleRe.MatchString(r):
			return fmt.Sprintf("%q is not a lint rule: use IDs like L017", naming.CleanText(r, 20))
		case seen[r]:
			return r + " is listed twice"
		}
		seen[r] = true
	}
	return ""
}

func validRetention(s string) bool {
	if m := retentionRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n > 0
	}
	d, err := time.ParseDuration(s)
	return err == nil && d > 0
}

func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f
}
