package memory

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// AgentRepository is an in-memory domain.AgentRepository. It is the offline
// stand-in for the PostgreSQL agent table (DESIGN §8 / EU-05 authoring).
// The same interface is implemented by the production PostgreSQL adapter.
type AgentRepository struct {
	mu     sync.RWMutex
	agents map[string]domain.AgentSpec
}

// NewAgentRepository builds an empty agent repository.
func NewAgentRepository() *AgentRepository {
	return &AgentRepository{agents: map[string]domain.AgentSpec{}}
}

func (r *AgentRepository) Save(a domain.AgentSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[a.ID] = a
	return nil
}

func (r *AgentRepository) Get(id string) (domain.AgentSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	return a, ok
}

func (r *AgentRepository) List(tenantID string) []domain.AgentSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.AgentSpec, 0, len(r.agents))
	for _, a := range r.agents {
		if tenantID == "" || a.TenantID == tenantID {
			out = append(out, a)
		}
	}
	return out
}

func (r *AgentRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
	return nil
}

var _ domain.AgentRepository = (*AgentRepository)(nil)
