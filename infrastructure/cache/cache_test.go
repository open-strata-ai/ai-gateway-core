package cache_test

import (
	"context"
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/cache"
)

// TestCacheRedisBehavior exercises the in-memory stand-in for the Redis cache
// repository (Batch B1, DESIGN §5.4). When enabled, values are stored and
// returned; when disabled, the cache is a transparent no-op.
func TestCacheRedisBehavior(t *testing.T) {
	c := cache.New(true)
	resp := &domain.ChatResponse{Model: "m", Content: "hi"}
	c.Set(context.Background(), "k", resp)
	got, ok := c.Get(context.Background(), "k")
	if !ok || got.Content != "hi" {
		t.Fatalf("expected cached value, got ok=%v v=%+v", ok, got)
	}
	if !c.Enabled() {
		t.Fatalf("cache should be enabled")
	}
}

func TestCacheDisabledIsNoop(t *testing.T) {
	c := cache.New(false)
	c.Set(context.Background(), "k", &domain.ChatResponse{Content: "x"})
	if _, ok := c.Get(context.Background(), "k"); ok {
		t.Fatalf("disabled cache must not return values")
	}
	if c.Enabled() {
		t.Fatalf("cache should report disabled")
	}
}
