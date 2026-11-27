package config

import (
	"errors"
	"strconv"
	"time"

	"github.com/swayam5342/plugit/internal/policy"
)

// CurrentVersion is the plugit.yaml schema version this build reads and
// writes. The schema restarted at 1 for the protocol-core rebuild (clean
// break, no pre-1.0 migration; SPEC §8.1.3).
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
	AuthBasic  AuthType = "basic"
	AuthOAuth2 AuthType = "oauth2"
)

// OAuth flows (auth.flow).
const (
	FlowClientCredentials = "client_credentials"
	FlowDeviceCode        = "device_code"
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
	Protocol string             `yaml:"protocol"`
	Source   Source             `yaml:"source"`
	Endpoint string             `yaml:"endpoint"`
	Prefix   string             `yaml:"prefix,omitempty"`
	Fixed    []Fixed            `yaml:"fixed,omitempty"`
	Network  Network            `yaml:"network"`
	Auth     Auth               `yaml:"auth"`
	Policy   Policy             `yaml:"policy"`
	Log      Log                `yaml:"log"`
	Lint     Lint               `yaml:"lint,omitempty"`
	GraphQL  *GraphQLConfig     `yaml:"graphql,omitempty"`
	Toolsets map[string]Toolset `yaml:"toolsets"`
}

// GraphQLConfig is the graphql: section (SPEC §3.6). Zero fields mean the
// defaults.
type GraphQLConfig struct {
	SelectionDepth int `yaml:"selection_depth,omitempty"`
	MaxFields      int `yaml:"max_fields,omitempty"`
}

// GraphQL selection limits (SPEC §3.5 P7, §3.6).
const (
	DefaultSelectionDepth = 2
	MaxSelectionDepth     = 12
	DefaultMaxFields      = 100
	MaxMaxFields          = 1000
)

// GraphQLSettings is the effective graphql: section: the configured values,
// with the defaults for an absent section or an unset (zero) field. The
// section itself stays nil when absent, so REST configs never grow one.
func (c *API) GraphQLSettings() GraphQLConfig {
	g := GraphQLConfig{SelectionDepth: DefaultSelectionDepth, MaxFields: DefaultMaxFields}
	if c.GraphQL != nil {
		if c.GraphQL.SelectionDepth != 0 {
			g.SelectionDepth = c.GraphQL.SelectionDepth
		}
		if c.GraphQL.MaxFields != 0 {
			g.MaxFields = c.GraphQL.MaxFields
		}
	}
	return g
}

// Source records where the spec came from.
type Source struct {
	URL    string `yaml:"url"`
	Format string `yaml:"format,omitempty"`
	SHA256 string `yaml:"sha256,omitempty"`
}

// Fixed is a parameter sent on every request and hidden from the model.
type Fixed struct {
	Name  string `yaml:"name"`
	In    string `yaml:"in,omitempty"`
	Value string `yaml:"value"`
}

// Network holds per-API network limits.
type Network struct {
	AllowPrivate     bool          `yaml:"allow_private"`
	AllowInsecure    bool          `yaml:"allow_insecure,omitempty"`
	Timeout          time.Duration `yaml:"timeout,omitempty"`
	MaxResponseBytes int64         `yaml:"max_response_bytes,omitempty"`
}

// Auth describes the API credential.
type Auth struct {
	Type   AuthType `yaml:"type,omitempty"`
	Header string   `yaml:"header,omitempty"`
	Param  string   `yaml:"param,omitempty"`
	Secret string   `yaml:"secret,omitempty"`

	Flow      string   `yaml:"flow,omitempty"`
	TokenURL  string   `yaml:"token_url,omitempty"`
	DeviceURL string   `yaml:"device_url,omitempty"`
	ClientID  string   `yaml:"client_id,omitempty"`
	Scopes    []string `yaml:"scopes,omitempty"`
}

// Policy holds the API-wide call policy.
type Policy struct {
	Mode      string `yaml:"mode,omitempty"`
	RateLimit string `yaml:"rate_limit,omitempty"`
}

// Log holds call-log settings (the call log itself arrives in M4).
type Log struct {
	Args            LogArgs `yaml:"args,omitempty"`
	ResponsePreview bool    `yaml:"response_preview"`
	Retention       string  `yaml:"retention,omitempty"`
}

// Toolset is a named selection of tools with per-tool overrides.
type Toolset struct {
	Tools       map[string]ToolOverride `yaml:"tools,omitempty"`
	IncludeTags []string                `yaml:"include_tags,omitempty"`
	Safety      []string                `yaml:"safety,omitempty"`
	Methods     []string                `yaml:"methods,omitempty"`
}

// ToolOverride customizes one tool. Empty fields keep the generated value.
type ToolOverride struct {
	Name           string   `yaml:"name,omitempty"`
	Description    string   `yaml:"description,omitempty"`
	Policy         string   `yaml:"policy,omitempty"`
	RateLimit      string   `yaml:"rate_limit,omitempty"`
	ResponseFields []string `yaml:"response_fields,omitempty"`
	LintIgnore     []string `yaml:"lint_ignore,omitempty"`
}

// IsZero reports whether the override changes nothing.
func (o ToolOverride) IsZero() bool {
	return o.Name == "" && o.Description == "" && o.Policy == "" && o.RateLimit == "" && len(o.ResponseFields) == 0 && len(o.LintIgnore) == 0
}

// Lint holds lint settings (version 6).
type Lint struct {
	Ignore []string `yaml:"ignore,omitempty"`
}

// IsZero lets an empty lint block be left out of the file.
func (l Lint) IsZero() bool { return len(l.Ignore) == 0 }

// PolicyMode is the validated policy mode.
func (c *API) PolicyMode() policy.Mode { return policy.Mode(c.Policy.Mode) }

// DefaultRetention is how long the call log keeps an API's calls when
// log.retention is unset (SPEC §3.6).
const DefaultRetention = 30 * 24 * time.Hour

// RetentionPeriod is log.retention as a duration ("30d" or a Go duration).
// Validate has checked the syntax; anything unparsable falls back to the
// default.
func (c *API) RetentionPeriod() time.Duration {
	s := c.Log.Retention
	if m := retentionRe.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return DefaultRetention
}
