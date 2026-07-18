package breaker

import (
	"testing"
	"time"
)

func TestBreaker_OpensAfterThreshold(t *testing.T) {
	b := New(Config{ErrorThreshold: 0.5, MinRequests: 4, Cooldown: 30 * time.Second})
	key := "p1"
	// 2 ok, 2 fail → ratio 0.5 ≥ threshold → open
	b.Record(key, true)
	b.Record(key, true)
	b.Record(key, false)
	b.Record(key, false)
	if got := b.State(key); got != StateOpen {
		t.Fatalf("want open, got %s", got)
	}
	if b.Allow(key) {
		t.Fatalf("open breaker must reject calls")
	}
}

func TestBreaker_HalfOpenThenClose(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	b := New(Config{ErrorThreshold: 0.5, MinRequests: 2, Cooldown: 30 * time.Second, Now: clock})
	key := "p1"
	b.Record(key, false)
	b.Record(key, false)
	if b.State(key) != StateOpen {
		t.Fatalf("should be open")
	}
	// advance past cooldown → half-open probe allowed
	now = now.Add(31 * time.Second)
	if !b.Allow(key) {
		t.Fatalf("should allow a probe after cooldown")
	}
	if b.State(key) != StateHalfOpen {
		t.Fatalf("want half_open, got %s", b.State(key))
	}
	// successful probe closes the breaker
	b.Record(key, true)
	if b.State(key) != StateClosed {
		t.Fatalf("want closed after successful probe, got %s", b.State(key))
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	now := time.Unix(0, 0)
	b := New(Config{ErrorThreshold: 0.5, MinRequests: 2, Cooldown: time.Second, Now: func() time.Time { return now }})
	key := "p1"
	b.Record(key, false)
	b.Record(key, false)
	now = now.Add(2 * time.Second)
	b.Allow(key) // → half-open
	b.Record(key, false)
	if b.State(key) != StateOpen {
		t.Fatalf("failed probe must reopen, got %s", b.State(key))
	}
}

func TestBreaker_ClosedAllowsByDefault(t *testing.T) {
	b := New(Config{})
	if !b.Allow("fresh") {
		t.Fatalf("fresh key should be allowed")
	}
}
