package graphql

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

// mapArg loads sdl, finds argument arg of Query.field and maps it.
func mapArg(t *testing.T, sdl, field, arg string) (raw json.RawMessage, required, degraded bool) {
	t.Helper()
	s, err := LoadSchema([]byte(sdl), FormatSDL)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	f := s.Query.Fields.ForName(field)
	if f == nil {
		t.Fatalf("no Query.%s", field)
	}
	a := f.Arguments.ForName(arg)
	if a == nil {
		t.Fatalf("no argument %s on Query.%s", arg, field)
	}
	return ArgSchema(s, a.DefaultValue, a.Type)
}

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("schema %s is not a JSON object: %v", raw, err)
	}
	return v
}

func TestMapScalarsIntBounds(t *testing.T) {
	sdl := `type Query { f(i: Int!, x: Float, s: String, id: ID, b: Boolean): Int }`
	tests := []struct {
		arg      string
		want     string
		required bool
	}{
		{"i", `{"maximum":2147483647,"minimum":-2147483648,"type":"integer"}`, true},
		{"x", `{"type":"number"}`, false},
		{"s", `{"type":"string"}`, false},
		{"id", `{"type":"string"}`, false},
		{"b", `{"type":"boolean"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			raw, required, degraded := mapArg(t, sdl, "f", tt.arg)
			if string(raw) != tt.want {
				t.Errorf("schema = %s, want %s", raw, tt.want)
			}
			if required != tt.required {
				t.Errorf("required = %v, want %v", required, tt.required)
			}
			if degraded {
				t.Error("built-in scalar reported degraded")
			}
		})
	}
}

func TestMapEnum(t *testing.T) {
	sdl := `
"""Sort <b>order</b> for results."""
enum Order { ZED ALPHA MID }
enum Bare { ONE TWO }
type Query { f(o: Order!, b: Bare): Int }
`
	raw, required, degraded := mapArg(t, sdl, "f", "o")
	want := `{"description":"Sort order for results.","enum":["ZED","ALPHA","MID"]}`
	if string(raw) != want {
		t.Errorf("schema = %s, want %s (values in schema order, description cleaned)", raw, want)
	}
	if !required || degraded {
		t.Errorf("required, degraded = %v, %v; want true, false", required, degraded)
	}

	raw, _, _ = mapArg(t, sdl, "f", "b")
	if want := `{"enum":["ONE","TWO"]}`; string(raw) != want {
		t.Errorf("schema = %s, want %s", raw, want)
	}
}

func TestMapInputObjectRequired(t *testing.T) {
	sdl := `
input Filter {
  zeta: String!
  "The <i>minimum</i> age."
  alpha: Int!
  defaulted: Int! = 3
  optional: String
  tags: [String!]!
}
type Query { f(filter: Filter): Int }
`
	raw, required, degraded := mapArg(t, sdl, "f", "filter")
	if required || degraded {
		t.Errorf("required, degraded = %v, %v; want false, false", required, degraded)
	}
	want := `{"properties":{` +
		`"alpha":{"description":"The minimum age.","maximum":2147483647,"minimum":-2147483648,"type":"integer"},` +
		`"defaulted":{"maximum":2147483647,"minimum":-2147483648,"type":"integer"},` +
		`"optional":{"type":"string"},` +
		`"tags":{"items":{"type":"string"},"type":"array"},` +
		`"zeta":{"type":"string"}},` +
		`"required":["alpha","tags","zeta"],"type":"object"}`
	if string(raw) != want {
		t.Errorf("schema =\n%s\nwant\n%s", raw, want)
	}

	// Output bytes are stable across calls.
	for range 20 {
		again, _, _ := mapArg(t, sdl, "f", "filter")
		if !bytes.Equal(again, raw) {
			t.Fatalf("output not deterministic:\n%s\n%s", raw, again)
		}
	}
}

func TestMapInputObjectNoRequiredFields(t *testing.T) {
	sdl := `
input Page { size: Int = 10 }
type Query { f(p: Page!): Int }
`
	raw, required, _ := mapArg(t, sdl, "f", "p")
	if want := `{"properties":{"size":{"maximum":2147483647,"minimum":-2147483648,"type":"integer"}},"type":"object"}`; string(raw) != want {
		t.Errorf("schema = %s, want %s (no empty required list)", raw, want)
	}
	if !required {
		t.Error("non-null argument without default not required")
	}
}

func TestMapDefaultedArgNotRequired(t *testing.T) {
	sdl := `type Query { users(first: Int! = 30, after: String!, last: Int, q: String = "x"): Int }`
	tests := []struct {
		arg  string
		want bool
	}{
		{"first", false},
		{"after", true},
		{"last", false},
		{"q", false},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			_, required, _ := mapArg(t, sdl, "users", tt.arg)
			if required != tt.want {
				t.Errorf("required = %v, want %v", required, tt.want)
			}
		})
	}
}

func TestMapList(t *testing.T) {
	sdl := `
enum E { A B }
type Query { f(ids: [ID!]!, grid: [[Int]], es: [E]): Int }
`
	tests := []struct {
		arg      string
		want     string
		required bool
	}{
		{"ids", `{"items":{"type":"string"},"type":"array"}`, true},
		{"grid", `{"items":{"items":{"maximum":2147483647,"minimum":-2147483648,"type":"integer"},"type":"array"},"type":"array"}`, false},
		{"es", `{"items":{"enum":["A","B"]},"type":"array"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			raw, required, degraded := mapArg(t, sdl, "f", tt.arg)
			if string(raw) != tt.want {
				t.Errorf("schema = %s, want %s", raw, tt.want)
			}
			if required != tt.required || degraded {
				t.Errorf("required, degraded = %v, %v; want %v, false", required, degraded, tt.required)
			}
		})
	}
}

func TestMapCustomScalarDegrades(t *testing.T) {
	sdl := `
scalar DateTime
input Window { from: DateTime!, label: String }
type Query { f(since: DateTime, at: [DateTime!]!, w: Window): Int }
`
	tests := []struct {
		arg  string
		want string
	}{
		{"since", `{}`},
		{"at", `{"items":{},"type":"array"}`},
		{"w", `{"properties":{"from":{},"label":{"type":"string"}},"required":["from"],"type":"object"}`},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			raw, _, degraded := mapArg(t, sdl, "f", tt.arg)
			if string(raw) != tt.want {
				t.Errorf("schema = %s, want %s", raw, tt.want)
			}
			if !degraded {
				t.Error("custom scalar not reported degraded")
			}
		})
	}
}

func TestMapUnmappableDegrades(t *testing.T) {
	s, err := LoadSchema([]byte(`type Query { f: Int }`), FormatSDL)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		s    *ast.Schema
		t    *ast.Type
	}{
		{"nil type", s, nil},
		{"nil schema", nil, ast.NamedType("Int", nil)},
		{"unknown type", s, ast.NamedType("Nope", nil)},
		{"output object", s, ast.NamedType("Query", nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _, degraded := ArgSchema(tt.s, nil, tt.t)
			if string(raw) != `{}` || !degraded {
				t.Errorf("got %s degraded=%v, want {} degraded=true", raw, degraded)
			}
		})
	}
}

func TestMapDepthCap(t *testing.T) {
	sdl := `
input Node { value: Int, next: Node }
type Query { f(n: Node): Int }
`
	raw, _, degraded := mapArg(t, sdl, "f", "n")
	if !degraded {
		t.Error("depth cap not reported degraded")
	}
	// Follow properties.next down: exactly maxSchemaDepth object levels, then {}.
	cur := decode(t, raw)
	levels := 0
	for len(cur) > 0 {
		if cur["type"] != "object" {
			t.Fatalf("level %d is not an object: %v", levels, cur)
		}
		levels++
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			t.Fatalf("level %d has no properties: %v", levels, cur)
		}
		next, ok := props["next"].(map[string]any)
		if !ok {
			t.Fatalf("level %d has no next property: %v", levels, cur)
		}
		cur = next
	}
	if levels != maxSchemaDepth {
		t.Errorf("object levels = %d, want %d", levels, maxSchemaDepth)
	}

	// Lists count as levels too: a self-referential list nests no deeper.
	sdl = `
input L { next: [L] }
type Query { f(l: [L]): Int }
`
	raw, _, degraded = mapArg(t, sdl, "f", "l")
	if !degraded {
		t.Error("list depth cap not reported degraded")
	}
	if d := schemaLevels(decode(t, raw)); d != maxSchemaDepth {
		t.Errorf("schema levels = %d, want %d", d, maxSchemaDepth)
	}
}

// schemaLevels counts nested schema levels through items and properties.
func schemaLevels(s map[string]any) int {
	if len(s) == 0 {
		return 0
	}
	best := 0
	if it, ok := s["items"].(map[string]any); ok {
		best = max(best, schemaLevels(it))
	}
	if props, ok := s["properties"].(map[string]any); ok {
		for _, p := range props {
			if ps, ok := p.(map[string]any); ok {
				best = max(best, schemaLevels(ps))
			}
		}
	}
	return best + 1
}

func TestMapNodeBudget(t *testing.T) {
	// Fan-out 4 to depth 12 would be ~22M nodes without the budget.
	sdl := `
input W { a: W, b: W, c: W, d: W }
type Query { f(w: W): Int }
`
	raw, _, degraded := mapArg(t, sdl, "f", "w")
	if !degraded {
		t.Error("node budget not reported degraded")
	}
	var count func(map[string]any) int
	count = func(s map[string]any) int {
		if len(s) == 0 {
			return 0
		}
		n := 1
		if props, ok := s["properties"].(map[string]any); ok {
			for _, p := range props {
				n += count(p.(map[string]any))
			}
		}
		return n
	}
	if n := count(decode(t, raw)); n > maxSchemaNodes {
		t.Errorf("mapped %d schema nodes, budget is %d", n, maxSchemaNodes)
	}
}

// countNodes counts the non-empty schema nodes in raw, through properties and items.
func countNodes(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var walk func(map[string]any) int
	walk = func(s map[string]any) int {
		if len(s) == 0 {
			return 0
		}
		n := 1
		if it, ok := s["items"].(map[string]any); ok {
			n += walk(it)
		}
		if props, ok := s["properties"].(map[string]any); ok {
			for _, p := range props {
				n += walk(p.(map[string]any))
			}
		}
		return n
	}
	return walk(decode(t, raw))
}

func TestMapNodeBudgetSharedAcrossArgs(t *testing.T) {
	sdl := `
input W { a: W, b: W, c: W, d: W }
type Query { f(w1: W, w2: W): Int }
`
	s, err := LoadSchema([]byte(sdl), FormatSDL)
	if err != nil {
		t.Fatal(err)
	}
	f := s.Query.Fields.ForName("f")
	m := newArgMapper(s)
	total := 0
	var degraded2 bool
	for i, name := range []string{"w1", "w2"} {
		a := f.Arguments.ForName(name)
		raw, _, degraded := m.arg(a.DefaultValue, a.Type)
		total += countNodes(t, raw)
		if i == 1 {
			degraded2 = degraded
		}
	}
	if total > maxSchemaNodes {
		t.Errorf("two arguments mapped %d schema nodes in total, budget per tool is %d", total, maxSchemaNodes)
	}
	if !degraded2 {
		t.Error("second argument not degraded after the shared budget was spent")
	}
}

func TestMapDegradedResetsPerArg(t *testing.T) {
	sdl := `
scalar DateTime
type Query { f(at: DateTime, n: Int): Int }
`
	s, err := LoadSchema([]byte(sdl), FormatSDL)
	if err != nil {
		t.Fatal(err)
	}
	f := s.Query.Fields.ForName("f")
	m := newArgMapper(s)
	if _, _, degraded := m.arg(nil, f.Arguments.ForName("at").Type); !degraded {
		t.Error("custom scalar not degraded")
	}
	raw, _, degraded := m.arg(nil, f.Arguments.ForName("n").Type)
	if degraded {
		t.Errorf("Int after a degraded argument reported degraded (%s)", raw)
	}
}
