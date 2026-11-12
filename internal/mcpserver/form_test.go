package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

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
