// Package provider holds LLMProvider adapters. Each adapter is an Anti-Corruption
// Layer (ACL, DESIGN §6.3) that normalizes one upstream protocol into the domain
// contract. For offline builds the network call is replaced by a deterministic
// stub; production swaps in real HTTP/gRPC clients (OpenAI/Claude/Qwen, vLLM/TGI).
package provider

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Kind identifies an upstream protocol family.
type Kind string

const (
	KindOpenAI     Kind = "openai"
	KindClaude     Kind = "claude"
	KindQwen       Kind = "qwen"
	KindSelfHosted Kind = "self_hosted" // vLLM/TGI, Phase 4 (stub only)
)

// Transport performs the actual upstream call. In production this is an HTTP/gRPC
// client; tests inject a stub. It receives an already-normalized request.
type Transport func(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error)

// Adapter implements domain.LLMProvider for a single model instance.
type Adapter struct {
	kind      Kind
	modelID   string
	source    string
	transport Transport
	health    domain.HealthStatus
	mu        sync.RWMutex
}

// Config configures an Adapter.
type Config struct {
	Kind    Kind
	ModelID string
	Source  string // domain.SourceThirdParty / SourceSelfHosted
	// Transport is optional; when nil an echo stub is used (offline).
	Transport Transport
}

// New builds an Adapter.
func New(cfg Config) *Adapter {
	a := &Adapter{
		kind:      cfg.Kind,
		modelID:   cfg.ModelID,
		source:    cfg.Source,
		transport: cfg.Transport,
		health:    domain.HealthStatus{State: domain.HealthHealthy},
	}
	if a.transport == nil {
		a.transport = echoStub(cfg.ModelID)
	}
	if a.source == "" {
		if cfg.Kind == KindSelfHosted {
			a.source = domain.SourceSelfHosted
		} else {
			a.source = domain.SourceThirdParty
		}
	}
	return a
}

// Chat normalizes the request per protocol family (ACL) and calls the transport.
func (a *Adapter) Chat(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
	norm := a.normalize(req)
	return a.transport(ctx, norm)
}

// ChatStream returns a single-shot stream over the non-streaming result (the
// stub does not fragment; a real adapter parses SSE, DESIGN §6.3).
func (a *Adapter) ChatStream(ctx context.Context, req domain.ChatRequest) (<-chan domain.ChatChunk, error) {
	resp, err := a.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan domain.ChatChunk, 2)
	ch <- domain.ChatChunk{Delta: resp.Content, Done: false}
	ch <- domain.ChatChunk{Usage: resp.Usage, Done: true}
	close(ch)
	return ch, nil
}

func (a *Adapter) Embed(ctx context.Context, req domain.EmbedRequest) (*domain.EmbedResponse, error) {
	// Deterministic offline embedding: fixed-width hashed vector.
	out := make([][]float32, len(req.Input))
	for i, in := range req.Input {
		out[i] = hashEmbed(in, 8)
	}
	return &domain.EmbedResponse{Model: a.modelID, Embeddings: out,
		Usage: domain.TokenUsage{PromptTokens: totalLen(req.Input) / 4}}, nil
}

func (a *Adapter) Rerank(ctx context.Context, req domain.RerankRequest) (*domain.RerankResponse, error) {
	results := make([]domain.RerankResult, len(req.Documents))
	for i, d := range req.Documents {
		results[i] = domain.RerankResult{Index: i, Score: overlapScore(req.Query, d)}
	}
	return &domain.RerankResponse{Model: a.modelID, Results: results}, nil
}

func (a *Adapter) Health(ctx context.Context) domain.HealthStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.health
}

// SetHealth lets health probes update the adapter (DESIGN §6.4).
func (a *Adapter) SetHealth(h domain.HealthStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.health = h
}

func (a *Adapter) Describe() domain.ProviderMeta {
	return domain.ProviderMeta{
		Name:       string(a.kind),
		Version:    "1.0.0",
		Capability: domain.CapChat,
		Source:     a.source,
		ModelID:    a.modelID,
	}
}

// normalize applies per-protocol ACL adjustments (DESIGN §6.3).
func (a *Adapter) normalize(req domain.ChatRequest) domain.ChatRequest {
	switch a.kind {
	case KindClaude:
		// Claude: hoist a leading system message; internal []Message is preserved
		// but we ensure the system prompt is the first element.
		req.Messages = hoistSystem(req.Messages)
	case KindQwen:
		// Qwen/DashScope: parameter naming handled at transport; nothing structural.
	default:
		// OpenAI baseline: direct mapping.
	}
	req.Model = a.modelID
	return req
}

// errUpstream is a sentinel used by the failing stub for tests.
var errUpstream = errors.New("stub upstream failure")

// FailingStub is a Transport that always errors (for fallback/breaker tests).
func FailingStub() Transport {
	return func(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
		return nil, errUpstream
	}
}

func echoStub(modelID string) Transport {
	return func(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
		last := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == domain.RoleUser {
				last = req.Messages[i].Content
				break
			}
		}
		content := "echo: " + last
		pt := totalMsgLen(req.Messages) / 4
		ct := len(content) / 4
		return &domain.ChatResponse{
			Model:        modelID,
			Content:      content,
			FinishReason: "stop",
			Usage:        domain.TokenUsage{PromptTokens: pt, CompletionTokens: ct, TotalTokens: pt + ct},
		}, nil
	}
}

func hoistSystem(msgs []domain.Message) []domain.Message {
	var sys []domain.Message
	var rest []domain.Message
	for _, m := range msgs {
		if m.Role == domain.RoleSystem {
			sys = append(sys, m)
		} else {
			rest = append(rest, m)
		}
	}
	return append(sys, rest...)
}

func hashEmbed(s string, dim int) []float32 {
	v := make([]float32, dim)
	for i, r := range s {
		v[i%dim] += float32((int(r)%17)-8) / 8.0
	}
	return v
}

func overlapScore(q, d string) float64 {
	qs := strings.Fields(strings.ToLower(q))
	set := map[string]bool{}
	for _, w := range qs {
		set[w] = true
	}
	if len(set) == 0 {
		return 0
	}
	hits := 0
	for _, w := range strings.Fields(strings.ToLower(d)) {
		if set[w] {
			hits++
		}
	}
	return float64(hits) / float64(len(set))
}

func totalLen(ss []string) int {
	n := 0
	for _, s := range ss {
		n += len(s)
	}
	return n
}

func totalMsgLen(ms []domain.Message) int {
	n := 0
	for _, m := range ms {
		n += len(m.Content)
	}
	return n
}

var _ domain.LLMProvider = (*Adapter)(nil)
