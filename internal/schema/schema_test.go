package schema

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func p(name string, in model.Location, required bool, schema string) model.Param {
	return model.Param{Name: name, UpstreamName: name, In: in, Required: required, Schema: json.RawMessage(schema)}
}

func body(required bool, schema string) *model.Body {
	return &model.Body{ContentType: "application/json", Required: required, Schema: json.RawMessage(schema)}
}

func find(t *testing.T, tl model.Tool, name string) model.Param {
	t.Helper()
	for _, pr := range tl.Params {
		if pr.Name == name {
			return pr
		}
	}
	t.Fatalf("no param with model name %q in %+v", name, tl.Params)
	return model.Param{}
}

func names(tl model.Tool) []string {
	var out []string
	for _, pr := range tl.Params {
		out = append(out, pr.Name)
	}
	return out
}

func assertJSON(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: invalid JSON %s: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

func hasIssue(tl model.Tool, kind model.IssueKind) bool {
	for _, is := range tl.Issues {
		if is.Kind == kind {
			return true
		}
	}
	return false
}

func TestInputSchemaSimple(t *testing.T) {
	in := model.Tool{Key: "GET /pets/{petId}", Params: []model.Param{
		p("petId", model.InPath, true, `{"type":"string"}`),
		{Name: "limit", UpstreamName: "limit", In: model.InQuery, Schema: json.RawMessage(`{"type":"integer"}`), Desc: "Max items"},
		{Name: "q", UpstreamName: "q", In: model.InQuery, Schema: json.RawMessage(`{"type":"string","description":"own"}`), Desc: "param desc"},
	}}
	out := Prepare(in, Options{})
	assertJSON(t, "input schema", InputSchema(out), `{
		"type":"object",
		"properties":{
			"petId":{"type":"string"},
			"limit":{"type":"integer","description":"Max items"},
			"q":{"type":"string","description":"own"}
		},
		"required":["petId"],
		"additionalProperties":false
	}`)
}

func TestInputSchemaNoParams(t *testing.T) {
	assertJSON(t, "empty", InputSchema(Prepare(model.Tool{Key: "GET /x"}, Options{})),
		`{"type":"object","properties":{},"additionalProperties":false}`)
}

func TestFlattenObjectBody(t *testing.T) {
	in := model.Tool{Key: "POST /pets", Body: body(true, `{
		"type":"object","required":["name"],
		"properties":{"name":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}}}`)}
	out := Prepare(in, Options{})

	if !reflect.DeepEqual(names(out), []string{"name", "tags"}) {
		t.Fatalf("names = %v", names(out))
	}
	name := find(t, out, "name")
	if name.In != model.InBody || name.UpstreamName != "name" || !name.Required {
		t.Errorf("name = %+v", name)
	}
	if find(t, out, "tags").Required {
		t.Error("tags should be optional")
	}
	if out.Body == nil || out.Body.ContentType != "application/json" {
		t.Error("Body must be kept for the content type")
	}
	if hasIssue(out, model.IssueBodyNested) {
		t.Error("unexpected body-nested issue")
	}
}

func TestFlattenOptionalBodyMakesFieldsOptional(t *testing.T) {
	in := model.Tool{Key: "PATCH /pets", Body: body(false, `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)}
	if find(t, Prepare(in, Options{}), "name").Required {
		t.Error("a field of an optional body must not be required")
	}
}

func TestFlattenCollisionRenamesBodyField(t *testing.T) {
	in := model.Tool{Key: "PUT /pets/{id}",
		Params: []model.Param{p("id", model.InPath, true, `{"type":"string"}`)},
		Body:   body(true, `{"type":"object","properties":{"id":{"type":"integer"},"name":{"type":"string"}}}`)}
	out := Prepare(in, Options{})
	bid := find(t, out, "body_id")
	if bid.UpstreamName != "id" || bid.In != model.InBody {
		t.Errorf("body_id = %+v", bid)
	}
	if find(t, out, "id").In != model.InPath {
		t.Error("path id must keep its name")
	}
}

func TestFlattenDoubleCollisionNestsBody(t *testing.T) {
	in := model.Tool{Key: "PUT /pets/{id}",
		Params: []model.Param{p("id", model.InPath, true, `{"type":"string"}`)},
		Body:   body(true, `{"type":"object","properties":{"id":{"type":"integer"},"body_id":{"type":"string"}}}`)}
	out := Prepare(in, Options{})
	if !reflect.DeepEqual(names(out), []string{"id", "body"}) {
		t.Fatalf("names = %v, want [id body]", names(out))
	}
	b := find(t, out, "body")
	if b.In != model.InBodyRoot || !b.Required {
		t.Errorf("body = %+v", b)
	}
	assertJSON(t, "nested body schema", b.Schema, `{"type":"object","properties":{"id":{"type":"integer"},"body_id":{"type":"string"}}}`)
	if !hasIssue(out, model.IssueBodyNested) {
		t.Error("missing body-nested issue")
	}
}

func TestNonObjectBodies(t *testing.T) {
	arr := model.Tool{Key: "POST /batch", Body: body(true, `{"type":"array","items":{"type":"string"}}`)}
	out := Prepare(arr, Options{})
	b := find(t, out, "body")
	if b.In != model.InBodyRoot || b.UpstreamName != "" || !b.Required {
		t.Errorf("array body = %+v", b)
	}

	taken := model.Tool{Key: "POST /batch",
		Params: []model.Param{p("body", model.InQuery, false, `{"type":"string"}`)},
		Body:   body(false, `{"type":"string"}`)}
	out = Prepare(taken, Options{})
	if find(t, out, "body_payload").In != model.InBodyRoot {
		t.Error("body_payload should hold the body when 'body' is taken")
	}
}

func TestObjectBodiesThatCannotBeFlattened(t *testing.T) {
	for name, s := range map[string]string{
		"additionalProperties": `{"type":"object","properties":{"a":{}},"additionalProperties":{"type":"string"}}`,
		"oneOf":                `{"oneOf":[{"type":"object","properties":{"a":{}}},{"type":"object","properties":{"b":{}}}]}`,
		"no properties":        `{"type":"object"}`,
		"degraded":             `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			out := Prepare(model.Tool{Key: "POST /x", Body: body(true, s)}, Options{})
			if !reflect.DeepEqual(names(out), []string{"body"}) || find(t, out, "body").In != model.InBodyRoot {
				t.Errorf("params = %+v, want a single bodyRoot param", out.Params)
			}
		})
	}
}

func TestForbiddenHeadersRemoved(t *testing.T) {
	var params []model.Param
	for _, h := range []string{
		"Authorization", "cookie", "Host", "Proxy-Authorization", "X-Forwarded-For", "x-forwarded-host",
		"Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "TE", "Trailer", "Upgrade", "Content-Type",
	} {
		params = append(params, p(h, model.InHeader, false, `{"type":"string"}`))
	}
	params = append(params, p("X-Request-Id", model.InHeader, false, `{"type":"string"}`))
	// The same names in a non-header location are ordinary parameters.
	params = append(params, p("host", model.InQuery, false, `{"type":"string"}`))

	out := Prepare(model.Tool{Key: "GET /x", Params: params}, Options{})
	if !reflect.DeepEqual(names(out), []string{"X-Request-Id", "host"}) {
		t.Errorf("names = %v, want [X-Request-Id host]", names(out))
	}
}

func TestFixedParamsAreHidden(t *testing.T) {
	in := model.Tool{Key: "POST /x",
		Params: []model.Param{
			p("api_key", model.InQuery, true, `{"type":"string"}`),
			p("X-Api-Version", model.InHeader, true, `{"type":"string"}`),
			p("q", model.InQuery, false, `{"type":"string"}`),
		},
		Body: body(true, `{"type":"object","required":["tenant"],"properties":{"tenant":{"type":"string"},"name":{"type":"string"}}}`)}
	out := Prepare(in, Options{Fixed: []string{"api_key", "x-api-version", "tenant"}})

	for _, n := range []string{"api_key", "X-Api-Version", "tenant"} {
		if !find(t, out, n).Hidden {
			t.Errorf("%s should be hidden", n)
		}
	}
	assertJSON(t, "input schema", InputSchema(out), `{
		"type":"object",
		"properties":{"q":{"type":"string"},"name":{"type":"string"}},
		"additionalProperties":false
	}`)
}

func TestFixedQueryNameIsCaseSensitive(t *testing.T) {
	in := model.Tool{Key: "GET /x", Params: []model.Param{p("API_KEY", model.InQuery, false, `{}`)}}
	if find(t, Prepare(in, Options{Fixed: []string{"api_key"}}), "API_KEY").Hidden {
		t.Error("query names are case-sensitive; API_KEY must not match api_key")
	}
}

func TestNamesAreSanitizedAndUnique(t *testing.T) {
	in := model.Tool{Key: "GET /items/{id}", Params: []model.Param{
		p("id", model.InPath, true, `{}`),
		p("id", model.InQuery, false, `{}`),
		p("ID", model.InHeader, false, `{}`),
		p("filter[status]", model.InQuery, false, `{}`),
		p("filter(status)", model.InQuery, false, `{}`),
	}}
	out := Prepare(in, Options{})
	want := []string{"id", "query_id", "header_ID", "filter_status", "filter_status_2"}
	if !reflect.DeepEqual(names(out), want) {
		t.Fatalf("names = %v, want %v", names(out), want)
	}
	if find(t, out, "filter_status").UpstreamName != "filter[status]" {
		t.Error("UpstreamName must keep the original name")
	}
}

func TestPrepareDoesNotMutateInput(t *testing.T) {
	in := model.Tool{Key: "PUT /pets/{id}",
		Params: []model.Param{p("id", model.InPath, true, `{}`), p("Authorization", model.InHeader, false, `{}`)},
		Body:   body(true, `{"type":"object","properties":{"id":{}}}`)}
	before, _ := json.Marshal(in)
	_ = Prepare(in, Options{Fixed: []string{"id"}})
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Errorf("input mutated:\n%s\n%s", before, after)
	}
}

func TestPrepareIsDeterministic(t *testing.T) {
	in := model.Tool{Key: "POST /x", Body: body(true, `{"type":"object","properties":{"z":{},"a":{},"m":{},"b":{}}}`)}
	first := names(Prepare(in, Options{}))
	if !reflect.DeepEqual(first, []string{"a", "b", "m", "z"}) {
		t.Errorf("body fields = %v, want sorted", first)
	}
	for range 10 {
		if got := names(Prepare(in, Options{})); !reflect.DeepEqual(got, first) {
			t.Fatalf("non-deterministic: %v vs %v", got, first)
		}
	}
}
