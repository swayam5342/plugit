package graphql

import (
	"slices"
	"strings"
	"testing"
)

func TestResponseFieldsCompiles(t *testing.T) {
	s := loadSDL(t, selSDL)
	user := queryField(t, s, "user")
	tests := []struct {
		name  string
		paths []string
		want  string
		ps    []string
	}{
		{"basic", []string{"id", "org.name"}, "{ id org { name } }", []string{"id", "org.name"}},
		{"merged in schema order", []string{"org.name", "id", "org.id", "id", "org.name"}, "{ id org { id name } }", []string{"id", "org.id", "org.name"}},
		{"through a cycle", []string{"org.owner.login"}, "{ org { owner { login } } }", []string{"org.owner.login"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := SelectionFromPaths(s, user, tt.paths)
			if err != nil {
				t.Fatalf("SelectionFromPaths: %v", err)
			}
			if got := sel.Render(); got != tt.want {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
			if got := sel.Paths(); !slices.Equal(got, tt.ps) {
				t.Errorf("Paths = %q, want %q", got, tt.ps)
			}
			assertValidDoc(t, s, "user", sel)
		})
	}
}

func TestResponseFieldsUnionPath(t *testing.T) {
	s := loadSDL(t, string(readFixture(t, "user.graphql")))
	tests := []struct {
		name  string
		field string
		paths []string
		want  string
	}{
		{"in every fragment", "search", []string{"id"}, "{ __typename ... on Org { id } ... on User { id } }"},
		{"in one fragment", "search", []string{"role", "org.name"}, "{ __typename ... on User { org { name } role } }"},
		{"typed differently", "search", []string{"name"}, "{ __typename ... on Org { name_Org: name } ... on User { name_User: name } }"},
		{"interface", "node", []string{"id", "email"}, "{ __typename ... on Org { id } ... on User { id email } }"},
		// Org.members takes only optional arguments, so it may be selected.
		{"optional-arg field", "search", []string{"members.id"}, "{ __typename ... on Org { members { id } } }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := SelectionFromPaths(s, queryField(t, s, tt.field), tt.paths)
			if err != nil {
				t.Fatalf("SelectionFromPaths: %v", err)
			}
			if got := sel.Render(); got != tt.want {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
			assertValidDoc(t, s, tt.field, sel)
		})
	}
}

func TestResponseFieldsPartialMatchInFragments(t *testing.T) {
	// owner.login compiles under A (owner: User has login) but not under B
	// (owner: Team has none), so it goes into A's fragment only.
	s := loadSDL(t, `
union U = A | B
type A { owner: User }
type B { owner: Team }
type User { login: String }
type Team { slug: String }
type Query { u: U }
`)
	sel, err := SelectionFromPaths(s, queryField(t, s, "u"), []string{"owner.login"})
	if err != nil {
		t.Fatalf("SelectionFromPaths: %v", err)
	}
	if got, want := sel.Render(), "{ __typename ... on A { owner_A: owner { login } } }"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
	assertValidDoc(t, s, "u", sel)
}

func TestResponseFieldsUnknownPathErrors(t *testing.T) {
	s := loadSDL(t, string(readFixture(t, "user.graphql")))
	tests := []struct {
		name, field, path string
	}{
		{"unknown field", "user", "nope"},
		{"unknown nested", "user", "org.nope"},
		{"leaf has no fields", "user", "id.x"},
		{"object is not a leaf", "user", "org"},
		{"union member object is not a leaf", "search", "org"},
		{"empty", "user", ""},
		{"empty segment", "user", "org..name"},
		{"trailing dot", "user", "org."},
		{"not a name", "user", "na-me"},
		{"meta field", "user", "__typename"},
		{"in no fragment", "search", "nope"},
		{"leaf return", "matrix", "x"},
		{"in no fragment, nested", "search", "members.login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := SelectionFromPaths(s, queryField(t, s, tt.field), []string{"id", tt.path})
			if tt.field == "matrix" {
				sel, err = SelectionFromPaths(s, queryField(t, s, tt.field), []string{tt.path})
			}
			if err == nil {
				t.Fatalf("SelectionFromPaths = %q, want an error", sel.Render())
			}
			if !strings.Contains(err.Error(), strings.TrimSpace(tt.path)) {
				t.Errorf("error %q does not name the path %q", err, tt.path)
			}
		})
	}
}

func TestResponseFieldsRequiredArgFieldErrors(t *testing.T) {
	s := loadSDL(t, `
type User { id: ID! avatar(size: Int!): String }
type Query { me: User }
`)
	if _, err := SelectionFromPaths(s, queryField(t, s, "me"), []string{"avatar"}); err == nil {
		t.Fatal("SelectionFromPaths(avatar) succeeded, want an error for a field with a required argument")
	}
}

func TestResponseFieldsNoPaths(t *testing.T) {
	s := loadSDL(t, selSDL)
	if _, err := SelectionFromPaths(s, queryField(t, s, "user"), nil); err == nil {
		t.Error("no paths on an object return: want an error")
	}
	sel, err := SelectionFromPaths(s, queryField(t, s, "count"), nil)
	if err != nil {
		t.Fatalf("no paths on a leaf return: %v", err)
	}
	if sel.Render() != "" {
		t.Errorf("Render = %q, want no selection set", sel.Render())
	}
}

func TestResponseFieldsExpansionCapped(t *testing.T) {
	// Every member of U has x: U, so "x.x.....y" would compile into
	// 10^depth fragments; the step cap turns it into an error instead.
	var sdl strings.Builder
	sdl.WriteString("union U =")
	for i := range 10 {
		if i > 0 {
			sdl.WriteString(" |")
		}
		sdl.WriteString(" M" + string(rune('0'+i)))
	}
	sdl.WriteString("\n")
	for i := range 10 {
		sdl.WriteString("type M" + string(rune('0'+i)) + " { x: U y: Int }\n")
	}
	sdl.WriteString("type Query { u: U }\n")
	s := loadSDL(t, sdl.String())
	path := strings.Repeat("x.", 20) + "y"
	_, err := SelectionFromPaths(s, queryField(t, s, "u"), []string{path})
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("err = %v, want the expansion cap", err)
	}
	// A short path through the same union still compiles.
	sel, err := SelectionFromPaths(s, queryField(t, s, "u"), []string{"x.y"})
	if err != nil {
		t.Fatalf("x.y: %v", err)
	}
	assertValidDoc(t, s, "u", sel)
}
