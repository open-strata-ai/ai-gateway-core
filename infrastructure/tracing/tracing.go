// Package tracing is a domain.TracingPort implementation. Offline it is a no-op
// (optionally logging WARN over-budget spans); production wires OTel/Langfuse
// (Tracing SPI 1.0.0, DESIGN §12).
package tracing

import (
	"context"
	"log"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Noop discards spans; Verbose logs WARN over-budget events.
type Noop struct {
	Verbose bool
}

// New builds a tracer.
func New(verbose bool) *Noop { return &Noop{Verbose: verbose} }

func (n *Noop) Start(ctx context.Context, span string) (context.Context, func()) {
	return ctx, func() {}
}

func (n *Noop) Warn(ctx context.Context, stage, msg string) {
	if n.Verbose {
		log.Printf("WARN trace stage=%s %s", stage, msg)
	}
}

var _ domain.TracingPort = (*Noop)(nil)
