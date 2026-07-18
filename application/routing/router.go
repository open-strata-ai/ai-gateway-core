// Package routing implements domain.ModelRouter (DESIGN §5.1): capability match,
// health/quota fallback and cost-aware self-hosted preference.
package routing

import (
	"context"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Router is the default ModelRouter. It is pure logic over the ModelCatalog.
type Router struct {
	catalog        domain.ModelCatalog
	defaultModel   string
	defaultChain   []string
	costAware      bool
	quotaExceeded  func(tenantID, modelID string) bool // optional hook (§5.1 step 2)
	forceSelfHost  func(tenantID string) bool           // deny_egress → self-hosted only (§5.5)
}

// Config configures a Router.
type Config struct {
	DefaultModel  string
	FallbackChain []string
	CostAware     bool
	// QuotaExceeded, when set, lets the router skip a preferred model whose quota
	// is already spent (§5.1 step 2). Optional.
	QuotaExceeded func(tenantID, modelID string) bool
	// ForceSelfHost forces self-hosted-only routing for deny_egress tenants (§5.5).
	ForceSelfHost func(tenantID string) bool
}

// New builds a Router.
func New(catalog domain.ModelCatalog, cfg Config) *Router {
	return &Router{
		catalog:       catalog,
		defaultModel:  cfg.DefaultModel,
		defaultChain:  cfg.FallbackChain,
		costAware:     cfg.CostAware,
		quotaExceeded: cfg.QuotaExceeded,
		forceSelfHost: cfg.ForceSelfHost,
	}
}

// Resolve applies the §5.1 algorithm and returns a RouteDecision.
func (r *Router) Resolve(ctx context.Context, req domain.ChatRequest) domain.RouteDecision {
	capability := req.Capability
	if capability == "" {
		capability = domain.CapChat
	}

	// Candidate order: request model → request fallback chain → config default → config chain.
	candidates := []string{}
	if req.Model != "" {
		candidates = append(candidates, req.Model)
	}
	candidates = append(candidates, req.FallbackChain...)
	if r.defaultModel != "" {
		candidates = append(candidates, r.defaultModel)
	}
	candidates = append(candidates, r.defaultChain...)
	candidates = dedupe(candidates)

	denyEgress := r.forceSelfHost != nil && r.forceSelfHost(req.TenantID)
	reason := "capability"

	filter := func(id string) (keep bool) {
		card, ok := r.catalog.Get(id)
		if !ok {
			return false
		}
		// (§5.1.1) capability must match.
		if card.Capability != capability {
			return false
		}
		// tenant whitelist.
		if !card.AllowsTenant(req.TenantID) {
			return false
		}
		// (§5.5) deny_egress forces self-hosted only.
		if denyEgress && card.Source != domain.SourceSelfHosted {
			reason = "egress"
			return false
		}
		// (§5.1.2) skip unhealthy or quota-exceeded preferred.
		if card.Health == domain.HealthDown {
			reason = "latency"
			return false
		}
		if r.quotaExceeded != nil && r.quotaExceeded(req.TenantID, id) {
			reason = "quota"
			return false
		}
		return true
	}

	var viable []string
	for _, id := range candidates {
		if filter(id) {
			viable = append(viable, id)
		}
	}

	// (§5.1.1) capability jump: if no requested candidate matched, fall back to
	// any catalog model that supports the requested capability for this tenant.
	if len(viable) == 0 {
		for _, card := range r.catalog.ListByCapability(capability, req.TenantID) {
			if !contains(candidates, card.ModelID) && filter(card.ModelID) {
				viable = append(viable, card.ModelID)
			}
		}
	}

	// (§5.1.3) cost-aware: prefer self-hosted among viable candidates.
	if r.costAware && len(viable) > 1 {
		viable = stableCostSort(r.catalog, viable)
		reason = "cost"
	}

	if len(viable) == 0 {
		return domain.RouteDecision{Reason: reason}
	}
	return domain.RouteDecision{
		Preferred:     viable[0],
		FallbackChain: viable[1:],
		Reason:        reason,
	}
}

// stableCostSort moves self-hosted models to the front, preserving relative order,
// then orders the remainder by ascending input price (cost awareness, §5.1.3).
func stableCostSort(catalog domain.ModelCatalog, ids []string) []string {
	selfHosted := []string{}
	thirdParty := []string{}
	for _, id := range ids {
		card, _ := catalog.Get(id)
		if card.Source == domain.SourceSelfHosted {
			selfHosted = append(selfHosted, id)
		} else {
			thirdParty = append(thirdParty, id)
		}
	}
	// cheapest third-party first (simple insertion sort keeps stdlib-only + stable enough)
	for i := 1; i < len(thirdParty); i++ {
		j := i
		for j > 0 {
			a, _ := catalog.Get(thirdParty[j-1])
			b, _ := catalog.Get(thirdParty[j])
			if b.PriceIn < a.PriceIn {
				thirdParty[j-1], thirdParty[j] = thirdParty[j], thirdParty[j-1]
				j--
				continue
			}
			break
		}
	}
	return append(selfHosted, thirdParty...)
}

func contains(in []string, s string) bool {
	for _, v := range in {
		if v == s {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
