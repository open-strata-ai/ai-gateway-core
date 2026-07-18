package provider

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Registry maps model_id → LLMProvider adapter. It satisfies chat.ProviderRegistry.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]domain.LLMProvider
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]domain.LLMProvider{}}
}

// Register binds a provider to a model_id.
func (r *Registry) Register(modelID string, p domain.LLMProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[modelID] = p
}

// ForModel returns the provider bound to modelID.
func (r *Registry) ForModel(modelID string) (domain.LLMProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[modelID]
	return p, ok
}
