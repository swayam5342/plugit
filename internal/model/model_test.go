package model

import (
	"encoding/json"
	"testing"
)

func TestSafetyForMethod(t *testing.T) {
	tests := []struct {
		method string
		want   Safety
	}{
		{"GET", ReadOnly}, {"get", ReadOnly}, {"HEAD", ReadOnly}, {"OPTIONS", ReadOnly},
		{"POST", Write}, {"PUT", Write}, {"PATCH", Write}, {"patch", Write},
		{"DELETE", Destructive},
		{"TRACE", Destructive}, {"CONNECT", Destructive}, {"", Destructive}, {"BREW", Destructive},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			if got := SafetyForMethod(tc.method); got != tc.want {
				t.Errorf("SafetyForMethod(%q) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

func TestNewOpKey(t *testing.T) {
	tests := []struct {
		method, path string
		want         OpKey
	}{
		{"get", "/repos/{owner}/{repo}/issues", "GET /repos/{owner}/{repo}/issues"},
		{"POST", "/pets", "POST /pets"},
		{"Delete", "/pets/{id}", "DELETE /pets/{id}"},
	}
	for _, tc := range tests {
		if got := NewOpKey(tc.method, tc.path); got != tc.want {
			t.Errorf("NewOpKey(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestOpKeyParts(t *testing.T) {
	k := NewOpKey("get", "/a/{b}")
	if k.Method() != "GET" || k.Path() != "/a/{b}" {
		t.Errorf("Method/Path = %q %q, want GET /a/{b}", k.Method(), k.Path())
	}
	var empty OpKey
	if empty.Method() != "" || empty.Path() != "" {
		t.Errorf("empty OpKey parts = %q %q, want empty", empty.Method(), empty.Path())
	}
}

func TestStringsAndJSON(t *testing.T) {
	for s, want := range map[Safety]string{ReadOnly: "read-only", Write: "write", Destructive: "destructive"} {
		if s.String() != want {
			t.Errorf("Safety(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
	for l, want := range map[Location]string{
		InPath: "path", InQuery: "query", InHeader: "header", InCookie: "cookie", InBody: "body", InBodyRoot: "bodyRoot",
	} {
		if l.String() != want {
			t.Errorf("Location(%d).String() = %q, want %q", l, l.String(), want)
		}
	}

	// Golden files depend on stable, readable JSON for these enums.
	b, err := json.Marshal(struct {
		S Safety
		L Location
	}{Destructive, InBodyRoot})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"S":"destructive","L":"bodyRoot"}`; got != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	in := Manifest{Title: "t", SpecVersion: "3.1.0", Tools: []Tool{{
		Key: "POST /x", Name: "x", Method: "POST", Path: "/x", Safety: Write,
		Params: []Param{{Name: "b", UpstreamName: "", In: InBodyRoot, Schema: json.RawMessage(`{"type":"array"}`)}},
		Issues: []Issue{{Kind: IssueBodyNested, Detail: "d"}},
	}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Manifest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	b2, _ := json.Marshal(out)
	if string(b) != string(b2) {
		t.Errorf("round trip changed the manifest:\n%s\n%s", b, b2)
	}
	if out.Tools[0].Safety != Write || out.Tools[0].Params[0].In != InBodyRoot {
		t.Errorf("enums not decoded: %v %v", out.Tools[0].Safety, out.Tools[0].Params[0].In)
	}
}

func TestEnumUnmarshalRejectsUnknown(t *testing.T) {
	var s Safety
	if err := json.Unmarshal([]byte(`"yolo"`), &s); err == nil {
		t.Error("unknown safety must fail")
	}
	var l Location
	if err := json.Unmarshal([]byte(`"formData"`), &l); err == nil {
		t.Error("unknown location must fail")
	}
}

func TestParseLocation(t *testing.T) {
	for _, s := range []string{"path", "query", "header", "cookie", "body", "bodyRoot"} {
		l, ok := ParseLocation(s)
		if !ok || l.String() != s {
			t.Errorf("ParseLocation(%q) = %v, %v", s, l, ok)
		}
	}
	if _, ok := ParseLocation("formData"); ok {
		t.Error("ParseLocation(formData) should fail")
	}
}
