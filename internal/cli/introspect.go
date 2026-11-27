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
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/spec"
	"github.com/swayam5342/plugit/internal/utils"
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
// non-2xx status or a result over the cap is an error. So is a result that
// carries the credential or a secret fixed value in any encoded form (T11):
// it would become the snapshot, the manifest and tool descriptions, so
// invoke.Fetch refuses it and nothing is saved.
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
		if schema.IsForbiddenHeader(it.Name) || utils.HasControl(it.Value) {
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
	red := redact.New(redact.Options{Secrets: fx.secretValues()})
	data, truncated, status, err := invoke.Fetch(ctx, invoke.Deps{Doer: client, Cred: cred, Endpoint: endpoint, Redactor: red}, req)
	switch {
	case errors.Is(err, invoke.ErrSecretInResponse):
		return nil, fmt.Errorf("introspecting %s: %w; refusing to save it or build tools from it", where, invoke.ErrSecretInResponse)
	case err != nil:
		return nil, fmt.Errorf("introspecting %s: %w", where, err)
	case status < 200 || status > 299:
		return nil, fmt.Errorf("introspecting %s: HTTP %d %s (%s)", where, status, http.StatusText(status), introspectionDisabledHint)
	case truncated:
		return nil, fmt.Errorf("introspecting %s: the result is over %d bytes", where, config.MaxSnapshotBytes)
	case len(bytes.TrimSpace(data)) == 0:
		return nil, fmt.Errorf("introspecting %s: empty response", where)
	}
	if msgs, ok := introspectionErrors(data); ok {
		// Fetch has already refused a body carrying a secret in play; String
		// also masks known token formats.
		return nil, fmt.Errorf("introspecting %s: the endpoint answered with GraphQL errors and no schema: %s; %s",
			where, red.With(cred.SecretValues()...).String(msgs), introspectionDisabledHint)
	}
	return data, nil
}

// introspectionDisabledHint is what to do when an endpoint won't answer
// the introspection query.
const introspectionDisabledHint = "introspection is often disabled on public APIs: download the schema (SDL) and run plugit add NAME FILE --format graphql-sdl --endpoint URL"

// Limits on the upstream messages an errors envelope is reported with.
const (
	maxEnvelopeMessages    = 3
	maxEnvelopeMessageRune = 200
)

// introspectionErrors reports whether body is a GraphQL errors envelope with
// no data.__schema (how servers answer when introspection is turned off),
// and returns its first messages, cleaned (naming.CleanText), capped and
// quoted, for the error. A body with a schema, or that isn't such an
// envelope, is left to the isolate child to parse and judge.
//
// This is the only parsing of the untrusted result outside the child, and it
// is deliberately minimal: the body is already size-capped (the client's
// MaxSnapshotBytes), json.Unmarshal goes into a two-field struct of
// json.RawMessage (no schema text is built, and data.__schema is only
// checked for presence), and only the "message" strings of at most a few
// errors are decoded.
func introspectionErrors(body []byte) (string, bool) {
	var env struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Errors) == 0 {
		return "", false
	}
	var data struct {
		Schema json.RawMessage `json:"__schema"`
	}
	if json.Unmarshal(env.Data, &data) == nil && len(data.Schema) > 0 && string(data.Schema) != "null" {
		return "", false
	}
	var msgs []string
	for _, raw := range env.Errors {
		if len(msgs) == maxEnvelopeMessages {
			break
		}
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		if m := naming.CleanText(e.Message, maxEnvelopeMessageRune); m != "" {
			msgs = append(msgs, `"`+m+`"`)
		}
	}
	out := strings.Join(msgs, "; ")
	if out == "" {
		out = "(no message)"
	}
	if n := len(env.Errors) - len(msgs); n > 0 && len(msgs) == maxEnvelopeMessages {
		out += fmt.Sprintf(" (and %d more)", n)
	}
	return out, true
}
