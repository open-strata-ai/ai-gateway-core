// Package breaker implements domain.CircuitBreaker as a per-key half-open state
// machine (DESIGN §5.2): Closed → Open (error rate over threshold) → HalfOpen
// (probe) → Closed/Open.
package breaker

import (
	"sync"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

const (
	StateClosed   = "closed"
	StateOpen     = "open"
	StateHalfOpen = "half_open"
)

type entry struct {
	state      string
	failures   int
	successes  int
	total      int
	openedAt   time.Time
	probes     int
}

// Breaker is a thread-safe circuit breaker keyed by provider instance.
type Breaker struct {
	mu             sync.Mutex
	entries        map[string]*entry
	errorThreshold float64       // trip when failure ratio >= threshold
	minRequests    int           // minimum sample before evaluating
	cooldown       time.Duration // Open → HalfOpen after this window
	now            func() time.Time
}

// Config configures a Breaker.
type Config struct {
	ErrorThreshold float64
	MinRequests    int
	Cooldown       time.Duration
	Now            func() time.Time // injectable clock for tests
}

// New builds a Breaker.
func New(cfg Config) *Breaker {
	if cfg.ErrorThreshold <= 0 {
		cfg.ErrorThreshold = 0.5
	}
	if cfg.MinRequests <= 0 {
		cfg.MinRequests = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Breaker{
		entries:        map[string]*entry{},
		errorThreshold: cfg.ErrorThreshold,
		minRequests:    cfg.MinRequests,
		cooldown:       cfg.Cooldown,
		now:            cfg.Now,
	}
}

func (b *Breaker) get(key string) *entry {
	e, ok := b.entries[key]
	if !ok {
		e = &entry{state: StateClosed}
		b.entries[key] = e
	}
	return e
}

// Allow reports whether a call to key may proceed.
func (b *Breaker) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.get(key)
	switch e.state {
	case StateOpen:
		if b.now().Sub(e.openedAt) >= b.cooldown {
			e.state = StateHalfOpen
			e.probes = 0
			return true // allow a single probe
		}
		return false
	case StateHalfOpen:
		// allow limited probes
		if e.probes < 1 {
			e.probes++
			return true
		}
		return false
	default: // Closed
		return true
	}
}

// Record feeds a completed call outcome back into the state machine.
func (b *Breaker) Record(key string, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.get(key)

	switch e.state {
	case StateHalfOpen:
		if success {
			b.reset(e)
		} else {
			e.state = StateOpen
			e.openedAt = b.now()
		}
		return
	case StateOpen:
		return
	}

	// Closed: accumulate and evaluate.
	e.total++
	if success {
		e.successes++
	} else {
		e.failures++
	}
	if e.total >= b.minRequests {
		ratio := float64(e.failures) / float64(e.total)
		if ratio >= b.errorThreshold {
			e.state = StateOpen
			e.openedAt = b.now()
		}
	}
}

// State returns the current state for key.
func (b *Breaker) State(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.get(key).state
}

func (b *Breaker) reset(e *entry) {
	e.state = StateClosed
	e.failures = 0
	e.successes = 0
	e.total = 0
	e.probes = 0
}

var _ domain.CircuitBreaker = (*Breaker)(nil)
