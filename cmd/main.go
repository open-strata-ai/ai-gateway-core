// Command ai-gateway-core boots the gateway. Dependency wiring is done by hand
// here (the offline stand-in for Wire compile-time DI, DESIGN §1.4). Production
// swaps in Hertz/go-zero on the hot path and real Redis/PostgreSQL/Vault adapters.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/application/breaker"
	"github.com/open-strata-ai/ai-gateway-core/application/chat"
	appmetering "github.com/open-strata-ai/ai-gateway-core/application/metering"
	"github.com/open-strata-ai/ai-gateway-core/application/ratelimit"
	"github.com/open-strata-ai/ai-gateway-core/application/riskcontrol"
	"github.com/open-strata-ai/ai-gateway-core/application/routing"
	"github.com/open-strata-ai/ai-gateway-core/application/security"
	"github.com/open-strata-ai/ai-gateway-core/application/session"
	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/audit"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/auth"
	billingcli "github.com/open-strata-ai/ai-gateway-core/infrastructure/billing"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/cache"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/catalog"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/config"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/metering"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/provider"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/repository/memory"
	pgrepo "github.com/open-strata-ai/ai-gateway-core/infrastructure/repository/postgres"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/scanner"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/storage"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/tracing"
	httpapi "github.com/open-strata-ai/ai-gateway-core/interfaces/http"
)

func main() {
	cfg, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if addr := os.Getenv("ADDR"); addr != "" {
		cfg.Gateway.Listen = addr
	}
	h, closeFn := Bootstrap(cfg)
	defer closeFn()

	log.Printf("ai-gateway-core listening on %s", cfg.Gateway.Listen)
	srv := &http.Server{
		Addr:              cfg.Gateway.Listen,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// Bootstrap assembles the full object graph from config and returns the HTTP
// handler plus a cleanup function. It is exported so tests can reuse the wiring.
func Bootstrap(cfg config.Config) (*httpapi.Handler, func()) {
	pgDSN := os.Getenv("DATABASE_URL")
	var cat domain.ModelCatalog
	if pgDSN != "" {
		pgcat, err := catalog.NewPostgres(pgDSN)
		if err != nil {
			log.Printf("WARN: falling back to in-memory catalog (%v)", err)
			cat = catalog.NewWithCards(defaultCards()...)
	} else {
		cat = pgcat
		// Idempotently ensure the seeded default catalog (incl. the qwen3.7
		// family) is present. ON CONFLICT DO UPDATE makes re-seeding every boot
		// safe and guarantees new seed cards (RC-5) appear.
		for _, card := range defaultCards() {
			cat.Upsert(card)
		}
	}
	} else {
		cat = catalog.NewWithCards(defaultCards()...)
	}

	reg := provider.NewRegistry()
	for _, card := range defaultCards() {
		kind := provider.KindOpenAI
		switch {
		case card.Source == domain.SourceSelfHosted:
			kind = provider.KindSelfHosted
		case containsCI(card.ModelID, "qwen"):
			kind = provider.KindQwen
		case containsCI(card.ModelID, "claude"):
			kind = provider.KindClaude
		}
		// Use a real upstream transport when credentials are configured
		// (OPENAI_API_KEY / DASHSCOPE_API_KEY / ANTHROPIC_API_KEY); otherwise
		// New() falls back to the deterministic echo stub for offline use.
		transport := provider.TransportFromEnv(kind, card.ModelID)
		if transport != nil {
			log.Printf("provider %s (%s): real upstream transport enabled", card.ModelID, kind)
		} else {
			log.Printf("provider %s (%s): echo stub (no API key configured)", card.ModelID, kind)
		}
		reg.Register(card.ModelID, provider.New(provider.Config{
			Kind: kind, ModelID: card.ModelID, Source: card.Source,
			Transport: transport,
		}))
	}

	router := routing.New(cat, routing.Config{
		DefaultModel:  cfg.ModelRouting.Default,
		FallbackChain: cfg.ModelRouting.FallbackChain,
		CostAware:     cfg.ModelRouting.CostAware,
	})
	limiter := ratelimit.New(ratelimit.Config{GlobalQPS: cfg.RateLimit.GlobalQPS})
	brk := breaker.New(breaker.Config{
		ErrorThreshold: cfg.CircuitBreaker.ErrorThreshold,
		Cooldown:       time.Duration(cfg.CircuitBreaker.CooldownMs) * time.Millisecond,
	})
	risk := riskcontrol.New(riskcontrol.Config{PIIScan: cfg.Egress.PIIScan})
	c := cache.New(cfg.Cache.Enabled)
	var aud domain.AuditRecorder
	if pgDSN != "" {
		pgaud, err := audit.NewPostgres(pgDSN)
		if err != nil {
			log.Printf("WARN: falling back to in-memory audit (%v)", err)
			aud = audit.New()
		} else {
			aud = pgaud
		}
	} else {
		aud = audit.New()
	}
	trc := tracing.New(false)
	// Single billing ACL client: used both for the portal /usage BFF and for
	// pushing real token usage so the tenant cost dashboard is not always zero.
	billingClient := billingcli.NewFromEnv()
	rep := appmetering.New(1024, metering.BillingSink(billingClient, "ai-gateway-core"))

	chatSvc := chat.New(chat.Deps{
		Router:    router,
		Catalog:   cat,
		Limiter:   limiter,
		Breaker:   brk,
		Risk:      risk,
		Cache:     c,
		Providers: reg,
		Metering:  rep,
		Audit:     aud,
		Tracer:    trc,
	}, chat.Config{DenyEgressTenants: cfg.Egress.DenyEgressTenants})

	authPort := auth.New("local")

	// Batch B1: session / file-upload / content-security wiring (DESIGN §1.2).
	// Batch E2: prefer MinIO (S3) object storage when configured; otherwise
	// fall back to the in-memory stand-in for DEV/offline.
	sec := security.New(scanner.New(scanner.Config{PIIScan: cfg.Egress.PIIScan}))
	// Batch B2: agent + session repositories are PostgreSQL-backed when
	// DATABASE_URL is configured; otherwise the offline in-memory stand-ins
	// are used. This is the fix for EU-05 authoring / EU-04 chat history not
	// surviving a gateway restart (DESIGN §8).
	var sessRepo domain.SessionRepository = memory.NewSessionRepository()
	if pgDSN != "" {
		if pgs, err := pgrepo.NewSessionRepository(pgDSN); err != nil {
			log.Printf("WARN: session repo falls back to in-memory (%v)", err)
		} else {
			sessRepo = pgs
		}
	}
	var agentRepo domain.AgentRepository = memory.NewAgentRepository()
	if pgDSN != "" {
		if pga, err := pgrepo.NewAgentRepository(pgDSN); err != nil {
			log.Printf("WARN: agent repo falls back to in-memory (%v)", err)
		} else {
			agentRepo = pga
		}
	}
	agentCat := catalog.NewAgentInMemory()
	var fileStore domain.FileStoragePort = storage.NewMemory()
	if m := storage.NewMinIOFromEnv(); m != nil {
		fileStore = m
	}
	sessionSvc := session.New(session.Deps{
		Chat:      chatSvc,
		Security:  sec,
		Storage:   fileStore,
		Sessions:  sessRepo,
		Agents:    agentCat,
		AgentRepo: agentRepo,
		Tracer:    trc,
	})

	handler := httpapi.New(chatSvc, cat, authPort, sessionSvc, agentCat, agentRepo)
	// BFF: portal GET /usage → billing-service cost/budget (ACL). If billing is
	// down the handler returns 502 and the portal degrades to a static fallback.
	handler.SetUsageReporter(usageAdapter{c: billingClient})
	return handler, func() { rep.Close() }
}

// usageAdapter converts the billing ACL client's Metrics into the HTTP layer's
// UsageMetrics contract.
type usageAdapter struct{ c *billingcli.Client }

func (a usageAdapter) Usage(ctx context.Context, tenantID string) (*httpapi.UsageMetrics, error) {
	// TokenUsed comes from the gateway's live cumulative counter (real, per-session
	// chat usage). It must never be blanked by a billing-side gap.
	promptTok, completionTok := metering.TokenTotals(tenantID)
	m := &httpapi.UsageMetrics{
		TokenUsed: float64(promptTok + completionTok),
		Source:    "gateway",
	}
	// Cost/budget are best-effort: billing may be down or lack a price rule
	// for the dimension (PRICE_RULE_NOT_FOUND). We surface whatever billing
	// returns but never fail the whole /usage call because of it.
	if bm, err := a.c.Usage(ctx, tenantID); err == nil {
		m.CostBudget = bm.CostBudget
		m.CostActual = bm.CostActual
		m.QPSQuota = bm.QPSQuota
		m.QPSCurrent = bm.QPSCurrent
		m.VectorQuota = bm.VectorQuota
		m.VectorUsed = bm.VectorUsed
		m.Source = bm.Source
	}
	return m, nil
}

func defaultCards() []domain.ModelCard {
	return []domain.ModelCard{
		{
			ModelID: "cloud-qwen-max", Source: domain.SourceThirdParty, Capability: domain.CapChat,
			ContextWindow: 32000, PriceIn: 2.0, PriceOut: 6.0, LatencySLA: 1800, TPS: 40,
			RateLimit: domain.RateLimit{QPSPerTenant: 20, TPMPerTenant: 200000}, Health: domain.HealthHealthy,
		},
		{
			ModelID: "cloud-gpt-4o", Source: domain.SourceThirdParty, Capability: domain.CapChat,
			ContextWindow: 128000, PriceIn: 5.0, PriceOut: 15.0, LatencySLA: 1800, TPS: 30,
			RateLimit: domain.RateLimit{QPSPerTenant: 10, TPMPerTenant: 150000}, Health: domain.HealthHealthy,
		},
		{
			ModelID: "local-qwen-72b", Source: domain.SourceSelfHosted, Capability: domain.CapChat,
			ContextWindow: 32000, PriceIn: 0.5, PriceOut: 0.5, LatencySLA: 2000, TPS: 60,
			RateLimit: domain.RateLimit{QPSPerTenant: 50, TPMPerTenant: 500000}, Health: domain.HealthHealthy,
		},
		{
			ModelID: "cloud-bge-m3", Source: domain.SourceThirdParty, Capability: domain.CapEmbedding,
			ContextWindow: 8192, PriceIn: 0.1, PriceOut: 0, LatencySLA: 500, TPS: 100,
			RateLimit: domain.RateLimit{QPSPerTenant: 50, TPMPerTenant: 1000000}, Health: domain.HealthHealthy,
		},
		// Seeded model catalog (RC-5): explicit Qwen 3.7 family so the portal
		// /models page is never blank. These resolve to the Qwen provider kind
		// (echo stub unless DASHSCOPE_API_KEY is configured).
		{
			ModelID: "qwen3.7-max-preview", Source: domain.SourceThirdParty, Capability: domain.CapChat,
			ContextWindow: 128000, PriceIn: 3.0, PriceOut: 9.0, LatencySLA: 1500, TPS: 45,
			RateLimit: domain.RateLimit{QPSPerTenant: 20, TPMPerTenant: 200000}, Health: domain.HealthHealthy,
		},
		{
			ModelID: "qwen3.7-max-2026-05-20", Source: domain.SourceThirdParty, Capability: domain.CapChat,
			ContextWindow: 128000, PriceIn: 3.0, PriceOut: 9.0, LatencySLA: 1500, TPS: 45,
			RateLimit: domain.RateLimit{QPSPerTenant: 20, TPMPerTenant: 200000}, Health: domain.HealthHealthy,
		},
		{
			ModelID: "qwen3.7-plus", Source: domain.SourceThirdParty, Capability: domain.CapChat,
			ContextWindow: 64000, PriceIn: 1.0, PriceOut: 3.0, LatencySLA: 1200, TPS: 55,
			RateLimit: domain.RateLimit{QPSPerTenant: 30, TPMPerTenant: 300000}, Health: domain.HealthHealthy,
		},
	}
}

func containsCI(s, sub string) bool {
	return len(s) >= len(sub) && indexCI(s, sub) >= 0
}

func indexCI(s, sub string) int {
	ls, lsub := len(s), len(sub)
	for i := 0; i+lsub <= ls; i++ {
		if eqCI(s[i:i+lsub], sub) {
			return i
		}
	}
	return -1
}

func eqCI(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
