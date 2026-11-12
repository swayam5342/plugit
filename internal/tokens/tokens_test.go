package tokens

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func TestTextBasics(t *testing.T) {
	if Text("") != 0 {
		t.Error("empty text has tokens")
	}
	if n := Text("hello"); n != 1 {
		t.Errorf("one short word = %d, want 1", n)
	}
	// Spaces before words merge into them, so a sentence is about one token
	// per word.
	if n := Text("List all the pets in the store"); n < 6 || n > 9 {
		t.Errorf("7-word sentence = %d", n)
	}
	// Long identifiers split.
	if Text("getRepositoryCollaboratorPermissionLevel") <= Text("getRepo") {
		t.Error("a long identifier should cost more than a short one")
	}
	// Non-ASCII text costs at least a token per character.
	if n := Text("日本語のテキスト"); n < 8 {
		t.Errorf("CJK text = %d", n)
	}
}

func TestTextIsMonotonic(t *testing.T) {
	base := `{"type":"object","properties":{"id":{"type":"string"}}}`
	if Text(base+base) < Text(base) {
		t.Error("more text, fewer tokens")
	}
}

// The estimate stays within the range real tokenizers give for JSON-heavy
// English text: between 1 token per 6 bytes and 1 per 2 bytes.
func TestTextPlausibleRange(t *testing.T) {
	samples := []string{
		`{"name":"listPets","description":"List all pets in the store, newest first.","input_schema":{"type":"object","properties":{"limit":{"type":"integer","description":"How many items to return at one time (max 100)"},"status":{"type":"string","enum":["available","pending","sold"]}},"required":["status"],"additionalProperties":false}}`,
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20),
	}
	for _, s := range samples {
		n := Text(s)
		if n < len(s)/6 || n > len(s)/2 {
			t.Errorf("Text(%d bytes) = %d, outside [%d, %d]: %.60q", len(s), n, len(s)/6, len(s)/2, s)
		}
	}
	// Dense punctuation costs more per byte than prose, as it does in real
	// tokenizers, but never more than a token per byte.
	dense := `{"a":1,"b":2,"c":[1,2,3],"d":{"e":"f"}}`
	prose := samples[1]
	if float64(Text(dense))/float64(len(dense)) <= float64(Text(prose))/float64(len(prose)) {
		t.Error("dense JSON should cost more per byte than prose")
	}
	if Text(dense) > len(dense) {
		t.Errorf("dense JSON = %d tokens for %d bytes", Text(dense), len(dense))
	}
}

func testTool(name, desc string) model.Tool {
	return model.Tool{
		Key: model.NewOpKey("GET", "/pets"), Name: name, Description: desc, Method: "GET", Path: "/pets",
		Params: []model.Param{{Name: "limit", UpstreamName: "limit", In: model.InQuery, Schema: json.RawMessage(`{"type":"integer"}`), Desc: "Page size"}},
	}
}

func TestToolAndTotal(t *testing.T) {
	a := testTool("listPets", "List all pets.")
	b := testTool("listPetsWithAMuchLongerName", strings.Repeat("A much longer description. ", 30))
	if Tool(b) <= Tool(a) {
		t.Errorf("Tool(b)=%d should exceed Tool(a)=%d", Tool(b), Tool(a))
	}
	if Total([]model.Tool{a, b}) != Tool(a)+Tool(b) {
		t.Error("Total is not the sum of Tool")
	}
	// Hidden params aren't sent to the model, so they cost nothing.
	h := a
	h.Params = append([]model.Param(nil), a.Params...)
	h.Params[0].Hidden = true
	if Tool(h) >= Tool(a) {
		t.Errorf("hidden param still counted: %d >= %d", Tool(h), Tool(a))
	}
	if Total(nil) != 0 {
		t.Error("no tools, some tokens")
	}
}

func TestFormat(t *testing.T) {
	for n, want := range map[int]string{
		0: "≈0", 7: "≈7", 999: "≈999", 1000: "≈1.0k", 3249: "≈3.2k", 9960: "≈10k", 12_400: "≈12k", 1_250_000: "≈1250k",
	} {
		if got := Format(n); got != want {
			t.Errorf("Format(%d) = %q, want %q", n, got, want)
		}
	}
}
