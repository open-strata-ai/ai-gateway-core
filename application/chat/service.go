// Package chat is the central use case (DESIGN §4 request path): risk control →
// cache → route → quota → provider call with fallback + circuit breaker →
// metering + audit.
package chat

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// ProviderRegistry resolves the LLMProvider adapter bound to a model_id.
type ProviderRegistry interface {
	ForModel(modelID string) (domain.LLMProvider, bool)
}

// Deps are the collaborators of the chat Service (constructor-injected).
type Deps struct {
	Router    domain.ModelRouter
	Catalog   domain.ModelCatalog
	Limiter   domain.RateLimiter
	Breaker   domain.CircuitBreaker
	Risk      domain.RiskController
	Cache     domain.Cache
	Providers ProviderRegistry
	Metering  domain.MeteringPort
	Audit     domain.AuditRecorder
	Tracer    domain.TracingPort
}

// Config holds tunables for the Service.
type Config struct {
	DenyEgressTenants []string
	MaxAttempts       int
}

// Service orchestrates a single chat completion.
type Service struct {
	d          Deps
	denyEgress map[string]bool
	maxAtt     int
	now        func() time.Time
}

// New builds a Service.
func New(d Deps, cfg Config) *Service {
	deny := map[string]bool{}
	for _, t := range cfg.DenyEgressTenants {
		deny[t] = true
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	return &Service{d: d, denyEgress: deny, maxAtt: cfg.MaxAttempts, now: time.Now}
}

// Complete runs the full pipeline and returns a normalized ChatResponse.
func (s *Service) Complete(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
	if s.d.Tracer != nil {
		var end func()
		ctx, end = s.d.Tracer.Start(ctx, "chat.complete")
		defer end()
	}

	// 1) basic risk control (R8/R9, §5.5)
	denied := s.denyEgress[req.TenantID]
	if s.d.Risk != nil {
		var ok bool
		var reason string
		req, ok, reason = s.d.Risk.Inspect(req, denied)
		if !ok {
			s.audit(req.TenantID, "risk", req.Model, "reject", reason)
			return nil, domain.NewError(domain.ErrRiskRejected, http.StatusBadRequest, reason)
		}
	}

	// 2) optional semantic/exact cache (R7, §5.4)
	cacheKey := ""
	if s.d.Cache != nil && s.d.Cache.Enabled() {
		cacheKey = cacheKeyOf(req)
		if resp, hit := s.d.Cache.Get(ctx, cacheKey); hit {
			s.audit(req.TenantID, "cache", resp.Model, "allow", "hit")
			return resp, nil
		}
	}

	// 3) routing decision (R2/R5, §5.1)
	decision := s.d.Router.Resolve(ctx, req)
	if decision.Preferred == "" {
		s.audit(req.TenantID, "route", req.Model, "reject", decision.Reason)
		return nil, domain.NewError(domain.ErrModelNotFound, http.StatusNotFound,
			"no viable model for request ("+decision.Reason+")")
	}
	chain := append([]string{decision.Preferred}, decision.FallbackChain...)

	estTokens := estimateTokens(req)
	attempts := 0
	rateLimitedAll := true
	var lastErr error

	for _, modelID := range chain {
		if attempts >= s.maxAtt {
			break
		}

		card, hasCard := s.d.Catalog.Get(modelID)

		// 4) tenant×model quota / rate limit (R3, §5.3)
		if s.d.Limiter != nil && hasCard {
			if allowed, why := s.d.Limiter.Allow(req.TenantID, modelID, estTokens, card.RateLimit); !allowed {
				s.audit(req.TenantID, "ratelimit", modelID, "reject", why)
				lastErr = domain.NewError(domain.ErrRateLimited, http.StatusTooManyRequests, why)
				continue // downgrade to next in chain
			}
		}
		rateLimitedAll = false

		// 5) circuit breaker per provider (R4, §5.2)
		if s.d.Breaker != nil && !s.d.Breaker.Allow(modelID) {
			s.audit(req.TenantID, "breaker", modelID, "reject", "circuit_open")
			continue
		}

		provider, found := s.d.Providers.ForModel(modelID)
		if !found {
			continue
		}

		attempts++
		call := req
		call.Model = modelID
		start := s.now()
		resp, err := provider.Chat(ctx, call)
		latency := s.now().Sub(start).Milliseconds()
		success := err == nil && resp != nil

		if s.d.Breaker != nil {
			s.d.Breaker.Record(modelID, success)
		}

		usage := domain.TokenUsage{}
		if success {
			usage = resp.Usage
		}
		s.meter(req.TenantID, modelID, usage, latency, success)

		if !success {
			lastErr = err
			s.audit(req.TenantID, "upstream", modelID, "error", errString(err))
			continue // fallback to next
		}

		if modelID != decision.Preferred {
			resp.RoutedFrom = decision.Preferred
		}
		s.audit(req.TenantID, "chat", modelID, "allow", "ok")
		if cacheKey != "" {
			s.d.Cache.Set(ctx, cacheKey, resp)
		}
		return resp, nil
	}

	if rateLimitedAll && lastErr != nil {
		return nil, lastErr // 429
	}
	return nil, domain.NewError(domain.ErrAllProvidersDown, http.StatusBadGateway,
		"all providers failed for request")
}

func (s *Service) audit(tenant, action, model, outcome, detail string) {
	if s.d.Audit == nil {
		return
	}
	s.d.Audit.Append(domain.AuditEntry{
		TenantID:  tenant,
		Action:    action,
		Model:     model,
		Outcome:   outcome,
		Detail:    detail,
		UnixNanos: s.now().UnixNano(),
	})
}

func (s *Service) meter(tenant, model string, u domain.TokenUsage, latencyMs int64, success bool) {
	if s.d.Metering == nil {
		return
	}
	s.d.Metering.Report(domain.UsageEvent{
		TenantID:  tenant,
		Model:     model,
		Usage:     u,
		LatencyMs: latencyMs,
		Success:   success,
		UnixNanos: s.now().UnixNano(),
	})
}

func estimateTokens(req domain.ChatRequest) int {
	chars := 0
	for _, m := range req.Messages {
		chars += len(m.Content)
	}
	est := chars / 4 // rough heuristic
	if req.MaxTokens > 0 {
		est += req.MaxTokens
	}
	if est <= 0 {
		est = 1
	}
	return est
}

func cacheKeyOf(req domain.ChatRequest) string {
	var b strings.Builder
	b.WriteString(req.TenantID)
	b.WriteString("|")
	b.WriteString(req.Capability)
	for _, m := range req.Messages {
		b.WriteString("|")
		b.WriteString(string(m.Role))
		b.WriteString(":")
		b.WriteString(m.Content)
	}
	return b.String()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
