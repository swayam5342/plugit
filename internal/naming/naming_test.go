package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestToolID(t *testing.T) {
	k := model.OpKey("GET /pets/{petId}")
	sum := sha256.Sum256([]byte(k))
	want := hex.EncodeToString(sum[:])[:8]
	if got := ToolID(k); got != want {
		t.Errorf("ToolID = %q, want %q", got, want)
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"listPets", "listPets"},
		{"list-pets", "list-pets"},
		{"list pets", "list_pets"},
		{"repos/list-for-org", "repos_list-for-org"},
		{"a___b", "a_b"},
		{"__a__", "a"},
		{"a.b.c", "a_b_c"},
		{"créer", "cr_er"},
		{"", "tool"},
		{"___", "tool"},
		{"!!!", "tool"},
		{"\u202egnp.exe", "gnp_exe"},
	}
	for _, tc := range tests {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct {
		opID, method, path, want string
	}{
		{"listPets", "GET", "/pets", "listPets"},
		{"", "GET", "/pets/{petId}", "get_pets_petId"},
		{"", "post", "/repos/{owner}/{repo}/issues", "post_repos_owner_repo_issues"},
		{"", "GET", "/", "get"},
		{"  ", "DELETE", "/a.b/{c}", "delete_a_b_c"},
	}
	for _, tc := range tests {
		if got := BaseName(tc.opID, tc.method, tc.path); got != tc.want {
			t.Errorf("BaseName(%q, %q, %q) = %q, want %q", tc.opID, tc.method, tc.path, got, tc.want)
		}
	}
}

func TestAssignPrefixAndTruncation(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10) // 100 chars
	got := Assign([]Candidate{
		{Key: "GET /a", OperationID: "listPets"},
		{Key: "GET /b", OperationID: long},
	}, "pets")

	if got["GET /a"] != "pets_listPets" {
		t.Errorf("prefixed name = %q, want pets_listPets", got["GET /a"])
	}
	if n := got["GET /b"]; len(n) != 64 || !strings.HasPrefix(n, "pets_abcdefghij") {
		t.Errorf("long name = %q (len %d), want 64 chars starting pets_abcdefghij", n, len(n))
	}
}

func TestAssignPrefixIsSanitized(t *testing.T) {
	got := Assign([]Candidate{{Key: "GET /a", OperationID: "x"}}, "my api_")
	if got["GET /a"] != "my_api_x" {
		t.Errorf("name = %q, want my_api_x", got["GET /a"])
	}
}

func TestAssignDedupesInStableOrder(t *testing.T) {
	cands := []Candidate{
		{Key: "POST /pets", OperationID: "pets"},
		{Key: "GET /pets", OperationID: "pets"},
		{Key: "DELETE /pets", OperationID: "Pets"}, // case-insensitive collision
	}
	got := Assign(cands, "")
	// Sorted OpKey order: "DELETE /pets" < "GET /pets" < "POST /pets".
	want := map[model.OpKey]string{"DELETE /pets": "Pets", "GET /pets": "pets_2", "POST /pets": "pets_3"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}

	// Input order must not matter.
	rev := []Candidate{cands[2], cands[0], cands[1]}
	got2 := Assign(rev, "")
	for k := range want {
		if got[k] != got2[k] {
			t.Errorf("%s: %q vs %q depending on input order", k, got[k], got2[k])
		}
	}
}

func TestAssignSuffixFitsIn64(t *testing.T) {
	long := strings.Repeat("x", 80)
	var cands []Candidate
	for i := range 12 {
		cands = append(cands, Candidate{Key: model.OpKey(fmt.Sprintf("GET /p%02d", i)), OperationID: long})
	}
	got := Assign(cands, "")
	seen := map[string]bool{}
	for _, c := range cands {
		n := got[c.Key]
		if !validName.MatchString(n) {
			t.Errorf("%s: invalid name %q (len %d)", c.Key, n, len(n))
		}
		if seen[strings.ToLower(n)] {
			t.Errorf("%s: duplicate name %q", c.Key, n)
		}
		seen[strings.ToLower(n)] = true
	}
	if got["GET /p00"] != strings.Repeat("x", 64) {
		t.Errorf("first = %q, want 64 x", got["GET /p00"])
	}
	if want := strings.Repeat("x", 61) + "_10"; got["GET /p09"] != want {
		t.Errorf("tenth = %q, want %q", got["GET /p09"], want)
	}
}

func TestAssignHundredCollisions(t *testing.T) {
	var cands []Candidate
	for i := range 100 {
		// All of these slugify to "get_item".
		cands = append(cands, Candidate{Key: model.OpKey(fmt.Sprintf("GET /item/%03d", i)), OperationID: "get item" + strings.Repeat("!", i)})
	}
	got := Assign(cands, "")
	if len(got) != len(cands) {
		t.Fatalf("Assign returned %d names, want %d", len(got), len(cands))
	}
	if got["GET /item/000"] != "get_item" || got["GET /item/099"] != "get_item_100" {
		t.Errorf("first/last = %q/%q, want get_item/get_item_100", got["GET /item/000"], got["GET /item/099"])
	}
	seen := map[string]bool{}
	for _, n := range got {
		if !validName.MatchString(n) {
			t.Errorf("invalid name %q", n)
		}
		if seen[strings.ToLower(n)] {
			t.Errorf("duplicate name %q", n)
		}
		seen[strings.ToLower(n)] = true
	}
}

func TestAssignAvoidsCollisionWithExistingSuffix(t *testing.T) {
	got := Assign([]Candidate{
		{Key: "GET /a", OperationID: "x"},
		{Key: "GET /b", OperationID: "x_2"},
		{Key: "GET /c", OperationID: "x"},
	}, "")
	if len(got) != 3 {
		t.Fatalf("Assign returned %d names, want 3", len(got))
	}
	seen := map[string]bool{}
	for k, n := range got {
		if seen[n] {
			t.Errorf("%s: duplicate %q", k, n)
		}
		seen[n] = true
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"  The pet's <b>name</b>\u202e.  ", 100, "The pet's name."},
		{"", 100, ""},
		{"\u200b<br>", 100, ""},
		{"abcdef", 4, "abc…"},
		{"abc", 0, "abc"}, // 0 means the default cap
	}
	for _, tc := range tests {
		if got := CleanText(tc.in, tc.max); got != tc.want {
			t.Errorf("CleanText(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name                  string
		summary, desc, method string
		path                  string
		max                   int
		want                  string
	}{
		{"summary wins", "List pets", "Long description", "GET", "/pets", 1024, "List pets"},
		{"description fallback", "", "Returns all pets.", "GET", "/pets", 1024, "Returns all pets."},
		{"method path fallback", " ", "", "get", "/pets/{id}", 1024, "GET /pets/{id}"},
		{"strips html", "<b>Bold</b> and <a href='x'>link</a>", "", "GET", "/", 1024, "Bold and link"},
		{"markdown links keep text", "See [the docs](https://evil.test/x) now", "", "GET", "/", 1024, "See the docs now"},
		{"strips control and bidi", "Safe\u202e\u200btext\x07 here\u2066", "", "GET", "/", 1024, "Safetext here"},
		{"collapses whitespace", "a\n\n  b\t c", "", "GET", "/", 1024, "a b c"},
		{"caps length", strings.Repeat("é", 30), "", "GET", "/", 10, strings.Repeat("é", 9) + "…"},
		{"empty after stripping falls back", "<br/>\u200b", "", "PUT", "/x", 1024, "PUT /x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Describe(tc.summary, tc.desc, tc.method, tc.path, tc.max); got != tc.want {
				t.Errorf("Describe = %q, want %q", got, tc.want)
			}
		})
	}
}
