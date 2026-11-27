package lint

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
)

const gqlHostile = "../../testdata/hostile/graphql/"

// gqlManifest builds the GraphQL manifest of sdl, as the isolate child does.
func gqlManifest(t *testing.T, sdl string, opts ingest.Options) *model.Manifest {
	t.Helper()
	s, err := graphql.LoadSchema([]byte(sdl), graphql.FormatSDL)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	m, err := graphql.Build(s, graphql.FormatSDL, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func gqlFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(gqlHostile + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// gqlInput lints every tool of m as toolset "default" of a GraphQL API.
func gqlInput(m *model.Manifest) Input {
	keys := map[string]config.ToolOverride{}
	for _, x := range m.Tools {
		keys[string(x.Key)] = config.ToolOverride{}
	}
	a := api(keys)
	a.Protocol = string(model.GraphQL)
	a.Endpoint = "https://api.example.test/graphql"
	return Input{API: a, Tools: m.Tools, Unsupported: m.Unsupported}
}

func findings(fs []Finding, rule string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// L001 for GraphQL: a field without a description gets the OpKey as its
// fallback description ("query bare"), which is flagged like REST's
// "METHOD /path" fallback; a described field is not.
func TestL001GraphQLMissingDescription(t *testing.T) {
	m := gqlManifest(t, `
type Query {
  "Look up one thing."
  described: Int
  bare: Int
}
type Mutation { touch: Int }
`, ingest.Options{})
	got := findings(Run(gqlInput(m)), "L001")
	var tools []string
	for _, f := range got {
		if f.Severity != Warn {
			t.Errorf("L001 = %+v, want a warning", f)
		}
		tools = append(tools, string(f.Tool))
	}
	if slices.Sort(tools); !slices.Equal(tools, []string{"mutation touch", "query bare"}) {
		t.Errorf("L001 on %q, want mutation touch and query bare", tools)
	}
}

// L020: a default selection cut short by graphql.max_fields is a warning on
// that tool, naming the cap; a selection that fits is not flagged.
func TestL020GraphQLTruncated(t *testing.T) {
	m := gqlManifest(t, `
type Wide { a: Int b: Int c: Int d: Int e: Int f: Int }
type Query { wide: Wide narrow: Int }
`, ingest.Options{MaxFields: 3})
	got := findings(Run(gqlInput(m)), "L020")
	if len(got) != 1 {
		t.Fatalf("L020 findings = %+v, want one", got)
	}
	f := got[0]
	if f.Tool != "query wide" || f.Severity != Warn || !strings.Contains(f.Message, "max_fields (3)") {
		t.Errorf("L020 = %+v, want a warning on query wide naming max_fields (3)", f)
	}
}

// L013: a Subscription field becomes no tool, recorded in the manifest's
// Unsupported list; lint turns each into an info finding for the API (no
// toolset, no tool), and lint.ignore hides them like any info.
func TestL013Subscription(t *testing.T) {
	m := gqlManifest(t, `
type Query { now: Int }
type Subscription { onEvent: String onOther(id: ID!): Int }
`, ingest.Options{})
	in := gqlInput(m)
	got := findings(Run(in), "L013")
	if len(got) != 2 {
		t.Fatalf("L013 findings = %+v, want two (warnings %q)", got, m.Warnings)
	}
	var fields []string
	for _, f := range got {
		if f.Severity != Info || f.Toolset != "" || f.Tool != "" {
			t.Errorf("L013 = %+v, want an API-wide info", f)
		}
		for _, name := range []string{"onEvent", "onOther"} {
			if strings.Contains(f.Message, `subscription "`+name+`"`) {
				fields = append(fields, name)
			}
		}
	}
	if slices.Sort(fields); !slices.Equal(fields, []string{"onEvent", "onOther"}) {
		t.Errorf("L013 messages name %q, want onEvent and onOther", fields)
	}

	in.API.Lint.Ignore = []string{"L013"}
	fs, ignored := RunCounting(in)
	if len(findings(fs, "L013")) != 0 || ignored != 2 {
		t.Errorf("with lint.ignore L013: findings %+v, ignored %d; want none shown, 2 ignored", fs, ignored)
	}

	// Warnings are for humans: lint reads only the Unsupported list.
	other := gqlInput(m)
	other.Unsupported = nil
	if got := findings(Run(other), "L013"); len(got) != 0 {
		t.Errorf("L013 without Unsupported entries: %+v", got)
	}
}

// Every subscription is reported, past the manifest's 25-warning cap.
func TestL013SubscriptionsPastWarningCap(t *testing.T) {
	var sdl strings.Builder
	sdl.WriteString("type Query { now: Int }\ntype Subscription {\n")
	for i := range 40 {
		fmt.Fprintf(&sdl, "  s%d: Int\n", i)
	}
	sdl.WriteString("}\n")
	m := gqlManifest(t, sdl.String(), ingest.Options{})
	if got := findings(Run(gqlInput(m)), "L013"); len(got) != 40 {
		t.Errorf("L013 findings = %d, want all 40 subscriptions (warnings %d)", len(got), len(m.Warnings))
	}
}

// L013 for a dropped GraphQL operation: a root field whose generated
// document failed validation becomes no tool, only a manifest warning, which
// no command showed. lint turns each into an API-wide info naming the
// operation (not the validator's text, which echoes schema content).
func TestL013GraphQLSkippedOperation(t *testing.T) {
	m := gqlManifest(t, `type Query { now: Int }`, ingest.Options{})
	in := gqlInput(m)
	in.Unsupported = []model.UnsupportedOp{
		{Operation: "query", Field: "broken", Reason: model.UnsupportedInvalidDocument},
		{Operation: "mutation", Field: "alsoBroken", Reason: model.UnsupportedInvalidDocument},
	}
	got := findings(Run(in), "L013")
	if len(got) != 2 {
		t.Fatalf("L013 findings = %+v, want two", got)
	}
	for i, want := range []string{`mutation "alsoBroken" has no tool`, `query "broken" has no tool`} { // findings are sorted
		f := got[i]
		if f.Severity != Info || f.Toolset != "" || f.Tool != "" || !strings.Contains(f.Message, want) ||
			!strings.Contains(f.Message, "failed validation") || strings.Contains(f.Message, "boom") {
			t.Errorf("L013 = %+v, want an API-wide info containing %s", f, want)
		}
	}
}

// L015 for GraphQL: a read-only query whose field name starts with a write
// verb is flagged, named by its operation key; a mutation is not, since its
// safety already comes from the operation type (deleteX is Destructive).
func TestL015GraphQLWriteNamedQuery(t *testing.T) {
	m := gqlManifest(t, gqlFixture(t, "write-named-query.graphql"), ingest.Options{Prefix: "gh"})
	got := findings(Run(gqlInput(m)), "L015")
	if len(got) != 1 {
		t.Fatalf("L015 findings = %+v, want exactly one (query resetCache)", got)
	}
	const want = `query resetCache is treated as read-only but looks like it changes something ("reset"); if it does, set its policy to confirm or deny`
	if f := got[0]; f.Tool != "query resetCache" || f.Severity != Warn || f.Message != want {
		t.Errorf("L015 = %+v\nwant a warning on query resetCache: %q", f, want)
	}

	// REST output is unchanged.
	x := tl("GET", "/admin/reset", "adminReset", "Reset.")
	f := find(Run(Input{API: api(all(string(x.Key))), Tools: []model.Tool{x}}), "L015")
	const restWant = `GET is treated as read-only but looks like it changes something ("reset"); if it does, set its policy to confirm or deny`
	if f == nil || f.Message != restWant {
		t.Errorf("REST L015 = %+v, want %q", f, restWant)
	}
}

// L012: an argument of a custom scalar maps to {} and is reported.
func TestL012GraphQLCustomScalar(t *testing.T) {
	m := gqlManifest(t, `
scalar DateTime
type Query { since(at: DateTime): Int plain(n: Int): Int }
`, ingest.Options{})
	got := findings(Run(gqlInput(m)), "L012")
	if len(got) != 1 {
		t.Fatalf("L012 findings = %+v, want one", got)
	}
	if f := got[0]; f.Tool != "query since" || f.Severity != Warn || !strings.Contains(f.Message, "argument at") {
		t.Errorf("L012 = %+v, want a warning on query since naming argument at", f)
	}
}

// L007 on the GraphQL poisoned fixture: every tool whose text hid an
// instruction is an error; the clean tool is not flagged.
func TestL007GraphQLPoisonedFixture(t *testing.T) {
	m := gqlManifest(t, gqlFixture(t, "poisoned-description.graphql"), ingest.Options{})
	fs := Run(gqlInput(m))
	if findSev(fs, "L007", Error) == nil {
		t.Fatalf("no L007 error: %+v", fs)
	}
	for _, f := range findings(fs, "L007") {
		if f.Tool == "query clean" {
			t.Errorf("clean tool flagged: %+v", f)
		}
	}
	caught := false
	for _, f := range fs {
		caught = caught || f.Rule == "L007" && f.Severity == Error && f.Tool == "query find"
	}
	if !caught {
		t.Errorf("query find not caught by L007: %+v", fs)
	}
}
