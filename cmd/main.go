// Command ai-gateway-core boots the gateway. Dependency wiring is done by hand
// here (the offline stand-in for Wire compile-time DI, DESIGN §1.4). Production
// swaps in Hertz/go-zero on the hot path and real Redis/PostgreSQL/Vault adapters.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/open-strata-ai/ai-gateway-core/application/breaker"
	"github.com/open-strata-ai/ai-gateway-core/application/chat"
	"github.com/open-strata-ai/ai-gateway-core/application/ratelimit"
	"github.com/open-strata-ai/ai-gateway-core/application/riskcontrol"
	"github.com/open-strata-ai/ai-gateway-core/application/routing"
	"github.com/open-strata-ai/ai-gateway-core/application/security"
	"github.com/open-strata-ai/ai-gateway-core/application/session"
	"github.com/open-strata-ai/ai-gateway-core/domain"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/repository/memory"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/scanner"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/storage"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/audit"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/auth"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/cache"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/catalog"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/config"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/metering"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/provider"
	"github.com/open-strata-ai/ai-gateway-core/infrastructure/tracing"
	httpapi "github.com/open-strata-ai/ai-gateway-core/interfaces/http"
	appmetering "github.com/open-strata-ai/ai-gateway-core/application/metering"
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
			// Seed default cards if catalog is empty
			if cards := cat.ListByCapability("", ""); len(cards) == 0 {
				for _, card := range defaultCards() {
					cat.Upsert(card)
				}
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
		reg.Register(card.ModelID, provider.New(provider.Config{
			Kind: kind, ModelID: card.ModelID, Source: card.Source,
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
	rep := appmetering.New(1024, metering.LogSink())

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
	sessRepo := memory.NewSessionRepository()
	agentCat := catalog.NewAgentInMemory()
	var fileStore domain.FileStoragePort = storage.NewMemory()
	if m := storage.NewMinIOFromEnv(); m != nil {
		fileStore = m
	}
	sessionSvc := session.New(session.Deps{
		Chat:     chatSvc,
		Security: sec,
		Storage:  fileStore,
		Sessions: sessRepo,
		Agents:   agentCat,
		Tracer:   trc,
	})

	handler := httpapi.New(chatSvc, cat, authPort, sessionSvc, agentCat)
	return handler, func() { rep.Close() }
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
