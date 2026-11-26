package graphql

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

func build(t *testing.T, sdl string, opts ingest.Options) *model.Manifest {
	t.Helper()
	m, err := Build(loadSDL(t, sdl), FormatSDL, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func userFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/user.graphql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func toolByKey(t *testing.T, m *model.Manifest, key model.OpKey) model.Tool {
	t.Helper()
	for _, tl := range m.Tools {
		if tl.Key == key {
			return tl
		}
	}
	t.Fatalf("no tool %q among %v", key, toolKeys(m))
	return model.Tool{}
}

func toolKeys(m *model.Manifest) []model.OpKey {
	var out []model.OpKey
	for _, tl := range m.Tools {
		out = append(out, tl.Key)
	}
	return out
}

func TestBuildQueryTool(t *testing.T) {
	sdl := userFixture(t)
	m := build(t, sdl, ingest.Options{})
	if m.Protocol != model.GraphQL || m.Format != FormatSDL {
		t.Errorf("protocol/format = %q/%q", m.Protocol, m.Format)
	}
	tl := toolByKey(t, m, "query user")
	if tl.Name != "user" || tl.ID != naming.ToolID("query user") {
		t.Errorf("name/id = %q/%q", tl.Name, tl.ID)
	}
	if tl.Description != "Fetch one user." {
		t.Errorf("description = %q", tl.Description)
	}
	if !slices.Equal(tl.Tags, []string{"query"}) || tl.Safety != model.ReadOnly || !tl.Idempotent {
		t.Errorf("tags/safety/idempotent = %v/%v/%v", tl.Tags, tl.Safety, tl.Idempotent)
	}
	want := []model.Param{{
		Name: "id", Bind: model.Binding{In: model.InVariable, Upstream: "id"},
		Required: true, Schema: []byte(`{"type":"string"}`),
	}}
	if !reflect.DeepEqual(tl.Params, want) {
		t.Errorf("params = %+v, want %+v", tl.Params, want)
	}
	const doc = `query PlugIt_user($id: ID!) { user(id: $id) { id name email createdAt org { id name } role } }`
	if tl.GraphQL == nil || tl.GraphQL.Document != doc {
		t.Fatalf("document = %+v\nwant %s", tl.GraphQL, doc)
	}
	if tl.GraphQL.Operation != "query" || tl.GraphQL.Field != "user" {
		t.Errorf("op = %+v", tl.GraphQL)
	}
	wantSel := []string{"id", "name", "email", "createdAt", "org.id", "org.name", "role"}
	if !slices.Equal(tl.GraphQL.Selection, wantSel) {
		t.Errorf("selection = %v, want %v", tl.GraphQL.Selection, wantSel)
	}
	if tl.HTTP != nil || tl.GRPC != nil {
		t.Error("a GraphQL tool must not carry HTTP or gRPC ops")
	}
}

// TestBuildDocumentsValidate checks every document of the fixture against the
// schema and pins the no-argument and leaf forms.
func TestBuildDocumentsValidate(t *testing.T) {
	s := loadSDL(t, userFixture(t))
	m, err := Build(s, FormatSDL, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 8 {
		t.Errorf("got %d tools, want 8: %v", len(m.Tools), toolKeys(m))
	}
	for _, tl := range m.Tools {
		if _, errs := gqlparser.LoadQueryWithRules(s, tl.GraphQL.Document, rules.NewDefaultRules()); errs != nil {
			t.Errorf("%s: %v\n%s", tl.Key, errs, tl.GraphQL.Document)
		}
	}
	if d := toolByKey(t, m, "query matrix").GraphQL.Document; d != "query PlugIt_matrix { matrix }" {
		t.Errorf("matrix document = %q", d)
	}
	if !slices.IsSortedFunc(m.Tools, func(a, b model.Tool) int { return strings.Compare(string(a.Key), string(b.Key)) }) {
		t.Errorf("tools not sorted by key: %v", toolKeys(m))
	}
}

func TestBuildDeclaresVariableDefault(t *testing.T) {
	m := build(t, `type Query { search(term: String, first: Int! = 30): [String!]! }`, ingest.Options{})
	tl := toolByKey(t, m, "query search")
	const doc = `query PlugIt_search($term: String, $first: Int! = 30) { search(term: $term, first: $first) }`
	if tl.GraphQL.Document != doc {
		t.Errorf("document = %q\nwant       %q", tl.GraphQL.Document, doc)
	}
	if len(tl.Params) != 2 || tl.Params[0].Required || tl.Params[1].Required {
		t.Errorf("params = %+v, want two optional", tl.Params)
	}
	if tl.Params[1].Name != "first" || tl.Params[1].Bind.In != model.InVariable {
		t.Errorf("param 1 = %+v", tl.Params[1])
	}
}

func TestBuildMutationSafety(t *testing.T) {
	m := build(t, `type Query { a: Int } type Mutation { deleteRepository(id: ID!): Boolean createRepository(name: String!): ID }`, ingest.Options{})
	del := toolByKey(t, m, "mutation deleteRepository")
	if del.Safety != model.Destructive || del.Idempotent || !slices.Equal(del.Tags, []string{"mutation"}) {
		t.Errorf("deleteRepository safety/idempotent/tags = %v/%v/%v", del.Safety, del.Idempotent, del.Tags)
	}
	if d := del.GraphQL.Document; d != `mutation PlugIt_deleteRepository($id: ID!) { deleteRepository(id: $id) }` {
		t.Errorf("document = %q", d)
	}
	if c := toolByKey(t, m, "mutation createRepository"); c.Safety != model.Write || c.Idempotent {
		t.Errorf("createRepository safety/idempotent = %v/%v", c.Safety, c.Idempotent)
	}
}

func TestBuildSubscriptionListedUnsupported(t *testing.T) {
	m := build(t, `type Query { a: Int } type Subscription { onEvent: String }`, ingest.Options{})
	for _, tl := range m.Tools {
		if tl.GraphQL.Field == "onEvent" {
			t.Errorf("subscription became tool %s", tl.Key)
		}
	}
	const w = `subscription "onEvent": subscriptions aren't supported yet (L013)`
	if !slices.Contains(m.Warnings, w) {
		t.Errorf("warnings = %q, want %q", m.Warnings, w)
	}
}

func TestBuildQueryBeatsMutationForName(t *testing.T) {
	m := build(t, `type User { id: ID! } type Query { user(id: ID!): User } type Mutation { user(id: ID!): User }`, ingest.Options{})
	if n := toolByKey(t, m, "query user").Name; n != "user" {
		t.Errorf("query user name = %q, want user", n)
	}
	if n := toolByKey(t, m, "mutation user").Name; n != "user_2" {
		t.Errorf("mutation user name = %q, want user_2", n)
	}
	p := build(t, `type Query { user: Int } type Mutation { user: Int }`, ingest.Options{Prefix: "gh"})
	if a, b := toolByKey(t, p, "query user").Name, toolByKey(t, p, "mutation user").Name; a != "gh_user" || b != "gh_user_2" {
		t.Errorf("prefixed names = %q, %q", a, b)
	}
}

func TestBuildSkipsInvalidDocument(t *testing.T) {
	// gqlparser does not type-check argument defaults in SDL, so this schema
	// loads, but the generated $first: Int! = "abc" fails validation.
	m := build(t, `type Query { good: Int bad(first: Int! = "abc"): Int }`, ingest.Options{})
	if keys := toolKeys(m); !slices.Equal(keys, []model.OpKey{"query good"}) {
		t.Errorf("tools = %v, want only query good", keys)
	}
	if len(m.Warnings) != 1 {
		t.Fatalf("warnings = %q, want one", m.Warnings)
	}
	w := m.Warnings[0]
	for _, part := range []string{"query", `"bad"`, "Int cannot represent non-integer value"} {
		if !strings.Contains(w, part) {
			t.Errorf("warning %q lacks %q", w, part)
		}
	}
	if strings.ContainsAny(w, "\n\r") {
		t.Errorf("warning not cleaned: %q", w)
	}
}

func TestBuildSkipsIntrospectionFields(t *testing.T) {
	s := loadSDL(t, `type Query { a: Int }`)
	if s.Query.Fields.ForName("__schema") == nil {
		t.Fatal("precondition: gqlparser exposes __schema on the query type")
	}
	m, err := Build(s, FormatSDL, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if keys := toolKeys(m); !slices.Equal(keys, []model.OpKey{"query a"}) {
		t.Errorf("tools = %v, want only query a", keys)
	}
}

func TestBuildTruncationIssue(t *testing.T) {
	m := build(t, `type U { a: Int b: Int c: Int } type Query { u: U }`, ingest.Options{MaxFields: 2})
	tl := toolByKey(t, m, "query u")
	want := model.Issue{Kind: model.IssueSelectionTruncated, Detail: "selection hit graphql.max_fields (2) and was truncated"}
	if !slices.Contains(tl.Issues, want) {
		t.Errorf("issues = %+v, want %+v", tl.Issues, want)
	}
	if tl.GraphQL.Document != "query PlugIt_u { u { a b } }" {
		t.Errorf("document = %q", tl.GraphQL.Document)
	}
	if d := toolByKey(t, build(t, `type U { a: Int b: Int c: Int } type Query { u: U }`, ingest.Options{}), "query u"); len(d.Issues) != 0 {
		t.Errorf("untruncated issues = %+v", d.Issues)
	}
}

func TestBuildDegradedArgIssue(t *testing.T) {
	m := build(t, `scalar JSON type Query { q(data: JSON, n: Int): Int }`, ingest.Options{})
	tl := toolByKey(t, m, "query q")
	want := []model.Issue{{Kind: model.IssueSchemaDegraded, Detail: "argument data: schema simplified to {} (custom scalar, depth or node budget)"}}
	if !reflect.DeepEqual(tl.Issues, want) {
		t.Errorf("issues = %+v, want %+v", tl.Issues, want)
	}
	if string(tl.Params[0].Schema) != `{}` {
		t.Errorf("data schema = %s", tl.Params[0].Schema)
	}
}

func TestBuildParamNamesDeduped(t *testing.T) {
	m := build(t, `type Query { q(id: ID, ID: ID, Id_2: ID): Int }`, ingest.Options{})
	tl := toolByKey(t, m, "query q")
	var names, ups []string
	for _, p := range tl.Params {
		names, ups = append(names, p.Name), append(ups, p.Bind.Upstream)
	}
	if !slices.Equal(names, []string{"id", "ID_2", "Id_2_2"}) || !slices.Equal(ups, []string{"id", "ID", "Id_2"}) {
		t.Errorf("names = %v, upstream = %v", names, ups)
	}
	if d := tl.GraphQL.Document; d != "query PlugIt_q($id: ID, $ID: ID, $Id_2: ID) { q(id: $id, ID: $ID, Id_2: $Id_2) }" {
		t.Errorf("document = %q", d)
	}
}

func TestBuildDescriptionsAndDeprecation(t *testing.T) {
	sdl := `type Query {
  plain: Int
  "Old <b>thing</b>."
  old(
    "The <i>key</i>."
    k: String
  ): Int @deprecated(reason: "gone")
}`
	m := build(t, sdl, ingest.Options{})
	if d := toolByKey(t, m, "query plain").Description; d != "query plain" {
		t.Errorf("fallback description = %q", d)
	}
	old := toolByKey(t, m, "query old")
	if old.Description != "Old thing." || !old.Deprecated || old.Params[0].Desc != "The key." {
		t.Errorf("old = %q deprecated=%v param desc=%q", old.Description, old.Deprecated, old.Params[0].Desc)
	}
	if toolByKey(t, m, "query plain").Deprecated {
		t.Error("plain must not be deprecated")
	}
	if c := toolByKey(t, build(t, sdl, ingest.Options{MaxDescription: 5}), "query old").Description; c != "Old…" {
		t.Errorf("capped description = %q", c)
	}
}

func TestBuildNormalizedSDLRoundTrip(t *testing.T) {
	for _, name := range []string{"sdl", "introspection"} {
		t.Run(name, func(t *testing.T) {
			var s1Data []byte
			var err error
			format := FormatSDL
			if name == "sdl" {
				s1Data, err = os.ReadFile("testdata/user.graphql")
			} else {
				format = FormatIntrospection
				s1Data, err = os.ReadFile("testdata/user.introspection.json")
			}
			if err != nil {
				t.Fatal(err)
			}
			s1, err := LoadSchema(s1Data, format)
			if err != nil {
				t.Fatal(err)
			}
			m1, err := Build(s1, format, ingest.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if m1.GraphQL == nil || m1.GraphQL.NormalizedSDL == "" || m1.GraphQL.OverrideDocs != nil {
				t.Fatalf("GraphQL = %+v", m1.GraphQL)
			}
			sdl := m1.GraphQL.NormalizedSDL
			if strings.Contains(sdl, "__Schema") || strings.Contains(sdl, "scalar String") {
				t.Errorf("normalized SDL contains built-ins:\n%s", sdl)
			}
			s2, err := LoadSchema([]byte(sdl), FormatSDL)
			if err != nil {
				t.Fatalf("reload normalized SDL: %v\n%s", err, sdl)
			}
			m2, err := Build(s2, FormatSDL, ingest.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m1.Tools, m2.Tools) {
				t.Errorf("tools differ after round-trip:\n%+v\n%+v", m1.Tools, m2.Tools)
			}
			if m2.GraphQL.NormalizedSDL != sdl {
				t.Errorf("normalized SDL is not a fixed point")
			}
		})
	}
}

func TestBuildDeterministic(t *testing.T) {
	sdl := userFixture(t)
	a, b := build(t, sdl, ingest.Options{}), build(t, sdl, ingest.Options{})
	if !reflect.DeepEqual(a, b) {
		t.Error("two builds of the same schema differ")
	}
}

func TestBuildNilSchema(t *testing.T) {
	if _, err := Build(nil, FormatSDL, ingest.Options{}); err == nil {
		t.Error("Build(nil) succeeded")
	}
}

const hostileDir = "../../../testdata/hostile/graphql"

func TestBuildPoisonedDescriptions(t *testing.T) {
	data, err := os.ReadFile(hostileDir + "/poisoned-description.graphql")
	if err != nil {
		t.Fatal(err)
	}
	m := build(t, string(data), ingest.Options{})
	find := toolByKey(t, m, "query find")

	var hidden []string
	for _, is := range find.Issues {
		if is.Kind == model.IssueHiddenText {
			hidden = append(hidden, is.Detail)
		}
	}
	want := []string{
		"description contained invisible or bidi characters (removed)",
		"argument term description contained invisible or bidi characters (removed)",
		"input Filter field name description contained invisible or bidi characters (removed)",
		"enum Role description contained invisible or bidi characters (removed)",
	}
	slices.Sort(hidden)
	slices.Sort(want)
	// filter and other share Filter: its issue is listed once.
	if !slices.Equal(hidden, want) {
		t.Errorf("hidden-text issues = %q\nwant %q", hidden, want)
	}

	// The hidden content never reaches the model.
	texts := []string{find.Description}
	for _, p := range find.Params {
		texts = append(texts, p.Desc, string(p.Schema))
	}
	for _, s := range texts {
		for _, bad := range []string{"<!--", "SYSTEM", "deleteEverything", "term=admin", "\u200b", "\u202e"} {
			if strings.Contains(s, bad) {
				t.Errorf("model-facing text %q still contains %q", s, bad)
			}
		}
	}
	if find.Description != "Find things." || find.Params[0].Desc != "Search term." {
		t.Errorf("cleaned descriptions = %q / %q", find.Description, find.Params[0].Desc)
	}
	if c := toolByKey(t, m, "query clean"); len(c.Issues) != 0 {
		t.Errorf("clean tool issues = %+v", c.Issues)
	}
}

var validParamName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

func TestBuildParamNamesCappedAt64(t *testing.T) {
	long := strings.Repeat("a", 100)
	x1, x2 := strings.Repeat("x", 70)+"1", strings.Repeat("x", 70)+"2"
	m := build(t, "type Query { q("+long+": Int, "+x1+": Int, "+x2+": Int): Int }", ingest.Options{})
	tl := toolByKey(t, m, "query q")
	wantNames := []string{strings.Repeat("a", 64), strings.Repeat("x", 64), strings.Repeat("x", 62) + "_2"}
	wantUp := []string{long, x1, x2}
	seen := map[string]bool{}
	for i, p := range tl.Params {
		if p.Name != wantNames[i] || p.Bind.Upstream != wantUp[i] {
			t.Errorf("param %d = %q (upstream %q), want %q (upstream %q)", i, p.Name, p.Bind.Upstream, wantNames[i], wantUp[i])
		}
		if !validParamName.MatchString(p.Name) || seen[strings.ToLower(p.Name)] {
			t.Errorf("param name %q invalid or duplicate", p.Name)
		}
		seen[strings.ToLower(p.Name)] = true
	}
	if !strings.Contains(tl.GraphQL.Document, "$"+x2+": Int") {
		t.Errorf("document must keep the full upstream names: %s", tl.GraphQL.Document)
	}
}
