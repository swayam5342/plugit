package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
)

const userDoc = "query PlugIt_user($id: ID!, $orgLogin: String) { user(id: $id, orgLogin: $orgLogin) { id name } }"

// userTool has one param whose model-facing Name differs from its upstream
// variable name, as Task 7's 64-byte cap or de-duplication can produce.
func userTool() model.Tool {
	return model.Tool{
		Key:  "query user",
		Name: "user",
		Params: []model.Param{
			{Name: "id", Bind: model.Binding{In: model.InVariable, Upstream: "id"}, Required: true},
			{Name: "org_login_2", Bind: model.Binding{In: model.InVariable, Upstream: "orgLogin"}},
		},
		GraphQL: &model.GraphQLOp{Operation: "query", Field: "user", Document: userDoc},
	}
}

func endpoint(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse("https://api.example.test/v4/graphql")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type wireBody struct {
	Query         string                     `json:"query"`
	Variables     map[string]json.RawMessage `json:"variables"`
	OperationName string                     `json:"operationName"`
}

func encodeReq(t *testing.T, g GraphQL, args map[string]any) (*http.Request, []byte, wireBody) {
	t.Helper()
	req, err := g.Encode(context.Background(), protocol.Call{Tool: userTool(), Args: args, Endpoint: endpoint(t)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var wb wireBody
	if err := json.Unmarshal(raw, &wb); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	return req, raw, wb
}

func TestName(t *testing.T) {
	if got := (GraphQL{}).Name(); got != model.GraphQL {
		t.Errorf("Name() = %q, want %q", got, model.GraphQL)
	}
}

func TestEncodeRequestShape(t *testing.T) {
	req, raw, wb := encodeReq(t, GraphQL{}, map[string]any{"id": "42", "org_login_2": "acme"})
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if got := req.URL.String(); got != "https://api.example.test/v4/graphql" {
		t.Errorf("URL = %s, want the endpoint as is", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "application/graphql-response+json, application/json" {
		t.Errorf("Accept = %q", got)
	}
	if !bytes.HasPrefix(raw, []byte(`{"query":`)) {
		t.Errorf("body should start with the query: %s", raw)
	}
	if wb.Query != userDoc {
		t.Errorf("query = %q, want the fixed document", wb.Query)
	}
	if wb.OperationName != "PlugIt_user" {
		t.Errorf("operationName = %q, want PlugIt_user", wb.OperationName)
	}
	if string(wb.Variables["id"]) != `"42"` {
		t.Errorf("variables.id = %s", wb.Variables["id"])
	}
}

func TestEncodeVariablesKeyedByUpstream(t *testing.T) {
	_, _, wb := encodeReq(t, GraphQL{}, map[string]any{"id": "42", "org_login_2": "acme"})
	if string(wb.Variables["orgLogin"]) != `"acme"` {
		t.Errorf("variables.orgLogin = %s, want \"acme\" (keyed by Bind.Upstream)", wb.Variables["orgLogin"])
	}
	if _, ok := wb.Variables["org_login_2"]; ok {
		t.Error("variables must not be keyed by the model-facing param name")
	}
}

func TestEncodeRejectsUnknownArgument(t *testing.T) {
	_, err := GraphQL{}.Encode(context.Background(), protocol.Call{
		Tool: userTool(), Args: map[string]any{"id": "1", "orgLogin": "x"}, Endpoint: endpoint(t),
	})
	if !errors.Is(err, ErrBadArgument) {
		t.Fatalf("err = %v, want ErrBadArgument (upstream name is not a model-facing name)", err)
	}
}

func TestEncodeNoCredentials(t *testing.T) {
	g := GraphQL{Target: Target{
		UserAgent: "plugit-test",
		Fixed: []Fixed{
			{Name: "X-Tenant", In: model.InHeader, Value: "t1"},
			{Name: "orgLogin", In: model.InVariable, Value: "test-token-123", Secret: true},
		},
	}}
	req, _, _ := encodeReq(t, g, map[string]any{"id": "1"})
	// The exact header set: nothing beyond the protocol's own headers and
	// the fixed header, so no credential header can slip in.
	want := []string{"Accept", "Content-Type", "User-Agent", "X-Tenant"}
	if got := slices.Sorted(maps.Keys(req.Header)); !slices.Equal(got, want) {
		t.Errorf("Encode set headers %q, want exactly %q; invoke attaches credentials, not the protocol", got, want)
	}
	if req.URL.RawQuery != "" {
		t.Errorf("Encode added a query string %q; no credential may ride in the URL", req.URL.RawQuery)
	}
	if req.URL.User != nil {
		t.Error("Encode added userinfo to the URL")
	}
}

func TestEncodeMergesDeclaredFixedVariable(t *testing.T) {
	g := GraphQL{Target: Target{Fixed: []Fixed{{Name: "orgLogin", In: model.InVariable, Value: "fixed-org"}}}}
	_, _, wb := encodeReq(t, g, map[string]any{"id": "1", "org_login_2": "model-org"})
	if string(wb.Variables["orgLogin"]) != `"fixed-org"` {
		t.Errorf("variables.orgLogin = %s, want the fixed value to win", wb.Variables["orgLogin"])
	}
}

func TestEncodeSkipsUndeclaredFixedVariable(t *testing.T) {
	g := GraphQL{Target: Target{Fixed: []Fixed{{Name: "tenant", In: model.InVariable, Value: "undeclared-value"}}}}
	_, raw, wb := encodeReq(t, g, map[string]any{"id": "1"})
	if _, ok := wb.Variables["tenant"]; ok {
		t.Error("a fixed variable the document doesn't declare must not be sent")
	}
	if bytes.Contains(raw, []byte("undeclared-value")) {
		t.Errorf("undeclared fixed value leaked into the body: %s", raw)
	}
}

func TestEncodeFixedHeaderAndUserAgent(t *testing.T) {
	g := GraphQL{Target: Target{
		UserAgent: "plugit/test",
		Fixed:     []Fixed{{Name: "X-Tenant", In: model.InHeader, Value: "t1"}},
	}}
	req, _, _ := encodeReq(t, g, map[string]any{"id": "1"})
	if got := req.Header.Get("X-Tenant"); got != "t1" {
		t.Errorf("X-Tenant = %q, want t1", got)
	}
	if got := req.Header.Get("User-Agent"); got != "plugit/test" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestEncodeRejectsBadFixedHeader(t *testing.T) {
	for _, tc := range []struct{ name, header, value string }{
		{"CR", "X-Tenant", "a\rb"},
		{"LF", "X-Tenant", "a\nInjected: 1"},
		{"forbidden header", "Authorization", "Bearer test-token-123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := GraphQL{Target: Target{Fixed: []Fixed{{Name: tc.header, In: model.InHeader, Value: tc.value}}}}
			_, err := g.Encode(context.Background(), protocol.Call{Tool: userTool(), Args: map[string]any{"id": "1"}, Endpoint: endpoint(t)})
			if !errors.Is(err, ErrBuild) {
				t.Fatalf("err = %v, want ErrBuild", err)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("error echoes the fixed value: %v", err)
			}
		})
	}
}

func TestEncodeErrorsWithoutPanic(t *testing.T) {
	noOp := userTool()
	noOp.GraphQL = nil
	for _, tc := range []struct {
		name string
		call protocol.Call
	}{
		{"nil GraphQL op", protocol.Call{Tool: noOp, Endpoint: endpoint(t)}},
		{"nil endpoint", protocol.Call{Tool: userTool()}},
		{"non-http endpoint", protocol.Call{Tool: userTool(), Endpoint: &url.URL{Scheme: "file", Path: "/etc/passwd"}}},
		{"endpoint userinfo", protocol.Call{Tool: userTool(), Endpoint: &url.URL{Scheme: "https", Host: "h.test", User: url.User("u")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (GraphQL{}).Encode(context.Background(), tc.call); !errors.Is(err, ErrBuild) {
				t.Fatalf("err = %v, want ErrBuild", err)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	badge, target := GraphQL{}.Describe(userTool())
	if badge != "query" || target != "user" {
		t.Errorf("Describe = (%q, %q), want (query, user)", badge, target)
	}
	noOp := userTool()
	noOp.GraphQL = nil
	badge, target = GraphQL{}.Describe(noOp)
	if badge != "" || target != "query user" {
		t.Errorf("Describe(nil op) = (%q, %q), want (\"\", \"query user\")", badge, target)
	}
}

// A fixed variable's value is a config string; Encode sends it with the JSON
// type of the declared variable's param (integer, number, boolean, else
// string), and refuses one that doesn't parse without echoing the value.
func TestEncodeCoercesFixedVariables(t *testing.T) {
	tool := model.Tool{
		Key: "query items", Name: "items",
		Params: []model.Param{
			{Name: "first", Bind: model.Binding{In: model.InVariable, Upstream: "first"}, Schema: json.RawMessage(`{"type":"integer","minimum":-2147483648}`), Hidden: true},
			{Name: "ratio", Bind: model.Binding{In: model.InVariable, Upstream: "ratio"}, Schema: json.RawMessage(`{"type":"number"}`), Hidden: true},
			{Name: "draft", Bind: model.Binding{In: model.InVariable, Upstream: "draft"}, Schema: json.RawMessage(`{"type":"boolean"}`), Hidden: true},
			{Name: "org", Bind: model.Binding{In: model.InVariable, Upstream: "org"}, Schema: json.RawMessage(`{"type":"string"}`), Hidden: true},
			{Name: "role", Bind: model.Binding{In: model.InVariable, Upstream: "role"}, Schema: json.RawMessage(`{"enum":["ADMIN"]}`), Hidden: true},
		},
		GraphQL: &model.GraphQLOp{Operation: "query", Field: "items", Document: "query PlugIt_items { items }"},
	}
	encode := func(fixed ...Fixed) (map[string]json.RawMessage, error) {
		req, err := GraphQL{Target: Target{Fixed: fixed}}.Encode(context.Background(), protocol.Call{Tool: tool, Endpoint: endpoint(t)})
		if err != nil {
			return nil, err
		}
		var wb wireBody
		raw, _ := io.ReadAll(req.Body)
		if err := json.Unmarshal(raw, &wb); err != nil {
			t.Fatal(err)
		}
		return wb.Variables, nil
	}
	v, err := encode(
		Fixed{Name: "first", In: model.InVariable, Value: "25"},
		Fixed{Name: "ratio", In: model.InVariable, Value: "0.5"},
		Fixed{Name: "draft", In: model.InVariable, Value: "true"},
		Fixed{Name: "org", In: model.InVariable, Value: "42"},
		Fixed{Name: "role", In: model.InVariable, Value: "ADMIN"},
	)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := map[string]string{"first": `25`, "ratio": `0.5`, "draft": `true`, "org": `"42"`, "role": `"ADMIN"`}
	for k, w := range want {
		if got := string(v[k]); got != w {
			t.Errorf("variables.%s = %s, want %s", k, got, w)
		}
	}
	for _, bad := range []Fixed{
		{Name: "first", In: model.InVariable, Value: "test-token-123", Secret: true},
		{Name: "ratio", In: model.InVariable, Value: "NaN"},
		{Name: "draft", In: model.InVariable, Value: "test-token-123", Secret: true},
	} {
		_, err := encode(bad)
		if err == nil {
			t.Errorf("fixed %s=%q must be refused", bad.Name, bad.Value)
			continue
		}
		if !errors.Is(err, ErrBuild) || !strings.Contains(err.Error(), bad.Name) {
			t.Errorf("error should be ErrBuild naming %s: %v", bad.Name, err)
		}
		if strings.Contains(err.Error(), "test-token-123") {
			t.Errorf("error echoes the fixed value: %v", err)
		}
	}
}
