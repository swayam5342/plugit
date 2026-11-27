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
	if protoDescribe("bogus") != nil {
		t.Error("an unknown protocol should resolve to nil so the TUI falls back to REST method/path")
	}
}
