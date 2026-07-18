// Package audit is an in-memory append-only domain.AuditRecorder (production:
// PostgreSQL immutable audit_log, DESIGN §8 / R11).
package audit

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// InMemory stores audit entries in append-only order.
type InMemory struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

// New builds an audit recorder.
func New() *InMemory { return &InMemory{} }

// Append adds an immutable entry.
func (a *InMemory) Append(e domain.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

// Entries returns a copy of the recorded entries (test/inspection helper).
func (a *InMemory) Entries() []domain.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]domain.AuditEntry, len(a.entries))
	copy(out, a.entries)
	return out
}

var _ domain.AuditRecorder = (*InMemory)(nil)
