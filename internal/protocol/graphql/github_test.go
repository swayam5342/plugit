package graphql_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
)

var update = flag.Bool("update", false, "rewrite golden files")

// The test binary doubles as the isolate parse child, so ingest here runs
// through the same child path as plugit add and sync.
func TestMain(m *testing.M) {
	if os.Getenv("PLUGIT_TEST_CHILD") == "parse" {
		os.Exit(isolate.ChildMain(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func childRunner() *isolate.Runner {
	return &isolate.Runner{
		Exe:  os.Args[0],
		Args: []string{"-test.run=^$"},
		Env:  []string{"PLUGIT_TEST_CHILD=parse"},
	}
}

// githubOpts are the options plugit add uses for a GraphQL API with the
// default graphql: settings.
var githubOpts = ingest.Options{Prefix: "github", SelectionDepth: 2, MaxFields: 100}

// TestGitHubSDLPinned: the fixture decompresses to the SHA-256 recorded in
// SOURCES.md.
func TestGitHubSDLPinned(t *testing.T) {
	data := graphql.GitHubSDL(t)
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != graphql.GitHubSDLSHA256 {
		t.Fatalf("SHA-256 = %s, want %s", got, graphql.GitHubSDLSHA256)
	}
	if !graphql.DetectSDL(data) {
		t.Error("the GitHub SDL is not detected as SDL")
	}
}

// raceSlowdown scales the time bound under -race, where CI runs this test
// (it runs only go test -race). Measured on the development machine: about
// 0.25 s without -race and 1.8 s with it. A 10 s race bound leaves over 5x
// headroom for a slower CI runner and still fails on an order-of-magnitude
// regression; the plain build keeps the SPEC's 2 s bound.
const raceSlowdown = 5

// TestGitHubSDLIngestsUnder2s is the SPEC 7 M8 exit criterion: GitHub's
// public SDL ingests through the isolate child (process start, request,
// LoadSchema, Build with every document validated, response) in under 2 s.
func TestGitHubSDLIngestsUnder2s(t *testing.T) {
	data := graphql.GitHubSDL(t)
	limit := 2 * time.Second
	if graphql.RaceEnabled {
		limit *= raceSlowdown
	}

	start := time.Now()
	m, err := childRunner().Parse(t.Context(), data, "", githubOpts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	t.Logf("GitHub SDL (%d bytes): %d tools, %d warnings in %v (race=%v, limit %v)",
		len(data), len(m.Tools), len(m.Warnings), elapsed, graphql.RaceEnabled, limit)
	if elapsed > limit {
		t.Errorf("ingest took %v, want < %v", elapsed, limit)
	}
	if m.Protocol != model.GraphQL || m.Format != graphql.FormatSDL {
		t.Errorf("manifest is %q/%q, want %q/%q", m.Protocol, m.Format, model.GraphQL, graphql.FormatSDL)
	}
	if len(m.Tools) < 250 {
		t.Errorf("only %d tools, want at least 250", len(m.Tools))
	}
	if len(m.Warnings) != 0 {
		t.Errorf("warnings = %q, want none (no document may be dropped)", m.Warnings)
	}
}

// TestGitHubSDLGolden pins the tool list built from the GitHub SDL: key,
// name, safety, parameter names and the number of selected leaf fields.
// Regenerate with go test ./internal/protocol/graphql -run GitHubSDLGolden
// -update and review the diff.
func TestGitHubSDLGolden(t *testing.T) {
	data := graphql.GitHubSDL(t)
	s, err := graphql.LoadSchema(data, graphql.FormatSDL)
	if err != nil {
		t.Fatal(err)
	}
	m, err := graphql.Build(s, graphql.FormatSDL, githubOpts)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, tl := range m.Tools {
		params := make([]string, 0, len(tl.Params))
		for _, p := range tl.Params {
			req := ""
			if p.Required {
				req = "!"
			}
			params = append(params, p.Name+req)
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\n", tl.Key, tl.Name, tl.Safety, strings.Join(params, ","), len(tl.GraphQL.Selection))
	}
	got := []byte(b.String())
	path := filepath.Join("testdata", "github.graphql.txt")
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Errorf("%s differs from golden; run go test ./internal/protocol/graphql -run GitHubSDLGolden -update and review the diff", path)
	}
}

// Every hostile GraphQL fixture, and a generated huge introspection result,
// goes through the real isolate child: the child never crashes or hangs, and
// the outcome is a manifest or a GraphQL "unsupported" error.
func TestHostileFixturesThroughChild(t *testing.T) {
	files, err := filepath.Glob("../../../testdata/hostile/graphql/*.graphql")
	if err != nil || len(files) < 4 {
		t.Fatalf("hostile GraphQL fixtures: %v (%d found)", err, len(files))
	}
	inputs := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inputs[filepath.Base(f)] = data
	}
	inputs["huge-introspection"] = graphql.HugeIntrospection(hugeSize)
	inputs["deep-typeref-introspection"] = graphql.DeepTypeRefIntrospection(5000)

	for name, data := range inputs {
		t.Run(name, func(t *testing.T) {
			m, err := childRunner().Parse(t.Context(), data, "", ingest.Options{SelectionDepth: 2, MaxFields: 100})
			switch {
			case errors.Is(err, isolate.ErrParserCrashed), errors.Is(err, isolate.ErrParseTimeout):
				t.Fatalf("the child did not handle it: %v", err)
			case err != nil:
				if !strings.Contains(err.Error(), "unsupported spec") {
					t.Fatalf("error = %v, want an unsupported-spec error", err)
				}
				if name != "deep-typeref-introspection" {
					t.Fatalf("%s was rejected: %v", name, err)
				}
			case name == "deep-typeref-introspection":
				t.Fatal("a 5000-deep type reference was accepted")
			case len(m.Tools) == 0:
				t.Fatal("no tools built")
			}
		})
	}
}

// hugeSize is the generated introspection result's shape: big enough to
// exercise every count cap, small enough to stay fast under -race.
var hugeSize = graphql.HugeSize{Types: 2000, Fields: 20, Roots: 400, Subscriptions: 100, DescriptionRunes: 1 << 20}

// The isolate output cap holds: a manifest bigger than the runner's
// MaxOutput is an error, not an unbounded read.
func TestHugeIntrospectionOutputCapped(t *testing.T) {
	r := childRunner()
	r.MaxOutput = 64 << 10
	_, err := r.Parse(t.Context(), graphql.HugeIntrospection(hugeSize), "", ingest.Options{})
	if !errors.Is(err, isolate.ErrParserCrashed) || !strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("err = %v, want the output cap to stop the child", err)
	}
}
