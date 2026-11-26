package graphql

import (
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// Selection defaults (SPEC 3.5 P7: graphql.selection_depth and
// graphql.max_fields). DefaultSelection uses them for a zero or negative
// SelectionOpts value.
const (
	DefaultSelectionDepth = 2
	DefaultMaxFields      = 100
)

// maxPathSteps bounds the work of one SelectionFromPaths call. A path through
// nested unions is compiled into every member, so on a hostile schema a short
// path could otherwise expand exponentially (T18).
const maxPathSteps = 10000

// SelectionOpts caps a default selection. Depth is the number of field
// levels selected (the return type's own fields are depth 1); MaxFields is
// the number of field selections, leaf or object, that may be emitted
// (__typename is structural and not counted).
type SelectionOpts struct {
	Depth, MaxFields int
}

// Selection is the selection set of one generated GraphQL operation: what
// the server sends back. The zero value is "no selection set", which is what
// a scalar or enum return type needs.
type Selection struct {
	root *selSet
}

type selSet struct {
	typename bool        // select __typename (abstract types, or a root with nothing else)
	fields   []*selField // in schema order
	frags    []*selFrag  // inline fragments, sorted by type name
}

type selField struct {
	name  string
	alias string  // "" when the response name is the field name
	index int     // position in the parent type's field list, for schema order
	sub   *selSet // nil for a scalar or enum field
}

type selFrag struct {
	typeName string
	set      *selSet
}

// Render returns the selection set as GraphQL source, e.g.
// "{ id login org { id name } }", or "" when the return type is a scalar or
// enum. The output is stable: fields keep schema order and inline fragments
// are sorted by type name.
func (sel Selection) Render() string {
	if sel.root == nil {
		return ""
	}
	var b strings.Builder
	writeSet(&b, sel.root)
	return b.String()
}

func writeSet(b *strings.Builder, set *selSet) {
	b.WriteString("{")
	if set.typename {
		b.WriteString(" __typename")
	}
	for _, f := range set.fields {
		b.WriteByte(' ')
		if f.alias != "" {
			b.WriteString(f.alias)
			b.WriteString(": ")
		}
		b.WriteString(f.name)
		if f.sub != nil {
			b.WriteByte(' ')
			writeSet(b, f.sub)
		}
	}
	for _, fr := range set.frags {
		b.WriteString(" ... on ")
		b.WriteString(fr.typeName)
		b.WriteByte(' ')
		writeSet(b, fr.set)
	}
	b.WriteString(" }")
}

// Paths returns the dotted paths of the scalar and enum fields the selection
// selects, in render order without duplicates, e.g.
// ["id", "login", "org.id", "org.name"]. Each segment is the response name
// (the alias when there is one), so a path names a key of the JSON response.
// Inline fragments add no segment of their own: a field selected in several
// fragments under the same response name is listed once. __typename is not
// listed.
func (sel Selection) Paths() []string {
	if sel.root == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	var walk func(set *selSet, prefix string)
	walk = func(set *selSet, prefix string) {
		for _, f := range set.fields {
			p := prefix + f.responseName()
			if f.sub != nil {
				walk(f.sub, p+".")
				continue
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		for _, fr := range set.frags {
			walk(fr.set, prefix)
		}
	}
	walk(sel.root, "")
	return out
}

// fieldCount is the number of field selections (leaf and object), the unit
// MaxFields counts.
func (sel Selection) fieldCount() int {
	var count func(set *selSet) int
	count = func(set *selSet) int {
		if set == nil {
			return 0
		}
		n := 0
		for _, f := range set.fields {
			n += 1 + count(f.sub)
		}
		for _, fr := range set.frags {
			n += count(fr.set)
		}
		return n
	}
	return count(sel.root)
}

func (f *selField) responseName() string {
	if f.alias != "" {
		return f.alias
	}
	return f.name
}

// DefaultSelection builds the default selection for a field returning
// returnType (SPEC 3.5 P7): scalar and enum fields of the return type, then
// fields of nested objects, down to opts.Depth. Lists are followed.
//
//   - A scalar or enum return type gets no selection set.
//   - A field with a required argument (non-null, no default) is skipped; one
//     whose arguments are all optional is selected without arguments. A return
//     type left with nothing selectable selects only __typename.
//   - An interface or union selects __typename plus one inline fragment per
//     possible object type, sorted by name, each built at the same depth. A
//     field name the possible types declare with different types is aliased
//     per type (see aliasTable).
//   - The opts.MaxFields budget is spent breadth-first, level by level, so
//     truncation drops the deepest fields first; rendering keeps schema
//     order. truncated reports that at least one selectable field was dropped
//     for the budget (lint L020).
//   - No selection set is ever empty: an object, interface or union field
//     whose sub-selection would be empty (depth limit, budget, or nothing
//     selectable) is left out.
//
// Zero or negative opts fields use DefaultSelectionDepth and
// DefaultMaxFields. The result is deterministic.
func DefaultSelection(s *ast.Schema, returnType *ast.Type, opts SelectionOpts) (sel Selection, truncated bool) {
	b := newSelBuilder(s)
	def := b.lookup(returnType)
	if def == nil || !isComposite(def) {
		return Selection{}, false
	}
	if opts.Depth <= 0 {
		opts.Depth = DefaultSelectionDepth
	}
	if opts.MaxFields <= 0 {
		opts.MaxFields = DefaultMaxFields
	}

	type item struct {
		set   *selSet
		def   *ast.Definition
		depth int
	}
	budget := opts.MaxFields
	root := &selSet{}
	queue := []item{{root, def, 1}}

	// fill adds the selectable fields of obj (a fragment of abstract when
	// that is non-nil) to set and queues their sub-selections. It returns
	// false once the budget is spent and a selectable field is left over.
	fill := func(set *selSet, obj, abstract *ast.Definition, depth int) bool {
		for i, f := range obj.Fields {
			if !selectable(f) {
				continue
			}
			fdef := b.lookup(f.Type)
			switch {
			case fdef == nil:
				continue
			case isLeaf(fdef):
			case isComposite(fdef) && depth < opts.Depth && b.fillable(fdef, depth+1 < opts.Depth):
			default:
				continue
			}
			if budget == 0 {
				return false
			}
			budget--
			sf := &selField{name: f.Name, index: i}
			if abstract != nil {
				sf.alias = b.alias(abstract, obj.Name, f.Name)
			}
			if !isLeaf(fdef) {
				sf.sub = &selSet{}
				queue = append(queue, item{sf.sub, fdef, depth + 1})
			}
			set.fields = append(set.fields, sf)
		}
		return true
	}

	for len(queue) > 0 && !truncated {
		it := queue[0]
		queue = queue[1:]
		if it.def.Kind == ast.Object {
			truncated = !fill(it.set, it.def, nil, it.depth)
			continue
		}
		it.set.typename = true
		for _, m := range b.possibleTypes(it.def) {
			fr := &selFrag{typeName: m.Name, set: &selSet{}}
			it.set.frags = append(it.set.frags, fr)
			if !fill(fr.set, m, it.def, it.depth) {
				truncated = true
				break
			}
		}
	}

	if !prune(root) {
		root.typename = true
	}
	return Selection{root: root}, truncated
}

// prune drops object fields and fragments whose selection set ended up
// empty, bottom-up, and reports whether set selects any field.
func prune(set *selSet) bool {
	fields := set.fields[:0]
	for _, f := range set.fields {
		if f.sub == nil || prune(f.sub) {
			fields = append(fields, f)
		}
	}
	set.fields = fields
	frags := set.frags[:0]
	for _, fr := range set.frags {
		if prune(fr.set) {
			frags = append(frags, fr)
		}
	}
	set.frags = frags
	return len(set.fields) > 0 || len(set.frags) > 0
}

func typeString(t *ast.Type) string {
	var b strings.Builder
	var write func(t *ast.Type)
	write = func(t *ast.Type) {
		switch {
		case t == nil:
		case t.NamedType != "":
			b.WriteString(t.NamedType)
		default:
			b.WriteByte('[')
			write(t.Elem)
			b.WriteByte(']')
		}
		if t != nil && t.NonNull {
			b.WriteByte('!')
		}
	}
	write(t)
	return b.String()
}

// selectable reports whether f may be selected without arguments: it is not
// a meta field and has no required argument.
func selectable(f *ast.FieldDefinition) bool {
	return f != nil && !strings.HasPrefix(f.Name, "__") && !requiresArg(f)
}

// requiresArg reports whether f has an argument that is non-null with no
// default value.
func requiresArg(f *ast.FieldDefinition) bool {
	for _, a := range f.Arguments {
		if a != nil && a.Type != nil && a.Type.NonNull && a.DefaultValue == nil {
			return true
		}
	}
	return false
}

func isLeaf(def *ast.Definition) bool {
	return def.Kind == ast.Scalar || def.Kind == ast.Enum
}

func isComposite(def *ast.Definition) bool {
	return def.Kind == ast.Object || def.Kind == ast.Interface || def.Kind == ast.Union
}
