package graphql

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

// fixtureDir holds the hand-written GraphQL fixtures shared with other packages.
const fixtureDir = "../../../testdata/specs/graphql"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// summarize renders every non-built-in part of s as sorted lines, so two
// schemas can be compared for equivalence regardless of source format.
func summarize(s *ast.Schema) []string {
	var out []string
	root := func(label string, d *ast.Definition) {
		if d != nil {
			out = append(out, label+" "+d.Name)
		}
	}
	root("query", s.Query)
	root("mutation", s.Mutation)
	root("subscription", s.Subscription)
	for _, d := range s.Types {
		if d.BuiltIn {
			continue
		}
		ifaces := slices.Clone(d.Interfaces)
		slices.Sort(ifaces)
		members := slices.Clone(d.Types)
		slices.Sort(members)
		out = append(out, fmt.Sprintf("%s %s %q implements=%v members=%v%s",
			d.Kind, d.Name, d.Description, ifaces, members, directives(d.Directives)))
		for _, f := range d.Fields {
			var args []string
			for _, a := range f.Arguments {
				args = append(args, a.Name+":"+a.Type.String()+defaultOf(a.DefaultValue))
			}
			out = append(out, fmt.Sprintf("%s.%s(%s): %s%s %q%s",
				d.Name, f.Name, strings.Join(args, ","), f.Type.String(), defaultOf(f.DefaultValue),
				f.Description, directives(f.Directives)))
		}
		for _, v := range d.EnumValues {
			out = append(out, fmt.Sprintf("%s.%s %q%s", d.Name, v.Name, v.Description, directives(v.Directives)))
		}
	}
	for _, d := range s.Directives {
		if d.Position != nil && d.Position.Src != nil && d.Position.Src.BuiltIn {
			continue
		}
		var args []string
		for _, a := range d.Arguments {
			args = append(args, a.Name+":"+a.Type.String()+defaultOf(a.DefaultValue))
		}
		out = append(out, fmt.Sprintf("@%s(%s) repeatable=%v on %v %q",
			d.Name, strings.Join(args, ","), d.IsRepeatable, d.Locations, d.Description))
	}
	slices.Sort(out)
	return out
}

func defaultOf(v *ast.Value) string {
	if v == nil {
		return ""
	}
	return "=" + v.String()
}

func directives(list ast.DirectiveList) string {
	var b strings.Builder
	for _, d := range list {
		b.WriteString(" @" + d.Name)
		for _, a := range d.Arguments {
			b.WriteString(" " + a.Name + ":" + a.Value.String())
		}
	}
	return b.String()
}

func TestLoadSDL(t *testing.T) {
	s, err := LoadSchema(readFixture(t, "user.graphql"), FormatSDL)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if s.Query == nil || s.Query.Name != "Query" {
		t.Fatalf("query root = %v, want Query", s.Query)
	}
	if s.Mutation == nil || s.Mutation.Name != "Mutation" {
		t.Fatalf("mutation root = %v, want Mutation", s.Mutation)
	}
	user := s.Query.Fields.ForName("user")
	if user == nil || user.Type.String() != "User" || user.Arguments.ForName("id").Type.String() != "ID!" {
		t.Fatalf("Query.user = %+v, want user(id: ID!): User", user)
	}
	if got := s.Types["SearchResult"]; got == nil || got.Kind != ast.Union {
		t.Fatalf("SearchResult = %+v, want a union", got)
	}
}

func TestLoadIntrospection(t *testing.T) {
	sdl, err := LoadSchema(readFixture(t, "user.graphql"), FormatSDL)
	if err != nil {
		t.Fatalf("LoadSchema(sdl): %v", err)
	}
	intro, err := LoadSchema(readFixture(t, "user.introspection.json"), FormatIntrospection)
	if err != nil {
		t.Fatalf("LoadSchema(introspection): %v", err)
	}

	// Same query fields (the brief's acceptance check) ...
	names := func(s *ast.Schema) []string {
		var n []string
		for _, f := range s.Query.Fields {
			if !strings.HasPrefix(f.Name, "__") {
				n = append(n, f.Name+": "+f.Type.String())
			}
		}
		slices.Sort(n)
		return n
	}
	if a, b := names(sdl), names(intro); !slices.Equal(a, b) {
		t.Fatalf("query fields differ:\nsdl:   %v\nintro: %v", a, b)
	}

	// ... and the whole user-defined schema is equivalent.
	a, b := summarize(sdl), summarize(intro)
	if !slices.Equal(a, b) {
		t.Errorf("schemas differ:\nsdl:\n  %s\nintrospection:\n  %s",
			strings.Join(a, "\n  "), strings.Join(b, "\n  "))
	}

	// The bare {"__schema": ...} form loads the same schema.
	raw := readFixture(t, "user.introspection.json")
	start := strings.Index(string(raw), `"__schema"`)
	end := strings.LastIndex(string(raw), "}")
	bare := []byte("{" + string(raw[start:end]))
	bareSchema, err := LoadSchema(bare, FormatIntrospection)
	if err != nil {
		t.Fatalf("LoadSchema(bare __schema): %v", err)
	}
	if c := summarize(bareSchema); !slices.Equal(b, c) {
		t.Errorf("bare form differs from data-wrapped form")
	}
}

// TestDetectSDLCheapReject proves JSON and plain non-SDL text are rejected by
// the token-level check, without reaching the full parser. Two checks:
//   - sdlKeywordFirst (the token-level path DetectSDL runs first) rejects them;
//   - DetectSDL on them allocates nothing, while DetectSDL on real SDL (which
//     goes on to the full parse) allocates. A full gqlparser parse cannot run
//     without allocating, so zero allocations means it never ran. No
//     production hook is needed for this.
func TestDetectSDLCheapReject(t *testing.T) {
	rejects := map[string][]byte{
		"json":       []byte(`{"openapi":"3.0.0","info":{"title":"type schema","version":"1"}}`),
		"json array": []byte(`[{"type":"query"}]`),
		"plain text": []byte("hello world, this is not a schema.\ntype Query { a: Int }\n"),
		"near miss":  []byte("# comment\ntypes Query { a: Int }"),
		"yaml":       []byte("openapi: 3.0.0\ninfo:\n  title: t\n"),
		"empty":      nil,
		"comments":   []byte("# only\n# comments\n"),
		"one quote":  []byte(`"desc" type Query { a: Int }`),
		"big text":   []byte("lorem " + strings.Repeat("ipsum dolor sit amet { [ ( ", 50000)),
	}
	for name, data := range rejects {
		t.Run(name, func(t *testing.T) {
			if sdlKeywordFirst(data) {
				t.Fatalf("sdlKeywordFirst accepted %q", trunc(data))
			}
			if DetectSDL(data) {
				t.Fatalf("DetectSDL accepted %q", trunc(data))
			}
			if !raceEnabled {
				if n := testing.AllocsPerRun(20, func() { DetectSDL(data) }); n != 0 {
					t.Fatalf("DetectSDL allocated %v times on a cheap reject; want 0 (no full parse)", n)
				}
			}
		})
	}

	// Control: the allocation check is sensitive. Real SDL passes the token
	// check and then runs the full parse, which allocates.
	sdl := readFixture(t, "user.graphql")
	if !sdlKeywordFirst(sdl) || !DetectSDL(sdl) {
		t.Fatal("fixture SDL not detected")
	}
	if !raceEnabled {
		if n := testing.AllocsPerRun(5, func() { DetectSDL(sdl) }); n == 0 {
			t.Fatal("DetectSDL on SDL allocated nothing; the allocation proof above is not meaningful")
		}
	}

	// Each accepted first token, including a """ description after comments.
	for _, kw := range []string{"type", "schema", "interface", "enum", "input", "scalar", "union", "directive", "extend"} {
		if !sdlKeywordFirst([]byte(bom + "# c\n , " + kw + " X")) {
			t.Errorf("sdlKeywordFirst rejected leading %q", kw)
		}
	}
	if !sdlKeywordFirst([]byte("  \"\"\"doc\"\"\" type Query { a: Int }")) {
		t.Error(`sdlKeywordFirst rejected a leading """ description`)
	}
}

// bom is a UTF-8 byte-order mark, spelled as bytes so no invisible character
// sits in the source.
const bom = "\xef\xbb\xbf"

func trunc(b []byte) string {
	if len(b) > 60 {
		return string(b[:60]) + "..."
	}
	return string(b)
}

func TestDetectOnlyOne(t *testing.T) {
	intro := readFixture(t, "user.introspection.json")
	start := strings.Index(string(intro), `"__schema"`)
	bare := []byte("{" + string(intro[start:strings.LastIndex(string(intro), "}")]))

	tests := []struct {
		name      string
		data      []byte
		wantSDL   bool
		wantIntro bool
	}{
		{"sdl", readFixture(t, "user.graphql"), true, false},
		{"introspection data envelope", intro, false, true},
		{"introspection bare", bare, false, true},
		{"openapi json", []byte(`{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{"/users":{"get":{"description":"returns {\"__schema\": 1}","responses":{"200":{"description":"ok"}}}}}}`), false, false},
		{"openapi yaml", []byte("openapi: 3.0.0\ninfo: {title: t, version: '1'}\npaths: {}\n"), false, false},
		{"json schema yaml", []byte("type: object\nproperties:\n  id: {type: string}\n"), false, false},
		{"graphql response without schema", []byte(`{"data":{"user":{"id":"1"}}}`), false, false},
		{"__schema not an object", []byte(`{"data":{"__schema":"nope"}}`), false, false},
		{"query document", []byte(`query { user(id: 1) { id } }`), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectSDL(tc.data); got != tc.wantSDL {
				t.Errorf("DetectSDL = %v, want %v", got, tc.wantSDL)
			}
			if got := DetectIntrospection(tc.data); got != tc.wantIntro {
				t.Errorf("DetectIntrospection = %v, want %v", got, tc.wantIntro)
			}
		})
	}
}

// nestedRef builds an introspection type ref with depth LIST wrappers.
func nestedRef(depth int) string {
	ref := `{"kind":"SCALAR","name":"Int","ofType":null}`
	for range depth {
		ref = `{"kind":"LIST","name":null,"ofType":` + ref + `}`
	}
	return ref
}

func introWithField(name, ref, extra string) []byte {
	return []byte(`{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[` +
		`{"name":"` + name + `","args":[` + extra + `],"type":` + ref + `}]}],"directives":[]}}`)
}

func TestLoadRejectsGarbage(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // G404: deterministic test input, not security randomness
	var cases [][]byte
	for range 200 {
		b := make([]byte, rng.Intn(512))
		rng.Read(b)
		cases = append(cases, b)
	}
	cases = append(cases,
		nil,
		[]byte("{"),
		[]byte(`{"__schema":null}`),
		[]byte(`{"__schema":{"types":"x"}}`),
		[]byte(`{"__schema":{"queryType":{"name":"Missing"},"types":[]}}`),
		[]byte("type Query { a: Missing }"),
		[]byte("type Query {"),
		[]byte(strings.Repeat("[", 100000)),
	)
	for i, data := range cases {
		for _, format := range []string{FormatSDL, FormatIntrospection} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("case %d (%s) panicked: %v", i, format, r)
					}
				}()
				s, err := LoadSchema(data, format)
				if err == nil {
					t.Fatalf("case %d (%s): loaded %v from garbage %q", i, format, s, trunc(data))
				}
				if !errors.Is(err, ErrUnsupportedSpec) {
					t.Fatalf("case %d (%s): err = %v, want wrapped ErrUnsupportedSpec", i, format, err)
				}
			}()
		}
		// Detection never panics either.
		DetectSDL(data)
		DetectIntrospection(data)
	}
}

func TestLoadIntrospectionHostile(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ok   bool
	}{
		{"ofType at the cap", introWithField("a", nestedRef(maxTypeRefDepth-1), ""), true},
		{"ofType past the cap", introWithField("a", nestedRef(maxTypeRefDepth+5), ""), false},
		{"ofType very deep", introWithField("a", nestedRef(5000), ""), false},
		{"bad field name", introWithField("a: Int } type Evil { b", `{"kind":"SCALAR","name":"Int","ofType":null}`, ""), false},
		{"bad type name", introWithField("a", `{"kind":"SCALAR","name":"Int } type Evil {","ofType":null}`, ""), false},
		{"named ref without name", introWithField("a", `{"kind":"SCALAR","name":null,"ofType":null}`, ""), false},
		{"non-null without ofType", introWithField("a", `{"kind":"NON_NULL","name":null,"ofType":null}`, ""), false},
		{"default value injection", introWithField("a", `{"kind":"SCALAR","name":"Int","ofType":null}`,
			`{"name":"x","type":{"kind":"SCALAR","name":"Int","ofType":null},"defaultValue":"1) : Int } type Evil { b: Int"}`), false},
		{"default value comment", introWithField("a", `{"kind":"SCALAR","name":"Int","ofType":null}`,
			`{"name":"x","type":{"kind":"SCALAR","name":"Int","ofType":null},"defaultValue":"1 # )"}`), true},
		// gqlparser's nesting limit (1000) rejects this default value
		// before it can exhaust the stack.
		{"default value nested very deep", introWithField("a", `{"kind":"SCALAR","name":"Int","ofType":null}`,
			`{"name":"x","type":{"kind":"SCALAR","name":"Int","ofType":null},"defaultValue":"`+strings.Repeat("[", 2000)+strings.Repeat("]", 2000)+`"}`), false},
		{"unknown kind", []byte(`{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"a","args":[],"type":{"kind":"SCALAR","name":"Int","ofType":null}}]},{"kind":"WEIRD","name":"X"}]}}`), false},
		{"description with quotes", []byte(`{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","description":"\"\"\" } type Evil { b: Int } \"\\u0000","fields":[{"name":"a","args":[],"type":{"kind":"SCALAR","name":"Int","ofType":null}}]}]}}`), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := LoadSchema(tc.data, FormatIntrospection)
			if tc.ok {
				if err != nil {
					t.Fatalf("LoadSchema: %v", err)
				}
				if s.Types["Evil"] != nil {
					t.Fatal("injected type Evil appeared in the schema")
				}
				return
			}
			if err == nil {
				t.Fatalf("loaded hostile input; types=%v", len(s.Types))
			}
			if !errors.Is(err, ErrUnsupportedSpec) {
				t.Fatalf("err = %v, want wrapped ErrUnsupportedSpec", err)
			}
		})
	}
}

// TestLoadIntrospectionBOM: a BOM-prefixed result (e.g. written by PowerShell
// 5.1's Set-Content -Encoding UTF8) is detected, so it must also load.
func TestLoadIntrospectionBOM(t *testing.T) {
	plain := readFixture(t, "user.introspection.json")
	withBOM := append([]byte(bom), plain...)
	if !DetectIntrospection(withBOM) {
		t.Fatal("BOM-prefixed introspection not detected")
	}
	want, err := LoadSchema(plain, FormatIntrospection)
	if err != nil {
		t.Fatalf("LoadSchema(plain): %v", err)
	}
	got, err := LoadSchema(withBOM, FormatIntrospection)
	if err != nil {
		t.Fatalf("LoadSchema(BOM-prefixed): %v", err)
	}
	if a, b := summarize(want), summarize(got); !slices.Equal(a, b) {
		t.Errorf("BOM-prefixed schema differs:\nplain: %v\nbom:   %v", a, b)
	}
}

func TestLoadUnknownFormat(t *testing.T) {
	_, err := LoadSchema(readFixture(t, "user.graphql"), "openapi")
	if !errors.Is(err, ErrUnsupportedSpec) {
		t.Fatalf("err = %v, want wrapped ErrUnsupportedSpec", err)
	}
}
