package cli

import (
	"fmt"
	"net/url"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/protocol/rest"
)

// This file is the one place protocols are switched on and built (SPEC §3.7
// P5). There is no registry and no init()-time registration: resolveProto and
// describeFor each name every protocol this build serves, and
// TestProtocolSwitchesExhaustive keeps both in step with the P3 table.

// resolveProto builds the serving protocol for a manifest's protocol name and
// binds its per-serve target from the resolved fixed params. A protocol this
// build has no impl for is refused ("needs a newer plugit"), never served as
// REST by accident (one serve = one protocol). An empty name is a pre-field
// manifest, which is REST.
func resolveProto(name model.Protocol, fx resolvedFixed, baseURL *url.URL) (protocol.Protocol, error) {
	switch name {
	case "", model.REST:
		return rest.REST{Target: restTargetFrom(fx, baseURL)}, nil
	case model.GraphQL:
		return graphql.GraphQL{Target: graphqlTargetFrom(fx)}, nil
	default:
		return nil, fmt.Errorf("this plugit build can't serve the %q protocol; it may need a newer version of plugit", name)
	}
}

// describeFor is a tool's per-protocol badge and target for the TUI and `try`
// (SPEC §2.3): REST "GET /pets", GraphQL "query user". It switches the same
// way as resolveProto, on zero-value impls since Describe needs no target. A
// protocol this build doesn't serve shows the bare key with no badge.
func describeFor(name model.Protocol, t model.Tool) (badge, target string) {
	switch name {
	case "", model.REST:
		return rest.REST{}.Describe(t)
	case model.GraphQL:
		return graphql.GraphQL{}.Describe(t)
	default:
		return "", string(t.Key)
	}
}

// restTargetFrom maps resolved fixed params to a REST target pinned to baseURL.
func restTargetFrom(fx resolvedFixed, baseURL *url.URL) rest.Target {
	tg := rest.Target{BaseURL: baseURL, UserAgent: fx.userAgent}
	for _, it := range fx.items {
		in := model.InQuery
		switch it.In {
		case "header":
			in = model.InHeader
		case "cookie":
			in = model.InCookie
		}
		tg.Fixed = append(tg.Fixed, rest.Fixed{Name: it.Name, In: in, Value: it.Value, Secret: it.Secret})
	}
	return tg
}

// graphqlTargetFrom maps resolved fixed params to a GraphQL target. GraphQL
// has two fixed locations (SPEC §3.4 P3): "header" is a header, and
// "variable" or an unset location is a variable; there is no query location.
func graphqlTargetFrom(fx resolvedFixed) graphql.Target {
	tg := graphql.Target{UserAgent: fx.userAgent}
	for _, it := range fx.items {
		in := model.InVariable
		if it.In == "header" {
			in = model.InHeader
		}
		tg.Fixed = append(tg.Fixed, graphql.Fixed{Name: it.Name, In: in, Value: it.Value, Secret: it.Secret})
	}
	return tg
}
