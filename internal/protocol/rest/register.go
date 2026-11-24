package rest

import "github.com/swayam5342/plugit/internal/protocol"

// A zero-value REST registers the protocol so the serve path can resolve a
// manifest's protocol name to an impl (and refuse one it doesn't have). The
// per-serve Target (fixed params, user agent, credential) is bound by the
// caller before use; see cli.resolveProto.
func init() { protocol.Register(REST{}) }
