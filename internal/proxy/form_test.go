package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

const formCT = "application/x-www-form-urlencoded"

func formTool(enc map[string]model.Encoding, fields ...string) model.Tool {
	t := model.Tool{Key: "POST /v1/customers", Name: "createCustomer", Method: "POST", Path: "/v1/customers",
		Body: &model.Body{ContentType: formCT, Schema: json.RawMessage(`{"type":"object"}`), Encoding: enc}}
	for _, f := range fields {
		t.Params = append(t.Params, param(f, model.InBody, false))
	}
	return t
}

func formBody(t *testing.T, tl model.Tool, rawArgs string, tg Target) (url.Values, string) {
	t.Helper()
	req := mustBuild(t, tl, rawArgs, tg)
	b, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	v, err := url.ParseQuery(string(b))
	if err != nil {
		t.Fatalf("not a form body: %q", b)
	}
	return v, req.Header.Get("Content-Type")
}

func TestFormScalars(t *testing.T) {
	tl := formTool(nil, "email", "balance", "rate", "tax_exempt", "note")
	v, ct := formBody(t, tl, `{"email":"a@b.test","balance":1200,"rate":0.5,"tax_exempt":false,"note":"x&y=z"}`, Target{})
	if ct != formCT {
		t.Errorf("Content-Type = %q", ct)
	}
	want := url.Values{"email": {"a@b.test"}, "balance": {"1200"}, "rate": {"0.5"}, "tax_exempt": {"false"}, "note": {"x&y=z"}}
	if v.Encode() != want.Encode() {
		t.Errorf("body = %v, want %v", v, want)
	}
}

// OpenAPI's default for forms is style form, explode true: arrays repeat
// the key and objects spread into their properties.
func TestFormDefaultStyle(t *testing.T) {
	tl := formTool(nil, "tags", "address")
	v, _ := formBody(t, tl, `{"tags":["a","b"],"address":{"city":"Pune","zip":"411"}}`, Target{})
	if got := v["tags"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("tags = %v", got)
	}
	if v.Get("city") != "Pune" || v.Get("zip") != "411" || v.Has("address") {
		t.Errorf("object not spread: %v", v)
	}
}

// deepObject (Stripe) uses bracket keys, recursively, with indexed arrays.
func TestFormDeepObject(t *testing.T) {
	enc := map[string]model.Encoding{
		"metadata": {Style: "deepObject", Explode: true},
		"items":    {Style: "deepObject", Explode: true},
		"expand":   {Style: "deepObject", Explode: true},
	}
	tl := formTool(enc, "metadata", "items", "expand")
	v, _ := formBody(t, tl, `{"metadata":{"order":"42","tier":"gold"},"items":[{"price":"p_1","quantity":2},{"price":"p_2"}],"expand":["customer"]}`, Target{})
	want := url.Values{
		"metadata[order]":    {"42"},
		"metadata[tier]":     {"gold"},
		"items[0][price]":    {"p_1"},
		"items[0][quantity]": {"2"},
		"items[1][price]":    {"p_2"},
		"expand[0]":          {"customer"},
	}
	if v.Encode() != want.Encode() {
		t.Errorf("body = %v\nwant %v", v, want)
	}
}

// Objects inside a form-style array can't be expressed as repeated keys:
// they fall back to brackets. Nulls are left out.
func TestFormNestedFallbackAndNulls(t *testing.T) {
	tl := formTool(nil, "lines", "gone")
	v, _ := formBody(t, tl, `{"lines":[{"sku":"a"},"plain"],"gone":null}`, Target{})
	if v.Get("lines[0][sku]") != "a" || v.Get("lines[1]") != "plain" || v.Has("gone") {
		t.Errorf("body = %v", v)
	}
}

// A fixed in: body value joins the form; a flattened body nested under
// one parameter (bodyRoot) is encoded the same way.
func TestFormFixedAndRoot(t *testing.T) {
	tl := formTool(nil, "email")
	tl.Params = append(tl.Params, model.Param{Name: "account", UpstreamName: "account", In: model.InBody, Hidden: true, Schema: json.RawMessage(`{}`)})
	v, _ := formBody(t, tl, `{"email":"a@b.test"}`, Target{Fixed: []Fixed{{Name: "account", Value: "acct_1"}}})
	if v.Get("account") != "acct_1" || v.Get("email") != "a@b.test" {
		t.Errorf("body = %v", v)
	}

	root := formTool(nil)
	root.Params = []model.Param{param("body", model.InBodyRoot, true)}
	v, _ = formBody(t, root, `{"body":{"email":"c@d.test","tags":["x"]}}`, Target{})
	if v.Get("email") != "c@d.test" || v.Get("tags") != "x" {
		t.Errorf("root body = %v", v)
	}
	if req, err := build(t, root, `{"body":"not an object"}`, Target{}); err == nil {
		t.Errorf("a non-object form body was sent: %v", req)
	}
}

func TestMultipartTextFields(t *testing.T) {
	tl := formTool(nil, "title", "tags", "meta")
	tl.Body.ContentType = "multipart/form-data"
	req := mustBuild(t, tl, `{"title":"hello","tags":["a","b"],"meta":{"k":1}}`, Target{})
	mt, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q (%v)", req.Header.Get("Content-Type"), err)
	}
	form, err := multipart.NewReader(req.Body, params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := form.Value["title"]; len(got) != 1 || got[0] != "hello" {
		t.Errorf("title = %v", got)
	}
	if got := form.Value["tags"]; len(got) != 2 {
		t.Errorf("tags = %v", got)
	}
	if got := form.Value["meta"]; len(got) != 1 || !strings.Contains(got[0], `"k":1`) {
		t.Errorf("meta = %v (objects go as JSON text)", got)
	}
}

// JSON bodies are unchanged.
func TestJSONBodyUnchanged(t *testing.T) {
	tl := formTool(nil, "email")
	tl.Body.ContentType = "application/json"
	req := mustBuild(t, tl, `{"email":"a@b.test"}`, Target{})
	b, _ := io.ReadAll(req.Body)
	if string(b) != `{"email":"a@b.test"}` || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("body = %s, ct = %s", b, req.Header.Get("Content-Type"))
	}
}

// A spread object may not supply a key that another top-level field owns: it
// would put a second, model-chosen value beside a fixed (hidden) field, and
// many servers read the first one (SPEC T6).
func TestFormSpreadObjectCannotShadowFields(t *testing.T) {
	tl := formTool(nil, "email", "filter")
	tl.Params = append(tl.Params, model.Param{Name: "tenant_id", UpstreamName: "tenant_id", In: model.InBody, Hidden: true, Schema: json.RawMessage(`{}`)})
	tg := Target{Fixed: []Fixed{{Name: "tenant_id", Value: "t1"}}}

	for name, raw := range map[string]string{
		"fixed field":        `{"filter":{"tenant_id":"other"}}`,
		"fixed field, deep":  `{"filter":{"tenant_id":{"a":"other"}}}`,
		"another argument":   `{"email":"a@b.test","filter":{"email":"x@y.test"}}`,
		"an unsent argument": `{"filter":{"email":"x@y.test"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := build(t, tl, raw, tg); !errors.Is(err, ErrBadArgument) {
				t.Errorf("err = %v, want ErrBadArgument", err)
			}
		})
	}

	v, _ := formBody(t, tl, `{"filter":{"status":"open"}}`, tg)
	if got := v["tenant_id"]; len(got) != 1 || got[0] != "t1" {
		t.Errorf("tenant_id = %v, want just the fixed value", got)
	}
	if v.Get("status") != "open" {
		t.Errorf("spread object lost: %v", v)
	}
}
