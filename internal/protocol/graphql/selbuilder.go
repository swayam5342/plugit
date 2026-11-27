package graphql

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// SelectionFromPaths compiles a toolset's response_fields into a selection
// (SPEC 3.5 P7). Each path is dot-separated field names ("org.name"), lists
// are followed, and every path must end on a scalar or enum field. Under an
// interface or union a path is compiled into the inline fragment of every
// possible type it is valid for, and is an error only if it is valid for
// none; field names the possible types declare with different types are
// aliased as in DefaultSelection. A field with a required argument cannot be
// selected. Duplicate and overlapping paths merge; schema order is kept. A
// path has at most maxPathSegments segments, and one call at most
// maxPathSteps compile steps.
//
// A scalar or enum return type takes no paths and yields no selection set;
// an object, interface or union return type needs at least one path.
func SelectionFromPaths(s *ast.Schema, returnType *ast.Type, paths []string) (Selection, error) {
	b := newSelBuilder(s)
	def := b.lookup(returnType)
	if def == nil {
		return Selection{}, errors.New("response_fields: unknown return type")
	}
	if !isComposite(def) {
		if len(paths) > 0 {
			return Selection{}, fmt.Errorf("response_fields path %q: return type %s has no fields", paths[0], def.Name)
		}
		return Selection{}, nil
	}
	if len(paths) == 0 {
		return Selection{}, fmt.Errorf("response_fields: no paths for return type %s", def.Name)
	}
	root := &selSet{}
	for _, p := range paths {
		segs := strings.Split(p, ".")
		if len(segs) > maxPathSegments {
			return Selection{}, fmt.Errorf("response_fields path %q: more than %d segments", p, maxPathSegments)
		}
		for _, seg := range segs {
			if !isName(seg) {
				return Selection{}, fmt.Errorf("response_fields path %q: %q is not a GraphQL field name", p, seg)
			}
		}
		set, err := b.pathSet(def, segs)
		if err != nil {
			return Selection{}, fmt.Errorf("response_fields path %q: %w", p, err)
		}
		merge(root, set)
	}
	return Selection{root: root}, nil
}

// pathSet builds the selection set for segs under def.
func (b *selBuilder) pathSet(def *ast.Definition, segs []string) (*selSet, error) {
	if def.Kind == ast.Object {
		return b.objectPath(def, nil, segs)
	}
	set := &selSet{typename: true}
	var firstErr error
	for _, m := range b.possibleTypes(def) {
		sub, err := b.objectPath(m, def, segs)
		if err != nil {
			if errors.Is(err, errPathTooLarge) {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		set.frags = append(set.frags, &selFrag{typeName: m.Name, set: sub})
	}
	if len(set.frags) == 0 {
		if firstErr == nil {
			return nil, fmt.Errorf("%s has no possible types", def.Name)
		}
		return nil, fmt.Errorf("no possible type of %s has it (%w)", def.Name, firstErr)
	}
	return set, nil
}

var errPathTooLarge = errors.New("paths expand to too many selections")

// objectPath builds the selection set for segs under the object type obj,
// which is a possible type of abstract when that is non-nil.
func (b *selBuilder) objectPath(obj, abstract *ast.Definition, segs []string) (*selSet, error) {
	b.steps++
	if b.steps > maxPathSteps {
		return nil, errPathTooLarge
	}
	name := segs[0]
	i := slices.IndexFunc(obj.Fields, func(f *ast.FieldDefinition) bool { return f != nil && f.Name == name })
	if i < 0 || strings.HasPrefix(name, "__") {
		return nil, fmt.Errorf("%s has no field %q", obj.Name, name)
	}
	f := obj.Fields[i]
	if requiresArg(f) {
		return nil, fmt.Errorf("%s.%s has a required argument", obj.Name, name)
	}
	sf := &selField{name: name, index: i}
	if abstract != nil {
		sf.alias = b.alias(abstract, obj.Name, name)
	}
	fdef := b.lookup(f.Type)
	switch {
	case fdef == nil:
		return nil, fmt.Errorf("%s.%s has an unknown type", obj.Name, name)
	case isLeaf(fdef):
		if len(segs) > 1 {
			return nil, fmt.Errorf("%s.%s is a %s and has no field %q", obj.Name, name, fdef.Name, segs[1])
		}
	case isComposite(fdef):
		if len(segs) == 1 {
			return nil, fmt.Errorf("%s.%s is a %s, not a scalar or enum: name a field under it", obj.Name, name, fdef.Name)
		}
		sub, err := b.pathSet(fdef, segs[1:])
		if err != nil {
			return nil, err
		}
		sf.sub = sub
	default:
		return nil, fmt.Errorf("%s.%s has a non-output type %s", obj.Name, name, fdef.Name)
	}
	return &selSet{fields: []*selField{sf}}, nil
}

// merge adds src's selections to dst, keeping schema order for fields and
// type-name order for fragments.
func merge(dst, src *selSet) {
	dst.typename = dst.typename || src.typename
	for _, sf := range src.fields {
		j, found := slices.BinarySearchFunc(dst.fields, sf.index, func(f *selField, idx int) int { return f.index - idx })
		if !found {
			dst.fields = slices.Insert(dst.fields, j, sf)
			continue
		}
		if df := dst.fields[j]; df.sub != nil && sf.sub != nil {
			merge(df.sub, sf.sub)
		}
	}
	for _, fr := range src.frags {
		j, found := slices.BinarySearchFunc(dst.frags, fr.typeName, func(f *selFrag, name string) int { return strings.Compare(f.typeName, name) })
		if !found {
			dst.frags = slices.Insert(dst.frags, j, fr)
			continue
		}
		merge(dst.frags[j].set, fr.set)
	}
}

// selBuilder caches per-type facts for one build. Its maps are lookups only;
// output order never comes from map iteration.
type selBuilder struct {
	s        *ast.Schema
	possible map[string][]*ast.Definition
	aliases  map[string]map[string]string // abstract type -> "Member.field" -> alias
	fillOK   map[fillKey]bool
	steps    int
}

type fillKey struct {
	name    string
	nesting bool
}

func newSelBuilder(s *ast.Schema) *selBuilder {
	return &selBuilder{
		s:        s,
		possible: map[string][]*ast.Definition{},
		aliases:  map[string]map[string]string{},
		fillOK:   map[fillKey]bool{},
	}
}

// lookup returns the definition of t's named type, unwrapping lists and
// non-null, or nil.
func (b *selBuilder) lookup(t *ast.Type) *ast.Definition {
	for t != nil && t.NamedType == "" {
		t = t.Elem
	}
	if t == nil || b.s == nil {
		return nil
	}
	return b.s.Types[t.NamedType]
}

// possibleTypes returns the object types of the interface or union def,
// without duplicates, sorted by name.
func (b *selBuilder) possibleTypes(def *ast.Definition) []*ast.Definition {
	if pts, ok := b.possible[def.Name]; ok {
		return pts
	}
	var pts []*ast.Definition
	if b.s != nil {
		seen := map[string]bool{}
		for _, m := range b.s.GetPossibleTypes(def) {
			if m != nil && m.Kind == ast.Object && !seen[m.Name] {
				seen[m.Name] = true
				pts = append(pts, m)
			}
		}
	}
	slices.SortFunc(pts, func(x, y *ast.Definition) int { return strings.Compare(x.Name, y.Name) })
	b.possible[def.Name] = pts
	return pts
}

// fillable reports whether a selection set of def could select anything: def
// (or, for an interface or union, one of its possible types) has a selectable
// scalar or enum field, or, when nesting is allowed, a selectable object,
// interface or union field. It looks one level down only.
func (b *selBuilder) fillable(def *ast.Definition, nesting bool) bool {
	key := fillKey{def.Name, nesting}
	if ok, cached := b.fillOK[key]; cached {
		return ok
	}
	members := []*ast.Definition{def}
	if def.Kind != ast.Object {
		members = b.possibleTypes(def)
	}
	ok := false
	for _, m := range members {
		for _, f := range m.Fields {
			if !selectable(f) {
				continue
			}
			if fd := b.lookup(f.Type); fd != nil && (isLeaf(fd) || (nesting && isComposite(fd))) {
				ok = true
				break
			}
		}
		if ok {
			break
		}
	}
	b.fillOK[key] = ok
	return ok
}

// alias returns the response alias of member.field inside a selection on the
// interface or union abstract, or "" for none.
func (b *selBuilder) alias(abstract *ast.Definition, member, field string) string {
	t, ok := b.aliases[abstract.Name]
	if !ok {
		t = b.aliasTable(abstract)
		b.aliases[abstract.Name] = t
	}
	return t[member+"."+field]
}

// aliasTable computes the aliases of a selection on the interface or union
// def. Two possible types selecting the same field name with different types
// (list and non-null wrappers included) cannot be merged by the server, so
// every possible type's field of that name gets the alias <field>_<Type>; if
// that is already a field name of a possible type or an earlier alias, it
// gets _2, _3, ... The table depends only on the schema, never on which
// fields a selection keeps, so every selection of def aliases alike.
func (b *selBuilder) aliasTable(def *ast.Definition) map[string]string {
	members := b.possibleTypes(def)
	firstType := map[string]string{}
	differs := map[string]bool{}
	used := map[string]bool{"__typename": true}
	for _, m := range members {
		for _, f := range m.Fields {
			if !selectable(f) {
				continue
			}
			used[f.Name] = true
			ts := typeString(f.Type)
			if prev, seen := firstType[f.Name]; !seen {
				firstType[f.Name] = ts
			} else if prev != ts {
				differs[f.Name] = true
			}
		}
	}
	table := map[string]string{}
	for _, m := range members {
		for _, f := range m.Fields {
			key := m.Name + "." + f.Name
			if !selectable(f) || !differs[f.Name] || table[key] != "" {
				continue
			}
			a := f.Name + "_" + m.Name
			for n := 2; used[a]; n++ {
				a = f.Name + "_" + m.Name + "_" + strconv.Itoa(n)
			}
			used[a] = true
			table[key] = a
		}
	}
	return table
}
