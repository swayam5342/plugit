package curate

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
)

// OpChange is one operation that differs between two manifests.
type OpChange struct {
	Key    model.OpKey
	Old    *model.Tool // nil when added
	New    *model.Tool // nil when removed
	What   []string    // for changes: which aspects differ
	Listed []string    // toolsets that list this key, sorted
}

// SyncDiff is what `plugit sync` shows before adopting a new spec (SPEC
// §2.3 "Sync diff", T3).
type SyncDiff struct {
	Added, Removed, Changed     []OpChange // each sorted by key
	ServersBefore, ServersAfter []string
	ServersChanged              bool
	// BaseURLListed reports whether the new spec still declares the pinned
	// base_url among its servers. It's only meaningful when ServersChanged.
	BaseURLListed bool
}

// Empty reports whether nothing changed.
func (d SyncDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0 && !d.ServersChanged
}

// DiffManifests compares the manifest plugit has with a newly parsed one.
// Tools are matched by OpKey, their stable identity.
func DiffManifests(old, updated *model.Manifest, cfg config.API) SyncDiff {
	var d SyncDiff
	before := map[model.OpKey]*model.Tool{}
	for i := range old.Tools {
		before[old.Tools[i].Key] = &old.Tools[i]
	}
	after := map[model.OpKey]*model.Tool{}
	for i := range updated.Tools {
		after[updated.Tools[i].Key] = &updated.Tools[i]
	}
	listed := func(k model.OpKey) []string {
		var out []string
		for _, name := range slices.Sorted(maps.Keys(cfg.Toolsets)) {
			if _, ok := cfg.Toolsets[name].Tools[string(k)]; ok {
				out = append(out, name)
			}
		}
		return out
	}
	for k, n := range after {
		o, ok := before[k]
		switch {
		case !ok:
			d.Added = append(d.Added, OpChange{Key: k, New: n, Listed: listed(k)})
		default:
			if what := differences(o, n); len(what) > 0 {
				d.Changed = append(d.Changed, OpChange{Key: k, Old: o, New: n, What: what, Listed: listed(k)})
			}
		}
	}
	for k, o := range before {
		if _, ok := after[k]; !ok {
			d.Removed = append(d.Removed, OpChange{Key: k, Old: o, Listed: listed(k)})
		}
	}
	byKey := func(a, b OpChange) int { return cmp.Compare(a.Key, b.Key) }
	slices.SortFunc(d.Added, byKey)
	slices.SortFunc(d.Removed, byKey)
	slices.SortFunc(d.Changed, byKey)

	d.ServersBefore, d.ServersAfter = old.Servers, updated.Servers
	norm := func(ss []string) []string {
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = strings.TrimRight(s, "/")
		}
		return out
	}
	d.ServersChanged = !slices.Equal(norm(old.Servers), norm(updated.Servers))
	d.BaseURLListed = slices.Contains(norm(updated.Servers), strings.TrimRight(cfg.BaseURL, "/"))
	return d
}

// differences names what changed between two versions of one operation,
// in a fixed order.
func differences(o, n *model.Tool) []string {
	var out []string
	same := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	if o.Name != n.Name {
		out = append(out, "name")
	}
	if o.Description != n.Description {
		out = append(out, "description")
	}
	if !same(o.Params, n.Params) {
		out = append(out, "parameters")
	}
	if !same(o.Body, n.Body) {
		out = append(out, "request body")
	}
	if !slices.Equal(o.Tags, n.Tags) {
		out = append(out, "tags")
	}
	if o.Deprecated != n.Deprecated {
		out = append(out, "deprecated")
	}
	if !same(o.Issues, n.Issues) {
		out = append(out, "ingest issues")
	}
	return out
}

// AddToToolset lists keys in a toolset (creating it if needed). Keys
// already listed keep their overrides untouched: sync only ever adds.
func AddToToolset(cfg *config.API, toolset string, keys []model.OpKey) {
	if cfg.Toolsets == nil {
		cfg.Toolsets = map[string]config.Toolset{}
	}
	ts := cfg.Toolsets[toolset]
	ts.Tools = maps.Clone(ts.Tools)
	if ts.Tools == nil {
		ts.Tools = map[string]config.ToolOverride{}
	}
	for _, k := range keys {
		if _, ok := ts.Tools[string(k)]; !ok {
			ts.Tools[string(k)] = config.ToolOverride{}
		}
	}
	cfg.Toolsets[toolset] = ts
}
