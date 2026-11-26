// Package postman turns a Postman v2.0/v2.1 collection into a model.Manifest
// (SPEC §3.4, §3.5). The collection is untrusted input (SPEC §5.3): folder
// nesting and request counts are capped, unsafe paths are skipped, and all
// text that reaches the model is cleaned. It shares its manifest builder
// with the openapi package through restutil.
package postman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol/rest/restutil"
)

const (
	maxPostmanDepth  = 16
	maxPostmanItems  = 2000
	maxInferDepth    = 12
	postmanSchemaURL = "schema.getpostman.com/json/collection/v2"
)

// Detect reports whether data looks like a Postman v2 collection.
func Detect(data []byte) bool {
	if !bytes.Contains(data, []byte(postmanSchemaURL)) {
		return false
	}
	var head struct {
		Info struct {
			Schema string `json:"schema"`
		} `json:"info"`
	}
	return json.Unmarshal(data, &head) == nil && strings.Contains(head.Info.Schema, postmanSchemaURL)
}

var postmanMethods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

// Options configures Parse. It is the options type the REST parsers share.
type Options = restutil.Options

// Parse converts a Postman v2.0/v2.1 collection into a manifest.
// Requests become tools, top-level folders become tags, and the
// collection's base URL variables are offered as servers (the user still
// pins base_url, T3). Collection auth is ignored: plugit's auth applies.
func Parse(data []byte, opts Options) (*model.Manifest, error) {
	var col pmCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return nil, fmt.Errorf("parsing Postman collection: %w", err)
	}
	b := &restutil.Builder{Opts: opts}
	b.M.Format = "postman"
	switch {
	case strings.Contains(col.Info.Schema, "v2.1"):
		b.M.SpecVersion = "postman-2.1"
	case strings.Contains(col.Info.Schema, "v2.0"):
		b.M.SpecVersion = "postman-2.0"
	default:
		return nil, fmt.Errorf("parsing Postman collection: %w (schema %q)", restutil.ErrUnsupportedSpec, naming.CleanText(col.Info.Schema, 100))
	}
	b.M.Title = naming.CleanText(col.Info.Name, 200)

	vars := map[string]string{}
	for _, v := range col.Variable {
		if u, err := url.Parse(v.Value); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			vars[v.Key] = strings.TrimRight(v.Value, "/")
			addServer(b, vars[v.Key])
		}
	}
	p := &pmWalk{b: b, vars: vars}
	if len(col.Auth) > 0 && string(col.Auth) != "null" {
		p.authSeen = true
	}
	p.walk(col.Item, "", 1)
	if p.authSeen {
		b.Warn("the collection's auth settings are ignored: set the credential with plugit add --header and plugit auth")
	}
	if p.tooDeep {
		b.Warn(fmt.Sprintf("folders nested more than %d deep were skipped", maxPostmanDepth))
	}
	if p.skipped > 0 {
		b.Warn(fmt.Sprintf("only the first %d requests were imported (%d more skipped)", maxPostmanItems, p.skipped))
	}
	return b.Finish(), nil
}

// addServer offers s as a server, once.
func addServer(b *restutil.Builder, s string) {
	if !slices.Contains(b.M.Endpoints, s) {
		b.M.Endpoints = append(b.M.Endpoints, s)
	}
}
