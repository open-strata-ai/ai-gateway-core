// Package metering provides concrete sinks for the async metering Reporter
// (application/metering). Offline it logs; production pushes to ai-billing-service
// over the async event bus (async-events.yaml, R10 / DESIGN §4.7.2).
package metering

import (
	"context"
	"log"
	"sync"

	appmetering "github.com/open-strata-ai/ai-gateway-core/application/metering"
	"github.com/open-strata-ai/ai-gateway-core/domain"
	billingcli "github.com/open-strata-ai/ai-gateway-core/infrastructure/billing"
)

// tokenTotals keeps a per-tenant cumulative prompt/completion token count so the
// portal /usage page can show a live "token used" figure even before billing has
// aggregated the pushed events. It is process-local and resets on gateway restart
// (acceptable for the dev/single-process gateway; production sources this from
// ai-billing-service directly).
var (
	tokenMu    sync.RWMutex
	tokenTotals = map[string][2]int64{} // tenant -> [prompt, completion]
)

func recordTokens(tenant string, prompt, completion int64) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	pair := tokenTotals[tenant]
	pair[0] += prompt
	pair[1] += completion
	tokenTotals[tenant] = pair
}

// TokenTotals returns the cumulative prompt+completion tokens recorded for a
// tenant (0 if none).
func TokenTotals(tenant string) (prompt, completion int64) {
	tokenMu.RLock()
	defer tokenMu.RUnlock()
	p := tokenTotals[tenant]
	return p[0], p[1]
}

// LogSink returns a Sink that logs each usage event.
func LogSink() appmetering.Sink {
	return func(ev domain.UsageEvent) {
		log.Printf("metering tenant=%s model=%s tokens=%d latency_ms=%d success=%t",
			ev.TenantID, ev.Model, ev.Usage.TotalTokens, ev.LatencyMs, ev.Success)
	}
}

// BillingSink logs each event AND reports token usage to ai-billing-service so the
// tenant cost dashboard becomes real. It runs inside the Reporter's async drain
// worker, so a slow/failed billing POST never stalls the request path. Errors are
// logged and ignored (metering must be best-effort).
func BillingSink(c *billingcli.Client, appID string) appmetering.Sink {
	return func(ev domain.UsageEvent) {
		log.Printf("metering tenant=%s model=%s tokens=%d latency_ms=%d success=%t",
			ev.TenantID, ev.Model, ev.Usage.TotalTokens, ev.LatencyMs, ev.Success)
		if ev.Success {
			recordTokens(ev.TenantID, int64(ev.Usage.PromptTokens), int64(ev.Usage.CompletionTokens))
		}
		if c == nil {
			return
		}
		// Fire-and-forget: the worker already isolates this from the hot path.
		_ = c.ReportUsage(context.Background(), billingcli.UsageEventInput{
			TenantID:         ev.TenantID,
			AppID:            appID,
			Model:            ev.Model,
			PromptTokens:     ev.Usage.PromptTokens,
			CompletionTokens: ev.Usage.CompletionTokens,
		})
	}
}

// DiscardSink returns a Sink that ignores events (tests).
func DiscardSink() appmetering.Sink {
	return func(domain.UsageEvent) {}
}
