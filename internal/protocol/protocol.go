// Package protocol is the serving seam: each protocol Encodes a validated
// call into an *http.Request and Decodes the response into a neutral Result
// (SPEC §3.7 P5). A protocol never reaches the network or attaches a
// credential — internal/invoke does every network step (T17).
package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/swayam5342/plugit/internal/model"
)

// Protocol builds and reads one tool call.
type Protocol interface {
	Name() model.Protocol
	Encode(ctx context.Context, c Call) (*http.Request, error) // bound args → request; no credentials, no network
	Decode(c Call, resp *http.Response, body []byte, truncated bool) Result
	Describe(t model.Tool) (badge, target string) // for the TUI and `try`
}

// Call is one validated invocation. Hidden/fixed values are already injected
// by the pipeline before Encode runs.
type Call struct {
	Tool     model.Tool
	Args     map[string]any
	Endpoint *url.URL
	Timeout  time.Duration
}

// Result is a protocol-neutral decoded response.
type Result struct {
	Code    string          // call-log code (SPEC §3.7): "HTTP 404", "NOT_FOUND", …
	IsError bool            // the protocol's own error judgement
	Value   json.RawMessage // decoded body as raw JSON bytes (preserves numbers/order/escaping), or nil
	Text    string          // used when Value is nil (non-JSON bodies, error text)
	Binary  *BinaryInfo     // set for a binary body
	Status  string          // first line shown to the model, e.g. "HTTP 200 application/json"
}

// BinaryInfo describes a binary body that is summarized rather than shown.
type BinaryInfo struct {
	Bytes int
	Type  string
}

var registry = map[model.Protocol]Protocol{}

// Register adds p to the registry (called from each protocol package's wiring).
func Register(p Protocol) { registry[p.Name()] = p }

// For returns the registered Protocol for p, if any.
func For(p model.Protocol) (Protocol, bool) {
	x, ok := registry[p]
	return x, ok
}
