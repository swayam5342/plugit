package curate

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
)

func keys(cs []OpChange) []model.OpKey {
	var out []model.OpKey
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}

func syncFixture() (old, updated *model.Manifest, cfg config.API) {
	keep := tool("GET", "/pets", "gh_listPets", "pets")
	changed := tool("GET", "/pets/{id}", "gh_getPet", "pets")
	gone := tool("DELETE", "/pets/{id}", "gh_deletePet", "pets")
	old = &model.Manifest{Servers: []string{"https://api.example.test/v1"}, Tools: []model.Tool{keep, changed, gone}}

	changed2 := changed
	changed2.Description = "Get one pet. Now with more words."
	changed2.Params = []model.Param{{Name: "id", UpstreamName: "id", In: model.InPath, Required: true, Schema: json.RawMessage(`{"type":"integer"}`)}}
	changed2.Deprecated = true
	added := tool("POST", "/pets/{id}/photos", "gh_addPhoto", "pets")
	updated = &model.Manifest{Servers: []string{"https://evil.example.test"}, Tools: []model.Tool{keep, changed2, added}}

	cfg = config.API{
		BaseURL: "https://api.example.test/v1",
		Toolsets: map[string]config.Toolset{
			"default": {Tools: map[string]config.ToolOverride{
				"GET /pets":         {Name: "list", Description: "List pets.", RateLimit: "5/min"},
				"GET /pets/{id}":    {Policy: "confirm"},
				"DELETE /pets/{id}": {Policy: "deny"},
			}},
			"readonly": {Tools: map[string]config.ToolOverride{"GET /pets/{id}": {}}},
		},
	}
	return old, updated, cfg
}

func TestDiffManifests(t *testing.T) {
	old, updated, cfg := syncFixture()
	d := DiffManifests(old, updated, cfg)

	if got := keys(d.Added); !slices.Equal(got, []model.OpKey{"POST /pets/{id}/photos"}) {
		t.Errorf("added = %v", got)
	}
	if got := keys(d.Removed); !slices.Equal(got, []model.OpKey{"DELETE /pets/{id}"}) {
		t.Errorf("removed = %v", got)
	}
	if got := keys(d.Changed); !slices.Equal(got, []model.OpKey{"GET /pets/{id}"}) {
		t.Fatalf("changed = %v", got)
	}
	ch := d.Changed[0]
	if !slices.Equal(ch.What, []string{"description", "parameters", "deprecated"}) {
		t.Errorf("what = %v", ch.What)
	}
	if !slices.Equal(ch.Listed, []string{"default", "readonly"}) || !slices.Equal(d.Removed[0].Listed, []string{"default"}) || len(d.Added[0].Listed) != 0 {
		t.Errorf("listed: changed %v removed %v added %v", ch.Listed, d.Removed[0].Listed, d.Added[0].Listed)
	}
	if !d.ServersChanged || d.BaseURLListed || !slices.Equal(d.ServersAfter, []string{"https://evil.example.test"}) {
		t.Errorf("servers: changed=%v listed=%v after=%v", d.ServersChanged, d.BaseURLListed, d.ServersAfter)
	}
	if d.Empty() {
		t.Error("diff reported empty")
	}
	if !DiffManifests(old, old, cfg).Empty() {
		t.Error("identical manifests should give an empty diff")
	}
}

// Trailing slashes don't count as a server change, and a base_url the new
// spec still lists is reported as such.
func TestDiffServersNormalized(t *testing.T) {
	old := &model.Manifest{Servers: []string{"https://api.example.test/v1/"}}
	updated := &model.Manifest{Servers: []string{"https://api.example.test/v1", "https://mirror.example.test"}}
	d := DiffManifests(old, updated, config.API{BaseURL: "https://api.example.test/v1"})
	if !d.ServersChanged || !d.BaseURLListed {
		t.Errorf("changed=%v listed=%v", d.ServersChanged, d.BaseURLListed)
	}
	if DiffManifests(old, &model.Manifest{Servers: []string{"https://api.example.test/v1"}}, config.API{}).ServersChanged {
		t.Error("a trailing slash counted as a change")
	}
}

// Sync never removes or edits a toolset entry: it only adds the chosen new
// keys (SPEC M5 exit criterion: 100 % of overrides kept).
func TestAddToToolsetKeepsOverrides(t *testing.T) {
	_, _, cfg := syncFixture()
	before := map[string]config.Toolset{}
	for name, ts := range cfg.Toolsets {
		ts.Tools = maps.Clone(ts.Tools)
		before[name] = ts
	}
	AddToToolset(&cfg, "default", []model.OpKey{"POST /pets/{id}/photos", "GET /pets"})
	AddToToolset(&cfg, "fresh", []model.OpKey{"GET /pets"})

	def := cfg.Toolsets["default"].Tools
	if _, ok := def["POST /pets/{id}/photos"]; !ok {
		t.Error("new key not added")
	}
	for name, ts := range before {
		for k, o := range ts.Tools {
			if got := cfg.Toolsets[name].Tools[k]; !reflect.DeepEqual(got, o) {
				t.Errorf("%s %s: override %+v became %+v", name, k, o, got)
			}
		}
	}
	if _, ok := cfg.Toolsets["fresh"].Tools["GET /pets"]; !ok {
		t.Error("adding to a new toolset failed")
	}
}
