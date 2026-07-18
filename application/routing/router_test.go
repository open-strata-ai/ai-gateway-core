package routing

import (
	"context"
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/catalog"
)

func seed() *catalog.InMemory {
	return catalog.NewWithCards(
		domain.ModelCard{ModelID: "cloud-qwen-max", Source: domain.SourceThirdParty, Capability: domain.CapChat, PriceIn: 2.0, Health: domain.HealthHealthy},
		domain.ModelCard{ModelID: "cloud-gpt-4o", Source: domain.SourceThirdParty, Capability: domain.CapChat, PriceIn: 5.0, Health: domain.HealthHealthy},
		domain.ModelCard{ModelID: "local-qwen-72b", Source: domain.SourceSelfHosted, Capability: domain.CapChat, PriceIn: 0.5, Health: domain.HealthHealthy},
		domain.ModelCard{ModelID: "vision-model", Source: domain.SourceThirdParty, Capability: domain.CapVision, Health: domain.HealthHealthy},
	)
}

func TestResolve_PrefersRequestedHealthyModel(t *testing.T) {
	r := New(seed(), Config{DefaultModel: "cloud-qwen-max", CostAware: false})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "cloud-gpt-4o", Capability: domain.CapChat})
	if d.Preferred != "cloud-gpt-4o" {
		t.Fatalf("want cloud-gpt-4o, got %q", d.Preferred)
	}
}

func TestResolve_CapabilityJump(t *testing.T) {
	// requested a chat model but capability=vision → must jump to vision-model (§5.1.1)
	r := New(seed(), Config{DefaultModel: "cloud-qwen-max"})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "cloud-qwen-max", Capability: domain.CapVision})
	if d.Preferred != "vision-model" {
		t.Fatalf("want vision-model, got %q", d.Preferred)
	}
}

func TestResolve_SkipsUnhealthyPreferred(t *testing.T) {
	cat := seed()
	cat.UpdateHealth("cloud-qwen-max", domain.HealthStatus{State: domain.HealthDown})
	r := New(cat, Config{DefaultModel: "cloud-qwen-max", FallbackChain: []string{"cloud-gpt-4o"}})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "cloud-qwen-max", FallbackChain: []string{"cloud-gpt-4o"}, Capability: domain.CapChat})
	if d.Preferred == "cloud-qwen-max" {
		t.Fatalf("should have skipped down model, got %q", d.Preferred)
	}
}

func TestResolve_SkipsQuotaExceeded(t *testing.T) {
	r := New(seed(), Config{
		DefaultModel:  "cloud-qwen-max",
		FallbackChain: []string{"cloud-gpt-4o"},
		QuotaExceeded: func(_ , modelID string) bool { return modelID == "cloud-qwen-max" },
	})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "cloud-qwen-max", FallbackChain: []string{"cloud-gpt-4o"}, Capability: domain.CapChat})
	if d.Preferred == "cloud-qwen-max" {
		t.Fatalf("quota-exceeded model should be skipped, got %q (reason=%s)", d.Preferred, d.Reason)
	}
}

func TestResolve_CostAwarePrefersSelfHosted(t *testing.T) {
	r := New(seed(), Config{DefaultModel: "cloud-qwen-max", FallbackChain: []string{"cloud-gpt-4o", "local-qwen-72b"}, CostAware: true})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "cloud-qwen-max", FallbackChain: []string{"cloud-gpt-4o", "local-qwen-72b"}, Capability: domain.CapChat})
	if d.Preferred != "local-qwen-72b" {
		t.Fatalf("cost-aware should prefer self-hosted, got %q (reason=%s)", d.Preferred, d.Reason)
	}
	if d.Reason != "cost" {
		t.Fatalf("want reason=cost, got %q", d.Reason)
	}
}

func TestResolve_DenyEgressForcesSelfHosted(t *testing.T) {
	r := New(seed(), Config{
		DefaultModel:  "cloud-qwen-max",
		FallbackChain: []string{"local-qwen-72b"},
		ForceSelfHost: func(tenantID string) bool { return tenantID == "gov" },
	})
	d := r.Resolve(context.Background(), domain.ChatRequest{TenantID: "gov", Model: "cloud-qwen-max", FallbackChain: []string{"local-qwen-72b"}, Capability: domain.CapChat})
	if d.Preferred != "local-qwen-72b" {
		t.Fatalf("deny_egress must route self-hosted only, got %q", d.Preferred)
	}
}

func TestResolve_NoViableModel(t *testing.T) {
	// no catalog model supports the audio capability → no viable model
	r := New(seed(), Config{})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "does-not-exist", Capability: domain.CapAudio})
	if d.Preferred != "" {
		t.Fatalf("want empty preferred, got %q", d.Preferred)
	}
}

func TestResolve_CapabilityJumpFromCatalogWhenRequestUnknown(t *testing.T) {
	// unknown requested model but a chat-capable model exists → capability jump (§5.1.1)
	r := New(seed(), Config{})
	d := r.Resolve(context.Background(), domain.ChatRequest{Model: "does-not-exist", Capability: domain.CapChat})
	if d.Preferred == "" {
		t.Fatalf("expected a capability-matching model, got empty")
	}
}
