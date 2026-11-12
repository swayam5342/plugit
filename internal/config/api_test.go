package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/secrets"
)

const fullConfig = `version: 1
name: github
source:
  url: https://example.test/api.github.com.json
  sha256: 3f9a000000000000000000000000000000000000000000000000000000000000
base_url: https://api.github.com
prefix: gh
fixed:
  - name: X-GitHub-Api-Version
    in: header
    value: literal:2022-11-28
  - name: tenant
    value: env:GH_TENANT
network:
  allow_private: false
  timeout: 10s
  max_response_bytes: 2048
auth:
  type: bearer
  secret: keyring:plugit/github
policy:
  mode: confirm-writes
  rate_limit: 60/min
log:
  args: none
  response_preview: false
  retention: 30d
toolsets:
  default:
    tools:
      "GET /repos/{owner}/{repo}/issues":
        name: list_issues
        description: List issues in a repository.
      "POST /repos/{owner}/{repo}/issues":
        policy: confirm
      "GET /user": {}
  readonly:
    include_tags: [issues, pulls]
    methods: [GET]
`

func TestParseAPIFull(t *testing.T) {
	c, err := ParseAPI([]byte(fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "github" || c.BaseURL != "https://api.github.com" || c.Prefix != "gh" {
		t.Errorf("basic fields: %+v", c)
	}
	if c.Network.Timeout != 10*time.Second || c.Network.MaxResponseBytes != 2048 {
		t.Errorf("network: %+v", c.Network)
	}
	if c.Auth.Type != AuthBearer || c.Auth.Secret != "keyring:plugit/github" {
		t.Errorf("auth: %+v", c.Auth)
	}
	if c.PolicyMode() != policy.ConfirmWrites {
		t.Errorf("mode = %q", c.PolicyMode())
	}
	if len(c.Fixed) != 2 || c.Fixed[0].In != "header" {
		t.Errorf("fixed: %+v", c.Fixed)
	}
	ts := c.Toolsets["default"]
	if len(ts.Tools) != 3 || ts.Tools["GET /repos/{owner}/{repo}/issues"].Name != "list_issues" {
		t.Errorf("default toolset: %+v", ts)
	}
	if ts.Tools["POST /repos/{owner}/{repo}/issues"].Policy != "confirm" {
		t.Errorf("override policy lost")
	}
	if ro := c.Toolsets["readonly"]; len(ro.IncludeTags) != 2 || ro.Methods[0] != "GET" {
		t.Errorf("readonly toolset: %+v", ro)
	}
}

const minimalConfig = `version: 1
name: pets
source: {url: https://example.test/openapi.json}
base_url: https://pets.example.test/v1
toolsets:
  default:
    tools:
      "GET /pets": {}
`

// Omitted settings fall back to the secure defaults (SPEC §5.4).
func TestParseAPIDefaults(t *testing.T) {
	c, err := ParseAPI([]byte(minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.PolicyMode() != policy.ReadOnly {
		t.Errorf("default mode = %q, want read-only", c.PolicyMode())
	}
	if c.Network.AllowPrivate {
		t.Error("allow_private defaults to true")
	}
	if c.Network.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v", c.Network.Timeout)
	}
	if c.Network.MaxResponseBytes != 1<<20 {
		t.Errorf("default max_response_bytes = %d", c.Network.MaxResponseBytes)
	}
	if c.Auth.Type != AuthNone {
		t.Errorf("default auth type = %q", c.Auth.Type)
	}
	if c.Log.Args != LogArgsRedacted {
		t.Errorf("default log.args = %q", c.Log.Args)
	}
}

func TestParseAPIRejects(t *testing.T) {
	const secret = "test-token-123"
	base := minimalConfig
	cases := map[string]struct {
		yaml string
		want string // substring of the error
		is   error
	}{
		"unknown top-level key": {base + "polcy: {mode: full}\n", "polcy", ErrInvalidConfig},
		"unknown nested key":    {strings.Replace(base, "base_url:", "network: {allow_privat: true}\nbase_url:", 1), "allow_privat", ErrInvalidConfig},
		"duplicate key":         {base + "base_url: https://other.example.test\n", "base_url", ErrInvalidConfig},
		"multiple documents":    {base + "---\nversion: 1\n", "single document", ErrInvalidConfig},
		"empty file":            {"", "no documents", ErrInvalidConfig},
		"not a mapping":         {"- 1\n", "", ErrInvalidConfig},
		"missing version":       {strings.Replace(base, "version: 1\n", "", 1), "version", ErrInvalidConfig},
		"future version":        {strings.Replace(base, "version: 1", "version: 2", 1), "newer", ErrUnsupportedVersion},
		"bad name":              {strings.Replace(base, "name: pets", "name: ../etc", 1), "name", ErrInvalidConfig},
		"missing base_url":      {strings.Replace(base, "base_url: https://pets.example.test/v1\n", "", 1), "base_url", ErrInvalidConfig},
		"relative base_url":     {strings.Replace(base, "https://pets.example.test/v1", "/v1", 1), "base_url", ErrInvalidConfig},
		"ftp base_url":          {strings.Replace(base, "https://pets", "ftp://pets", 1), "base_url", ErrInvalidConfig},
		"base_url with userinfo": {strings.Replace(base, "https://pets", "https://u:"+secret+"@pets", 1),
			"base_url", ErrInvalidConfig},
		"base_url with query":   {strings.Replace(base, "/v1", "/v1?key=x", 1), "base_url", ErrInvalidConfig},
		"missing source":        {strings.Replace(base, "source: {url: https://example.test/openapi.json}\n", "", 1), "source.url", ErrInvalidConfig},
		"bad sha256":            {strings.Replace(base, "openapi.json}", "openapi.json, sha256: xyz}", 1), "sha256", ErrInvalidConfig},
		"bad prefix":            {base + "prefix: 'has space'\n", "prefix", ErrInvalidConfig},
		"literal auth secret":   {base + "auth: {type: bearer, secret: 'literal:" + secret + "'}\n", "auth.secret", secrets.ErrLiteralSecret},
		"bare auth secret":      {base + "auth: {type: bearer, secret: " + secret + "}\n", "auth.secret", secrets.ErrNoScheme},
		"bearer without secret": {base + "auth: {type: bearer}\n", "auth.secret", ErrInvalidConfig},
		"unknown auth type":     {base + "auth: {type: oauth, secret: env:X}\n", "auth.type", ErrInvalidConfig},
		"header auth without header": {base + "auth: {type: header, secret: env:X}\n",
			"auth.header", ErrInvalidConfig},
		"header auth with bad header name": {base + "auth: {type: header, header: 'X Key', secret: env:X}\n",
			"auth.header", ErrInvalidConfig},
		"header auth on a hop-by-hop header": {base + "auth: {type: header, header: Host, secret: env:X}\n",
			"auth.header", ErrInvalidConfig},
		"query auth without param": {base + "auth: {type: query, secret: env:X}\n", "auth.param", ErrInvalidConfig},
		"none with a secret":       {base + "auth: {type: none, secret: env:X}\n", "auth", ErrInvalidConfig},
		"bearer with a header":     {base + "auth: {type: bearer, header: X-Key, secret: env:X}\n", "auth.header", ErrInvalidConfig},
		"bare fixed value":         {base + "fixed: [{name: api_key, value: " + secret + "}]\n", "fixed[0].value", secrets.ErrNoScheme},
		"fixed without name":       {base + "fixed: [{value: 'literal:v2'}]\n", "fixed[0].name", ErrInvalidConfig},
		"fixed in path":            {base + "fixed: [{name: id, in: path, value: 'literal:1'}]\n", "fixed[0].in", ErrInvalidConfig},
		"fixed on a forbidden header": {base + "fixed: [{name: Authorization, in: header, value: env:X}]\n",
			"fixed[0].name", ErrInvalidConfig},
		"duplicate fixed": {base + "fixed: [{name: v, value: 'literal:1'}, {name: V, value: 'literal:2'}]\n",
			"fixed[1].name", ErrInvalidConfig},
		"bad mode":          {base + "policy: {mode: yolo}\n", "policy.mode", policy.ErrInvalid},
		"bad rate limit":    {base + "policy: {rate_limit: lots}\n", "policy.rate_limit", ErrInvalidConfig},
		"zero rate limit":   {base + "policy: {rate_limit: 0/min}\n", "policy.rate_limit", ErrInvalidConfig},
		"negative timeout":  {base + "network: {timeout: -1s}\n", "network.timeout", ErrInvalidConfig},
		"huge timeout":      {base + "network: {timeout: 2h}\n", "network.timeout", ErrInvalidConfig},
		"numeric timeout":   {base + "network: {timeout: 30}\n", "duration (write it like 30s)", ErrInvalidConfig},
		"huge response cap": {base + "network: {max_response_bytes: 1099511627776}\n", "network.max_response_bytes", ErrInvalidConfig},
		"bad log args":      {base + "log: {args: everything}\n", "log.args", ErrInvalidConfig},
		"bad retention":     {base + "log: {retention: forever}\n", "log.retention", ErrInvalidConfig},
		"no toolsets":       {strings.Split(base, "toolsets:")[0], "toolsets", ErrInvalidConfig},
		"bad toolset name":  {strings.Replace(base, "  default:", "  'Bad Name':", 1), "toolsets", ErrInvalidConfig},
		"bad tool key":      {strings.Replace(base, `"GET /pets"`, `"FETCH pets"`, 1), "FETCH pets", ErrInvalidConfig},
		"bad override name": {strings.Replace(base, `"GET /pets": {}`, `"GET /pets": {name: 'list pets'}`, 1),
			"name", ErrInvalidConfig},
		"bad override policy": {strings.Replace(base, `"GET /pets": {}`, `"GET /pets": {policy: maybe}`, 1),
			"policy", policy.ErrInvalid},
		"bad toolset method": {base + "  other:\n    methods: [FETCH]\n", "methods", ErrInvalidConfig},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAPI([]byte(tc.yaml))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("err = %v, want errors.Is %v", err, tc.is)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error echoes the secret: %v", err)
			}
		})
	}
}

// Every problem is reported at once, so a hand-edited file can be fixed in
// one pass.
func TestParseAPIReportsAllProblems(t *testing.T) {
	_, err := ParseAPI([]byte(minimalConfig + "policy: {mode: yolo}\nlog: {args: everything}\n"))
	if err == nil || !strings.Contains(err.Error(), "policy.mode") || !strings.Contains(err.Error(), "log.args") {
		t.Errorf("err = %v", err)
	}
}

// An alias bomb in a hand-edited config can't exhaust memory, wherever it
// sits. (Placed under a real field, a typed decode alone would stop at the
// first type error and never reach the alias limit.)
func TestParseAPIAliasBomb(t *testing.T) {
	bomb := `version: 1
toolsets:
  a: {include_tags: &a ["x","x","x","x","x","x","x","x","x"]}
  b: {include_tags: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]}
  c: {include_tags: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]}
  d: {include_tags: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]}
  e: {include_tags: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]}
  f: {include_tags: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]}
  g: {include_tags: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]}
  h: {include_tags: &h [*g,*g,*g,*g,*g,*g,*g,*g,*g]}
`
	start := time.Now()
	_, err := ParseAPI([]byte(bomb))
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("err = %v, want the alias limit", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("alias bomb took %v", time.Since(start))
	}
}

func writeAPIConfig(t *testing.T, root, name, content string) Paths {
	t.Helper()
	p := NewPaths(root)
	dir, err := p.APIDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	cfg, _ := p.APIConfig(name)
	if err := WriteFileAtomic(cfg, []byte(content)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAPI(t *testing.T) {
	root := t.TempDir()
	p := writeAPIConfig(t, root, "pets", minimalConfig)
	c, err := LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "pets" {
		t.Errorf("name = %q", c.Name)
	}

	t.Run("missing", func(t *testing.T) {
		_, err := LoadAPI(p, "nope")
		if !errors.Is(err, ErrAPINotFound) {
			t.Errorf("err = %v, want ErrAPINotFound", err)
		}
	})
	t.Run("invalid name", func(t *testing.T) {
		if _, err := LoadAPI(p, "../x"); !errors.Is(err, ErrInvalidAPIName) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("name mismatch", func(t *testing.T) {
		writeAPIConfig(t, root, "other", minimalConfig)
		if _, err := LoadAPI(p, "other"); err == nil || !strings.Contains(err.Error(), "pets") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		writeAPIConfig(t, root, "big", minimalConfig+"# "+strings.Repeat("x", MaxConfigBytes)+"\n")
		if _, err := LoadAPI(p, "big"); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("error names the file", func(t *testing.T) {
		writeAPIConfig(t, root, "broken", minimalConfig+"bogus: 1\n")
		_, err := LoadAPI(p, "broken")
		if err == nil || !strings.Contains(err.Error(), filepath.Join("broken", "plugit.yaml")) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("directory instead of file", func(t *testing.T) {
		dir, _ := p.APIDir("dir")
		if err := os.MkdirAll(filepath.Join(dir, "plugit.yaml"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAPI(p, "dir"); err == nil {
			t.Error("loaded a directory")
		}
	})
}
