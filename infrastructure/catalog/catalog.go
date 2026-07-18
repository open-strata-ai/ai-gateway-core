// Package catalog is an in-memory domain.ModelCatalog (production: PostgreSQL
// model_catalog table, DESIGN §8).
package catalog

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// InMemory is a thread-safe ModelCatalog.
type InMemory struct {
	mu    sync.RWMutex
	cards map[string]domain.ModelCard
}

// New builds an empty catalog.
func New() *InMemory {
	return &InMemory{cards: map[string]domain.ModelCard{}}
}

// NewWithCards seeds the catalog.
func NewWithCards(cards ...domain.ModelCard) *InMemory {
	c := New()
	for _, card := range cards {
		c.Upsert(card)
	}
	return c
}

func (c *InMemory) Get(modelID string) (domain.ModelCard, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	card, ok := c.cards[modelID]
	return card, ok
}

func (c *InMemory) ListByCapability(capability, tenantID string) []domain.ModelCard {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []domain.ModelCard
	for _, card := range c.cards {
		if card.Capability == capability && card.AllowsTenant(tenantID) {
			out = append(out, card)
		}
	}
	return out
}

func (c *InMemory) UpdateHealth(modelID string, h domain.HealthStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if card, ok := c.cards[modelID]; ok {
		card.Health = h.State
		c.cards[modelID] = card
	}
}

func (c *InMemory) Upsert(card domain.ModelCard) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if card.Health == "" {
		card.Health = domain.HealthHealthy
	}
	c.cards[card.ModelID] = card
}

var _ domain.ModelCatalog = (*InMemory)(nil)
