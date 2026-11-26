package graphql

import (
	"regexp"
	"strings"
	"testing"

	gg "github.com/graph-gophers/graphql-go"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"

	"github.com/swayam5342/plugit/internal/ingest"
)

// The independent check (SPEC 7 M8 exit): every document Build generates,
// default and response_fields override, is validated by
// github.com/graph-gophers/graphql-go, a second GraphQL implementation with
// its own parser and validator, so a gqlparser bug the builder relies on
// cannot hide an invalid document.
//
// How graph-gophers is used (Step 1 spike, 2026-11-26, v1.11.1):
//   - Validation only: ParseSchema with a nil resolver gives a schema that
//     can be inspected and validated but never executed; Schema.Validate
//     parses and validates a document without running it.
//   - Validate also coerces runtime variable values (rule
//     VariablesOfCorrectType) and, given none, reports every non-null
//     variable as null. Those values are not part of the document (the model
//     supplies them per call, checked against the tool's input schema), so
//     that rule is ignored here.
//   - No explicit schema { query mutation } block is needed: Query and
//     Mutation are found by name.
//   - Newer SDL syntax parses: interfaces implementing interfaces, @oneOf,
//     @specifiedBy, repeatable directives.
//   - Limitation: graph-gophers rejects GitHub's SDL at schema validation,
//     not at parse: "interface "Reactable" field "databaseId" is not
//     deprecated but implementing type "PullRequest" marks it as deprecated".
//     The GraphQL spec has no such rule. Deprecation never changes whether a
//     document is valid (graph-gophers only checks it with an opt-in), so
//     for GitHub the field @deprecated directives are removed from the copy
//     of the schema given to graph-gophers; the documents are unchanged.
//   - Known divergence: for an argument `a: T! = v` the builder declares
//     `$a: T! = v`. graph-gophers rejects that (DefaultValuesOfCorrectType:
//     "is required and will not use the default value"), the rule graphql-js
//     dropped in v14 when the spec began allowing defaults on non-null
//     variables; gqlparser and the current spec accept it. The generator is
//     not changed for it: knownDivergence accepts exactly that error, and
//     only for a variable the document really declares as T! = default.

// builderCasesSDL covers every builder case: all built-in scalars and custom
// scalars, enums as arguments, defaults and return types, nested input
// objects with object and list defaults, lists of lists, non-null arguments
// with defaults, interfaces (one implementing another), unions whose members
// declare a field with different types (aliased <field>_<Type>), recursion,
// deprecated fields, fields with optional arguments inside the selection,
// and mutations returning scalars, objects and lists.
const builderCasesSDL = `
scalar DateTime
scalar JSON
enum Color { RED GREEN BLUE }
interface Node { id: ID! }
interface Named implements Node { id: ID! name: String }
type User implements Named & Node {
  id: ID! name: String age: Int score: Float ok: Boolean color: Color at: DateTime raw: JSON
  friends(first: Int = 3): [User!]! tags: [[String!]] best: Pet old: String @deprecated(reason: "x")
}
type Pet implements Named & Node { id: ID! name: String age: Float owner: User }
union Thing = User | Pet
input Inner { color: Color = RED, at: DateTime, n: [Int!] = [1, 2] }
input Outer { inner: Inner = {color: GREEN}, list: [[Inner!]!], must: Int! }
type Query {
  user(id: ID!): User
  things(kinds: [Color!] = [RED], limit: Int! = 10): [Thing!]!
  named: [Named]
  node(id: ID!): Node
  color(c: Color = BLUE): Color
  search(o: Outer!, opt: Outer, when: DateTime, j: JSON, f: Float = 1.5, b: Boolean = true, s: String = "x\ny"): [[Thing]]
  count: Int!
  matrix(m: [[Int!]!]! = [[1]]): [[[Int]]]
}
type Mutation {
  createUser(name: String!, o: Outer): User!
  deleteUser(id: ID!): Boolean
  tagAll(ids: [ID!]!): [Thing]
}
`

// ggSchema parses sdl for graph-gophers, schema-only (nil resolver). With
// stripDeprecated, field @deprecated directives are removed first (see the
// GitHub limitation above).
func ggSchema(t *testing.T, sdl string, stripDeprecated bool) *gg.Schema {
	t.Helper()
	if stripDeprecated {
		s, err := LoadSchema([]byte(sdl), FormatSDL)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range s.Types {
			for _, f := range d.Fields {
				var keep ast.DirectiveList
				for _, dir := range f.Directives {
					if dir.Name != "deprecated" {
						keep = append(keep, dir)
					}
				}
				f.Directives = keep
			}
		}
		var b strings.Builder
		formatter.NewFormatter(&b).FormatSchema(s)
		sdl = b.String()
	}
	gs, err := gg.ParseSchema(sdl, nil)
	if err != nil {
		t.Fatalf("graph-gophers cannot parse the schema: %v", err)
	}
	return gs
}

var requiredDefault = regexp.MustCompile(`^Variable "\$([_A-Za-z][_0-9A-Za-z]*)" of type "[^"]+!" is required and will not use the default value\.`)

// knownDivergence reports whether a graph-gophers error is the documented
// non-null-variable-with-default divergence for a variable doc really
// declares as T! = default.
func knownDivergence(doc, rule, msg string) bool {
	m := requiredDefault.FindStringSubmatch(msg)
	if rule != "DefaultValuesOfCorrectType" || m == nil {
		return false
	}
	decl := regexp.MustCompile(`\$` + regexp.QuoteMeta(m[1]) + `: [^,)=]+! = `)
	return decl.MatchString(doc)
}

// ggValidate validates doc with graph-gophers and returns its errors, the
// runtime-value rule and the known divergence left out; divergences counts
// the latter.
func ggValidate(gs *gg.Schema, doc string) (errs []string, divergences int) {
	for _, e := range gs.Validate(doc) {
		switch {
		case e.Rule == "VariablesOfCorrectType":
		case knownDivergence(doc, e.Rule, e.Message):
			divergences++
		default:
			errs = append(errs, "["+e.Rule+"] "+e.Message)
		}
	}
	return errs, divergences
}

func TestGeneratedDocumentsValidateIndependently(t *testing.T) {
	cases := []struct {
		name            string
		sdl             string
		stripDeprecated bool
		opts            ingest.Options
		overrides       map[string][]string // tool key -> response_fields
		minTools        int
		divergences     int // known T! = default divergences expected (one per document declaring one)
	}{
		{
			name: "builder cases", sdl: builderCasesSDL, minTools: 11, divergences: 3, // things, matrix, and the things override
			overrides: map[string][]string{
				"query user":          {"id", "friends.best.owner.name", "tags"},
				"query things":        {"id", "name", "age", "owner.id"},
				"query named":         {"name", "age"},
				"mutation createUser": {"id", "best.name"},
			},
		},
		{
			name: "builder cases deep", sdl: builderCasesSDL, minTools: 11, divergences: 2,
			opts: ingest.Options{SelectionDepth: 6, MaxFields: 500},
		},
		{
			name: "user fixture", sdl: userFixture(t), minTools: 8,
			overrides: map[string][]string{"query org": {"name", "members.id"}, "query search": {"id", "name"}},
		},
		{
			name: "hostile type cycle", sdl: hostileFixture(t, "type-cycle.graphql"), minTools: 4,
			overrides: map[string][]string{"query a": {"self.self.self.id", "many.b.a.any.id"}},
		},
		{
			name: "github", sdl: string(GitHubSDL(t)), stripDeprecated: true, minTools: 250,
			opts: ingest.Options{SelectionDepth: 2, MaxFields: 100},
			overrides: map[string][]string{
				"query viewer":     {"login", "name", "repositories.totalCount"},
				"query repository": {"nameWithOwner", "issues.nodes.title", "owner.login"},
				"mutation addStar": {"starrable.id", "starrable.viewerHasStarred"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			if tc.overrides != nil {
				opts.ResponseFields = map[string]map[string][]string{"default": tc.overrides}
			}
			m, err := Build(loadSDL(t, tc.sdl), FormatSDL, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Tools) < tc.minTools {
				t.Fatalf("%d tools, want at least %d", len(m.Tools), tc.minTools)
			}
			gs := ggSchema(t, tc.sdl, tc.stripDeprecated)

			checked, divergences := 0, 0
			check := func(what, doc string) {
				t.Helper()
				errs, d := ggValidate(gs, doc)
				divergences += d
				checked++
				if len(errs) > 0 {
					t.Errorf("%s: graph-gophers rejects a document gqlparser accepted:\n  %s\n  %s", what, doc, strings.Join(errs, "\n  "))
				}
			}
			for _, tl := range m.Tools {
				check(string(tl.Key), tl.GraphQL.Document)
			}
			if m.GraphQL.OverrideErrors != nil {
				t.Fatalf("response_fields did not compile: %v", m.GraphQL.OverrideErrors)
			}
			if got := len(m.GraphQL.OverrideDocs["default"]); got != len(tc.overrides) {
				t.Fatalf("%d override documents, want %d", got, len(tc.overrides))
			}
			for key, doc := range m.GraphQL.OverrideDocs["default"] {
				check(key+" (response_fields)", doc)
			}
			if divergences != tc.divergences {
				t.Errorf("%d known T! = default divergences, want %d", divergences, tc.divergences)
			}
			t.Logf("%d documents validated independently (%d tools)", checked, len(m.Tools))
		})
	}
}

// The independent validator is a real check: it rejects documents that are
// wrong in the ways a builder bug would make them.
func TestIndependentValidatorRejectsBadDocuments(t *testing.T) {
	gs := ggSchema(t, builderCasesSDL, false)
	for name, doc := range map[string]string{
		"unknown field":         `query PlugIt_user($id: ID!) { user(id: $id) { id nope } }`,
		"missing selection":     `query PlugIt_user($id: ID!) { user(id: $id) }`,
		"scalar with selection": `query PlugIt_count { count { id } }`,
		"conflicting union":     `query PlugIt_things { things { ... on User { age } ... on Pet { age } } }`,
		"wrong variable type":   `query PlugIt_user($id: Int!) { user(id: $id) { id } }`,
		"unused variable":       `query PlugIt_count($x: Int) { count }`,
		"undeclared variable":   `query PlugIt_user { user(id: $id) { id } }`,
	} {
		if errs, _ := ggValidate(gs, doc); len(errs) == 0 {
			t.Errorf("%s: accepted %s", name, doc)
		}
	}
	// The known divergence is accepted only where the document declares T! = default.
	if !knownDivergence(`query Q($limit: Int! = 10) { count }`, "DefaultValuesOfCorrectType",
		`Variable "$limit" of type "Int!" is required and will not use the default value. Perhaps you meant to use type "Int".`) {
		t.Error("the documented divergence is not recognised")
	}
	if knownDivergence(`query Q($limit: Int = 10) { count }`, "DefaultValuesOfCorrectType",
		`Variable "$limit" of type "Int!" is required and will not use the default value.`) {
		t.Error("the divergence was accepted for a variable not declared T! = default")
	}
}
