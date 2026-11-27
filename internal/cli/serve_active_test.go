package cli

import (
	"slices"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

// An override document takes the override's selection when the manifest
// records one; a cache without it keeps the default selection, never nil.
func TestOverrideOpSelection(t *testing.T) {
	base := model.GraphQLOp{Operation: "query", Field: "u", Document: "query PlugIt_u { u { a b } }", Selection: []string{"a", "b"}}
	got := overrideOp(base, "query PlugIt_u { u { a } }", map[string][]string{"query u": {"a"}}, "query u")
	if got.Document != "query PlugIt_u { u { a } }" || !slices.Equal(got.Selection, []string{"a"}) {
		t.Errorf("with a recorded selection: %+v", got)
	}
	got = overrideOp(base, "query PlugIt_u { u { a } }", nil, "query u")
	if got.Document != "query PlugIt_u { u { a } }" || !slices.Equal(got.Selection, []string{"a", "b"}) {
		t.Errorf("without a recorded selection: %+v, want the default kept", got)
	}
	if !slices.Equal(base.Selection, []string{"a", "b"}) || base.Document != "query PlugIt_u { u { a b } }" {
		t.Error("overrideOp changed its input")
	}
}
