package policy

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Rate is a call budget: N calls per Per (SPEC §9 item 5).
type Rate struct {
	N   int
	Per time.Duration
}

var rateRe = regexp.MustCompile(`^([0-9]{1,9})/(sec|min|hour)$`)

var ratePeriods = map[string]time.Duration{"sec": time.Second, "min": time.Minute, "hour": time.Hour}

// ParseRate parses N/sec, N/min or N/hour with N > 0.
func ParseRate(s string) (Rate, error) {
	m := rateRe.FindStringSubmatch(s)
	if m == nil {
		return Rate{}, errors.New("use N/sec, N/min or N/hour with N > 0")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return Rate{}, errors.New("use N/sec, N/min or N/hour with N > 0")
	}
	return Rate{N: n, Per: ratePeriods[m[2]]}, nil
}

func (r Rate) String() string {
	for name, d := range ratePeriods {
		if d == r.Per {
			return fmt.Sprintf("%d/%s", r.N, name)
		}
	}
	return fmt.Sprintf("%d/%s", r.N, r.Per)
}

// Limiter is a token bucket: a burst of N calls, refilled at N per period.
// It is safe for concurrent use. A nil *Limiter allows everything.
type Limiter struct {
	rate   Rate
	now    func() time.Time
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// NewLimiter starts a full bucket. now is the clock (time.Now if nil).
func NewLimiter(r Rate, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{rate: r, now: now, tokens: float64(r.N), last: now()}
}

// Rate is the limiter's budget.
func (l *Limiter) Rate() Rate { return l.rate }

// Allow takes one call from the budget. When there is none, it reports how
// long until there will be.
func (l *Limiter) Allow() (ok bool, retryAfter time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	perToken := l.rate.Per / time.Duration(l.rate.N)
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens = math.Min(float64(l.rate.N), l.tokens+float64(elapsed)/float64(perToken))
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}
	wait := time.Duration((1 - l.tokens) * float64(perToken))
	return false, wait.Round(time.Millisecond)
}
