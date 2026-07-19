package catalog

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// AgentInMemory is an in-memory domain.AgentCatalog (DESIGN §3.2, EU-05). The
// authoritative catalog lives in ai-platform-api; the gateway keeps a read-only
// projection it can list without a network hop.
type AgentInMemory struct {
	mu     sync.RWMutex
	agents map[string]domain.AgentSummary
}

// NewAgentInMemory builds an empty agent catalog.
func NewAgentInMemory() *AgentInMemory {
	return &AgentInMemory{agents: map[string]domain.AgentSummary{}}
}

// Put adds/updates an agent summary.
func (c *AgentInMemory) Put(a domain.AgentSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents[a.ID] = a
}

// ListAvailable returns all published agents (tenant scoping is a no-op in the
// offline projection; production filters by tenant entitlement).
func (c *AgentInMemory) ListAvailable(tenantID string) []domain.AgentSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]domain.AgentSummary, 0, len(c.agents))
	for _, a := range c.agents {
		if a.Status == "published" || a.Status == "" {
			out = append(out, a)
		}
	}
	return out
}

var _ domain.AgentCatalog = (*AgentInMemory)(nil)
