package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/store"
)

func limiter(n int) *policy.Limiter {
	return policy.NewLimiter(policy.Rate{N: n, Per: time.Minute}, nil)
}

// SPEC §3.7 step 3: over the limit is an isError result with a retry-after,
// and the API is not called.
func TestToolRateLimit(t *testing.T) {
	rec := &recorded{}
	tools := []Tool{{Tool: listPets, Limit: limiter(2)}, {Tool: headPets}}
	r := setup(t, Config{Tools: tools, Mode: policy.ReadOnly, Log: callLog(rec, "none", false)}, nil)
	for range 2 {
		if res, _ := r.call(t, "listPets", map[string]any{}); res.IsError {
			t.Fatalf("within the limit: %s", text(res))
		}
	}
	res, _ := r.call(t, "listPets", map[string]any{})
	if !res.IsError || !strings.Contains(text(res), "rate limit") || !strings.Contains(text(res), "2/min") || !strings.Contains(text(res), "retry in") {
		t.Errorf("isError=%v text=%s", res.IsError, text(res))
	}
	if r.hits.Load() != 2 {
		t.Errorf("upstream hits = %d, want 2", r.hits.Load())
	}
	if c := rec.last(t); c.Outcome != store.RateLimited {
		t.Errorf("recorded %+v", c)
	}
	// Other tools have their own (here: no) tool limit.
	if res, _ := r.call(t, "headPets", map[string]any{}); res.IsError {
		t.Errorf("headPets limited by listPets' budget: %s", text(res))
	}
}

// policy.rate_limit is shared by every tool of the API.
func TestAPIRateLimit(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets, headPets), Mode: policy.ReadOnly, Limit: limiter(2)}, nil)
	for _, name := range []string{"listPets", "headPets"} {
		if res, err := r.call(t, name, map[string]any{}); err != nil || res.IsError {
			t.Fatalf("%s within the limit: %v %s", name, err, text(res))
		}
	}
	res, _ := r.call(t, "listPets", map[string]any{})
	if !res.IsError || !strings.Contains(text(res), "for this API") {
		t.Errorf("isError=%v text=%s", res.IsError, text(res))
	}
}

// A confirmed call is one call: the retry that carries the answer doesn't
// spend the budget again.
func TestRateLimitCountsConfirmedCallOnce(t *testing.T) {
	e := &elicitor{answer: accept(true)}
	cfg := Config{Tools: []Tool{{Tool: createPetBody, Limit: limiter(1)}}, Mode: policy.ConfirmWrites}
	r := setupClient(t, cfg, nil, e.opts())
	if res, err := r.call(t, "createPet", map[string]any{"name": "rex"}); err != nil || res.IsError {
		t.Fatalf("err=%v text=%s", err, text(res))
	}
	if res, _ := r.call(t, "createPet", map[string]any{"name": "rex"}); !res.IsError || !strings.Contains(text(res), "rate limit") {
		t.Errorf("second call should be limited: %s", text(res))
	}
}
