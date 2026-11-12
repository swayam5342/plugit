package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pb33f/go-yaml"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
)

// CurrentVersion is the plugit.yaml schema version this build reads and
// writes. Changing the schema means bumping it and adding a migration.
const CurrentVersion = 1

// MaxConfigBytes caps plugit.yaml. A real config is a few kilobytes.
const MaxConfigBytes = 1 << 20

// Limits on network settings. They keep a hand-edited value from disabling
// the protections they exist for (SPEC T12).
const (
	DefaultTimeout          = 30 * time.Second
	MaxTimeout              = 10 * time.Minute
	DefaultMaxResponseBytes = 1 << 20
	MaxMaxResponseBytes     = 64 << 20
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrInvalidConfig      = errors.New("invalid config")
	ErrUnsupportedVersion = errors.New("unsupported config version")
	ErrAPINotFound        = errors.New("API not found")
)

// AuthType is how the API credential is attached.
type AuthType string

// Auth types (SPEC §3.6, §9 item 2).
const (
	AuthNone   AuthType = "none"
	AuthBearer AuthType = "bearer"
	AuthHeader AuthType = "header"
	AuthQuery  AuthType = "query"
	AuthBasic  AuthType = "basic" // the secret is "user:pass"
)

// LogArgs says how call arguments are logged.
type LogArgs string

// Log argument modes.
const (
	LogArgsNone     LogArgs = "none"
	LogArgsRedacted LogArgs = "redacted"
	LogArgsFull     LogArgs = "full"
)

// API is one API's plugit.yaml (SPEC §3.6). Secret-bearing fields hold
// references (env:, keyring:), never values.
type API struct {
	Version  int                `yaml:"version"`
	Name     string             `yaml:"name"`
	Source   Source             `yaml:"source"`
	BaseURL  string             `yaml:"base_url"`
	Prefix   string             `yaml:"prefix,omitempty"`
	Fixed    []Fixed            `yaml:"fixed,omitempty"`
	Network  Network            `yaml:"network"`
	Auth     Auth               `yaml:"auth"`
	Policy   Policy             `yaml:"policy"`
	Log      Log                `yaml:"log"`
	Toolsets map[string]Toolset `yaml:"toolsets"`
}

// Source records where the spec came from.
type Source struct {
	URL    string `yaml:"url"` // URL or file path
	SHA256 string `yaml:"sha256,omitempty"`
}

// Fixed is a parameter sent on every request and hidden from the model.
type Fixed struct {
	Name  string `yaml:"name"`
	In    string `yaml:"in,omitempty"` // query | header; the spec-declared location wins
	Value string `yaml:"value"`        // literal:V | env:VAR | keyring:service/key
}

// Network holds per-API network limits.
type Network struct {
	AllowPrivate     bool          `yaml:"allow_private"`
	Timeout          time.Duration `yaml:"timeout,omitempty"`
	MaxResponseBytes int64         `yaml:"max_response_bytes,omitempty"`
}

// Auth describes the API credential.
type Auth struct {
	Type   AuthType `yaml:"type,omitempty"`
	Header string   `yaml:"header,omitempty"` // type header
	Param  string   `yaml:"param,omitempty"`  // type query
	Secret string   `yaml:"secret,omitempty"` // env:VAR | keyring:service/key
}

// Policy holds the API-wide call policy.
type Policy struct {
	Mode      string `yaml:"mode,omitempty"`
	RateLimit string `yaml:"rate_limit,omitempty"` // N/sec | N/min | N/hour (enforced from M5)
}

// Log holds call-log settings (the call log itself arrives in M4).
type Log struct {
	Args            LogArgs `yaml:"args,omitempty"`
	ResponsePreview bool    `yaml:"response_preview"`
	Retention       string  `yaml:"retention,omitempty"` // e.g. 30d or 72h
}

// Toolset is a named selection of tools with per-tool overrides.
type Toolset struct {
	Tools       map[string]ToolOverride `yaml:"tools,omitempty"` // key: "METHOD /path"
	IncludeTags []string                `yaml:"include_tags,omitempty"`
	Methods     []string                `yaml:"methods,omitempty"`
}

// ToolOverride customizes one tool. Empty fields keep the generated value.
type ToolOverride struct {
	Name        string `yaml:"name,omitempty"`
	Description string `yaml:"description,omitempty"`
	Policy      string `yaml:"policy,omitempty"` // allow | confirm | deny
}

// PolicyMode is the validated policy mode.
func (c *API) PolicyMode() policy.Mode { return policy.Mode(c.Policy.Mode) }

// LoadAPI reads and validates apis/<name>/plugit.yaml.
func LoadAPI(p Paths, name string) (*API, error) {
	path, err := p.APIConfig(name)
	if err != nil {
		return nil, err
	}
	data, err := readCapped(path, MaxConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %q (no %s); add it with `plugit add`", ErrAPINotFound, name, path)
	}
	if err != nil {
		return nil, err
	}
	c, err := ParseAPI(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Name != name {
		return nil, fmt.Errorf("%s: %w: name is %q but the directory is %q", path, ErrInvalidConfig, c.Name, name)
	}
	return c, nil
}

// readCapped reads a regular file of at most limit bytes.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- path is built from a validated API name
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("reading %s: not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("reading %s: file too large (over %d bytes)", path, limit)
	}
	return data, nil
}

// ParseAPI decodes and validates a plugit.yaml document and fills in
// defaults. Unknown and duplicate keys are errors, so a typo can't silently
// disable a safety setting. All problems are reported together.
func ParseAPI(data []byte) (*API, error) {
	// First pass: decode the whole document generically. That expands every
	// alias, so the library's alias limit applies to all of it (a typed
	// decode can stop early on a type error). It also lets the version be
	// checked first: a newer schema may use keys this build doesn't know, and
	// "upgrade plugit" is the useful message for that.
	var raw any
	if err := yaml.Load(data, &raw, yamlOpts()...); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfig, yamlError(err))
	}
	doc, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: expected a mapping of settings at the top level", ErrInvalidConfig)
	}
	if v, ok := doc["version"].(int); ok && v > CurrentVersion {
		return nil, fmt.Errorf("%w: version %d is newer than this plugit supports (%d); upgrade plugit",
			ErrUnsupportedVersion, v, CurrentVersion)
	}

	var c API
	if err := yaml.Load(data, &c, append(yamlOpts(), yaml.WithKnownFields())...); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfig, yamlError(err))
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func yamlOpts() []yaml.Option {
	// The library's default limits cap nesting depth and alias expansion, and
	// a stream with more than one document is an error.
	return []yaml.Option{yaml.WithUniqueKeys()}
}

var goTypeRe = regexp.MustCompile(` (in|into) (type )?[\w.*\[\]{}"\\: ]*config\.\w+`)

// yamlError drops Go type names from decoder messages.
func yamlError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: construct errors: ")
	msg = strings.ReplaceAll(msg, "into time.Duration", "into a duration (write it like 30s)")
	msg = goTypeRe.ReplaceAllString(msg, "")
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	return msg
}

func (c *API) applyDefaults() {
	if c.Network.Timeout == 0 {
		c.Network.Timeout = DefaultTimeout
	}
	if c.Network.MaxResponseBytes == 0 {
		c.Network.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if c.Auth.Type == "" {
		c.Auth.Type = AuthNone
	}
	if c.Policy.Mode == "" {
		c.Policy.Mode = string(policy.ReadOnly)
	}
	if c.Log.Args == "" {
		c.Log.Args = LogArgsRedacted
	}
}

var (
	prefixRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{0,32}$`)
	toolNameRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,256}$")
	sha256Re     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	rateLimitRe  = regexp.MustCompile(`^([0-9]{1,9})/(sec|min|hour)$`)
	retentionRe  = regexp.MustCompile(`^([0-9]{1,5})d$`)
)

// toolMethods are the methods an operation key may use (TRACE is never a
// tool, SPEC §3.5 rule 5).
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
	if err := ValidateAPIName(c.Name); err != nil {
		wrap("name", err)
	}
	if strings.TrimSpace(c.Source.URL) == "" {
		bad("source.url", "missing")
	}
	if c.Source.SHA256 != "" && !sha256Re.MatchString(c.Source.SHA256) {
		bad("source.sha256", "must be 64 lowercase hex characters")
	}
	if err := validateBaseURL(c.BaseURL); err != nil {
		bad("base_url", "%s", err)
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
		if f.In != "" && f.In != "query" && f.In != "header" {
			bad(field+".in", "%q: use query or header (or leave it out to use the spec's location)", f.In)
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

	c.validateAuth(bad, wrap)

	if _, err := policy.ParseMode(c.Policy.Mode); err != nil {
		wrap("policy.mode", err)
	}
	if c.Policy.RateLimit != "" {
		if m := rateLimitRe.FindStringSubmatch(c.Policy.RateLimit); m == nil || strings.Trim(m[1], "0") == "" {
			bad("policy.rate_limit", "%q: use N/sec, N/min or N/hour with N > 0", naming.CleanText(c.Policy.RateLimit, 40))
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

	if len(c.Toolsets) == 0 {
		bad("toolsets", "define at least one toolset (usually \"default\")")
	}
	for name, ts := range c.Toolsets {
		c.validateToolset(name, ts, bad, wrap)
	}
	return errors.Join(errs...)
}

func (c *API) validateAuth(bad func(string, string, ...any), wrap func(string, error)) {
	a := c.Auth
	needSecret := true
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
		// Authorization is the one forbidden header a credential may use.
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
	default:
		needSecret = false
		bad("auth.type", "%q: use none, bearer, header, query or basic", naming.CleanText(string(a.Type), 40))
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
	if !apiNameRe.MatchString(name) {
		bad("toolsets", "toolset name %q: use lowercase letters, digits, '-' or '_'", naming.CleanText(name, 64))
	}
	for key, o := range ts.Tools {
		k := model.OpKey(key)
		kf := field + ".tools[" + naming.CleanText(key, 120) + "]"
		if !toolMethods[k.Method()] || !strings.HasPrefix(k.Path(), "/") || key != k.Method()+" "+k.Path() {
			bad(kf, "keys look like \"GET /pets/{id}\"")
		}
		if o.Name != "" && !toolNameRe.MatchString(o.Name) {
			bad(kf+".name", "use 1-64 letters, digits, '_' or '-'")
		}
		if _, err := policy.ParseOverride(o.Policy); err != nil {
			wrap(kf+".policy", err)
		}
	}
	for _, m := range ts.Methods {
		if !toolMethods[strings.ToUpper(m)] {
			bad(field+".methods", "%q is not an HTTP method plugit serves", naming.CleanText(m, 20))
		}
	}
	for _, tag := range ts.IncludeTags {
		if strings.TrimSpace(tag) == "" {
			bad(field+".include_tags", "empty tag")
		}
	}
}

// validateBaseURL accepts an absolute http(s) URL with a host and no
// userinfo, query or fragment. The value is never echoed: it may carry
// credentials.
func validateBaseURL(raw string) error {
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

const apiHeader = `# PlugIt API config. Edit by hand, or with ` + "`plugit edit <name>`" + `.
# Secrets are references (env:VAR or keyring:service/key), never values.
`

// MarshalAPI renders c as plugit.yaml. The result is validated by parsing
// it back, so a file plugit writes always loads.
func MarshalAPI(c *API) ([]byte, error) {
	out, err := yaml.Dump(c, yaml.WithIndent(2))
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	out = append([]byte(apiHeader), out...)
	if _, err := ParseAPI(out); err != nil {
		return nil, fmt.Errorf("refusing to write an invalid config: %w", err)
	}
	return out, nil
}

// SaveAPI writes apis/<name>/plugit.yaml atomically and owner-only,
// creating the API directory if needed.
func SaveAPI(p Paths, c *API) error {
	data, err := MarshalAPI(c)
	if err != nil {
		return err
	}
	dir, err := p.APIDir(c.Name)
	if err != nil {
		return err
	}
	if err := EnsureDir(dir); err != nil {
		return err
	}
	path, err := p.APIConfig(c.Name)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data)
}
