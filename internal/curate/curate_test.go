package curate

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

func tool(method, path, name string, tags ...string) model.Tool {
	return model.Tool{
		Key: model.NewOpKey(method, path), Name: name, Description: method + " " + path,
		Method: method, Path: path, Tags: tags, Safety: model.SafetyForMethod(method),
	}
}

var manifestTools = []model.Tool{
	tool("GET", "/pets", "gh_listPets", "pets"),
	tool("POST", "/pets", "gh_createPet", "pets"),
	tool("GET", "/pets/{id}", "gh_getPet", "pets"),
	tool("DELETE", "/pets/{id}", "gh_deletePet", "pets"),
	tool("GET", "/store/inventory", "gh_getInventory", "store"),
}

func names(r Result) []string {
	var out []string
	for _, s := range r.Tools {
		out = append(out, s.Tool.Name)
	}
	return out
}

func TestApplySelectsListedKeys(t *testing.T) {
	r, err := Apply(manifestTools, config.Toolset{Tools: map[string]config.ToolOverride{
		"GET /pets":         {},
		"DELETE /pets/{id}": {Policy: "deny"},
		"GET /gone":         {},
	}}, "gh")
	if err != nil {
		t.Fatal(err)
	}
	// Manifest order, not map order.
	if got := names(r); !slices.Equal(got, []string{"gh_listPets", "gh_deletePet"}) {
		t.Errorf("tools = %v", got)
	}
	if r.Tools[1].Override != policy.OverrideDeny {
		t.Errorf("override = %q", r.Tools[1].Override)
	}
	if !slices.Equal(r.Missing, []model.OpKey{"GET /gone"}) {
		t.Errorf("missing = %v", r.Missing)
	}
}

func TestApplyOverrides(t *testing.T) {
	r, err := Apply(manifestTools, config.Toolset{Tools: map[string]config.ToolOverride{
		"GET /pets": {Name: "list_pets", Description: "List pets.\u202e hidden <b>bold</b>"},
	}}, "gh")
	if err != nil {
		t.Fatal(err)
	}
	got := r.Tools[0].Tool
	// The prefix also applies to user-chosen names (SPEC §9 item 7).
	if got.Name != "gh_list_pets" {
		t.Errorf("name = %q", got.Name)
	}
	// Override descriptions are cleaned like spec text.
	if strings.ContainsRune(got.Description, '\u202e') || strings.Contains(got.Description, "<b>") {
		t.Errorf("description not cleaned: %q", got.Description)
	}
	if manifestTools[0].Name != "gh_listPets" {
		t.Error("Apply modified its input")
	}

	r, err = Apply(manifestTools, config.Toolset{Tools: map[string]config.ToolOverride{
		"GET /pets": {Name: "list_pets"},
	}}, "")
	if err != nil || r.Tools[0].Tool.Name != "list_pets" {
		t.Errorf("no prefix: %v %v", names(r), err)
	}
}

func TestApplyFilters(t *testing.T) {
	// Filters alone select from the whole manifest.
	r, err := Apply(manifestTools, config.Toolset{IncludeTags: []string{"PETS"}, Methods: []string{"get"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(r); !slices.Equal(got, []string{"gh_listPets", "gh_getPet"}) {
		t.Errorf("tools = %v", got)
	}
	// With listed keys, filters narrow them.
	r, _ = Apply(manifestTools, config.Toolset{
		Tools:   map[string]config.ToolOverride{"GET /pets": {}, "POST /pets": {}, "GET /store/inventory": {}},
		Methods: []string{"GET"}, IncludeTags: []string{"pets"},
	}, "")
	if got := names(r); !slices.Equal(got, []string{"gh_listPets"}) {
		t.Errorf("narrowed tools = %v", got)
	}
	// Neither keys nor filters: nothing is selected.
	r, _ = Apply(manifestTools, config.Toolset{}, "")
	if len(r.Tools) != 0 {
		t.Errorf("empty toolset selected %v", names(r))
	}
}

func TestApplyRejectsNameProblems(t *testing.T) {
	cases := map[string]struct {
		ts     config.Toolset
		prefix string
		want   string
	}{
		"rename onto another tool": {config.Toolset{Tools: map[string]config.ToolOverride{
			"GET /pets": {Name: "getPet"}, "GET /pets/{id}": {},
		}}, "gh", "gh_getPet"},
		"collision ignores case": {config.Toolset{Tools: map[string]config.ToolOverride{
			"GET /pets": {Name: "x"}, "GET /pets/{id}": {Name: "X"},
		}}, "", "x"},
		"prefixed name too long": {config.Toolset{Tools: map[string]config.ToolOverride{
			"GET /pets": {Name: strings.Repeat("a", 62)},
		}}, "gh", "64"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Apply(manifestTools, tc.ts, tc.prefix)
			if !errors.Is(err, ErrNameConflict) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want ErrNameConflict mentioning %q", err, tc.want)
			}
		})
	}
}
