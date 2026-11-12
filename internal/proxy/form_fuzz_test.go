package proxy

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

// Arguments come from the model: whatever JSON they are, encoding a form
// body must not panic, and a form it produces must parse back.
func FuzzEncodeBody(f *testing.F) {
	for _, seed := range []string{
		`{"a":"x","b":[1,2],"c":{"d":{"e":[{"f":true}]}}}`,
		`{"metadata":{"k":"v"},"items":[{"price":"p","quantity":2}],"n":null}`,
		`{"x":[[["deep"]]],"y":1e30,"z":-0.0}`,
	} {
		f.Add(seed, true)
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, raw string, deep bool) {
		args, err := schema.DecodeArgs(json.RawMessage(raw))
		if err != nil {
			return
		}
		enc := map[string]model.Encoding{}
		for k := range args {
			if deep {
				enc[k] = model.Encoding{Style: "deepObject", Explode: true}
			} else {
				enc[k] = model.Encoding{Style: "form", Explode: len(k)%2 == 0}
			}
		}
		for _, ct := range []string{ctForm, ctMultipart} {
			body, gotCT, err := encodeBody(&model.Body{ContentType: ct, Encoding: enc}, args, nil, false)
			if err != nil {
				continue
			}
			if ct == ctForm && gotCT == ctForm {
				if _, err := url.ParseQuery(string(body)); err != nil {
					t.Fatalf("form body doesn't parse: %q: %v", body, err)
				}
			}
		}
	})
}
