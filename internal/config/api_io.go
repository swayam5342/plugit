package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/pb33f/go-yaml"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

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
	var raw any
	if err := yaml.Load(data, &raw, yamlOpts()...); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfig, yamlError(err))
	}
	doc, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: expected a mapping of settings at the top level", ErrInvalidConfig)
	}
	if v, ok := doc["version"].(int); ok && v > CurrentVersion {
		// The config schema restarted at 1 for the protocol-core rebuild
		// (SPEC §8.1.3). A higher version is a pre-rebuild file; there is no
		// migration, so the user re-adds the API.
		return nil, fmt.Errorf("%w: config from a pre-1.0 plugit (version %d): re-run `plugit add`",
			ErrUnsupportedVersion, v)
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
	if c.Protocol == "" {
		c.Protocol = string(model.REST)
	}
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
