// Package metering provides concrete sinks for the async metering Reporter
// (application/metering). Offline it logs; production pushes to ai-billing-service
// over the async event bus (async-events.yaml, R10 / DESIGN §4.7.2).
package metering

import (
	"log"

	appmetering "github.com/open-strata-ai/ai-gateway-core/application/metering"
	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// LogSink returns a Sink that logs each usage event.
func LogSink() appmetering.Sink {
	return func(ev domain.UsageEvent) {
		log.Printf("metering tenant=%s model=%s tokens=%d latency_ms=%d success=%t",
			ev.TenantID, ev.Model, ev.Usage.TotalTokens, ev.LatencyMs, ev.Success)
	}
}

// DiscardSink returns a Sink that ignores events (tests).
func DiscardSink() appmetering.Sink {
	return func(domain.UsageEvent) {}
}
