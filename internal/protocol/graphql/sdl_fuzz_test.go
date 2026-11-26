package graphql

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/validator/rules"

	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
)

// FuzzSDL feeds arbitrary bytes through detection, LoadSchema (as SDL and as
// an introspection result) and Build, the whole ingest path the isolate child
// runs on hostile input. It must never panic; an error must wrap
// ErrUnsupportedSpec; and a built manifest must respect its caps: valid,
// unique tool names, descriptions within the cap, warnings capped, every
// selection within max_fields, every document valid against the schema, the
// normalized SDL reloadable, and the build deterministic.
func FuzzSDL(f *testing.F) {
	for _, glob := range []string{"testdata/*.graphql", "testdata/*.json", hostileDir + "/*.graphql"} {
		files, err := filepath.Glob(glob)
		if err != nil {
			f.Fatal(err)
		}
		for _, p := range files {
			data, err := os.ReadFile(p)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(data, uint8(2), uint8(100))
		}
	}
	f.Add([]byte(selSDL), uint8(1), uint8(3))
	f.Add([]byte(builderCasesSDL), uint8(3), uint8(20))
	f.Add([]byte("type Query { a: Int } type Subscription { s: Int }"), uint8(0), uint8(0))
	f.Add([]byte(`{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"a","args":[],"type":{"kind":"SCALAR","name":"Int"}}]}]}}`), uint8(2), uint8(10))
	f.Add(DeepTypeRefIntrospection(40), uint8(2), uint8(10))

	f.Fuzz(func(t *testing.T, data []byte, depth, maxFields uint8) {
		if len(data) > 64<<10 {
			return
		}
		opts := ingest.Options{
			Prefix:         "fz",
			MaxDescription: 200,
			SelectionDepth: int(depth % 6),      // 0 means the default
			MaxFields:      int(maxFields % 40), // 0 means the default
		}
		for _, format := range []string{FormatSDL, FormatIntrospection} {
			if format == FormatSDL {
				_ = DetectSDL(data)
			} else {
				_ = DetectIntrospection(data)
			}
			s, err := LoadSchema(data, format)
			if err != nil {
				if !errors.Is(err, ErrUnsupportedSpec) {
					t.Fatalf("%s: error does not wrap ErrUnsupportedSpec: %v", format, err)
				}
				continue
			}
			m, err := Build(s, format, opts)
			if err != nil {
				continue
			}
			checkFuzzManifest(t, format, m, opts)
			again, err := Build(s, format, opts)
			if err != nil {
				t.Fatalf("%s: second build failed: %v", format, err)
			}
			a, _ := json.Marshal(m)
			b, _ := json.Marshal(again)
			if string(a) != string(b) {
				t.Fatalf("%s: non-deterministic build", format)
			}
		}
	})
}

func checkFuzzManifest(t *testing.T, format string, m *model.Manifest, opts ingest.Options) {
	t.Helper()
	s, err := LoadSchema([]byte(m.GraphQL.NormalizedSDL), FormatSDL)
	if err != nil {
		t.Fatalf("%s: normalized SDL does not reload: %v\n%s", format, err, m.GraphQL.NormalizedSDL)
	}
	if len(m.Warnings) > maxWarnings+1 {
		t.Fatalf("%s: %d warnings, over the cap", format, len(m.Warnings))
	}
	maxFields := opts.MaxFields
	if maxFields <= 0 {
		maxFields = DefaultMaxFields
	}
	names := map[string]bool{}
	for _, tl := range m.Tools {
		if !validToolName.MatchString(tl.Name) || names[strings.ToLower(tl.Name)] {
			t.Fatalf("%s: tool name %q invalid or duplicate", format, tl.Name)
		}
		names[strings.ToLower(tl.Name)] = true
		if n := utf8.RuneCountInString(tl.Description); n > opts.MaxDescription {
			t.Fatalf("%s: %s description is %d runes, over %d", format, tl.Key, n, opts.MaxDescription)
		}
		if tl.GraphQL == nil || !strings.HasPrefix(string(tl.Key), tl.GraphQL.Operation+" ") {
			t.Fatalf("%s: %s has no matching GraphQL operation", format, tl.Key)
		}
		if n := len(tl.GraphQL.Selection); n > maxFields {
			t.Fatalf("%s: %s selects %d fields, over %d", format, tl.Key, n, maxFields)
		}
		for _, p := range tl.Params {
			if !validParamName.MatchString(p.Name) || !json.Valid(p.Schema) {
				t.Fatalf("%s: %s parameter %q invalid (schema %s)", format, tl.Key, p.Name, p.Schema)
			}
		}
		if _, errs := gqlparser.LoadQueryWithRules(s, tl.GraphQL.Document, rules.NewDefaultRules()); errs != nil {
			t.Fatalf("%s: %s document invalid against the normalized schema: %v\n%s", format, tl.Key, errs, tl.GraphQL.Document)
		}
	}
}

// validToolName is the MCP tool name grammar.
var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
