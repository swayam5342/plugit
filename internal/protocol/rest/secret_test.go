package rest

import "testing"

func TestSecretValues(t *testing.T) {
	tg := Target{
		Fixed: []Fixed{
			{Name: "tenant", Value: "t-secret-9", Secret: true},
			{Name: "version", Value: "v2"},
		},
	}
	got := map[string]bool{}
	for _, s := range tg.SecretValues() {
		got[s] = true
	}
	for _, want := range []string{"t-secret-9"} {
		if !got[want] {
			t.Errorf("SecretValues() missing %q: %v", want, tg.SecretValues())
		}
	}
	for _, not := range []string{"v2", ""} {
		if got[not] {
			t.Errorf("SecretValues() must not include %q", not)
		}
	}
}
