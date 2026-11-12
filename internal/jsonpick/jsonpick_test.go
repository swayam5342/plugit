package jsonpick

import (
	"encoding/json"
	"strings"
	"testing"
)

func pick(t *testing.T, doc string, paths ...string) string {
	t.Helper()
	p, err := Parse(paths)
	if err != nil {
		t.Fatalf("Parse(%q): %v", paths, err)
	}
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(p.Apply(v))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestApply(t *testing.T) {
	repo := `{"id":1,"name":"plugit","owner":{"login":"me","id":9,"avatar":"x"},"topics":["go","mcp"],
		"items":[{"title":"a","body":"long"},{"title":"b","body":"long"}],"license":null}`
	cases := []struct {
		name  string
		doc   string
		paths []string
		want  string
	}{
		{"top-level keys", repo, []string{"id", "name"}, `{"id":1,"name":"plugit"}`},
		{"nested key", repo, []string{"owner.login"}, `{"owner":{"login":"me"}}`},
		{"whole subtree", repo, []string{"owner"}, `{"owner":{"avatar":"x","id":9,"login":"me"}}`},
		{"arrays map automatically", repo, []string{"items.title"}, `{"items":[{"title":"a"},{"title":"b"}]}`},
		{"explicit [*]", repo, []string{"items[*].title"}, `{"items":[{"title":"a"},{"title":"b"}]}`},
		{"array of scalars kept whole", repo, []string{"topics"}, `{"topics":["go","mcp"]}`},
		{"missing paths are absent", repo, []string{"nope", "owner.nope", "id"}, `{"id":1,"owner":{}}`},
		{"null kept", repo, []string{"license"}, `{"license":null}`},
		{"top-level array", `[{"id":1,"x":2},{"id":3,"x":4}]`, []string{"id"}, `[{"id":1},{"id":3}]`},
		{"top-level [*]", `[{"id":1,"x":2}]`, []string{"[*].id"}, `[{"id":1}]`},
		{"merged paths", repo, []string{"owner.login", "owner.id"}, `{"owner":{"id":9,"login":"me"}}`},
		{"scalar root unchanged", `"just text"`, []string{"id"}, `"just text"`},
		{"nested arrays", `{"a":[[{"b":1,"c":2}]]}`, []string{"a.b"}, `{"a":[[{"b":1}]]}`},
		{"keys with odd characters", `{"$ref":1,"@id":2,"a-b":3,"x":4}`, []string{"$ref", "@id", "a-b"}, `{"$ref":1,"@id":2,"a-b":3}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pick(t, tc.doc, tc.paths...); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	bad := [][]string{
		{""},
		{"a..b"},
		{".a"},
		{"a."},
		{"a[0]"},
		{"a[*"},
		{"a b"},
		{"a[*]x"},
		{strings.Repeat("a", MaxPathLen+1)},
	}
	for _, paths := range bad {
		if _, err := Parse(paths); err == nil {
			t.Errorf("Parse(%q) accepted", paths)
		}
	}
	tooMany := make([]string, MaxPaths+1)
	for i := range tooMany {
		tooMany[i] = "a"
	}
	if _, err := Parse(tooMany); err == nil {
		t.Error("more than MaxPaths accepted")
	}
	if p, err := Parse(nil); err != nil || p != nil {
		t.Errorf("Parse(nil) = %v, %v; want nil, nil (no filtering)", p, err)
	}
}

// Apply never changes its input.
func TestApplyDoesNotMutate(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"a":{"b":1,"c":2}}`), &v)
	p, _ := Parse([]string{"a.b"})
	p.Apply(v)
	if out, _ := json.Marshal(v); string(out) != `{"a":{"b":1,"c":2}}` {
		t.Errorf("input changed: %s", out)
	}
}
