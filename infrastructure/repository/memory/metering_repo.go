package memory

import (
	"sync"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// MeteringRepository is an in-memory aggregation store standing in for the
// PostgreSQL metering table (Batch B1, DESIGN §8). Production pushes UsageEvents
// to this store via ai-billing-service.
type MeteringRepository struct {
	mu      sync.Mutex
	records []domain.UsageEvent
}

// NewMeteringRepository builds an empty repository.
func NewMeteringRepository() *MeteringRepository {
	return &MeteringRepository{}
}

// Record appends a usage event.
func (r *MeteringRepository) Record(ev domain.UsageEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, ev)
}

// GetTenantUsage aggregates token usage for a tenant since a timestamp. A zero
// value for since includes all records.
func (r *MeteringRepository) GetTenantUsage(tenantID string, since time.Time) UsageSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sum UsageSummary
	sinceNano := since.UnixNano()
	for _, ev := range r.records {
		if ev.TenantID != tenantID {
			continue
		}
		if sinceNano != 0 && ev.UnixNanos < sinceNano {
			continue
		}
		sum.TotalTokens += ev.Usage.TotalTokens
		sum.PromptTokens += ev.Usage.PromptTokens
		sum.CompletionTokens += ev.Usage.CompletionTokens
		sum.Calls++
	}
	sum.TenantID = tenantID
	return sum
}

// UsageSummary aggregates metering totals for a tenant.
type UsageSummary struct {
	TenantID         string
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int
	Calls            int
}
