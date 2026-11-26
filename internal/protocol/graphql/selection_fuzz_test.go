package graphql

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

// FuzzSelection builds default and path selections for every Query field of a
// fuzzed schema. The builder must never panic, never exceed its caps, never
// emit an empty selection set, and only produce documents gqlparser accepts.
func FuzzSelection(f *testing.F) {
	f.Add(selSDL, uint8(2), uint8(100), "id,org.name")
	f.Add(selSDL, uint8(3), uint8(5), "org.owner.login")
	f.Add(`
interface Thing { name: String }
type User implements Thing { id: ID! name: String login: String id_Org: Boolean }
type Org implements Thing { id: Int name: String }
union Any = User | Org
type Query { thing: Thing any: [Any!]! }
`, uint8(2), uint8(4), "id,name,login")
	f.Add(`
type Node { id: ID! next: Node children: [Node!]! }
type Query { head: Node }
`, uint8(9), uint8(10), "next.next.id")
	f.Add(`
type Secret { a(x: Int!): String }
type Query { secret: Secret count: Int }
`, uint8(1), uint8(1), "a")
	f.Add(`
union U = A | B
type A { owner: User }
type B { owner: Team }
type User { login: String }
type Team { slug: String }
type Query { u: U }
`, uint8(3), uint8(2), "owner.login,owner.slug")
	f.Fuzz(func(t *testing.T, sdl string, depth, maxFields uint8, paths string) {
		if len(sdl) > 4096 || len(paths) > 512 {
			return
		}
		s, err := LoadSchema([]byte(sdl), FormatSDL)
		if err != nil {
			return
		}
		// Small caps keep each iteration fast.
		opts := SelectionOpts{Depth: 1 + int(depth%5), MaxFields: 1 + int(maxFields%20)}
		pathList := strings.Split(paths, ",")
		for i, fd := range s.Query.Fields {
			if i >= 16 {
				break
			}
			if strings.HasPrefix(fd.Name, "__") {
				continue
			}
			sel, _ := DefaultSelection(s, fd.Type, opts)
			again, _ := DefaultSelection(s, fd.Type, opts)
			checkFuzzSelection(t, s, fd.Name, sel, opts.MaxFields)
			if sel.Render() != again.Render() {
				t.Fatalf("%s: non-deterministic: %q vs %q", fd.Name, sel.Render(), again.Render())
			}
			if psel, err := SelectionFromPaths(s, fd.Type, pathList); err == nil {
				checkFuzzSelection(t, s, fd.Name, psel, -1)
			}
		}
	})
}

func checkFuzzSelection(t *testing.T, s *ast.Schema, field string, sel Selection, maxFields int) {
	t.Helper()
	r := sel.Render()
	if strings.Contains(r, "{ }") || strings.Contains(r, "{}") {
		t.Fatalf("%s: empty selection set in %q", field, r)
	}
	if maxFields >= 0 && sel.fieldCount() > maxFields {
		t.Fatalf("%s: %d fields, over MaxFields %d: %q", field, sel.fieldCount(), maxFields, r)
	}
	if doc, errs := validateDoc(s, field, sel); len(errs) > 0 {
		t.Fatalf("%s: invalid document %q: %v", field, doc, errs)
	}
}
