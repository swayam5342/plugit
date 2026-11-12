package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

func param(name string, in model.Location, required bool) model.Param {
	return model.Param{Name: name, UpstreamName: name, In: in, Required: required, Schema: json.RawMessage(`{}`)}
}

func base(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func args(t *testing.T, raw string) map[string]any {
	t.Helper()
	a, err := schema.DecodeArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func build(t *testing.T, tl model.Tool, rawArgs string, tg Target) (*http.Request, error) {
	t.Helper()
	if tg.BaseURL == nil {
		tg.BaseURL = base(t, "https://api.example.test/v1")
	}
	return Build(t.Context(), tl, args(t, rawArgs), tg)
}

func mustBuild(t *testing.T, tl model.Tool, rawArgs string, tg Target) *http.Request {
	t.Helper()
	req, err := build(t, tl, rawArgs, tg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return req
}

var getPet = model.Tool{Key: "GET /pets/{petId}", Name: "getPet", Method: "GET", Path: "/pets/{petId}", Params: []model.Param{
	param("petId", model.InPath, true),
	param("limit", model.InQuery, false),
	param("tags", model.InQuery, false),
	param("filter", model.InQuery, false),
	param("X-Trace", model.InHeader, false),
}}

func TestBuildPathQueryHeader(t *testing.T) {
	req := mustBuild(t, getPet, `{"petId": "a b", "limit": 10, "tags": ["x", "y z"], "filter": {"state": "open", "n": 2}, "X-Trace": "t-1"}`, Target{UserAgent: "plugit/test"})
	if req.Method != http.MethodGet {
		t.Errorf("method = %s", req.Method)
	}
	if got, want := req.URL.EscapedPath(), "/v1/pets/a%20b"; got != want {
		t.Errorf("path = %s, want %s", got, want)
	}
	q := req.URL.Query()
	if q.Get("limit") != "10" || !reflect.DeepEqual(q["tags"], []string{"x", "y z"}) {
		t.Errorf("query = %v", q)
	}
	if q.Get("state") != "open" || q.Get("n") != "2" {
		t.Errorf("object query param must explode into its properties, got %v", q)
	}
	if req.Header.Get("X-Trace") != "t-1" || req.Header.Get("User-Agent") != "plugit/test" {
		t.Errorf("headers = %v", req.Header)
	}
	if req.Body != nil && req.ContentLength != 0 {
		t.Error("GET without a body must not send one")
	}
	if req.Header.Get("Content-Type") != "" {
		t.Error("no Content-Type without a body")
	}
}

func TestBuildScalarFormatting(t *testing.T) {
	tl := model.Tool{Method: "GET", Path: "/x", Params: []model.Param{
		param("i", model.InQuery, false), param("big", model.InQuery, false), param("f", model.InQuery, false),
		param("b", model.InQuery, false), param("n", model.InQuery, false),
	}}
	q := mustBuild(t, tl, `{"i": 3, "big": 12345678901234567890, "f": 1.5, "b": true, "n": null}`, Target{}).URL.Query()
	for k, want := range map[string]string{"i": "3", "big": "12345678901234567890", "f": "1.5", "b": "true"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if _, ok := q["n"]; ok {
		t.Error("null arguments must be omitted")
	}
}

func TestBuildRejectsDangerousPathValues(t *testing.T) {
	for _, v := range []string{
		``, `.`, `..`, `a/b`, `a\\b`, `%2e%2e`, `%2E%2e`, `..%2f`, `%2F`, `a%2fb`, `%5c`,
		"a\nb", "a\rb", "a\u0000b", "a\tb",
	} {
		t.Run(v, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"petId": v})
			_, err := build(t, getPet, string(raw), Target{})
			if !errors.Is(err, ErrBadArgument) {
				t.Errorf("petId %q: err = %v, want ErrBadArgument", v, err)
			}
		})
	}
}

func TestBuildPathValuesAreEncodedNotInterpreted(t *testing.T) {
	for v, want := range map[string]string{
		"x?admin=1": "/v1/pets/x%3Fadmin=1",
		"x#frag":    "/v1/pets/x%23frag",
		"100%":      "/v1/pets/100%25",
		"é":         "/v1/pets/%C3%A9",
		"a;b=c":     "/v1/pets/a%3Bb=c", // ';' is encoded: some servers treat it as a path-parameter separator
	} {
		raw, _ := json.Marshal(map[string]any{"petId": v})
		req := mustBuild(t, getPet, string(raw), Target{})
		if got := req.URL.EscapedPath(); got != want {
			t.Errorf("petId %q: path = %s, want %s", v, got, want)
		}
		if req.URL.RawQuery != "" || req.URL.Fragment != "" {
			t.Errorf("petId %q leaked into query/fragment: %s", v, req.URL)
		}
	}
}

func TestBuildPathRejectsNonScalars(t *testing.T) {
	for _, v := range []string{`{"petId": [1]}`, `{"petId": {"a": 1}}`} {
		if _, err := build(t, getPet, v, Target{}); !errors.Is(err, ErrBadArgument) {
			t.Errorf("%s: err = %v, want ErrBadArgument", v, err)
		}
	}
}

func TestBuildRejectsBadHeaderValues(t *testing.T) {
	for _, v := range []string{"a\r\nX-Injected: 1", "a\nb", "a\rb", "a\u0000b", "a\u007fb"} {
		raw, _ := json.Marshal(map[string]any{"petId": "1", "X-Trace": v})
		if _, err := build(t, getPet, string(raw), Target{}); !errors.Is(err, ErrBadArgument) {
			t.Errorf("header %q: err = %v, want ErrBadArgument", v, err)
		}
	}
}

func TestBuildRejectsUnknownAndHiddenArguments(t *testing.T) {
	tl := getPet
	tl.Params = append([]model.Param{}, getPet.Params...)
	tl.Params = append(tl.Params, model.Param{Name: "api_key", UpstreamName: "api_key", In: model.InQuery, Hidden: true, Schema: json.RawMessage(`{}`)})
	for _, raw := range []string{`{"petId": "1", "url": "http://evil.test"}`, `{"petId": "1", "api_key": "attacker"}`} {
		if _, err := build(t, tl, raw, Target{}); !errors.Is(err, ErrBadArgument) {
			t.Errorf("%s: err = %v, want ErrBadArgument", raw, err)
		}
	}
}

func TestBuildUnresolvedPlaceholder(t *testing.T) {
	tl := model.Tool{Method: "GET", Path: "/pets/{petId}/toys/{toyId}", Params: []model.Param{param("petId", model.InPath, true)}}
	if _, err := build(t, tl, `{"petId": "1"}`, Target{}); !errors.Is(err, ErrBuild) {
		t.Errorf("err = %v, want ErrBuild", err)
	}
	// A declared but missing path argument is also unresolved.
	if _, err := build(t, getPet, `{}`, Target{}); !errors.Is(err, ErrBuild) {
		t.Errorf("missing path arg: err = %v, want ErrBuild", err)
	}
}

func TestBuildBaseURLJoining(t *testing.T) {
	tl := model.Tool{Method: "GET", Path: "/items"}
	for b, want := range map[string]string{
		"https://api.example.test":       "https://api.example.test/items",
		"https://api.example.test/":      "https://api.example.test/items",
		"https://api.example.test/v1":    "https://api.example.test/v1/items",
		"https://api.example.test/v1/":   "https://api.example.test/v1/items",
		"https://api.example.test/a%20b": "https://api.example.test/a%20b/items",
	} {
		req := mustBuild(t, tl, `{}`, Target{BaseURL: base(t, b)})
		if req.URL.String() != want {
			t.Errorf("base %s: url = %s, want %s", b, req.URL, want)
		}
	}
	root := model.Tool{Method: "GET", Path: "/"}
	if got := mustBuild(t, root, `{}`, Target{BaseURL: base(t, "https://api.example.test/v1")}).URL.String(); got != "https://api.example.test/v1/" {
		t.Errorf("root path url = %s", got)
	}
}

func TestBuildFlattenedAndRootBodies(t *testing.T) {
	flat := model.Tool{Method: "PUT", Path: "/pets/{id}", Body: &model.Body{ContentType: "application/json"}, Params: []model.Param{
		param("id", model.InPath, true),
		{Name: "body_id", UpstreamName: "id", In: model.InBody, Schema: json.RawMessage(`{}`)},
		{Name: "name", UpstreamName: "name", In: model.InBody, Schema: json.RawMessage(`{}`)},
		{Name: "tags", UpstreamName: "tags", In: model.InBody, Schema: json.RawMessage(`{}`)},
	}}
	req := mustBuild(t, flat, `{"id": "p1", "body_id": 7, "name": "Rex", "tags": ["a"]}`, Target{})
	assertBody(t, req, `{"id": 7, "name": "Rex", "tags": ["a"]}`)
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", req.Header.Get("Content-Type"))
	}

	root := model.Tool{Method: "POST", Path: "/batch", Body: &model.Body{ContentType: "application/json"}, Params: []model.Param{
		{Name: "body", In: model.InBodyRoot, Schema: json.RawMessage(`{}`)},
	}}
	assertBody(t, mustBuild(t, root, `{"body": [1, "two", {"three": 3}]}`, Target{}), `[1, "two", {"three": 3}]`)

	// A body tool called without any body arguments sends no body.
	req = mustBuild(t, flat, `{"id": "p1"}`, Target{})
	if req.ContentLength != 0 {
		t.Errorf("no body args: ContentLength = %d", req.ContentLength)
	}
}

func assertBody(t *testing.T, req *http.Request, want string) {
	t.Helper()
	b, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("body %q: %v", b, err)
	}
	_ = json.Unmarshal([]byte(want), &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("body = %s, want %s", b, want)
	}
}

func TestBuildFixedParams(t *testing.T) {
	tl := model.Tool{Method: "POST", Path: "/x", Body: &model.Body{ContentType: "application/json"}, Params: []model.Param{
		{Name: "api_key", UpstreamName: "api_key", In: model.InQuery, Hidden: true, Schema: json.RawMessage(`{}`)},
		{Name: "X-Api-Version", UpstreamName: "X-Api-Version", In: model.InHeader, Hidden: true, Schema: json.RawMessage(`{}`)},
		{Name: "tenant", UpstreamName: "tenant", In: model.InBody, Hidden: true, Schema: json.RawMessage(`{}`)},
		{Name: "name", UpstreamName: "name", In: model.InBody, Schema: json.RawMessage(`{}`)},
	}}
	req := mustBuild(t, tl, `{"name": "n"}`, Target{Fixed: []Fixed{
		{Name: "api_key", In: model.InHeader, Value: "test-token-123"}, // declared in query: the spec wins
		{Name: "x-api-version", Value: "2022-11-28"},                   // header names match case-insensitively
		{Name: "tenant", Value: "t-9"},
		{Name: "region", In: model.InHeader, Value: "eu"}, // undeclared: configured location
		{Name: "trace", Value: "on"},                      // undeclared, no location: query
	}})
	if req.URL.Query().Get("api_key") != "test-token-123" || req.Header.Get("api_key") != "" {
		t.Errorf("declared fixed param must go where the spec says: url=%s", req.URL)
	}
	if req.Header.Get("X-Api-Version") != "2022-11-28" || req.Header.Get("Region") != "eu" || req.URL.Query().Get("trace") != "on" {
		t.Errorf("fixed headers/query: headers=%v url=%s", req.Header, req.URL)
	}
	assertBody(t, req, `{"tenant": "t-9", "name": "n"}`)
}

func TestBuildAuth(t *testing.T) {
	tl := model.Tool{Method: "GET", Path: "/me"}
	const secret = "test-token-123"
	cases := map[AuthType]func(t *testing.T, r *http.Request){
		AuthBearer: func(t *testing.T, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
		},
		AuthHeader: func(t *testing.T, r *http.Request) {
			if r.Header.Get("X-Api-Key") != secret {
				t.Errorf("X-Api-Key = %q", r.Header.Get("X-Api-Key"))
			}
		},
		AuthQuery: func(t *testing.T, r *http.Request) {
			if r.URL.Query().Get("key") != secret {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
		},
		AuthBasic: func(t *testing.T, r *http.Request) {
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+secret))
			if r.Header.Get("Authorization") != want {
				t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), want)
			}
		},
		AuthNone: func(t *testing.T, r *http.Request) {
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Error("no credentials expected")
			}
		},
	}
	for typ, check := range cases {
		t.Run(string(typ), func(t *testing.T) {
			s := secret
			if typ == AuthBasic {
				s = "alice:" + secret
			}
			req := mustBuild(t, tl, `{}`, Target{Auth: Auth{Type: typ, Header: "X-Api-Key", Param: "key", Secret: s}})
			check(t, req)
		})
	}
}

func TestBuildAuthOverridesModelSuppliedHeaders(t *testing.T) {
	// Even if a spec smuggled an auth-named header past schema.Prepare, the
	// configured credential replaces whatever the model sent.
	tl := model.Tool{Method: "GET", Path: "/me", Params: []model.Param{param("X-Api-Key", model.InHeader, false)}}
	req := mustBuild(t, tl, `{"X-Api-Key": "attacker"}`, Target{Auth: Auth{Type: AuthHeader, Header: "X-Api-Key", Secret: "test-token-123"}})
	if got := req.Header.Values("X-Api-Key"); !reflect.DeepEqual(got, []string{"test-token-123"}) {
		t.Errorf("X-Api-Key = %v", got)
	}
}

func TestBuildRejectsBaseURLWithUserinfoOrQuery(t *testing.T) {
	tl := model.Tool{Method: "GET", Path: "/x"}
	for _, b := range []string{"https://user:pw@api.example.test", "https://api.example.test/?a=1", "ftp://api.example.test", "/relative"} {
		if _, err := build(t, tl, `{}`, Target{BaseURL: base(t, b)}); !errors.Is(err, ErrBuild) {
			t.Errorf("base %s: err = %v, want ErrBuild", b, err)
		}
	}
}

func TestBuildErrorsNeverContainSecrets(t *testing.T) {
	tl := model.Tool{Method: "GET", Path: "/pets/{petId}", Params: []model.Param{param("petId", model.InPath, true)}}
	_, err := build(t, tl, `{"petId": ".."}`, Target{Auth: Auth{Type: AuthBearer, Secret: "test-token-123"}, Fixed: []Fixed{{Name: "k", Value: "fixed-secret-456"}}})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range []string{"test-token-123", "fixed-secret-456"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %q: %v", s, err)
		}
	}
}
