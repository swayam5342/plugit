package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/schema"
)

// A tool with a form body (Stripe style) arrives at the API as a form,
// with deepObject fields in brackets.
func TestFormBodyEndToEnd(t *testing.T) {
	createCustomer := model.Tool{Key: "POST /v1/customers", Name: "createCustomer", Method: "POST", Path: "/v1/customers",
		Description: "Create a customer.", Safety: model.Write}
	createCustomer.Body = &model.Body{
		ContentType: "application/x-www-form-urlencoded",
		Schema:      json.RawMessage(`{"type":"object","required":["email"],"properties":{"email":{"type":"string"},"metadata":{"type":"object","additionalProperties":{"type":"string"}},"expand":{"type":"array","items":{"type":"string"}}}}`),
		Required:    true,
		Encoding:    map[string]model.Encoding{"metadata": {Style: "deepObject", Explode: true}, "expand": {Style: "deepObject", Explode: true}},
	}
	createCustomer = schema.Prepare(createCustomer, schema.Options{})

	var got atomic.Value
	upstream := func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			got.Store(r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cus_1"}`))
	}
	r := setup(t, Config{Tools: served(createCustomer), Mode: policy.Full}, upstream)
	res, err := r.call(t, "createCustomer", map[string]any{"email": "a@b.test", "metadata": map[string]any{"plan": "pro"}, "expand": []any{"tax"}})
	if err != nil || res.IsError {
		t.Fatalf("err=%v text=%s", err, text(res))
	}
	form, _ := got.Load().(url.Values)
	if form.Get("email") != "a@b.test" || form.Get("metadata[plan]") != "pro" || form.Get("expand[0]") != "tax" {
		t.Errorf("upstream form = %v", form)
	}
}

// A tool's response_fields reach the model filtered.
func TestResponseFields(t *testing.T) {
	p, err := jsonpick.Parse([]string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	upstream := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"secret_notes":"long text"}]`))
	}
	r := setup(t, Config{Tools: []Tool{{Tool: listPets, Fields: p}}, Mode: policy.ReadOnly}, upstream)
	res, err := r.call(t, "listPets", map[string]any{})
	if err != nil || res.IsError {
		t.Fatalf("err=%v text=%s", err, text(res))
	}
	if strings.Contains(text(res), "secret_notes") || !strings.Contains(text(res), "Filtered to fields: id") {
		t.Errorf("text = %s", text(res))
	}
}

// Cookie parameters reach the API as cookies; the model never sees or sets
// the Cookie header itself.
func TestCookieParamsEndToEnd(t *testing.T) {
	me := schema.Prepare(model.Tool{Key: "GET /me", Name: "getMe", Method: "GET", Path: "/me", Description: "Who am I.", Safety: model.ReadOnly,
		Params: []model.Param{
			{Name: "theme", UpstreamName: "theme", In: model.InCookie, Schema: []byte(`{"type":"string"}`)},
			{Name: "Cookie", UpstreamName: "Cookie", In: model.InHeader, Schema: []byte(`{"type":"string"}`)},
		}}, schema.Options{})
	if strings.Contains(string(schema.InputSchema(me)), `"Cookie"`) {
		t.Fatalf("the Cookie header is a tool parameter: %s", schema.InputSchema(me))
	}
	var got atomic.Value
	upstream := func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("theme"); err == nil {
			got.Store(c.Value)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
	r := setup(t, Config{Tools: served(me), Mode: policy.ReadOnly}, upstream)
	if res, err := r.call(t, "getMe", map[string]any{"theme": "dark"}); err != nil || res.IsError {
		t.Fatalf("err=%v text=%s", err, text(res))
	}
	if got.Load() != "dark" {
		t.Errorf("upstream cookie = %v", got.Load())
	}
	if res, _ := r.call(t, "getMe", map[string]any{"theme": "dark; admin=1"}); !res.IsError {
		t.Error("a cookie injection was sent")
	}
}
