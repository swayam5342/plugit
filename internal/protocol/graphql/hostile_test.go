package graphql

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Hostile GraphQL inputs (SPEC 6.3, T18). Each one is handled: no panic, no
// escape, and every cap holds. github_test.go runs the same inputs through
// the isolate child.

// HugeSize shapes a generated introspection result.
type HugeSize struct {
	Types, Fields, Roots, Subscriptions int
	DescriptionRunes                    int
}

// HugeShape is the shape the huge-introspection tests use: big enough to
// exercise every count cap, small enough to stay fast under -race.
var HugeShape = HugeSize{Types: 2000, Fields: 20, Roots: 400, Subscriptions: 100, DescriptionRunes: 1 << 20}

// HugeIntrospection generates an introspection result with sz.Types object
// types of sz.Fields fields each (half of them pointing at other types, so
// the graph is full of cycles), sz.Roots Query fields, sz.Subscriptions
// Subscription fields, and descriptions of sz.DescriptionRunes runes on the
// first type and the first root field. It is generated rather than checked in
// because it is several megabytes.
func HugeIntrospection(sz HugeSize) []byte {
	named := func(kind, name string) *introRef { return &introRef{Kind: kind, Name: name} }
	nonNull := func(r *introRef) *introRef { return &introRef{Kind: "NON_NULL", OfType: r} }
	long := strings.Repeat("a", sz.DescriptionRunes)

	types := make([]introType, 0, sz.Types+2)
	for i := range sz.Types {
		t := introType{Kind: "OBJECT", Name: fmt.Sprintf("T%d", i), Interfaces: []introRef{}}
		if i == 0 {
			t.Description = long
		}
		for j := range sz.Fields {
			ref := named("SCALAR", "Int")
			if j%2 == 1 {
				ref = named("OBJECT", fmt.Sprintf("T%d", (i+j)%sz.Types))
			}
			t.Fields = append(t.Fields, introField{Name: fmt.Sprintf("f%d", j), Args: []introInput{}, Type: ref})
		}
		types = append(types, t)
	}
	query := introType{Kind: "OBJECT", Name: "Query", Interfaces: []introRef{}}
	for i := range sz.Roots {
		f := introField{
			Name: fmt.Sprintf("q%d", i),
			Args: []introInput{{Name: "id", Type: nonNull(named("SCALAR", "ID"))}},
			Type: named("OBJECT", fmt.Sprintf("T%d", i%sz.Types)),
		}
		if i == 0 {
			f.Description = long
		}
		query.Fields = append(query.Fields, f)
	}
	sub := introType{Kind: "OBJECT", Name: "Subscription", Interfaces: []introRef{}}
	for i := range sz.Subscriptions {
		sub.Fields = append(sub.Fields, introField{Name: fmt.Sprintf("s%d", i), Args: []introInput{}, Type: named("SCALAR", "Int")})
	}
	types = append(types, query, sub)

	s := introSchema{QueryType: &introNamed{Name: "Query"}, SubscriptionType: &introNamed{Name: "Subscription"}, Types: types}
	out, err := json.Marshal(map[string]any{"data": map[string]any{"__schema": s}})
	if err != nil {
		panic(err) // plain structs of strings and slices: cannot fail
	}
	return out
}

// DeepTypeRefIntrospection generates an introspection result whose one Query
// field has a type reference nested depth LIST levels deep.
func DeepTypeRefIntrospection(depth int) []byte {
	ref := &introRef{Kind: "SCALAR", Name: "Int"}
	for range depth {
		ref = &introRef{Kind: "LIST", OfType: ref}
	}
	s := introSchema{QueryType: &introNamed{Name: "Query"}, Types: []introType{{
		Kind: "OBJECT", Name: "Query", Interfaces: []introRef{},
		Fields: []introField{{Name: "deep", Args: []introInput{}, Type: ref}},
	}}}
	out, err := json.Marshal(map[string]any{"__schema": s})
	if err != nil {
		panic(err) // plain structs: cannot fail
	}
	return out
}

func hostileFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(hostileDir + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// jsonDepth is the nesting depth of a JSON value.
func jsonDepth(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON schema %s: %v", raw, err)
	}
	var depth func(v any) int
	depth = func(v any) int {
		d := 0
		switch x := v.(type) {
		case map[string]any:
			for _, c := range x {
				d = max(d, depth(c))
			}
		case []any:
			for _, c := range x {
				d = max(d, depth(c))
			}
		default:
			return 0
		}
		return d + 1
	}
	return depth(v)
}

// checkCaps asserts the caps every tool of m must respect, and that its
// document validates.
func checkCaps(t *testing.T, m *model.Manifest, s string, depthCap, fieldCap int) {
	t.Helper()
	schema, err := LoadSchema([]byte(s), FormatSDL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range m.Tools {
		for _, p := range tl.Params {
			// Each schema level is at most an object plus its properties map.
			if d := jsonDepth(t, p.Schema); d > 2*maxSchemaDepth+3 {
				t.Errorf("%s: parameter %s schema is %d levels deep (cap %d)", tl.Key, p.Name, d, maxSchemaDepth)
			}
		}
		if n := len(tl.GraphQL.Selection); n > fieldCap {
			t.Errorf("%s: %d selected fields, over %d", tl.Key, n, fieldCap)
		}
		for _, path := range tl.GraphQL.Selection {
			if segs := strings.Count(path, ".") + 1; segs > depthCap {
				t.Errorf("%s: selection path %q is %d deep, over %d", tl.Key, path, segs, depthCap)
			}
		}
		if _, errs := gqlparser.LoadQueryWithRules(schema, tl.GraphQL.Document, rules.NewDefaultRules()); errs != nil {
			t.Errorf("%s: document does not validate: %v\n%s", tl.Key, errs, tl.GraphQL.Document)
		}
	}
}

func hasIssue(tl model.Tool, kind model.IssueKind) bool {
	for _, is := range tl.Issues {
		if is.Kind == kind {
			return true
		}
	}
	return false
}

// Nesting 64 levels deep (input objects, object types) and a 40-level list
// stop at the caps: argument schemas degrade (L012), selections stop at
// selection_depth, documents validate.
func TestHostileDeepNesting(t *testing.T) {
	sdl := hostileFixture(t, "deep-nesting.graphql")
	m := build(t, sdl, ingest.Options{})
	checkCaps(t, m, sdl, DefaultSelectionDepth, DefaultMaxFields)
	for _, key := range []model.OpKey{"query deep", "query lists"} {
		if tl := toolByKey(t, m, key); !hasIssue(tl, model.IssueSchemaDegraded) {
			t.Errorf("%s: no schema-degraded issue; issues %+v", key, tl.Issues)
		}
	}
	// A deeper configured selection is still bounded by its own cap.
	m = build(t, sdl, ingest.Options{SelectionDepth: 12})
	checkCaps(t, m, sdl, 12, DefaultMaxFields)
}

// Type cycles (direct, mutual, through lists, interfaces and a union; input
// cycles too) terminate within the caps, and a response_fields path that
// walks the cycle compiles to a valid document.
func TestHostileTypeCycle(t *testing.T) {
	sdl := hostileFixture(t, "type-cycle.graphql")
	for _, opts := range []ingest.Options{{}, {SelectionDepth: 12, MaxFields: 1000}} {
		m := build(t, sdl, opts)
		depth, fields := opts.SelectionDepth, opts.MaxFields
		if depth == 0 {
			depth, fields = DefaultSelectionDepth, DefaultMaxFields
		}
		checkCaps(t, m, sdl, depth, fields)
		grow := toolByKey(t, m, "query grow")
		for _, p := range grow.Params {
			if len(p.Schema) > 1<<20 {
				t.Errorf("query grow parameter %s schema is %d bytes", p.Name, len(p.Schema))
			}
		}
	}

	s := loadSDL(t, sdl)
	m, err := Build(s, FormatSDL, ingest.Options{ResponseFields: map[string]map[string][]string{
		"default": {"query a": {"self.self.self.self.self.id", "many.b.a.any.id"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc := m.GraphQL.OverrideDocs["default"]["query a"]
	if doc == "" {
		t.Fatalf("no override document; errors %v", m.GraphQL.OverrideErrors)
	}
	if _, errs := gqlparser.LoadQueryWithRules(s, doc, rules.NewDefaultRules()); errs != nil {
		t.Errorf("override does not validate: %v\n%s", errs, doc)
	}
}

// A huge introspection result loads within the count caps: warnings stop at
// maxWarnings plus a summary line, descriptions are cut to the description
// cap, and every selection stays within max_fields.
func TestHostileHugeIntrospection(t *testing.T) {
	sz := HugeShape
	data := HugeIntrospection(sz)
	t.Logf("generated %d bytes", len(data))
	if !DetectIntrospection(data) {
		t.Fatal("not detected as introspection")
	}
	s, err := LoadSchema(data, FormatIntrospection)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	m, err := Build(s, FormatIntrospection, ingest.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.Tools) != sz.Roots {
		t.Errorf("%d tools, want %d", len(m.Tools), sz.Roots)
	}
	if len(m.Warnings) != maxWarnings+1 {
		t.Errorf("%d warnings, want %d", len(m.Warnings), maxWarnings+1)
	} else if want := fmt.Sprintf("... and %d more warnings", sz.Subscriptions-maxWarnings); m.Warnings[maxWarnings] != want {
		t.Errorf("last warning = %q, want %q", m.Warnings[maxWarnings], want)
	}
	// The structured list lint reads is not capped like the warnings.
	if len(m.Unsupported) != sz.Subscriptions {
		t.Errorf("%d unsupported operations recorded, want all %d subscriptions", len(m.Unsupported), sz.Subscriptions)
	}
	for _, u := range m.Unsupported {
		if u.Operation != "subscription" || u.Reason != model.UnsupportedSubscription || u.Field == "" {
			t.Errorf("unsupported entry %+v", u)
		}
	}
	for _, tl := range m.Tools {
		if n := utf8.RuneCountInString(tl.Description); n > naming.DefaultMaxDescription {
			t.Errorf("%s: description is %d runes", tl.Key, n)
		}
		if n := len(tl.GraphQL.Selection); n > DefaultMaxFields {
			t.Errorf("%s: %d selected fields", tl.Key, n)
		}
	}
}

// A type reference nested 5000 levels deep is refused at the ofType cap
// before anything is built.
func TestHostileDeepTypeRef(t *testing.T) {
	_, err := LoadSchema(DeepTypeRefIntrospection(5000), FormatIntrospection)
	if !errors.Is(err, ErrUnsupportedSpec) || !strings.Contains(err.Error(), fmt.Sprintf("deeper than %d", maxTypeRefDepth)) {
		t.Fatalf("err = %v, want the ofType depth cap", err)
	}
	if _, err := LoadSchema(DeepTypeRefIntrospection(maxTypeRefDepth-2), FormatIntrospection); err != nil {
		t.Errorf("a reference within the cap was refused: %v", err)
	}
}

// query resetCache is read-only by its operation type (lint flags its name,
// L015); mutation deleteX is Destructive.
func TestHostileWriteNamedQuery(t *testing.T) {
	m := build(t, hostileFixture(t, "write-named-query.graphql"), ingest.Options{})
	if tl := toolByKey(t, m, "query resetCache"); tl.Safety != model.ReadOnly {
		t.Errorf("query resetCache safety = %v, want read-only", tl.Safety)
	}
	if tl := toolByKey(t, m, "mutation deleteX"); tl.Safety != model.Destructive {
		t.Errorf("mutation deleteX safety = %v, want destructive", tl.Safety)
	}
}
