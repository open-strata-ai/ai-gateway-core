package domain

import "context"

// LLMProvider is the source-independent model capability SPI (interface_versions
// .LLMProvider = 1.0.0). Each Adapter normalizes one upstream protocol (DESIGN §3.2).
type LLMProvider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
	Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error)
	Health(ctx context.Context) HealthStatus
	Describe() ProviderMeta
}

// ModelRouter selects the provider/model for a request plus a downgrade order
// (DESIGN §3.3 / §5.1).
type ModelRouter interface {
	Resolve(ctx context.Context, req ChatRequest) RouteDecision
}

// ModelCatalog is the model card registry (DESIGN §3.4).
type ModelCatalog interface {
	Get(modelID string) (ModelCard, bool)
	ListByCapability(capability string, tenantID string) []ModelCard
	UpdateHealth(modelID string, h HealthStatus)
	Upsert(card ModelCard)
}

// RateLimiter enforces tenant×model QPS + TPM quotas with a global backstop
// (DESIGN §5.3, resolution R-003: per-tenant + global ceiling).
type RateLimiter interface {
	// Allow reserves one request (and estimated tokens) for tenant×model.
	// It returns false with a reason when the tenant or global ceiling is hit.
	Allow(tenantID, modelID string, estTokens int, limit RateLimit) (bool, string)
}

// CircuitBreaker guards a single provider instance (DESIGN §5.2).
type CircuitBreaker interface {
	// Allow reports whether a call to key may proceed (Closed/HalfOpen) or is
	// short-circuited (Open).
	Allow(key string) bool
	// Record feeds the outcome of a completed call back into the state machine.
	Record(key string, success bool)
	// State returns the current state for key ("closed"/"open"/"half_open").
	State(key string) string
}

// Cache is the optional semantic/exact response cache (DESIGN §5.4, R7).
type Cache interface {
	Enabled() bool
	Get(ctx context.Context, key string) (*ChatResponse, bool)
	Set(ctx context.Context, key string, resp *ChatResponse)
}

// AuthPort resolves a tenant context from an inbound request (Auth SPI 1.0.0).
type AuthPort interface {
	// Resolve maps a bearer token / tenant header to a tenant id and role.
	Resolve(ctx context.Context, bearer, tenantHeader string) (tenantID, role string, err error)
}

// TracingPort is the observability sink (Tracing SPI 1.0.0, OTel/Langfuse).
type TracingPort interface {
	Start(ctx context.Context, span string) (context.Context, func())
	Warn(ctx context.Context, stage, msg string)
}

// MeteringPort receives original usage events asynchronously (R10).
type MeteringPort interface {
	Report(ev UsageEvent)
}

// AuditRecorder appends immutable audit rows (R11).
type AuditRecorder interface {
	Append(e AuditEntry)
}

// RiskController runs basic risk control before the upstream call (R8/R9, §5.5).
type RiskController interface {
	// Inspect scans the request; ok=false means reject (injection). The returned
	// request may have PII masked. denyEgress forces self-hosted-only routing.
	Inspect(req ChatRequest, denyEgress bool) (out ChatRequest, ok bool, reason string)
}

// ContentSecurityService scans payloads for PII / injection before egress and
// after ingress (Batch B1, EU-03 / EU-04 / DV-14, DESIGN §1.2, §4.2).
type ContentSecurityService interface {
	ScanInput(ctx context.Context, msg *Message) (*ScanResult, error)
	ScanOutput(ctx context.Context, msg *Message) (*ScanResult, error)
	ScanFile(ctx context.Context, file *FileRef) (*ScanResult, error)
}

// FileStoragePort abstracts object storage for file uploads (EU-03). The default
// adapter is MinIO; a local-disk adapter is used for DEV/offline (DESIGN §6.2).
type FileStoragePort interface {
	Upload(ctx context.Context, file *FileUploadRequest) (*FileRef, error)
	Download(ctx context.Context, ref *FileRef) ([]byte, error)
	ScanContent(ctx context.Context, ref *FileRef) (*ScanResult, error)
}

// MemoryPort abstracts working + long-term memory (Redis + Qdrant/Milvus).
type MemoryPort interface {
	GetWorkingMemory(ctx context.Context, sessionID string) ([]Message, error)
	UpdateWorkingMemory(ctx context.Context, sessionID string, msgs []Message) error
	SearchLongTerm(ctx context.Context, query string, topK int) ([]MemoryEntry, error)
}

// MemoryEntry is a single long-term memory record returned by SearchLongTerm.
type MemoryEntry struct {
	SessionID string  `json:"session_id"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
}

// ToolRegistryPort is the gateway-side view of ai-tool-registry (DV-14).
type ToolRegistryPort interface {
	ListTools(ctx context.Context, tenantID string) ([]ToolSpec, error)
	ExecuteTool(ctx context.Context, toolID string, args map[string]any) (*ToolResult, error)
}

// ToolSpec describes a registered tool (DV-14).
type ToolSpec struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Protocol    string `json:"protocol"` // stdio|sse|http (MCP)
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	ToolID  string `json:"tool_id"`
	Output  string `json:"output"`
	Ok      bool   `json:"ok"`
	ErrText string `json:"error,omitempty"`
}

// AgentCatalog is the gateway-side read-only projection of published Agents
// (EU-05). The authoritative source is ai-platform-api; the gateway caches a
// lightweight list used by ListAvailableAgents.
type AgentCatalog interface {
	ListAvailable(tenantID string) []AgentSummary
}
