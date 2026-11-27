package cli

import (
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/secrets"
)

// The credential header is the Authorization --header when one is given
// (any case), wherever it sits; otherwise the first --header. plugit add
// and plugit run (for GraphQL) pick it the same way.
func TestCredentialHeaderPrefersAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []string
		want    string
	}{
		{"authorization second", []string{"X-Tenant=env:A", "authorization=env:B"}, "authorization"},
		{"authorization first", []string{"Authorization=env:B", "X-Tenant=env:A"}, "Authorization"},
		{"no authorization", []string{"X-Tenant=env:A", "X-Api-Key=env:B"}, "X-Tenant"},
	} {
		t.Run("add/"+tc.name, func(t *testing.T) {
			auth, fixed, err := credentialConfig(tc.headers, nil)
			if err != nil {
				t.Fatal(err)
			}
			if auth.Type != config.AuthHeader || auth.Header != tc.want {
				t.Errorf("auth = %+v, want header %s", auth, tc.want)
			}
			if len(fixed) != 1 || fixed[0].Name == tc.want || fixed[0].In != "header" {
				t.Errorf("fixed = %+v, want the other header as a fixed header", fixed)
			}
		})
		t.Run("run/"+tc.name, func(t *testing.T) {
			var flags []credFlag
			for _, h := range tc.headers {
				name, ref := cutHeaderFlag(t, h)
				flags = append(flags, credFlag{name: name, ref: ref})
			}
			rc, err := resolveRunCredentials(flags, nil, envResolver(map[string]string{"A": "test-a-123", "B": "test-b-123"}), true)
			if err != nil {
				t.Fatal(err)
			}
			if rc.cred.Header != tc.want {
				t.Errorf("credential header = %q, want %q", rc.cred.Header, tc.want)
			}
			if len(rc.fixed.items) != 1 || rc.fixed.items[0].Name == tc.want {
				t.Errorf("fixed = %+v, want the other header", rc.fixed.items)
			}
		})
	}
}

func cutHeaderFlag(t *testing.T, flag string) (string, secrets.Ref) {
	t.Helper()
	hs, err := parseHeaderFlags([]string{flag})
	if err != nil || len(hs) != 1 {
		t.Fatalf("parseHeaderFlags(%q): %v", flag, err)
	}
	return hs[0].name, hs[0].ref
}
