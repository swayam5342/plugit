package model

import "testing"

func TestProtocolTableREST(t *testing.T) {
	if !KeyValid(REST, "GET /pets/{id}") {
		t.Error("REST key \"GET /pets/{id}\" should be valid")
	}
	if KeyValid(REST, "query user") {
		t.Error("REST protocol must reject a GraphQL key")
	}
	if KeyValid(GraphQL, "query user") != true {
		t.Error("GraphQL key \"query user\" should be valid")
	}
	if KeyValid(GraphQL, "GET /pets") {
		t.Error("GraphQL protocol must reject a REST key")
	}
	if !BindingAllowed(REST, "path") || !BindingAllowed(REST, "cookie") {
		t.Error("REST bindings path and cookie should be allowed")
	}
	if BindingAllowed(REST, "variable") {
		t.Error("REST must reject the variable binding")
	}
	if !BindingAllowed(GraphQL, "variable") {
		t.Error("GraphQL binding variable should be allowed")
	}
	if !FixedInAllowed(REST, "cookie") {
		t.Error("REST fixed[].in cookie should be allowed")
	}
	if FixedInAllowed(REST, "variable") {
		t.Error("REST fixed[].in variable should be rejected")
	}
	if !AuthTypeAllowed(REST, "query") {
		t.Error("REST auth.type query should be allowed")
	}
	if AuthTypeAllowed(GRPC, "query") {
		t.Error("gRPC must reject auth.type query")
	}
	if !MethodsFilterAllowed(REST) {
		t.Error("REST methods: filter should be allowed")
	}
	if MethodsFilterAllowed(GRPC) {
		t.Error("gRPC methods: filter should be refused")
	}
	if got := ConfigSection(GraphQL); got != "graphql" {
		t.Errorf("ConfigSection(GraphQL) = %q, want \"graphql\"", got)
	}
	if got := ConfigSection(REST); got != "" {
		t.Errorf("ConfigSection(REST) = %q, want \"\"", got)
	}
}

func TestOpKeyMethodPathRESTOnly(t *testing.T) {
	k := NewOpKey("get", "/pets/{id}")
	if k.Method() != "GET" || k.Path() != "/pets/{id}" {
		t.Errorf("REST OpKey parsed as %q %q", k.Method(), k.Path())
	}
	for _, nk := range []OpKey{"query user", "mutation createIssue", "grpc acme.v1.Issues/Get"} {
		if nk.Method() != "" || nk.Path() != "" {
			t.Errorf("non-REST OpKey %q gave Method=%q Path=%q, want empty", nk, nk.Method(), nk.Path())
		}
	}
}

func TestIdempotentForMethod(t *testing.T) {
	for _, m := range []string{"GET", "head", "OPTIONS", "put", "DELETE"} {
		if !IdempotentForMethod(m) {
			t.Errorf("IdempotentForMethod(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"POST", "PATCH", "FROB"} {
		if IdempotentForMethod(m) {
			t.Errorf("IdempotentForMethod(%q) = true, want false", m)
		}
	}
}
