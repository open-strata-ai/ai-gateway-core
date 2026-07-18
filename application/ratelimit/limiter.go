// Package ratelimit implements domain.RateLimiter (DESIGN §5.3): a token bucket
// for QPS plus a sliding-window TPM counter, per tenant×model, with a global
// application-level ceiling as the hard backstop (resolution R-003).
package ratelimit

import (
	"sync"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

type bucket struct {
	tokens   float64
	last     time.Time
	tpmSum   int
	tpmMarks []mark // TPM sliding window entries
}

type mark struct {
	at     time.Time
	tokens int
}

// Limiter is a thread-safe in-memory rate limiter (production: Redis Lua, §5.3).
type Limiter struct {
	mu           sync.Mutex
	buckets      map[string]*bucket
	now          func() time.Time
	globalQPS    int
	globalMarks  []time.Time
}

// Config configures a Limiter.
type Config struct {
	// GlobalQPS is the application-level ceiling across all tenants (R-003).
	GlobalQPS int
	Now       func() time.Time
}

// New builds a Limiter.
func New(cfg Config) *Limiter {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Limiter{
		buckets:   map[string]*bucket{},
		now:       cfg.Now,
		globalQPS: cfg.GlobalQPS,
	}
}

// Allow reserves one request (and estTokens) for tenant×model against `limit`.
func (l *Limiter) Allow(tenantID, modelID string, estTokens int, limit domain.RateLimit) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	// Application-level (global) ceiling — hard backstop (R-003).
	if l.globalQPS > 0 {
		l.globalMarks = pruneTimes(l.globalMarks, now, time.Second)
		if len(l.globalMarks) >= l.globalQPS {
			return false, "global_qps_exceeded"
		}
	}

	key := tenantID + "|" + modelID
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(maxInt(limit.QPSPerTenant, 0)), last: now}
		l.buckets[key] = b
	}

	// QPS token bucket (per-tenant).
	if limit.QPSPerTenant > 0 {
		rate := float64(limit.QPSPerTenant) // tokens per second
		elapsed := now.Sub(b.last).Seconds()
		b.tokens = minFloat(float64(limit.QPSPerTenant), b.tokens+elapsed*rate)
		b.last = now
		if b.tokens < 1 {
			return false, "qps_exceeded"
		}
	}

	// TPM sliding window (per-tenant).
	if limit.TPMPerTenant > 0 {
		b.tpmMarks, b.tpmSum = pruneMarks(b.tpmMarks, now, time.Minute)
		if b.tpmSum+estTokens > limit.TPMPerTenant {
			return false, "tpm_exceeded"
		}
	}

	// Commit reservations.
	if limit.QPSPerTenant > 0 {
		b.tokens--
	}
	if limit.TPMPerTenant > 0 {
		b.tpmMarks = append(b.tpmMarks, mark{at: now, tokens: estTokens})
		b.tpmSum += estTokens
	}
	if l.globalQPS > 0 {
		l.globalMarks = append(l.globalMarks, now)
	}
	return true, ""
}

func pruneMarks(marks []mark, now time.Time, window time.Duration) ([]mark, int) {
	cutoff := now.Add(-window)
	i := 0
	sum := 0
	for _, m := range marks {
		if m.at.After(cutoff) {
			marks[i] = m
			i++
			sum += m.tokens
		}
	}
	return marks[:i], sum
}

func pruneTimes(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	i := 0
	for _, t := range ts {
		if t.After(cutoff) {
			ts[i] = t
			i++
		}
	}
	return ts[:i]
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ domain.RateLimiter = (*Limiter)(nil)
