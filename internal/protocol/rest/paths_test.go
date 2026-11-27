package rest

import (
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest/openapi"
	"github.com/swayam5342/plugit/internal/protocol/rest/postman"
	"github.com/swayam5342/plugit/internal/protocol/rest/restutil"
)

// These tests drive both REST parsers with the same hostile paths, so they
// live above openapi and postman.

// parseREST routes src as the ingest dispatcher would: a Postman collection
// to postman.Parse, anything else to openapi.Parse.
func parseREST(t *testing.T, src string, opts restutil.Options) *model.Manifest {
	t.Helper()
	p := openapi.Parse
	if postman.Detect([]byte(src)) {
		p = postman.Parse
	}
	m, err := p([]byte(src), opts)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func TestPathsCannotLeaveTheBasePath(t *testing.T) {
	oas := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /ok/{id}: {get: {responses: {'200': {description: ok}}}}
  /v1/%2e%2e/secret: {get: {responses: {'200': {description: ok}}}}
  /v1/%2E%2E%2Fadmin: {get: {responses: {'200': {description: ok}}}}
  /v1/.%2e/x: {get: {responses: {'200': {description: ok}}}}
  /v1/a%5c..%5cb: {get: {responses: {'200': {description: ok}}}}
  /v1/a%0d%0ab: {get: {responses: {'200': {description: ok}}}}
`
	m := parseREST(t, oas, restutil.Options{})
	if len(m.Tools) != 1 || m.Tools[0].Key != "GET /ok/{id}" {
		t.Errorf("openapi tools = %v", toolKeys(m))
	}
	if n := countWarnings(m, "skipped"); n != 5 {
		t.Errorf("openapi: %d paths reported as skipped, want 5: %q", n, m.Warnings)
	}

	postman := `{"info":{"name":"x","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[
{"name":"ok","request":{"method":"GET","url":"https://api.example.test/v1/pets/:id"}},
{"name":"up","request":{"method":"GET","url":"https://api.example.test/v1/../../admin"}},
{"name":"enc","request":{"method":"GET","url":"https://api.example.test/v1/%2e%2e/secret"}},
{"name":"esc","request":{"method":"GET","url":"https://api.example.test/v1/x\u001b]0;owned\u0007y"}},
{"name":"bidi","request":{"method":"GET","url":"https://api.example.test/v1/x\u202ey"}}]}`
	m = parseREST(t, postman, restutil.Options{})
	if len(m.Tools) != 1 || m.Tools[0].Key != "GET /v1/pets/{id}" {
		t.Errorf("postman tools = %v", toolKeys(m))
	}
	if n := countWarnings(m, "skipped"); n != 4 {
		t.Errorf("postman: %d requests reported as skipped, want 4: %q", n, m.Warnings)
	}
	for _, w := range m.Warnings {
		if strings.ContainsAny(w, "\x1b\a") {
			t.Errorf("warning carries a control character: %q", w)
		}
	}
}

func toolKeys(m *model.Manifest) []model.OpKey {
	keys := make([]model.OpKey, len(m.Tools))
	for i, tl := range m.Tools {
		keys[i] = tl.Key
	}
	return keys
}

func countWarnings(m *model.Manifest, substr string) int {
	n := 0
	for _, w := range m.Warnings {
		if strings.Contains(w, substr) {
			n++
		}
	}
	return n
}

func TestUnsafePathsNeedTheSwitch(t *testing.T) {
	postman := `{"info":{"name":"x","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[
{"name":"ok","request":{"method":"GET","url":"https://api.example.test/v1/pets/:id"}},
{"name":"up","request":{"method":"GET","url":"https://api.example.test/v1/../admin"}},
{"name":"space","request":{"method":"GET","url":"https://api.example.test/my folder/items"}},
{"name":"esc","request":{"method":"GET","url":"https://api.example.test/v1/x\u001b]0;owned\u0007y"}},
{"name":"bidi","request":{"method":"GET","url":"https://api.example.test/v1/x\u202ey"}}]}`
	oas := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /ok: {get: {responses: {'200': {description: ok}}}}
  /v1/%2e%2e/secret: {get: {responses: {'200': {description: ok}}}}
  /v1/a%5cb: {get: {responses: {'200': {description: ok}}}}
  /v1/a%0d%0ab: {get: {responses: {'200': {description: ok}}}}
  /v1/a?b=c: {get: {responses: {'200': {description: ok}}}}
`
	for _, tc := range []struct {
		name, src string
		off, on   int
	}{{"postman", postman, 1, 3}, {"openapi", oas, 1, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			m := parseREST(t, tc.src, restutil.Options{})
			if len(m.Tools) != tc.off {
				t.Errorf("switch off: tools = %v, want %d", toolKeys(m), tc.off)
			}
			if n := countWarnings(m, "allow_insecure"); n != tc.on-tc.off {
				t.Errorf("switch off: %d warnings name the switch, want %d: %q", n, tc.on-tc.off, m.Warnings)
			}
			m = parseREST(t, tc.src, restutil.Options{AllowUnsafePaths: true})
			if len(m.Tools) != tc.on {
				t.Errorf("switch on: tools = %v, want %d", toolKeys(m), tc.on)
			}
			for _, tl := range m.Tools {
				if strings.ContainsAny(string(tl.Key), "\x1b\a?") || strings.ContainsRune(string(tl.Key), 0x202e) {
					t.Errorf("switch on: %q was let through", tl.Key)
				}
			}
			if n := countWarnings(m, "skipped"); n != 2 {
				t.Errorf("switch on: %d paths still skipped, want 2: %q", n, m.Warnings)
			}
		})
	}
}
