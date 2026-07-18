// Package cache is an in-memory domain.Cache (production: Redis Vector Search
// semantic cache, DESIGN §5.4). Disabled by default (cache.enabled=false).
package cache

import (
	"context"
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// InMemory is a simple exact-match cache used as the offline stand-in.
type InMemory struct {
	mu      sync.RWMutex
	enabled bool
	store   map[string]*domain.ChatResponse
}

// New builds a cache; enabled mirrors config.cache.enabled.
func New(enabled bool) *InMemory {
	return &InMemory{enabled: enabled, store: map[string]*domain.ChatResponse{}}
}

func (c *InMemory) Enabled() bool { return c.enabled }

func (c *InMemory) Get(ctx context.Context, key string) (*domain.ChatResponse, bool) {
	if !c.enabled {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	resp, ok := c.store[key]
	return resp, ok
}

func (c *InMemory) Set(ctx context.Context, key string, resp *domain.ChatResponse) {
	if !c.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[key] = resp
}

var _ domain.Cache = (*InMemory)(nil)
