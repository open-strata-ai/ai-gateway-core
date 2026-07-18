// Package domain holds source-independent value types and Port interfaces for
// ai-gateway-core. It has no external dependencies (DDD domain layer, DESIGN §3).
package domain

import "context"

// Role is a chat message author role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Capability enumerates model capabilities (DESIGN §3.4).
const (
	CapChat      = "chat"
	CapEmbedding = "embedding"
	CapRerank    = "rerank"
	CapVision    = "vision"
	CapAudio     = "audio"
)

// Model source (DESIGN §8).
const (
	SourceSelfHosted = "self_hosted"
	SourceThirdParty = "third_party"
)

// Health states (DESIGN §3.4).
const (
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthDown     = "down"
)

// Message is a single chat turn.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the normalized inbound chat request (LLMProvider SPI 1.0.0).
type ChatRequest struct {
	Model         string    `json:"model"`
	Messages      []Message `json:"messages"`
	Temperature   float32   `json:"temperature,omitempty"`
	MaxTokens     int       `json:"max_tokens,omitempty"`
	Stream        bool      `json:"stream"`
	TenantID      string    `json:"-"` // injected by middleware, never serialized upstream
	FallbackChain []string  `json:"-"` // from AgentSpec.model_binding.fallback_chain
	Capability    string    `json:"-"` // chat/embedding/rerank/vision/audio
}

// ChatResponse is the normalized non-streaming chat response.
type ChatResponse struct {
	Model        string     `json:"model"`
	Content      string     `json:"content"`
	FinishReason string     `json:"finish_reason"`
	Usage        TokenUsage `json:"usage"`
	RoutedFrom   string     `json:"-"` // preferred model before the hit, for diagnostics
}

// TokenUsage reports token accounting for a call.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatChunk is a single SSE streaming fragment.
type ChatChunk struct {
	Delta string     `json:"delta"`
	Usage TokenUsage `json:"usage,omitempty"`
	Done  bool       `json:"done"`
}

// EmbedRequest / EmbedResponse (R1, /v1/embeddings).
type EmbedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	TenantID string   `json:"-"`
}

type EmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
	Usage      TokenUsage  `json:"usage"`
}

// RerankRequest / RerankResponse (R1, /v1/rerank).
type RerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TenantID  string   `json:"-"`
}

type RerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

type RerankResponse struct {
	Model   string         `json:"model"`
	Results []RerankResult `json:"results"`
	Usage   TokenUsage     `json:"usage"`
}

// HealthStatus is a provider health snapshot.
type HealthStatus struct {
	State   string `json:"state"` // healthy/degraded/down
	Message string `json:"message,omitempty"`
}

// ProviderMeta identifies a provider adapter instance (LLMProvider.Describe).
type ProviderMeta struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Capability string `json:"capability"`
	Source     string `json:"source"` // self_hosted / third_party
	ModelID    string `json:"model_id"`
}

// RateLimit is the per-tenant limit carried on a model card.
type RateLimit struct {
	QPSPerTenant int `json:"qps_per_tenant"`
	TPMPerTenant int `json:"tpm_per_tenant"` // tokens per minute
}

// ModelCard describes a routable model (DESIGN §3.4 / §8).
type ModelCard struct {
	ModelID       string    `json:"model_id"`
	Source        string    `json:"source"`
	Capability    string    `json:"capability"`
	ContextWindow int       `json:"context_window"`
	PriceIn       float64   `json:"price_in"`
	PriceOut      float64   `json:"price_out"`
	LatencySLA    int       `json:"latency_sla_ms"`
	TPS           int       `json:"tps"`
	RateLimit     RateLimit `json:"rate_limit"`
	Health        string    `json:"health"`
	TenantAccess  []string  `json:"tenant_access"` // whitelist; empty = all tenants
}

// AllowsTenant reports whether the card is visible to tenantID (empty list = all).
func (c ModelCard) AllowsTenant(tenantID string) bool {
	if len(c.TenantAccess) == 0 {
		return true
	}
	for _, t := range c.TenantAccess {
		if t == tenantID {
			return true
		}
	}
	return false
}

// RouteDecision is the outcome of ModelRouter.Resolve (DESIGN §3.3).
type RouteDecision struct {
	Preferred     string   // model_id
	FallbackChain []string // downgrade chain, priority order
	Reason        string   // cost/quota/latency/capability
}

// AuditEntry is an immutable audit record (R11, append-only).
type AuditEntry struct {
	TenantID  string
	Action    string
	Model     string
	Outcome   string // allow/reject/error
	Detail    string
	UnixNanos int64
}

// UsageEvent is an original metering datum reported asynchronously (R10).
type UsageEvent struct {
	TenantID   string
	Model      string
	Usage      TokenUsage
	LatencyMs  int64
	Success    bool
	UnixNanos  int64
}

// Ensure context is referenced by this package (used across ports.go).
var _ = context.Background
