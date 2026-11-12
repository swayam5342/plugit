package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/swayam5342/plugit/internal/model"
)

// ErrInvalidArgs means tool-call arguments failed validation.
var ErrInvalidArgs = errors.New("invalid arguments")

const maxErrorLen = 600

// Validator checks tool-call arguments against a tool's input schema
// (SPEC §3.7 step 4). The input schema sets additionalProperties: false, so
// unknown arguments (including attempts to set hidden fixed params) fail.
type Validator struct {
	schema   *jsonschema.Schema
	degraded bool
}

// NewValidator compiles t's input schema. If that fails (for example, a spec
// uses an ECMA-262-only regex), it falls back to a minimal schema that still
// enforces required arguments and rejects unknown ones; Degraded then
// reports true. It never falls back to accepting anything.
func NewValidator(t model.Tool) *Validator {
	if s, err := compile(InputSchema(t)); err == nil {
		return &Validator{schema: s}
	}
	s, err := compile(minimalSchema(t))
	if err != nil {
		// Unreachable: the minimal schema uses only basic keywords. Keep
		// failing closed rather than accepting anything.
		return &Validator{degraded: true}
	}
	return &Validator{schema: s, degraded: true}
}

// Degraded reports whether the full schema could not be compiled.
func (v *Validator) Degraded() bool { return v.degraded }

// Validate checks raw JSON arguments. Empty or null arguments mean {}.
func (v *Validator) Validate(raw json.RawMessage) error {
	if v.schema == nil {
		return fmt.Errorf("%w: tool schema unavailable", ErrInvalidArgs)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: arguments are not valid JSON", ErrInvalidArgs)
	}
	if err := v.schema.Validate(inst); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, shorten(err.Error()))
	}
	return nil
}

// DecodeArgs decodes raw arguments into a map, keeping numbers as
// json.Number so large integers keep full precision.
func DecodeArgs(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, fmt.Errorf("%w: arguments must be a JSON object", ErrInvalidArgs)
	}
	return out, nil
}

func compile(schemaJSON []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(refuseLoader{}) // never read files or URLs
	const loc = "plugit:///tool.json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, err
	}
	return c.Compile(loc)
}

type refuseLoader struct{}

func (refuseLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("loading external schema %q is not allowed", url)
}

// minimalSchema keeps the structural checks of InputSchema: known argument
// names only, and required ones present.
func minimalSchema(t model.Tool) []byte {
	props := map[string]any{}
	var required []string
	for _, p := range t.Params {
		if p.Hidden {
			continue
		}
		props[p.Name] = map[string]any{}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return mustJSON(out)
}

func shorten(s string) string {
	s = strings.ReplaceAll(s, "plugit:///tool.json#", "")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen] + "…"
	}
	return s
}
