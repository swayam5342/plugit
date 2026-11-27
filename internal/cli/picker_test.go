package cli

import (
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func TestProtoDescribe(t *testing.T) {
	restTool := model.Tool{Key: "GET /pets", HTTP: &model.HTTPOp{Method: "GET", Path: "/pets"}}

	fn := protoDescribe(model.REST)
	if fn == nil {
		t.Fatal("REST should resolve to a Describe func")
	}
	if b, tg := fn(restTool); b != "GET" || tg != "/pets" {
		t.Errorf("REST Describe = %q %q, want %q %q", b, tg, "GET", "/pets")
	}
	if protoDescribe("") == nil {
		t.Error("an empty protocol is a pre-field manifest: it should default to REST, not nil")
	}
	gql := model.Tool{Key: "query user", GraphQL: &model.GraphQLOp{Operation: "query", Field: "user"}}
	if b, tg := protoDescribe(model.GraphQL)(gql); b != "query" || tg != "user" {
		t.Errorf("GraphQL Describe = %q %q, want %q %q", b, tg, "query", "user")
	}
	if b, tg := protoDescribe("bogus")(restTool); b != "" || tg != "GET /pets" {
		t.Errorf("an unknown protocol should fall back to the bare key, got %q %q", b, tg)
	}
}
