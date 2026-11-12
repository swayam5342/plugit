package openapi

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/pb33f/go-yaml"
)

// checkRefs walks the parsed document once, without following YAML aliases,
// independently of libopenapi's own switches:
//
//   - local refs ("#/...") may chain through other pure-$ref nodes at most
//     MaxRefDepth hops, and may not form a cycle;
//   - anything else is external; it is counted (and never resolved);
//   - every media type under a "content" map must be an object, not a $ref;
//   - external $refs that are entries of other object maps (responses,
//     headers, links, examples, callbacks, ...) are replaced by empty
//     objects (see neutralizeExternalRefs).
//
// The last two avoid unrecoverable panics in libopenapi v0.42.5
// (pb33f/libopenapi#705); package isolate contains crashes nobody has found
// yet.
//
// The walk tracks whether a mapping's keys are spec keywords or user-chosen
// names, so a property or component that happens to be called "content" is
// not mistaken for a content map. Free-form data (x-*, example, default,
// enum, const, value) is skipped.
func checkRefs(root *yaml.Node) (external int, err error) {
	rc := &refChecker{root: root, ok: map[string]bool{}, index: map[*yaml.Node]map[string]*yaml.Node{}}
	type item struct {
		n     *yaml.Node
		names bool // keys are user-chosen names, not keywords
	}
	stack := []item{{n: root}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := it.n
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				stack = append(stack, item{n: c})
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if k.Value == "$ref" && v.Kind == yaml.ScalarNode {
					if !strings.HasPrefix(v.Value, "#") {
						external++
						continue
					}
					if err := rc.check(v.Value); err != nil {
						return external, err
					}
					continue
				}
				if it.names {
					stack = append(stack, item{n: v})
					continue
				}
				if freeForm(k.Value) {
					continue
				}
				if k.Value == "content" && v.Kind == yaml.MappingNode {
					if err := checkMediaTypes(v); err != nil {
						return external, err
					}
				}
				if objectMapKeys[k.Value] && v.Kind == yaml.MappingNode {
					external += neutralizeExternalRefs(v)
				}
				stack = append(stack, item{n: v, names: namesKeys[k.Value] && v.Kind == yaml.MappingNode})
			}
		case yaml.AliasNode:
			// Not followed: libopenapi bounds alias expansion, and walking
			// aliases here would re-create the expansion it guards against.
		}
	}
	return external, nil
}

// namesKeys are keywords whose mapping value is keyed by user-chosen names
// (paths, status codes, media types, component and property names).
var namesKeys = map[string]bool{
	"paths": true, "webhooks": true, "properties": true, "patternProperties": true,
	"$defs": true, "definitions": true, "dependentSchemas": true, "schemas": true,
	"responses": true, "parameters": true, "requestBodies": true, "headers": true,
	"examples": true, "links": true, "callbacks": true, "securitySchemes": true,
	"pathItems": true, "content": true, "encoding": true, "variables": true,
	"securityDefinitions": true, "mapping": true,
}

// objectMapKeys are names maps whose entries are reference-able objects other
// than schemas and media types (responses, headers, links, examples, ...).
var objectMapKeys = map[string]bool{
	"responses": true, "parameters": true, "requestBodies": true, "headers": true,
	"examples": true, "links": true, "callbacks": true, "securitySchemes": true,
	"pathItems": true, "encoding": true, "webhooks": true,
}

// neutralizeExternalRefs replaces each entry of m that is an external $ref
// with an empty object and returns how many it replaced. With
// SkipExternalRefResolution on, libopenapi v0.42.5 builds such entries as
// placeholders with a nil node store and then panics on its own goroutine
// (pb33f/libopenapi#705). PlugIt never resolves external refs, so an empty
// object loses nothing, and the rest of the spec stays usable.
func neutralizeExternalRefs(m *yaml.Node) int {
	n := 0
	for i := 1; i < len(m.Content); i += 2 {
		e := m.Content[i]
		if ref, isRef := pureRef(e); isRef && !strings.HasPrefix(ref, "#") {
			*e = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: e.Line, Column: e.Column}
			n++
		}
	}
	return n
}

func freeForm(key string) bool {
	switch key {
	case "example", "default", "enum", "const", "value":
		return true
	}
	return strings.HasPrefix(key, "x-")
}

func checkMediaTypes(content *yaml.Node) error {
	for i := 0; i+1 < len(content.Content); i += 2 {
		k, v := content.Content[i], content.Content[i+1]
		if v.Kind != yaml.MappingNode && v.Kind != yaml.AliasNode {
			return fmt.Errorf("%w: media type %q (line %d) must be an object", ErrInvalidSpec, truncateRef(k.Value), k.Line)
		}
		// Media Type Objects are not referenceable in OpenAPI 3.0/3.1, and
		// libopenapi v0.42.5 panics on them (pb33f/libopenapi#705).
		if _, isRef := pureRef(v); isRef {
			return fmt.Errorf("%w: media type %q (line %d) cannot be a $ref", ErrInvalidSpec, truncateRef(k.Value), k.Line)
		}
	}
	return nil
}

type refChecker struct {
	root  *yaml.Node
	ok    map[string]bool // refs already proven within limits
	index map[*yaml.Node]map[string]*yaml.Node
}

func (rc *refChecker) check(ref string) error {
	if rc.ok[ref] {
		return nil
	}
	seen := map[string]bool{ref: true}
	cur := ref
	for hops := 1; ; hops++ {
		target := rc.resolve(cur)
		next, isRef := pureRef(target)
		if !isRef || !strings.HasPrefix(next, "#") {
			break
		}
		if seen[next] {
			return fmt.Errorf("%w: %s", ErrRefCycle, truncateRef(ref))
		}
		if hops > MaxRefDepth {
			return fmt.Errorf("%w: more than %d hops from %s", ErrRefTooDeep, MaxRefDepth, truncateRef(ref))
		}
		seen[next] = true
		cur = next
	}
	for r := range seen {
		rc.ok[r] = true
	}
	return nil
}

// pureRef reports whether n is a mapping with a scalar $ref.
func pureRef(n *yaml.Node) (string, bool) {
	if n == nil || n.Kind != yaml.MappingNode {
		return "", false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "$ref" && n.Content[i+1].Kind == yaml.ScalarNode {
			return n.Content[i+1].Value, true
		}
	}
	return "", false
}

// resolve follows a local JSON pointer ("#/a/b~1c/0") through the tree.
func (rc *refChecker) resolve(ref string) *yaml.Node {
	ptr := strings.TrimPrefix(ref, "#")
	n := rc.root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if ptr == "" || ptr == "/" {
		return n
	}
	for seg := range strings.SplitSeq(strings.TrimPrefix(ptr, "/"), "/") {
		if s, err := url.PathUnescape(seg); err == nil {
			seg = s
		}
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		if n.Kind == yaml.AliasNode && n.Alias != nil {
			n = n.Alias
		}
		switch n.Kind {
		case yaml.MappingNode:
			n = rc.lookup(n, seg)
		case yaml.SequenceNode:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
		default:
			return nil
		}
		if n == nil {
			return nil
		}
	}
	return n
}

func (rc *refChecker) lookup(m *yaml.Node, key string) *yaml.Node {
	idx, ok := rc.index[m]
	if !ok {
		idx = make(map[string]*yaml.Node, len(m.Content)/2)
		for i := 0; i+1 < len(m.Content); i += 2 {
			if _, dup := idx[m.Content[i].Value]; !dup {
				idx[m.Content[i].Value] = m.Content[i+1]
			}
		}
		rc.index[m] = idx
	}
	return idx[key]
}

func truncateRef(ref string) string {
	if len(ref) > 120 {
		return ref[:120] + "…"
	}
	return ref
}

// checkV2PathItems rejects Swagger 2.0 path items with a scalar value that
// contains "$ref". libopenapi v0.42.5 treats any node of a v2 path item
// containing "$ref" as a reference and reads the node after it, which is
// past the end when that node is the item's last value: a panic on its own
// goroutine that recover can't catch (v2/path_item.go:83). v2 path items
// have no string fields except $ref, so these values are invalid anyway.
func checkV2PathItems(root *yaml.Node) error {
	doc := root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "paths" || doc.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		paths := doc.Content[i+1]
		for j := 0; j+1 < len(paths.Content); j += 2 {
			item := paths.Content[j+1]
			if item.Kind != yaml.MappingNode {
				continue
			}
			for k := 0; k+1 < len(item.Content); k += 2 {
				key, val := item.Content[k], item.Content[k+1]
				if key.Value != "$ref" && val.Kind == yaml.ScalarNode && strings.Contains(strings.ToLower(val.Value), "$ref") {
					return fmt.Errorf("%w: path %q (line %d): %q must be an object", ErrInvalidSpec, truncateRef(paths.Content[j].Value), key.Line, truncateRef(key.Value))
				}
			}
		}
	}
	return nil
}
