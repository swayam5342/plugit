// Package curate turns a parsed manifest and a toolset from plugit.yaml into
// the active toolset: which tools are served, under which names and
// descriptions, with which per-tool policy. It is pure: no I/O.
package curate

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
)

// ErrNameConflict means the final tool names clash or don't fit.
var ErrNameConflict = errors.New("tool name conflict")

// Selected is one active tool.
type Selected struct {
	Tool     model.Tool
	Override policy.Override
}

// Result is the active toolset.
type Result struct {
	Tools []Selected // in manifest order
	// Missing lists keys the toolset names that the manifest doesn't have
	// (the spec dropped or renamed the operation).
	Missing []model.OpKey
}

// Apply selects tools from all (a manifest's tools, already named with
// prefix) according to ts (SPEC §9 item 6):
//
//   - a tool is selected when its key is listed in ts.Tools;
//   - include_tags and methods narrow the selection, or, when no keys are
//     listed, select from the whole manifest;
//   - a toolset with neither selects nothing.
//
// A name override gets the prefix too (§9 item 7). Override descriptions are
// cleaned like spec text. Final names must be unique case-insensitively.
func Apply(all []model.Tool, ts config.Toolset, prefix string) (Result, error) {
	res := Select(all, ts, prefix)
	if err := CheckNames(res.Tools); err != nil {
		return Result{}, err
	}
	return res, nil
}

// Select is Apply without the final name check: clashing or overlong
// names are returned as they are (lint reports them all).
func Select(all []model.Tool, ts config.Toolset, prefix string) Result {
	var res Result
	listed := len(ts.Tools) > 0
	if !listed && len(ts.IncludeTags) == 0 && len(ts.Methods) == 0 {
		return res
	}

	present := make(map[model.OpKey]bool, len(all))
	for _, t := range all {
		present[t.Key] = true
		o, ok := ts.Tools[string(t.Key)]
		if listed && !ok {
			continue
		}
		if !matchesFilters(t, ts) {
			continue
		}
		if o.Name != "" {
			t.Name = o.Name
			if prefix != "" {
				t.Name = prefix + "_" + o.Name
			}
		}
		if o.Description != "" {
			t.Description = naming.CleanText(o.Description, naming.DefaultMaxDescription)
		}
		// config.Validate has checked the value; an unknown one fails closed
		// in policy.Check.
		res.Tools = append(res.Tools, Selected{Tool: t, Override: policy.Override(o.Policy)})
	}
	for key := range ts.Tools {
		if !present[model.OpKey(key)] {
			res.Missing = append(res.Missing, model.OpKey(key))
		}
	}
	slices.Sort(res.Missing)
	return res
}

func matchesFilters(t model.Tool, ts config.Toolset) bool {
	if len(ts.Methods) > 0 && !slices.ContainsFunc(ts.Methods, func(m string) bool { return strings.EqualFold(m, t.Method) }) {
		return false
	}
	if len(ts.IncludeTags) > 0 && !slices.ContainsFunc(t.Tags, func(tag string) bool {
		return slices.ContainsFunc(ts.IncludeTags, func(want string) bool { return strings.EqualFold(tag, want) })
	}) {
		return false
	}
	return true
}

// CheckNames reports the first name that is too long with the prefix or
// clashes with another (ignoring case).
func CheckNames(tools []Selected) error {
	seen := make(map[string]model.Tool, len(tools))
	for _, s := range tools {
		n := s.Tool.Name
		if len(n) > naming.MaxNameLen {
			return fmt.Errorf("%w: %s: name %q is longer than %d characters with the prefix", ErrNameConflict, s.Tool.Key, n, naming.MaxNameLen)
		}
		if other, dup := seen[strings.ToLower(n)]; dup {
			return fmt.Errorf("%w: %s (%q) and %s (%q) have the same name (names are compared ignoring case); rename one",
				ErrNameConflict, other.Key, other.Name, s.Tool.Key, n)
		}
		seen[strings.ToLower(n)] = s.Tool
	}
	return nil
}
