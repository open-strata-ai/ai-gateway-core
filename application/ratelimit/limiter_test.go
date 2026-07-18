package ratelimit

import (
	"testing"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

func TestLimiter_QPSExceeded(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(Config{Now: func() time.Time { return now }})
	limit := domain.RateLimit{QPSPerTenant: 2, TPMPerTenant: 0}
	if ok, _ := l.Allow("t1", "m1", 0, limit); !ok {
		t.Fatalf("1st should pass")
	}
	if ok, _ := l.Allow("t1", "m1", 0, limit); !ok {
		t.Fatalf("2nd should pass")
	}
	if ok, why := l.Allow("t1", "m1", 0, limit); ok || why != "qps_exceeded" {
		t.Fatalf("3rd should be qps_exceeded, ok=%v why=%s", ok, why)
	}
}

func TestLimiter_QPSRefillsOverTime(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(Config{Now: func() time.Time { return now }})
	limit := domain.RateLimit{QPSPerTenant: 1}
	l.Allow("t1", "m1", 0, limit)
	if ok, _ := l.Allow("t1", "m1", 0, limit); ok {
		t.Fatalf("should be limited immediately")
	}
	now = now.Add(2 * time.Second) // refill
	if ok, _ := l.Allow("t1", "m1", 0, limit); !ok {
		t.Fatalf("should pass after refill")
	}
}

func TestLimiter_TPMExceeded(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(Config{Now: func() time.Time { return now }})
	limit := domain.RateLimit{QPSPerTenant: 100, TPMPerTenant: 1000}
	if ok, _ := l.Allow("t1", "m1", 800, limit); !ok {
		t.Fatalf("800 tokens should pass")
	}
	if ok, why := l.Allow("t1", "m1", 300, limit); ok || why != "tpm_exceeded" {
		t.Fatalf("800+300>1000 should be tpm_exceeded, ok=%v why=%s", ok, why)
	}
}

func TestLimiter_GlobalCeiling(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(Config{GlobalQPS: 2, Now: func() time.Time { return now }})
	limit := domain.RateLimit{QPSPerTenant: 100}
	l.Allow("t1", "m1", 0, limit)
	l.Allow("t2", "m1", 0, limit)
	if ok, why := l.Allow("t3", "m1", 0, limit); ok || why != "global_qps_exceeded" {
		t.Fatalf("global ceiling should trip, ok=%v why=%s", ok, why)
	}
}

func TestLimiter_PerTenantIsolation(t *testing.T) {
	now := time.Unix(100, 0)
	l := New(Config{Now: func() time.Time { return now }})
	limit := domain.RateLimit{QPSPerTenant: 1}
	l.Allow("t1", "m1", 0, limit)
	// different tenant should have its own bucket
	if ok, _ := l.Allow("t2", "m1", 0, limit); !ok {
		t.Fatalf("t2 must not be affected by t1's usage")
	}
}
