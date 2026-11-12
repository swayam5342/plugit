package openapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

var update = flag.Bool("update", false, "rewrite golden files")

const specsDir = "../../testdata/specs"

// loadSpec reads a pinned spec, gunzipping *.gz, and checks its SHA-256
// against SOURCES.md.
func loadSpec(t *testing.T, file string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(specsDir, file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	if strings.HasSuffix(file, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if raw, err = io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := os.ReadFile(filepath.Join(specsDir, "SOURCES.md"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if !bytes.Contains(sources, []byte(hex.EncodeToString(sum[:]))) {
		t.Fatalf("%s: SHA-256 %x is not listed in SOURCES.md", file, sum)
	}
	return raw
}

// prepared is a manifest after schema.Prepare, as the server would see it.
type prepared struct {
	model.Manifest
	InputSchemas map[string]json.RawMessage `json:"inputSchemas"`
}

func prepare(m *model.Manifest) prepared {
	out := prepared{Manifest: *m, InputSchemas: map[string]json.RawMessage{}}
	out.Tools = make([]model.Tool, len(m.Tools))
	for i, tl := range m.Tools {
		out.Tools[i] = schema.Prepare(tl, schema.Options{})
		out.InputSchemas[out.Tools[i].Name] = schema.InputSchema(out.Tools[i])
	}
	return out
}

// compact renders one line per tool: key, name, safety, params, issues.
func compact(p prepared) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "title: %s\nversion: %s\nspec: %s\nservers: %v\ntools: %d\n", p.Title, p.Version, p.SpecVersion, p.Servers, len(p.Tools))
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	b.WriteString("\n")
	for _, tl := range p.Tools {
		var params []string
		for _, pr := range tl.Params {
			s := pr.Name + "@" + pr.In.String()
			if pr.Name != pr.UpstreamName && pr.UpstreamName != "" {
				s += "(" + pr.UpstreamName + ")"
			}
			if pr.Required {
				s += "*"
			}
			params = append(params, s)
		}
		var issues []string
		for _, is := range tl.Issues {
			issues = append(issues, string(is.Kind))
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t[%s]", tl.Key, tl.Name, tl.Safety, strings.Join(params, " "))
		if len(issues) > 0 {
			fmt.Fprintf(&b, "\tissues=%s", strings.Join(issues, ","))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s (run with -update to create it): %v", path, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden; run `go test ./internal/openapi -run Golden -update` and review the diff", name)
	}
}

func TestGoldenPetstore(t *testing.T) {
	m, err := Parse(loadSpec(t, "petstore3.json"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(prepare(m), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "petstore3.json", append(got, '\n'))
}

func TestGoldenLargeSpecs(t *testing.T) {
	for _, tc := range []struct {
		file, golden string
		minTools     int
	}{
		{"github.json.gz", "github.txt", 900},
		{"stripe.json.gz", "stripe.txt", 400},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data := loadSpec(t, tc.file)

			start := time.Now()
			m, err := Parse(data, Options{})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %d tools in %v", tc.file, len(m.Tools), elapsed)
			if len(m.Tools) < tc.minTools {
				t.Errorf("only %d tools, want at least %d", len(m.Tools), tc.minTools)
			}
			// SPEC §7 M1 exit: the GitHub spec parses in under 2 s. The race
			// detector slows everything down several times, so only assert
			// without it.
			if !raceEnabled && tc.file == "github.json.gz" && elapsed > 2*time.Second {
				t.Errorf("parse took %v, want < 2s", elapsed)
			}

			p := prepare(m)
			checkGolden(t, tc.golden, compact(p))

			// Determinism: a second parse gives byte-identical output.
			full1, _ := json.Marshal(p)
			m2, err := Parse(data, Options{})
			if err != nil {
				t.Fatal(err)
			}
			full2, _ := json.Marshal(prepare(m2))
			if !bytes.Equal(full1, full2) {
				t.Error("two parses of the same spec differ")
			}
			assertValidNames(t, p)
		})
	}
}

func assertValidNames(t *testing.T, p prepared) {
	t.Helper()
	seen := map[string]bool{}
	for _, tl := range p.Tools {
		if !validToolName(tl.Name) {
			t.Errorf("%s: invalid tool name %q", tl.Key, tl.Name)
		}
		if seen[strings.ToLower(tl.Name)] {
			t.Errorf("%s: duplicate tool name %q", tl.Key, tl.Name)
		}
		seen[strings.ToLower(tl.Name)] = true
		var is map[string]any
		if err := json.Unmarshal(p.InputSchemas[tl.Name], &is); err != nil {
			t.Errorf("%s: input schema is not valid JSON: %v", tl.Key, err)
		}
	}
}

func validToolName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r == '_' || r == '-' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}
