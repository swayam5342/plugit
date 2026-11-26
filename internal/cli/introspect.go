package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/invoke"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/spec"
)

// introspectionQuery is graphql-js's standard introspection query with
// descriptions. It leaves out the newer optional fields (specifiedByURL,
// isRepeatable, deprecated input values, isOneOf), which older servers
// reject as unknown.
const introspectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types { ...FullType }
    directives {
      name
      description
      locations
      args { ...InputValue }
    }
  }
}

fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args { ...InputValue }
    type { ...TypeRef }
    isDeprecated
    deprecationReason
  }
  inputFields { ...InputValue }
  interfaces { ...TypeRef }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes { ...TypeRef }
}

fragment InputValue on __InputValue {
  name
  description
  type { ...TypeRef }
  defaultValue
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
                ofType {
                  kind
                  name
                }
              }
            }
          }
        }
      }
    }
  }
}`

// introspectEndpoint is the endpoint --introspect pins: the URL the user
// typed, validated like --endpoint. An --endpoint that names anything else is
// refused, so the introspection query (and its credential) and the tool
// calls always go to the same, user-chosen place (SPEC T3, T8).
func introspectEndpoint(source, endpointFlag string) (*url.URL, error) {
	u, err := validateBaseURL(source, "the --introspect URL")
	if err != nil {
		return nil, err
	}
	if endpointFlag != "" {
		e, err := validateBaseURL(endpointFlag, "--endpoint")
		if err != nil {
			return nil, err
		}
		if e.String() != u.String() {
			return nil, errors.New("with --introspect the URL is the GraphQL endpoint: leave out --endpoint, or make it the same URL")
		}
	}
	return u, nil
}

// reintrospects reports whether cfg's source is an introspection of its own
// endpoint (plugit add --introspect), so sync introspects again instead of
// downloading source.url as a spec file.
func reintrospects(cfg *config.API) bool {
	return cfg.Source.Format == graphql.FormatIntrospection && cfg.Source.URL == cfg.Endpoint
}

// introspectAPI introspects cfg's pinned endpoint with cfg's credential and
// fixed headers (the only fixed params an introspection request carries),
// refusing credentials over plain http as serve would (T8), and returns the
// raw result and its sha256. add --introspect and sync share it. d.resolver
// and d.keyring default to the real environment and keyring.
func introspectAPI(ctx context.Context, cfg *config.API, d runDeps) (raw []byte, sha256Hex string, err error) {
	endpoint, err := validateBaseURL(cfg.Endpoint, "the endpoint")
	if err != nil {
		return nil, "", err
	}
	draft := *cfg
	draft.Fixed = nil
	for _, f := range cfg.Fixed {
		if f.In == "header" {
			draft.Fixed = append(draft.Fixed, f)
		}
	}
	if draft.Auth.Type != config.AuthNone || len(draft.Fixed) > 0 {
		if err := config.CheckCredentialTransport(endpoint.String(), cfg.Network.AllowInsecure); err != nil {
			return nil, "", fmt.Errorf("the GraphQL endpoint %w; to send it over http anyway, allow insecure (--allow-insecure, network.allow_insecure)", err)
		}
	}
	if d.resolver == nil {
		d.resolver = secrets.NewResolver()
	}
	if d.keyring == nil {
		d.keyring = secrets.OSKeyring{}
	}
	cred, _, err := buildCredential(&draft, d.resolver, d)
	if err != nil {
		return nil, "", err
	}
	fx, _, err := buildFixedTarget(&draft, d.resolver)
	if err != nil {
		return nil, "", err
	}
	fx.userAgent = "plugit/" + d.info.withDefaults().Version
	raw, err = introspect(ctx, endpoint, cred, fx, cfg.Network.AllowPrivate)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// introspect POSTs the standard introspection query to the pinned endpoint
// and returns the raw result, unparsed (it is parsed in the isolate child).
// The request goes through httpx with the spec loader's limits (size and
// time caps, the SSRF guard, allowPrivate honored, same-origin redirects
// only). invoke.Fetch attaches cred only to the endpoint's origin (T8);
// fx's header items are set as the GraphQL protocol sets fixed headers. A
// non-2xx status or a result over the cap is an error.
func introspect(ctx context.Context, endpoint *url.URL, cred invoke.Credential, fx resolvedFixed, allowPrivate bool) ([]byte, error) {
	where := safeLocation(endpoint.String())
	body, err := json.Marshal(map[string]string{"query": introspectionQuery})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("introspecting %s: %w", where, err)
	}
	for _, it := range fx.items {
		if it.In != "header" {
			continue
		}
		if schema.IsForbiddenHeader(it.Name) || strings.ContainsFunc(it.Value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fmt.Errorf("introspecting %s: fixed header %q can't be sent", where, it.Name)
		}
		req.Header.Set(it.Name, it.Value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/graphql-response+json, application/json")
	if fx.userAgent != "" {
		req.Header.Set("User-Agent", fx.userAgent)
	}

	client := httpx.NewClient(httpx.Options{AllowPrivate: allowPrivate, Timeout: spec.DefaultTimeout, MaxResponseBytes: config.MaxSnapshotBytes})
	data, truncated, status, err := invoke.Fetch(ctx, invoke.Deps{Doer: client, Cred: cred, Endpoint: endpoint}, req)
	switch {
	case err != nil:
		return nil, fmt.Errorf("introspecting %s: %w", where, err)
	case status < 200 || status > 299:
		return nil, fmt.Errorf("introspecting %s: HTTP %d %s (introspection is often disabled; add the API from an SDL file instead)",
			where, status, http.StatusText(status))
	case truncated:
		return nil, fmt.Errorf("introspecting %s: the result is over %d bytes", where, config.MaxSnapshotBytes)
	case len(bytes.TrimSpace(data)) == 0:
		return nil, fmt.Errorf("introspecting %s: empty response", where)
	}
	return data, nil
}
