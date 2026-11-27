package formats

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
)

const openapiDoc = `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"servers":[{"url":"https://api.example.test"}],
"paths":{"/pets":{"get":{"operationId":"listPets","responses":{"200":{"description":"ok"}}}}}}`

const openapiYAML = "openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths:\n  /pets:\n    get:\n      responses: {'200': {description: ok}}\n"

const swaggerYAML = "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths: {}\n"

const postmanDoc = `{"info":{"name":"c","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
"item":[{"name":"list","request":{"method":"GET","url":{"raw":"https://api.example.test/pets","host":["api","example","test"],"path":["pets"]}}}]}`

const graphqlTestdata = "../../testdata/specs/graphql"

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return b
}

func TestOrderedNames(t *testing.T) {
	var got []string
	for _, f := range Ordered() {
		got = append(got, f.Name())
	}
	want := []string{"postman", "openapi", "graphql-introspection", "graphql-sdl"}
	if len(got) != len(want) {
		t.Fatalf("Ordered() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Ordered() = %v, want %v", got, want)
		}
	}
}

// SPEC P4: no fixture is detected by two formats, and every format detects
// at least one fixture.
func TestOrderedPairwiseDetection(t *testing.T) {
	type fixture struct {
		data []byte
		want string
	}
	fixtures := map[string]fixture{
		"openapi json":          {[]byte(openapiDoc), "openapi"},
		"openapi yaml":          {[]byte(openapiYAML), "openapi"},
		"swagger yaml":          {[]byte(swaggerYAML), "openapi"},
		"petstore3":             {readFixture(t, "../../testdata/specs/petstore3.json"), "openapi"},
		"postman":               {[]byte(postmanDoc), "postman"},
		"graphql sdl":           {readFixture(t, graphqlTestdata+"/user.graphql"), "graphql-sdl"},
		"graphql introspection": {readFixture(t, graphqlTestdata+"/user.introspection.json"), "graphql-introspection"},
		"hostile graphql sdl":   {readFixture(t, "../../testdata/hostile/graphql/poisoned-description.graphql"), "graphql-sdl"},
		// SDL with an openapi:/swagger: line at column 0 stays SDL.
		"sdl column-0 field":                {[]byte("type Query {\nopenapi: String\n}\n"), "graphql-sdl"},
		"sdl description with swagger line": {[]byte("\"\"\"\nswagger: \"2.0\" mirror of /v1\n\"\"\"\ntype Query {\n  x: Int\n}\n"), "graphql-sdl"},
		"sdl directive argument":            {[]byte("schema @link(\nopenapi: 3\n) {\n  query: Query\n}\n\ntype Query {\n  x: Int\n}\n"), "graphql-sdl"},
		// Commas are GraphQL whitespace, so this first line is SDL, not a YAML key.
		"sdl comma-separated first line": {[]byte("type,Query,{a:\nInt @d(\nopenapi: 3)}\n"), "graphql-sdl"},
		// A contrived JSON object with a root openapi key and a __schema is OpenAPI only.
		"json openapi with __schema": {[]byte(`{"openapi":"3.0.0","info":{"title":"t","version":"1"},"paths":{},"__schema":{"queryType":{"name":"Query"},"types":[]}}`), "openapi"},
	}
	hostile, err := filepath.Glob("../../testdata/hostile/*.yaml")
	if err != nil || len(hostile) == 0 {
		t.Fatalf("globbing hostile REST fixtures: %v (%d found)", err, len(hostile))
	}
	for _, p := range hostile {
		fixtures["hostile "+filepath.Base(p)] = fixture{readFixture(t, p), "openapi"}
	}

	hits := map[string]int{}
	for name, fx := range fixtures {
		var by []string
		for _, f := range Ordered() {
			if f.Detect(fx.data) {
				by = append(by, f.Name())
				hits[f.Name()]++
			}
		}
		if len(by) != 1 || by[0] != fx.want {
			t.Errorf("%s: detected by %v, want exactly [%s]", name, by, fx.want)
		}
	}
	for _, f := range Ordered() {
		if hits[f.Name()] == 0 {
			t.Errorf("format %s detected no fixture", f.Name())
		}
	}
}

func TestDispatchDetectsGraphQL(t *testing.T) {
	for _, tc := range []struct {
		fixture, format string
	}{
		{"user.graphql", "graphql-sdl"},
		{"user.introspection.json", "graphql-introspection"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			data := readFixture(t, graphqlTestdata+"/"+tc.fixture)
			m, err := ingest.Dispatch(Ordered(), data, "", ingest.Options{})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if m.Protocol != model.GraphQL {
				t.Errorf("Protocol = %q, want %q", m.Protocol, model.GraphQL)
			}
			if m.Format != tc.format {
				t.Errorf("Format = %q, want %q", m.Format, tc.format)
			}
			if len(m.Tools) == 0 {
				t.Error("no tools built")
			}
		})
	}
}

func TestForceGraphQLFormat(t *testing.T) {
	sdl := readFixture(t, graphqlTestdata+"/user.graphql")
	intro := readFixture(t, graphqlTestdata+"/user.introspection.json")

	for _, tc := range []struct {
		force string
		data  []byte
	}{
		{"graphql-sdl", sdl},
		{"graphql-introspection", intro},
	} {
		t.Run(tc.force, func(t *testing.T) {
			m, err := ingest.Dispatch(Ordered(), tc.data, tc.force, ingest.Options{})
			if err != nil {
				t.Fatalf("forcing %s: %v", tc.force, err)
			}
			if m.Format != tc.force || m.Protocol != model.GraphQL {
				t.Errorf("forcing %s: got %q/%q", tc.force, m.Protocol, m.Format)
			}
		})
	}

	// A forced GraphQL format on the wrong input is tried and fails; it never
	// falls back to the detected format.
	for _, tc := range []struct {
		name, force string
		data        []byte
	}{
		{"sdl on openapi", "graphql-sdl", []byte(openapiDoc)},
		{"sdl on introspection", "graphql-sdl", intro},
		{"introspection on sdl", "graphql-introspection", sdl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ingest.Dispatch(Ordered(), tc.data, tc.force, ingest.Options{}); !errors.Is(err, graphql.ErrUnsupportedSpec) {
				t.Errorf("forcing %s on the wrong input: err = %v, want graphql.ErrUnsupportedSpec", tc.force, err)
			}
		})
	}
	t.Run("openapi on sdl", func(t *testing.T) {
		if _, err := ingest.Dispatch(Ordered(), sdl, "openapi", ingest.Options{}); err == nil {
			t.Error("forcing openapi on an SDL document should error")
		}
	})
}

func TestDispatchUnclaimedInput(t *testing.T) {
	for name, data := range map[string]string{
		"random text": "this is not an API description",
		"asyncapi":    "asyncapi: 2.6.0\ninfo: {title: t, version: '1'}\nchannels: {}\n",
		"json object": `{"hello":"world"}`,
		"empty":       "",
	} {
		if _, err := ingest.Dispatch(Ordered(), []byte(data), "", ingest.Options{}); !errors.Is(err, ingest.ErrUnsupportedFormat) {
			t.Errorf("%s: err = %v, want ingest.ErrUnsupportedFormat", name, err)
		}
	}
}

func TestNamesFor(t *testing.T) {
	for p, want := range map[model.Protocol][]string{
		model.REST:    {"postman", "openapi"},
		model.GraphQL: {"graphql-introspection", "graphql-sdl"},
		model.GRPC:    nil,
	} {
		got := NamesFor(p)
		if len(got) != len(want) {
			t.Errorf("NamesFor(%s) = %v, want %v", p, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("NamesFor(%s) = %v, want %v", p, got, want)
			}
		}
	}
}
