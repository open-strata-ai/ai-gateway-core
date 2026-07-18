// Package metering implements domain.MeteringPort (R10, DESIGN §4.7.2): original
// usage events are buffered on a channel and drained by a background worker so the
// hot path never blocks.
package metering

import (
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Sink receives drained usage events (production: async push to ai-billing-service).
type Sink func(domain.UsageEvent)

// Reporter is an async, non-blocking metering reporter.
type Reporter struct {
	ch     chan domain.UsageEvent
	wg     sync.WaitGroup
	closed chan struct{}
	sink   Sink
}

// New builds a Reporter with a buffered queue and starts the drain worker.
func New(buffer int, sink Sink) *Reporter {
	if buffer <= 0 {
		buffer = 1024
	}
	r := &Reporter{
		ch:     make(chan domain.UsageEvent, buffer),
		closed: make(chan struct{}),
		sink:   sink,
	}
	r.wg.Add(1)
	go r.drain()
	return r
}

// Report enqueues an event without blocking; if the buffer is full the event is
// dropped (metering must never stall the request path, §9).
func (r *Reporter) Report(ev domain.UsageEvent) {
	select {
	case r.ch <- ev:
	default:
		// buffer full: drop to protect the hot path.
	}
}

func (r *Reporter) drain() {
	defer r.wg.Done()
	for {
		select {
		case ev := <-r.ch:
			if r.sink != nil {
				r.sink(ev)
			}
		case <-r.closed:
			// flush remaining
			for {
				select {
				case ev := <-r.ch:
					if r.sink != nil {
						r.sink(ev)
					}
				default:
					return
				}
			}
		}
	}
}

// Close stops the worker after flushing buffered events.
func (r *Reporter) Close() {
	close(r.closed)
	r.wg.Wait()
}

var _ domain.MeteringPort = (*Reporter)(nil)
