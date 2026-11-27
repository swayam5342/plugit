package graphql

import (
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// selSDL is the user/User/Org fixture of the selection tests.
const selSDL = `
type User {
  id: ID!
  login: String!
  bio: String
  org: Org
}
type Org {
  id: ID!
  name: String!
  owner: User
}
type Query {
  user(id: ID!): User
  count: Int
  roles: [Role!]!
}
enum Role { ADMIN MEMBER }
`

func loadSDL(t *testing.T, sdl string) *ast.Schema {
	t.Helper()
	s, err := LoadSchema([]byte(sdl), FormatSDL)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	return s
}

// queryField returns the return type of Query.name.
func queryField(t *testing.T, s *ast.Schema, name string) *ast.Type {
	t.Helper()
	f := s.Query.Fields.ForName(name)
	if f == nil {
		t.Fatalf("no Query.%s", name)
	}
	return f.Type
}

// queryDoc wraps sel in a query on Query.field, passing the field's
// arguments as variables.
func queryDoc(s *ast.Schema, field string, sel Selection) string {
	f := s.Query.Fields.ForName(field)
	var vars, args []string
	for _, a := range f.Arguments {
		vars = append(vars, "$"+a.Name+": "+a.Type.String())
		args = append(args, a.Name+": $"+a.Name)
	}
	doc := "query Q"
	if len(vars) > 0 {
		doc += "(" + strings.Join(vars, ", ") + ")"
	}
	doc += " { " + field
	if len(args) > 0 {
		doc += "(" + strings.Join(args, ", ") + ")"
	}
	if r := sel.Render(); r != "" {
		doc += " " + r
	}
	return doc + " }"
}

// validateDoc validates sel under Query.field with gqlparser's default
// rules, returning the document and its errors.
func validateDoc(s *ast.Schema, field string, sel Selection) (string, gqlerror.List) {
	doc := queryDoc(s, field, sel)
	_, errs := gqlparser.LoadQueryWithRules(s, doc, rules.NewDefaultRules())
	return doc, errs
}

// assertValidDoc checks that sel under Query.field is a valid document.
func assertValidDoc(t *testing.T, s *ast.Schema, field string, sel Selection) {
	t.Helper()
	if doc, errs := validateDoc(s, field, sel); len(errs) > 0 {
		t.Errorf("document %q is invalid: %v", doc, errs)
	}
}

func TestDefaultSelectionDepth2(t *testing.T) {
	s := loadSDL(t, selSDL)
	sel, truncated := DefaultSelection(s, queryField(t, s, "user"), SelectionOpts{Depth: 2, MaxFields: 100})
	const want = "{ id login bio org { id name } }"
	if got := sel.Render(); got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	wantPaths := []string{"id", "login", "bio", "org.id", "org.name"}
	if got := sel.Paths(); !slices.Equal(got, wantPaths) {
		t.Errorf("Paths = %q, want %q", got, wantPaths)
	}
	assertValidDoc(t, s, "user", sel)
}

func TestDefaultSelectionDeterministic(t *testing.T) {
	s := loadSDL(t, string(readFixture(t, "user.graphql")))
	for _, f := range s.Query.Fields {
		if strings.HasPrefix(f.Name, "__") {
			continue
		}
		first, _ := DefaultSelection(s, f.Type, SelectionOpts{Depth: 3, MaxFields: 100})
		for range 5 {
			again, _ := DefaultSelection(s, f.Type, SelectionOpts{Depth: 3, MaxFields: 100})
			if first.Render() != again.Render() {
				t.Fatalf("%s: Render differs between calls: %q vs %q", f.Name, first.Render(), again.Render())
			}
		}
		assertValidDoc(t, s, f.Name, first)
	}
}

func TestLeafReturnNoSelectionSet(t *testing.T) {
	s := loadSDL(t, selSDL)
	for _, field := range []string{"count", "roles"} {
		t.Run(field, func(t *testing.T) {
			sel, truncated := DefaultSelection(s, queryField(t, s, field), SelectionOpts{Depth: 2, MaxFields: 100})
			if got := sel.Render(); got != "" {
				t.Errorf("Render = %q, want no selection set", got)
			}
			if truncated {
				t.Error("truncated = true, want false")
			}
			if p := sel.Paths(); len(p) != 0 {
				t.Errorf("Paths = %q, want none", p)
			}
			assertValidDoc(t, s, field, sel)
		})
	}
}

func TestAllArgFieldsSelectsTypename(t *testing.T) {
	s := loadSDL(t, `
type Secret {
  a(x: Int!): String
  b(y: String!, z: Int): Int
}
type Query { secret: Secret }
`)
	sel, truncated := DefaultSelection(s, queryField(t, s, "secret"), SelectionOpts{Depth: 2, MaxFields: 100})
	if got, want := sel.Render(), "{ __typename }"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	assertValidDoc(t, s, "secret", sel)
}

func TestSelectionSkipsArgFields(t *testing.T) {
	s := loadSDL(t, `
type User {
  id: ID!
  avatar(size: Int!): String
  posts(first: Int): Int
  tags(limit: Int! = 10): [String]
  name: String
}
type Query { me: User }
`)
	sel, _ := DefaultSelection(s, queryField(t, s, "me"), SelectionOpts{Depth: 2, MaxFields: 100})
	// avatar has a required argument; posts and tags have only optional ones.
	if got, want := sel.Render(), "{ id posts tags name }"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	assertValidDoc(t, s, "me", sel)
}

func TestSelectionAliasesConflictingFields(t *testing.T) {
	const sdl = `
interface Thing { name: String }
type User implements Thing { id: ID! name: String login: String }
type Org implements Thing { id: Int name: String }
union Any = User | Org
type Query { thing: Thing any: [Any!]! }
`
	s := loadSDL(t, sdl)
	// Possible types are sorted by name (Org before User, although User is
	// declared first); id differs in type so it is aliased per type, while
	// name has one type everywhere and is not.
	const want = "{ __typename ... on Org { id_Org: id name } ... on User { id_User: id name login } }"
	for _, field := range []string{"thing", "any"} {
		t.Run(field, func(t *testing.T) {
			sel, truncated := DefaultSelection(s, queryField(t, s, field), SelectionOpts{Depth: 2, MaxFields: 100})
			if got := sel.Render(); got != want {
				t.Errorf("Render = %q, want %q", got, want)
			}
			if truncated {
				t.Error("truncated = true, want false")
			}
			wantPaths := []string{"id_Org", "name", "id_User", "login"}
			if got := sel.Paths(); !slices.Equal(got, wantPaths) {
				t.Errorf("Paths = %q, want %q", got, wantPaths)
			}
			assertValidDoc(t, s, field, sel)
		})
	}
}

func TestSelectionAliasesNullabilityDifference(t *testing.T) {
	// [ID] and [ID!] are different types too, so the fields are aliased.
	s := loadSDL(t, `
union U = A | B
type A { ids: [ID] }
type B { ids: [ID!] }
type Query { u: U }
`)
	sel, _ := DefaultSelection(s, queryField(t, s, "u"), SelectionOpts{Depth: 2, MaxFields: 100})
	if got, want := sel.Render(), "{ __typename ... on A { ids_A: ids } ... on B { ids_B: ids } }"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	assertValidDoc(t, s, "u", sel)
}

func TestAliasCollisionSuffixed(t *testing.T) {
	tests := []struct {
		name, sdl, want string
	}{
		{
			name: "same fragment",
			sdl: `
union R = User | Org
type User { id: ID! id_User: String }
type Org { id: Int }
type Query { r: R }
`,
			want: "{ __typename ... on Org { id_Org: id } ... on User { id_User_2: id id_User } }",
		},
		{
			name: "other fragment",
			sdl: `
union R = User | Org
type User { id: ID! id_Org: Boolean }
type Org { id: Int }
type Query { r: R }
`,
			want: "{ __typename ... on Org { id_Org_2: id } ... on User { id_User: id id_Org } }",
		},
		{
			name: "suffix taken too",
			sdl: `
union R = User | Org
type User { id: ID! id_User: String id_User_2: String }
type Org { id: Int }
type Query { r: R }
`,
			want: "{ __typename ... on Org { id_Org: id } ... on User { id_User_3: id id_User id_User_2 } }",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := loadSDL(t, tt.sdl)
			sel, _ := DefaultSelection(s, queryField(t, s, "r"), SelectionOpts{Depth: 2, MaxFields: 100})
			if got := sel.Render(); got != tt.want {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
			assertValidDoc(t, s, "r", sel)
		})
	}
}

func TestSelectionDepthAndFieldCaps(t *testing.T) {
	s := loadSDL(t, selSDL)
	user := queryField(t, s, "user")
	tests := []struct {
		name      string
		opts      SelectionOpts
		want      string
		truncated bool
	}{
		{"max fields 3", SelectionOpts{Depth: 2, MaxFields: 3}, "{ id login bio }", true},
		// Breadth-first: all of depth 1 is spent before depth 2.
		{"max fields 5", SelectionOpts{Depth: 2, MaxFields: 5}, "{ id login bio org { id } }", true},
		{"max fields exact", SelectionOpts{Depth: 2, MaxFields: 6}, "{ id login bio org { id name } }", false},
		{"depth 1", SelectionOpts{Depth: 1, MaxFields: 100}, "{ id login bio }", false},
		{"depth 3", SelectionOpts{Depth: 3, MaxFields: 100}, "{ id login bio org { id name owner { id login bio } } }", false},
		{"depth 3 capped", SelectionOpts{Depth: 3, MaxFields: 8}, "{ id login bio org { id name owner { id } } }", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, truncated := DefaultSelection(s, user, tt.opts)
			if got := sel.Render(); got != tt.want {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
			if truncated != tt.truncated {
				t.Errorf("truncated = %v, want %v", truncated, tt.truncated)
			}
			if n := sel.fieldCount(); n > tt.opts.MaxFields {
				t.Errorf("%d fields selected, over MaxFields %d", n, tt.opts.MaxFields)
			}
			assertValidDoc(t, s, "user", sel)
		})
	}
}

func TestSelectionZeroOptsUseDefaults(t *testing.T) {
	s := loadSDL(t, selSDL)
	sel, _ := DefaultSelection(s, queryField(t, s, "user"), SelectionOpts{})
	if got, want := sel.Render(), "{ id login bio org { id name } }"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	assertValidDoc(t, s, "user", sel)
}

// An object return type with no selectable field (every field needs an
// argument, or is an object with nothing selectable) selects only
// __typename at the root: never an empty selection set.
func TestSelectionNoSelectableFields(t *testing.T) {
	for name, sdl := range map[string]string{
		"only required-arg fields": "type U { a(x: Int!): Int }\ntype Query { u: U }\n",
		"only dead-end objects":    "type E { a(x: Int!): Int }\ntype U { e: E }\ntype Query { u: U }\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := loadSDL(t, sdl)
			sel, truncated := DefaultSelection(s, queryField(t, s, "u"), SelectionOpts{})
			if got := sel.Render(); got != "{ __typename }" || truncated {
				t.Errorf("Render = %q (truncated %v), want { __typename }", got, truncated)
			}
			assertValidDoc(t, s, "u", sel)
		})
	}
}

func TestTruncationNeverEmptyObject(t *testing.T) {
	tests := []struct {
		name string
		sdl  string
		root string
		opts SelectionOpts
		want string
	}{
		// org is selected as the 4th field, then the budget runs out before
		// any of its fields: org is backed out.
		{"half-built object", selSDL, "user", SelectionOpts{Depth: 2, MaxFields: 4}, "{ id login bio }"},
		{"object first", `
type W { inner: User }
type User { id: ID! }
type Query { w: W }
`, "w", SelectionOpts{Depth: 2, MaxFields: 1}, "{ __typename }"},
		{"nested union at depth limit", `
union U = A | B
type A { a: Int }
type B { b: Int }
type Holder { id: ID! u: U }
type Query { h: Holder }
`, "h", SelectionOpts{Depth: 1, MaxFields: 100}, "{ id }"},
		{"object with only arg fields", `
type Holder { id: ID! s: Secret }
type Secret { a(x: Int!): Int }
type Query { h: Holder }
`, "h", SelectionOpts{Depth: 2, MaxFields: 100}, "{ id }"},
		{"union budget", `
union U = A | B
type A { a: Int }
type B { b: Int }
type Query { u: U }
`, "u", SelectionOpts{Depth: 2, MaxFields: 1}, "{ __typename ... on A { a } }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := loadSDL(t, tt.sdl)
			sel, _ := DefaultSelection(s, queryField(t, s, tt.root), tt.opts)
			got := sel.Render()
			if got != tt.want {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "{ }") || strings.Contains(got, "{}") {
				t.Errorf("Render %q contains an empty selection set", got)
			}
			assertValidDoc(t, s, tt.root, sel)
		})
	}
}

func TestSelectionCyclicTypeTerminates(t *testing.T) {
	s := loadSDL(t, `
type Node { id: ID! next: Node children: [Node!]! }
type Query { head: Node }
`)
	sel, truncated := DefaultSelection(s, queryField(t, s, "head"), SelectionOpts{Depth: 50, MaxFields: 40})
	if !truncated {
		t.Error("truncated = false, want true")
	}
	if n := sel.fieldCount(); n > 40 {
		t.Errorf("%d fields, over MaxFields 40", n)
	}
	assertValidDoc(t, s, "head", sel)
}
