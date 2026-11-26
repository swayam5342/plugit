package ingest_test

import (
	"testing"

	"github.com/swayam5342/plugit/internal/formats"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
)

const openapiDoc = `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"servers":[{"url":"https://api.example.test"}],
"paths":{"/pets":{"get":{"operationId":"listPets","responses":{"200":{"description":"ok"}}}}}}`

const postmanDoc = `{"info":{"name":"c","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
"item":[{"name":"list","request":{"method":"GET","url":{"raw":"https://api.example.test/pets","host":["api","example","test"],"path":["pets"]}}}]}`

func TestDispatchDetectsOpenAPIAndPostman(t *testing.T) {
	for _, tc := range []struct {
		name, data, format string
	}{
		{"openapi", openapiDoc, "openapi"},
		{"postman", postmanDoc, "postman"},
	} {
		m, err := ingest.Dispatch(formats.Ordered(), []byte(tc.data), "", ingest.Options{})
		if err != nil {
			t.Fatalf("%s: Dispatch: %v", tc.name, err)
		}
		if m.Protocol != model.REST {
			t.Errorf("%s: Protocol = %q, want rest", tc.name, m.Protocol)
		}
		if m.Format != tc.format {
			t.Errorf("%s: Format = %q, want %q", tc.name, m.Format, tc.format)
		}
	}
}

func TestDispatchTakesFormatsAndNeutralOptions(t *testing.T) {
	m, err := ingest.Dispatch(formats.Ordered(), []byte(openapiDoc), "", ingest.Options{Prefix: "pets"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(m.Tools))
	}
	if got := m.Tools[0].Name; got != "pets_listPets" {
		t.Errorf("tool name = %q, want the prefixed listPets tool", got)
	}
}

func TestForceFormatSkipsDetection(t *testing.T) {
	// Force postman on a doc postman would not detect: it must be tried and fail,
	// not silently fall back to openapi.
	if _, err := ingest.Dispatch(formats.Ordered(), []byte(openapiDoc), "postman", ingest.Options{}); err == nil {
		t.Error("forcing postman on an OpenAPI doc should error, not fall back")
	}
	if _, err := ingest.Dispatch(formats.Ordered(), []byte(postmanDoc), "nosuchformat", ingest.Options{}); err == nil {
		t.Error("an unknown --format should error")
	}
}

type echoFormat struct{}

func (echoFormat) Name() string             { return "echo" }
func (echoFormat) Protocol() model.Protocol { return model.Protocol("echo") }
func (echoFormat) Detect(d []byte) bool     { return len(d) > 0 && d[0] == '!' }
func (echoFormat) Parse(_ []byte, _ ingest.Options) (*model.Manifest, error) {
	return &model.Manifest{Protocol: "echo", Format: "echo"}, nil
}

func TestSecondFormat(t *testing.T) {
	fs := append([]ingest.Format{echoFormat{}}, formats.Ordered()...)
	m, err := ingest.Dispatch(fs, []byte("!x"), "", ingest.Options{})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.Protocol != "echo" || m.Format != "echo" {
		t.Errorf("routed to %q/%q, want echo/echo", m.Protocol, m.Format)
	}
}

// misnamed is a format whose Parse records some other format name.
type misnamed struct{}

func (misnamed) Name() string             { return "misnamed" }
func (misnamed) Protocol() model.Protocol { return model.REST }
func (misnamed) Detect(d []byte) bool     { return len(d) > 0 && d[0] == '?' }
func (misnamed) Parse(_ []byte, _ ingest.Options) (*model.Manifest, error) {
	return &model.Manifest{Protocol: model.REST, Format: "something-else"}, nil
}

const swaggerDoc = `{"swagger":"2.0","info":{"title":"t","version":"1"},"host":"api.example.test","schemes":["https"],
"paths":{"/pets":{"get":{"operationId":"listPets","responses":{"200":{"description":"ok"}}}}}}`

// Manifest.Format is the name of the ingest format that produced it (SPEC
// 3.4), detected or forced, whatever the format's parser wrote: a Swagger
// 2.0 document is format "openapi" (its version is in SpecVersion), since
// that is the name --format, source.format and a forced re-parse use.
func TestDispatchStampsFormatName(t *testing.T) {
	fs := append([]ingest.Format{misnamed{}}, formats.Ordered()...)
	for _, force := range []string{"", "misnamed"} {
		m, err := ingest.Dispatch(fs, []byte("?x"), force, ingest.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if m.Format != "misnamed" {
			t.Errorf("force %q: Format = %q, want misnamed", force, m.Format)
		}
	}
	for _, force := range []string{"", "openapi"} {
		m, err := ingest.Dispatch(formats.Ordered(), []byte(swaggerDoc), force, ingest.Options{})
		if err != nil {
			t.Fatalf("force %q: %v", force, err)
		}
		if m.Format != "openapi" || m.SpecVersion != "2.0" {
			t.Errorf("force %q: Swagger 2.0 Format/SpecVersion = %q/%q, want openapi/2.0", force, m.Format, m.SpecVersion)
		}
	}
}
