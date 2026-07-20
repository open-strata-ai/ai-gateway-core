// Package memory provides in-memory domain repositories used as the offline
// stand-in for PostgreSQL / Redis (Batch B1, DESIGN §8). The same interfaces are
// implemented by the production PostgreSQL adapters.
package memory

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// SessionRepository is an in-memory domain.SessionRepository.
type SessionRepository struct {
	mu       sync.RWMutex
	sessions map[string]*domain.Session
}

// NewSessionRepository builds an empty repository.
func NewSessionRepository() *SessionRepository {
	return &SessionRepository{sessions: map[string]*domain.Session{}}
}

func (r *SessionRepository) Save(s domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := s
	r.sessions[s.ID] = &cp
	return nil
}

func (r *SessionRepository) Get(id string) (domain.Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	if !ok {
		return domain.Session{}, false
	}
	return *s, true
}

func (r *SessionRepository) AppendMessage(id string, m domain.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return domain.NewError(domain.ErrInvalidRequest, 404, "session not found")
	}
	s.Messages = append(s.Messages, m)
	return nil
}

func (r *SessionRepository) ListByTenant(tenantID string) []domain.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.Session
	for _, s := range r.sessions {
		if s.TenantID == tenantID {
			out = append(out, *s)
		}
	}
	return out
}

var _ domain.SessionRepository = (*SessionRepository)(nil)
