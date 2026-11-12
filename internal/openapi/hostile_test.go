package openapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

const hostileDir = "../../testdata/hostile"

// hostileCases maps every fixture in testdata/hostile to its check. A
// fixture without an entry fails TestHostileFixturesAllCovered.
var hostileCases = map[string]func(t *testing.T, data []byte){
	"remote-ref.yaml": func(t *testing.T, data []byte) {
		m := mustParseQuickly(t, data)
		tl := tool(t, m, "GET /x")
		for _, n := range []string{"a", "b"} {
			assertJSON(t, n, param(t, tl, n).Schema, `{}`)
		}
		if !hasIssue(tl, model.IssueSchemaDegraded, "external $ref") || !containsWarning(m, "external $ref") {
			t.Errorf("external refs not reported: issues=%+v warnings=%v", tl.Issues, m.Warnings)
		}
	},
	"file-ref.yaml": func(t *testing.T, data []byte) {
		m := mustParseQuickly(t, data)
		tl := tool(t, m, "GET /x")
		for _, n := range []string{"a", "b", "c", "d"} {
			assertJSON(t, n, param(t, tl, n).Schema, `{}`)
		}
		out := marshalPrepared(t, m)
		for _, leak := range []string{"root:x:0:0", "[fonts]", "[extensions]", "localhost name resolution"} {
			if strings.Contains(out, leak) {
				t.Errorf("output contains local file content %q", leak)
			}
		}
	},
	"recursive-ref.yaml": func(t *testing.T, data []byte) {
		tl := tool(t, mustParseQuickly(t, data), "POST /tree")
		if !hasIssue(tl, model.IssueSchemaDegraded, "depth") {
			t.Errorf("recursion not depth-capped: %+v", tl.Issues)
		}
	},
	"deep-ref.yaml": func(t *testing.T, data []byte) {
		if _, err := Parse(data, Options{}); !errors.Is(err, ErrRefTooDeep) {
			t.Errorf("err = %v, want ErrRefTooDeep", err)
		}
	},
	"alias-bomb.yaml": func(t *testing.T, data []byte) {
		start := time.Now()
		_, err := Parse(data, Options{})
		if err == nil || !strings.Contains(err.Error(), "alias") {
			t.Errorf("err = %v, want an alias-expansion error", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("alias bomb took %v", time.Since(start))
		}
	},
	"poisoned-description.yaml": func(t *testing.T, data []byte) {
		m := mustParseQuickly(t, data)
		out := marshalPrepared(t, m)
		for i, r := range out {
			if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n') {
				t.Fatalf("output contains invisible/format character U+%04X at byte %d", r, i)
			}
		}
		for _, bad := range []string{"<img", "<!--", "onerror", "](https://"} {
			if strings.Contains(out, bad) {
				t.Errorf("output contains %q", bad)
			}
		}
		if get := tool(t, m, "GET /notes"); !strings.HasPrefix(get.Description, "List notes.") {
			t.Errorf("description = %q", get.Description)
		}
		// What cleaning removed is recorded, so lint (L007) can flag it.
		for key, wheres := range map[string][]string{
			"GET /notes":  {"summary", "description", `parameter "q" description`, "schema title", "schema description"},
			"POST /notes": {"summary", "schema description"},
		} {
			tl := tool(t, m, model.OpKey(key))
			for _, where := range wheres {
				if !hasIssue(tl, model.IssueHiddenText, where) {
					t.Errorf("%s: no hidden-text issue for %s: %+v", key, where, tl.Issues)
				}
			}
		}
	},
	"evil-servers.yaml": func(t *testing.T, data []byte) {
		m := mustParseQuickly(t, data)
		// Listed verbatim for the user to confirm or reject; never used implicitly.
		want := []string{"https://attacker.example.test/collect", "http://169.254.169.254/latest"}
		if !reflect.DeepEqual(m.Servers, want) {
			t.Errorf("Servers = %v, want %v", m.Servers, want)
		}
	},
	"header-smuggle.yaml": func(t *testing.T, data []byte) {
		tl := schema.Prepare(*tool(t, mustParseQuickly(t, data), "GET /x"), schema.Options{})
		var is struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(schema.InputSchema(tl), &is); err != nil {
			t.Fatal(err)
		}
		if len(is.Properties) != 1 || is.Properties["X-Request-Id"] == nil {
			t.Errorf("input schema properties = %v, want only X-Request-Id", keysOf(is.Properties))
		}
	},
	"name-collision.yaml": func(t *testing.T, data []byte) {
		m := mustParseQuickly(t, data)
		if len(m.Tools) != 100 {
			t.Fatalf("%d tools, want 100", len(m.Tools))
		}
		assertValidNames(t, prepare(m))
	},
	"body-collision.yaml": func(t *testing.T, data []byte) {
		m := mustParseQuickly(t, data)
		things := schema.Prepare(*tool(t, m, "PUT /things/{id}"), schema.Options{})
		got := map[string]string{}
		for _, p := range things.Params {
			got[p.Name] = p.In.String() + ":" + p.UpstreamName
		}
		want := map[string]string{"id": "path:id", "name": "query:name", "body_id": "body:id", "body_name": "body:name", "other": "body:other"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("params = %v, want %v", got, want)
		}

		double := schema.Prepare(*tool(t, m, "PUT /double/{id}"), schema.Options{})
		if len(double.Params) != 2 || double.Params[1].Name != "body" || double.Params[1].In != model.InBodyRoot {
			t.Errorf("double collision params = %+v, want id + nested body", double.Params)
		}
	},
	"traversal.yaml": func(t *testing.T, data []byte) {
		// Routing checks live in the proxy suite (M2); here it must parse.
		tool(t, mustParseQuickly(t, data), "GET /files/{name}")
	},
}

func TestHostileFixtures(t *testing.T) {
	for name, check := range hostileCases {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(hostileDir, name))
			if err != nil {
				t.Fatal(err)
			}
			check(t, data)
		})
	}
}

func TestHostileFixturesAllCovered(t *testing.T) {
	entries, err := os.ReadDir(hostileDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if _, ok := hostileCases[e.Name()]; !ok {
			t.Errorf("testdata/hostile/%s has no check in hostileCases", e.Name())
		}
	}
}

func mustParseQuickly(t *testing.T, data []byte) *model.Manifest {
	t.Helper()
	start := time.Now()
	m, err := Parse(data, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Parse took %v; something blocked (a network or file lookup?)", d)
	}
	return m
}

func marshalPrepared(t *testing.T, m *model.Manifest) string {
	t.Helper()
	b, err := json.Marshal(prepare(m))
	if err != nil {
		t.Fatal(err)
	}
	// Decode JSON escapes so escaped bidi/zero-width sequences are checked as runes.
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			sb.WriteString(x)
			sb.WriteByte('\n')
		case map[string]any:
			for k, e := range x {
				sb.WriteString(k)
				sb.WriteByte('\n')
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return sb.String()
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
