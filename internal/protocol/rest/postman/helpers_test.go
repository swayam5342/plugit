package postman

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

// These helpers match the openapi package's test helpers of the same names.

func tool(t *testing.T, m *model.Manifest, key model.OpKey) *model.Tool {
	t.Helper()
	for i := range m.Tools {
		if m.Tools[i].Key == key {
			return &m.Tools[i]
		}
	}
	keys := make([]string, len(m.Tools))
	for i, tl := range m.Tools {
		keys[i] = string(tl.Key)
	}
	t.Fatalf("tool %q not found; have %v", key, keys)
	return nil
}

func param(t *testing.T, tl *model.Tool, name string) *model.Param {
	t.Helper()
	for i := range tl.Params {
		if tl.Params[i].Bind.Upstream == name {
			return &tl.Params[i]
		}
	}
	t.Fatalf("%s: param %q not found", tl.Key, name)
	return nil
}

func assertJSON(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: invalid JSON %s: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

func hasIssue(tl *model.Tool, kind model.IssueKind, substr string) bool {
	for _, is := range tl.Issues {
		if is.Kind == kind && strings.Contains(is.Detail, substr) {
			return true
		}
	}
	return false
}

func containsWarning(m *model.Manifest, substr string) bool {
	for _, w := range m.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
