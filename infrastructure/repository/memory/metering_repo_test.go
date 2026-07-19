package memory_test

import (
	"testing"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/repository/memory"
)

func TestMeteringRepo_AggregatesByTenant(t *testing.T) {
	r := memory.NewMeteringRepository()
	now := time.Now()
	r.Record(domain.UsageEvent{TenantID: "t1", Model: "m", Usage: domain.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, UnixNanos: now.UnixNano()})
	r.Record(domain.UsageEvent{TenantID: "t1", Model: "m", Usage: domain.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}, UnixNanos: now.UnixNano()})
	r.Record(domain.UsageEvent{TenantID: "t2", Model: "m", Usage: domain.TokenUsage{TotalTokens: 999}, UnixNanos: now.UnixNano()})

	sum := r.GetTenantUsage("t1", time.Time{})
	if sum.TotalTokens != 45 {
		t.Fatalf("want 45 total tokens, got %d", sum.TotalTokens)
	}
	if sum.Calls != 2 {
		t.Fatalf("want 2 calls, got %d", sum.Calls)
	}
	if sum.TenantID != "t1" {
		t.Fatalf("bad tenant id: %q", sum.TenantID)
	}
}

func TestMeteringRepo_SinceFilter(t *testing.T) {
	r := memory.NewMeteringRepository()
	t0 := time.Now()
	r.Record(domain.UsageEvent{TenantID: "t1", Usage: domain.TokenUsage{TotalTokens: 10}, UnixNanos: t0.UnixNano()})
	t1 := t0.Add(time.Hour)
	r.Record(domain.UsageEvent{TenantID: "t1", Usage: domain.TokenUsage{TotalTokens: 20}, UnixNanos: t1.UnixNano()})
	sum := r.GetTenantUsage("t1", t1)
	if sum.TotalTokens != 20 {
		t.Fatalf("want 20 after since filter, got %d", sum.TotalTokens)
	}
}
