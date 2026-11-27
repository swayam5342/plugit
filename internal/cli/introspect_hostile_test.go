package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/invoke"
)

// withQueryDescription returns the introspection fixture with the Query
// type's description set to desc: a hostile upstream echoing what it was
// sent into the schema.
func withQueryDescription(t *testing.T, fixture []byte, desc string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture, &doc); err != nil {
		t.Fatal(err)
	}
	for _, ty := range doc["data"].(map[string]any)["__schema"].(map[string]any)["types"].([]any) {
		if tm := ty.(map[string]any); tm["name"] == "Query" {
			tm["description"] = desc
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// withRawQueryDescription is withQueryDescription with desc written into the
// JSON as is, between the quotes: desc is string-literal text, so it can
// spell characters with JSON escapes ("\/", \uXXXX) the way some
// servers do. The child decodes them like any JSON parser.
func withRawQueryDescription(t *testing.T, fixture []byte, desc string) []byte {
	t.Helper()
	const mark = "PLUGIT-RAW-DESCRIPTION-MARK"
	out := bytes.Replace(withQueryDescription(t, fixture, mark), []byte(mark), []byte(desc), 1)
	if !json.Valid(out) {
		t.Fatalf("description %q makes the fixture invalid JSON", desc)
	}
	return out
}

// jsonUEscape spells every rune of s (BMP) as a JSON \uXXXX escape.
func jsonUEscape(s string) string {
	var b strings.Builder
	for _, c := range s {
		fmt.Fprintf(&b, `\u%04x`, c)
	}
	return b.String()
}

// checkRefusedEcho fails unless err is the credential-echo refusal, names no
// secret, and (p non-nil) nothing of API name was written.
func checkRefusedEcho(t *testing.T, err error, p *config.Paths, name string, secrets ...string) {
	t.Helper()
	if !errors.Is(err, invoke.ErrSecretInResponse) || !strings.Contains(err.Error(), "refusing to save it") {
		t.Fatalf("err = %v, want the introspection result refused for carrying the credential", err)
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error repeats a secret: %v", err)
		}
	}
	if p == nil {
		return
	}
	for _, path := range apiFiles(t, *p, name) {
		if fileExists(path) {
			t.Errorf("%s was written", path)
		}
	}
}

func apiFiles(t *testing.T, p config.Paths, name string) []string {
	t.Helper()
	cfgPath, err1 := p.APIConfig(name)
	snap, err2 := p.SpecSnapshot(name)
	cache, err3 := p.Manifest(name)
	if err := errors.Join(err1, err2, err3); err != nil {
		t.Fatal(err)
	}
	return []string{cfgPath, snap, cache}
}

// T11 (hostile): an introspection result is the first credentialed response
// that becomes spec text (snapshot, manifest, tool descriptions, child parse
// errors). An upstream that echoes the credential, the bare token of a
// "Bearer <token>" header, or a secret fixed header, raw, percent-encoded or
// base64, into the schema is refused by add --introspect and run
// --introspect, the error doesn't repeat it, and nothing is written.
func TestHostileIntrospectionEchoesCredential(t *testing.T) {
	const tok = "tok/en+123=" // percent-encoding changes it
	const fixedSecret = "fixed-secret-456"
	fixture := introspectionFixture(t)
	env := map[string]string{"GH_TOKEN": "Bearer " + tok, "EXTRA": fixedSecret}
	secrets := []string{tok, url.QueryEscape(tok), base64.StdEncoding.EncodeToString([]byte(tok)), fixedSecret}
	cases := []struct {
		name, echo string
		raw        bool // echo is JSON string-literal text, escapes and all
	}{
		{"whole header value", "Bearer " + tok, false},
		{"bare token", tok, false},
		{"percent-encoded token", url.QueryEscape(tok), false},
		{"base64 token", base64.StdEncoding.EncodeToString([]byte(tok)), false},
		{"secret fixed header", fixedSecret, false},
		// The child decodes JSON escapes, so each of these becomes the
		// token in the schema text.
		{"solidus-escaped token", strings.ReplaceAll(tok, "/", `\/`), true},
		{"fully u-escaped token", jsonUEscape(tok), true},
		{"mixed-escape token", "t" + jsonUEscape("o") + `k\/en` + jsonUEscape("+") + "123" + jsonUEscape("="), true},
		{"u-escaped fixed header", jsonUEscape(fixedSecret), true},
	}
	describe := func(t *testing.T, echo string, raw bool) []byte {
		if raw {
			return withRawQueryDescription(t, fixture, "Root type. Your key: "+echo)
		}
		return withQueryDescription(t, fixture, "Root type. Your key: "+echo)
	}
	for _, tc := range cases {
		t.Run("add/"+tc.name, func(t *testing.T) {
			up := newGraphQLUpstream(t, func(u *gqlUpstream) {
				u.introspection = describe(t, tc.echo, tc.raw)
			})
			p, d, out := addEnv(t)
			d.resolver = envResolver(env)
			err := addAPI(t.Context(), addSettingsFor(up.URL+"/graphql", func(s *addSettings) {
				s.name, s.introspect, s.fromRun, s.allowPrivate = "gh", true, true, true
				s.headers = []string{"Authorization=env:GH_TOKEN", "X-Extra=env:EXTRA"}
			}), d)
			checkRefusedEcho(t, err, &p, "gh", secrets...)
			if up.hits.Load() != 1 {
				t.Errorf("upstream hits = %d, want the one introspection", up.hits.Load())
			}
			for _, s := range secrets {
				if strings.Contains(out.String(), s) || strings.Contains(d.stderr.(*bytes.Buffer).String(), s) {
					t.Errorf("output leaks a secret:\n%s\n%s", out, d.stderr)
				}
			}
		})
		t.Run("run/"+tc.name, func(t *testing.T) {
			up := newGraphQLUpstream(t, func(u *gqlUpstream) {
				u.introspection = describe(t, tc.echo, tc.raw)
			})
			stderr := &syncBuf{}
			// A transport no client connects to: should the result be
			// accepted, serve blocks until the timeout instead of refusing.
			st, _ := mcp.NewInMemoryTransports()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := runServe(ctx, settings(up.URL+"/graphql", func(s *runSettings) {
				s.introspect, s.allowPrivate = true, true
				s.headers = []string{"Authorization=env:GH_TOKEN", "X-Extra=env:EXTRA"}
			}), deps(st, stderr, env))
			checkRefusedEcho(t, err, nil, "", secrets...)
			for _, s := range secrets {
				if strings.Contains(stderr.String(), s) {
					t.Errorf("stderr leaks a secret:\n%s", stderr)
				}
			}
		})
	}
}

// T11 (hostile) for sync: once the API is added, an upstream that starts
// echoing the token into its schema is refused on re-introspection, and the
// saved config, snapshot and manifest stay exactly as they were.
func TestHostileSyncReintrospectionEchoesCredential(t *testing.T) {
	fixture := introspectionFixture(t)
	pinned := newIntrospectionServer(t, fixture)
	p, s, out := addIntrospected(t, pinned.URL+"/graphql")
	before := map[string][]byte{}
	for _, path := range apiFiles(t, p, "gh") {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = b
	}

	for _, echo := range []string{runSecret, base64.StdEncoding.EncodeToString([]byte(runSecret))} {
		pinned.schema.Store(withQueryField(t, withQueryDescription(t, fixture, "token: "+echo), "ping"))
		err := syncAPI(t.Context(), p, "gh", s)
		checkRefusedEcho(t, err, nil, "", runSecret, echo)
		for path, want := range before {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s changed after a refused sync (err %v)", path, err)
			}
		}
		if strings.Contains(out.String(), runSecret) || strings.Contains(out.String(), echo) {
			t.Errorf("sync output leaks the token:\n%s", out)
		}
	}
}

// T11 (hostile) for sync with JSON escapes: the re-introspected schema spells
// the token with escapes the child decodes ("\/" as PHP's json_encode
// writes it, \uXXXX, or a mix). Sync refuses each, and nothing changes.
func TestHostileSyncReintrospectionEscapedCredential(t *testing.T) {
	const tok = "tok/en+123="
	fixture := introspectionFixture(t)
	pinned := newIntrospectionServer(t, fixture)
	p, s, out := addIntrospectedWith(t, pinned.URL+"/graphql", tok)
	before := map[string][]byte{}
	for _, path := range apiFiles(t, p, "gh") {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = b
	}
	for name, echo := range map[string]string{
		"solidus-escaped": strings.ReplaceAll(tok, "/", `\/`),
		"fully u-escaped": jsonUEscape(tok),
		"mixed":           jsonUEscape("to") + `k\/en+` + jsonUEscape("123") + "=",
	} {
		pinned.schema.Store(withRawQueryDescription(t, withQueryField(t, fixture, "ping"), "token: "+echo))
		err := syncAPI(t.Context(), p, "gh", s)
		if !errors.Is(err, invoke.ErrSecretInResponse) {
			t.Fatalf("%s: err = %v, want the introspection result refused", name, err)
		}
		checkRefusedEcho(t, err, nil, "", tok, echo)
		for path, want := range before {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: %s changed after a refused sync (err %v)", name, path, err)
			}
		}
		if strings.Contains(out.String(), tok) || strings.Contains(out.String(), echo) {
			t.Errorf("%s: sync output leaks the token:\n%s", name, out)
		}
	}
}
