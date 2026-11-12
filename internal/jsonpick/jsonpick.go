// Package jsonpick keeps selected fields of a JSON value (SPEC M6 design
// note: response field filtering). Paths are a JSONPath subset:
// dot-separated keys, each optionally followed by [*]; arrays are mapped
// over automatically, so "items.title" and "items[*].title" are the same.
// It is pure: no I/O.
package jsonpick

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Limits on a field list.
const (
	MaxPaths   = 32
	MaxPathLen = 256
)

// A segment is a key (anything but '.', '[', ']' and whitespace) with an
// optional [*], or a bare [*] for "each element".
var segmentRe = regexp.MustCompile(`^(\[\*\]|[^.\[\]\s]+(\[\*\])?)$`)

// Picker keeps the fields it was parsed from. A nil *Picker keeps
// everything.
type Picker struct {
	root  *node
	paths []string
}

type node struct {
	keep     bool // keep this whole value
	children map[string]*node
}

// Parse checks paths and builds a Picker. No paths means no filtering
// (a nil Picker).
func Parse(paths []string) (*Picker, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > MaxPaths {
		return nil, fmt.Errorf("at most %d response fields", MaxPaths)
	}
	root := &node{children: map[string]*node{}}
	for _, p := range paths {
		if err := Check(p); err != nil {
			return nil, err
		}
		n := root
		for seg := range strings.SplitSeq(p, ".") {
			key := strings.TrimSuffix(seg, "[*]")
			if key == "" {
				continue // a bare [*]: arrays map anyway
			}
			c := n.children[key]
			if c == nil {
				c = &node{children: map[string]*node{}}
				n.children[key] = c
			}
			n = c
		}
		n.keep = true
	}
	return &Picker{root: root, paths: paths}, nil
}

// Check validates one path.
func Check(path string) error {
	switch {
	case path == "":
		return errors.New("empty response field")
	case len(path) > MaxPathLen:
		return fmt.Errorf("response field longer than %d characters", MaxPathLen)
	}
	for seg := range strings.SplitSeq(path, ".") {
		if !segmentRe.MatchString(seg) {
			return fmt.Errorf("response field %q: use keys separated by dots, optionally with [*] (like items[*].title)", path)
		}
	}
	return nil
}

// Paths are the fields the Picker keeps.
func (p *Picker) Paths() []string {
	if p == nil {
		return nil
	}
	return p.paths
}

// Apply returns a copy of v with only the selected parts. Arrays are
// mapped over; scalars at a path that goes deeper are dropped.
func (p *Picker) Apply(v any) any {
	if p == nil {
		return v
	}
	if _, ok := v.(map[string]any); !ok {
		if _, ok := v.([]any); !ok {
			return v // a scalar document: nothing to select from
		}
	}
	out, _ := apply(v, p.root)
	return out
}

// apply projects v onto n. ok is false when nothing at all matched.
func apply(v any, n *node) (any, bool) {
	if n.keep {
		return v, true
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, c := range n.children {
			if cv, present := x[k]; present {
				if pv, ok := apply(cv, c); ok {
					out[k] = pv
				}
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			if pv, ok := apply(e, n); ok {
				out = append(out, pv)
			}
		}
		return out, true
	}
	return nil, false
}
