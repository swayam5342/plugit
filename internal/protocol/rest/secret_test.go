package rest

import "testing"

const testSecret = "test-token-123"

func TestSecretValues(t *testing.T) {
	tg := Target{
		Auth: Auth{Type: AuthBasic, Secret: "alice:" + testSecret},
		Fixed: []Fixed{
			{Name: "tenant", Value: "t-secret-9", Secret: true},
			{Name: "version", Value: "v2"},
		},
	}
	got := map[string]bool{}
	for _, s := range tg.SecretValues() {
		got[s] = true
	}
	for _, want := range []string{"alice:" + testSecret, testSecret, "t-secret-9"} {
		if !got[want] {
			t.Errorf("SecretValues() missing %q: %v", want, tg.SecretValues())
		}
	}
	for _, not := range []string{"v2", "alice", ""} {
		if got[not] {
			t.Errorf("SecretValues() must not include %q", not)
		}
	}
}
