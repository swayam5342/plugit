package restutil

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/utils"
)

// Form body media types.
const (
	CTForm      = "application/x-www-form-urlencoded"
	CTMultipart = "multipart/form-data"
)

// FormBody models a form or multipart body. Its schema must be an object
// (its fields become parameters like a JSON body's). File fields are
// dropped with an issue, since their content can't come from a model; if
// one is required, the body isn't modelled at all.
func FormBody(c *Collector, ct string, raw json.RawMessage, enc map[string]model.Encoding, required bool) *model.Body {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil || (schema["type"] != nil && schema["type"] != "object") {
		c.Issue(model.IssueUnsupported, "%s body schema is not an object, so it can't be sent as fields", ct)
		return nil
	}
	props, _ := schema["properties"].(map[string]any)
	req, _ := schema["required"].([]any)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		if !isFileField(props[name]) {
			continue
		}
		delete(props, name)
		if slices.Contains(req, any(name)) {
			c.Issue(model.IssueUnsupported, "%s body needs the file field %q, which plugit can't send (file content can't come from a model)", ct, name)
			return nil
		}
		c.Issue(model.IssueUnsupported, "file field %q left out of the %s body (file content can't come from a model)", name, ct)
	}
	if len(props) == 0 {
		return nil
	}
	for k := range enc {
		if _, ok := props[k]; !ok {
			delete(enc, k)
		}
	}
	if len(enc) == 0 {
		enc = nil
	}
	return &model.Body{ContentType: ct, Schema: utils.MustJSON(schema), Required: required, Encoding: enc}
}

// isFileField reports a schema whose value is file content.
func isFileField(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if m["type"] == "file" || m["format"] == "binary" {
		return true
	}
	_, media := m["contentMediaType"]
	_, encoding := m["contentEncoding"]
	return media || encoding
}
