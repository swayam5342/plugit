package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func petTool() model.Tool {
	return Prepare(model.Tool{Key: "GET /pets/{id}", Name: "getPet", Params: []model.Param{
		p("id", model.InPath, true, `{"type":"integer","minimum":1}`),
		p("status", model.InQuery, false, `{"type":"string","enum":["available","sold"]}`),
		p("api_key", model.InQuery, false, `{"type":"string"}`),
	}}, Options{Fixed: []string{"api_key"}})
}

func TestValidatorAcceptsValidArgs(t *testing.T) {
	v := NewValidator(petTool())
	for _, args := range []string{`{"id": 3}`, `{"id": 3, "status": "sold"}`} {
		if err := v.Validate(json.RawMessage(args)); err != nil {
			t.Errorf("Validate(%s) = %v", args, err)
		}
	}
	if v.Degraded() {
		t.Error("a well-formed schema must not degrade")
	}
}

func TestValidatorRejects(t *testing.T) {
	v := NewValidator(petTool())
	for name, args := range map[string]string{
		"missing required":   `{}`,
		"wrong type":         `{"id": "three"}`,
		"below minimum":      `{"id": 0}`,
		"not in enum":        `{"id": 1, "status": "lost"}`,
		"unknown argument":   `{"id": 1, "url": "http://169.254.169.254/"}`,
		"fixed param by LLM": `{"id": 1, "api_key": "attacker"}`,
		"not an object":      `[1, 2]`,
		"invalid JSON":       `{"id": `,
	} {
		t.Run(name, func(t *testing.T) {
			err := v.Validate(json.RawMessage(args))
			if !errors.Is(err, ErrInvalidArgs) {
				t.Errorf("Validate(%s) = %v, want ErrInvalidArgs", args, err)
			}
		})
	}
}

func TestValidatorNullArgumentsMeanNone(t *testing.T) {
	v := NewValidator(Prepare(model.Tool{Key: "GET /x"}, Options{}))
	for _, args := range []string{``, `null`, `{}`} {
		if err := v.Validate(json.RawMessage(args)); err != nil {
			t.Errorf("Validate(%q) = %v", args, err)
		}
	}
}

func TestValidatorDegradesSafelyOnUncompilableSchema(t *testing.T) {
	// ECMA-262 lookahead isn't supported by Go's regexp: the schema won't compile.
	tl := Prepare(model.Tool{Key: "GET /x", Params: []model.Param{
		p("q", model.InQuery, true, `{"type":"string","pattern":"^(?=a)a$"}`),
	}}, Options{})
	v := NewValidator(tl)
	if !v.Degraded() {
		t.Fatal("expected a degraded validator")
	}
	// Still enforced: required args and no unknown args.
	if err := v.Validate(json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("missing required: %v", err)
	}
	if err := v.Validate(json.RawMessage(`{"q": "a", "extra": 1}`)); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("unknown arg: %v", err)
	}
	if err := v.Validate(json.RawMessage(`{"q": "anything"}`)); err != nil {
		t.Errorf("valid call rejected: %v", err)
	}
}

func TestValidatorNeverLoadsExternalSchemas(t *testing.T) {
	tl := Prepare(model.Tool{Key: "GET /x", Params: []model.Param{
		p("q", model.InQuery, false, `{"$ref":"file:///etc/passwd"}`),
	}}, Options{})
	v := NewValidator(tl)
	if !v.Degraded() {
		t.Error("a schema with an external $ref must not compile (the loader refuses all loads)")
	}
}

func TestValidationErrorIsReadable(t *testing.T) {
	err := NewValidator(petTool()).Validate(json.RawMessage(`{"id": "x", "bogus": 1}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"id", "bogus"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
	if len(msg) > 1000 {
		t.Errorf("error is %d chars; keep it short for the model", len(msg))
	}
}

func TestDecodeArgs(t *testing.T) {
	args, err := DecodeArgs(json.RawMessage(`{"n": 12345678901234567890, "f": 1.5, "s": "x", "a": [1], "o": {"k": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := args["n"].(json.Number); !ok || n.String() != "12345678901234567890" {
		t.Errorf("big integers must keep full precision as json.Number, got %#v", args["n"])
	}
	if _, err := DecodeArgs(json.RawMessage(`[1]`)); !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("array args: %v", err)
	}
	if m, err := DecodeArgs(json.RawMessage(`null`)); err != nil || len(m) != 0 {
		t.Errorf("null args = %v, %v", m, err)
	}
}
