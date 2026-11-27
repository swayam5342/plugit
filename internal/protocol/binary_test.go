package protocol

import "testing"

func TestIsBinary(t *testing.T) {
	for _, tc := range []struct {
		ct, body string
		want     bool
	}{
		{"application/json", `{"a":1}`, false},
		{"text/plain; charset=utf-8", "héllo", false},
		{"image/png", "PNG", true},
		{"application/octet-stream", "abc", true},
		{"application/pdf; x=y", "", true},
		{"text/plain", "nul\x00byte", true},
		{"text/plain", "\xff\xfe", true},
		{"", "plain", false},
	} {
		if got := IsBinary(tc.ct, []byte(tc.body)); got != tc.want {
			t.Errorf("IsBinary(%q, %q) = %v, want %v", tc.ct, tc.body, got, tc.want)
		}
	}
}
