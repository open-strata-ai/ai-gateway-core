# ai-gateway-core · Architecture (Architecture Overview)

> **Excerpted from** `docs/DESIGN.md` §1 Positioning and Boundaries · §2 List of Responsibilities · §3 Core Interface · §6 Adapter
> **Language · Framework**: Go · Hertz/go-zero (hot path) + Gin + Cobra + Wire (compile-time DI)
> **Field**: runtime (model service layer/AI unified gateway)
> **optional**: false (core · core required)
> **Platform version**: v1.0.0

---

## §1 Positioning and Boundary (Scope)

### 1.1 Positioning in one sentence

`ai-gateway-core` is the **data plane entrance + model supply center** of OpenStrata, carrying the architecture §4.4.1 "AI Unified Gateway" and §4.4.4–4.4.6 "Model Supply System". It converges "calls to model capabilities" into a single, standard, manageable entrance, and completely shields model source differences (third-party API/self-hosted inference) from the upper layer (Agent engine, Portal, SDK, CLI).

### 1.2 Core Problems Solved

Converging "N model suppliers × M calling protocols × K types of governance requirements (current limit/downgrade/cost/audit)" into an **OpenAI-compatible, routable, circuit breakerable, measurable, and auditable unified plane**.

### 1.3 Requirement and Applicable Scenarios

- **Required**: core (one of the core minimum independent combinations, §4.4.1 / §10.2)
- **Minimum Scenario**: Even if you only connect to the third-party LLM API (Phases 1~3), this repository is enough to establish
- **Extended Scenario**: Phase 4 full file enables self-hosted `SelfHostedAdapter` (vLLM/TGI)
- **Cannot be closed**: This repository is the entrance to the data plane. After closing, all AI calls will become invalid.

### 1.4 Architecture role

| Dimensions | Description |
| --- | --- |
| **Level** | DDD four layers: `domain/` (Port interface) · `application/` (use case: routing/rate limiting/caching) · `infrastructure/` (adapter/Wire DI) · `interfaces/` (handler) |
| **Hot Path Framework** | Hertz (CloudWeGo) or go-zero, high concurrency and low GC |
| **Control Plane Framework** | Gin (Management API) |
| **DI solution** | Wire (compile-time dependency injection), zero reflection overhead |
| **Data plane entrance** | Higress (Wasm plug-in: JWT authentication, global flow restriction, audit header collection) → this repository handler |
| **Protocol Commitment** | Externally OpenAI-compatible, internally gRPC/HTTP |

### 1.5 Division of labor with other Go components

| Component | Relationship Type | Description |
| --- | --- | --- |
| `ai-tool-registry` | Request link concatenation | The gateway is responsible for the "model calling" data plane; tool registration/calling is carried by tool-registry via `ToolRegistry` SPI. The two are connected in series in the request link (Agent adjusts the gateway → gateway forwarding tool → tool then adjusts the model through the gateway), but the responsibilities do not overlap. |
| `ai-platform-api` | Control plane vs data plane | `ai-platform-api` (Java) is control plane orchestration + tenant/user/metering summary; the gateway is **hot path runtime**, which does not do cross-tenant settlement, only `tenant×model` real-time quota/rate limiting and original metering reporting. |
| `ai-sandbox-manager` | Independent data plane | Code execution (sandbox) is another data plane, carried by `ai-sandbox-manager` via `Sandbox` SPI (§4.3.3), the gateway does not execute code. |
| `ai-cli` | client/server | `aictl` is the caller and drives deployment through the OpenAI-compatible API/CLI subcommand exposed by this repository. |

### 1.6 Boundary constraints

| Constraints | Description |
| --- | --- |
| **No cross-tenant settlement** | Only real-time quotas are used, aggregate billing is handled by `ai-platform-api` + `ai-billing-service` |
| **No code execution** | Code execution is the responsibility of `ai-sandbox-manager` |
| **Does not manage tools** | Tool registration/calling is managed by `ai-tool-registry` |
| **No model training/fine-tuning** | This repository only does inference routing. For training, see `ai-provisioning-engine` |

---

## §2 Responsibilities List

### 2.1 Complete list of responsibilities

| # | Responsibilities | Required/Optional | Trigger conditions | Description |
| --- | --- | --- | --- | --- |
| R1 | **OpenAI-compatible API access** | core | Each model call request | `/v1/chat/completions`, `/v1/embeddings`, `/v1/rerank`, `/v1/models`, etc., normalized upstream difference (§4.4.1) |
| R2 | **Request routing/model selection** | core | `ModelRouter.Resolve` call | Press `default + fallback_chain` to select source, support capability matching (§4.4.5) |
| R3 | **Current Limitation and Quota** | core | After each request passes risk control | QPS/Token quota of `tenant×model`; supplier-level current limit (§4.4.5, §4.7.4) |
| R4 | **Downgrade/Failover** | core | Main model unhealthy/timeout/quota exceeded | Downgrade chain sequence switching; cooperate with circuit breaker (§4.4.5, §4.4.6) |
| R5 | **Cost-aware routing** | optional→core (default enabled) | `costAware: true` | High-frequency traffic is prioritized for self-hosting, long-tail/scarce filling third party (§4.4.5) |
| R6 | **ModelCatalog** | core | Query when routing decisions | Model card registration: Capacity/Context/Price/SLA/Health/Tenant Whitelist (§4.4.5) |
| R7 | **Semantic/Exact Cache** | optional (default off) | `cache.enabled: true` | Redis Vector Search semantic cache (§4.3.4) |
| R8 | **Key Escrow and Outbound Control** | core | Before calling third-party Provider | Provider API Key stores Vault/K8s Secret and is not exposed to tenants; PII desensitization (§4.4.6) |
| R9 | **Basic risk control (injection/PII/rate limiting)** | core | After the request enters the handler | Injection detection + PII scanning + token bucket rate limiting, enabled by default (§4.7.4) |
| R10 | **Metering instrumentation (original usage reporting)** | core | Each model call is completed | Token input/output, number of calls, delay, asynchronous reporting `ai-billing-service` (§4.7.2) |
| R11 | **OTel traces + immutable audit** | core | Full link for each request | Basic observability is enabled by default (§4.8) |

### 2.2 Responsibility classification

| Level | Responsibility Number | Quantity | Description |
| --- | --- | --- | --- |
| **core (cannot be closed)** | R1, R2, R3, R4, R6, R8, R9, R10, R11 | 9 | Gateway minimum feasible set |
| **Default on (configurable off)** | R5 | 1 | Cost-aware routing (`costAware: true`) |
| **Default off (optional)** | R7 | 1 | Semantic cache (`cache.enabled: false`) |

### 2.3 Responsibility boundaries

- **Not included**: model training, model evaluation, data annotation, user management, tenant settlement, code execution
- **Exclusive to this repository**: unified protocol access, multi-provider routing, real-time circuit breaker/downgrade, model-level cost awareness

---

## §3 Core interface and abstraction

### 3.1 Design principles

The domain layer (`domain/`) only defines **Port (interface)** and does not rely on specific Provider. All upstream differences converge at the Adapter layer (`infrastructure/`). SPI versions are aligned with `bom.yaml` `interface_versions`. This repository follows the DDD classic four-layer architecture (§15.5.2).

### 3.2 LLMProvider SPI（v1.0.0）

Source-independent model capability abstraction, Provider adapter unified contract.

```go
// ===== LLMProvider SPI（interface_versions.LLMProvider = 1.0.0）=====
package domain

type Role string
const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
)

type Message struct {
    Role    Role   `json:"role"`
    Content string `json:"content"`
}

type ChatRequest struct {
    Model         string    `json:"model"`             //Target model_id (can be overridden via routing)
    Messages      []Message `json:"messages"`
    Temperature   float32   `json:"temperature,omitempty"`
    MaxTokens     int       `json:"max_tokens,omitempty"`
    Stream        bool      `json:"stream"`            //SSE streaming
    TenantID      string    `json:"-"`                 //Injected by gateway middleware and not connected to the network
    FallbackChain []string  `json:"-"`                 // AgentSpec.model_binding.fallback_chain
    Capability    string    `json:"-"`                 // chat/embedding/rerank/vision/audio
}

type ChatResponse struct {
    Model        string     `json:"model"`             //The model_id of the actual hit
    Content      string     `json:"content"`
    FinishReason string     `json:"finish_reason"`
    Usage        TokenUsage `json:"usage"`
    RoutedFrom   string     `json:"-"`                 //Preferred model before hit (used for diagnostics)
}

type TokenUsage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}

type ChatChunk struct {
    Delta string     `json:"delta"`
    Usage TokenUsage `json:"usage,omitempty"`
    Done  bool       `json:"done"`
}

type LLMProvider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error)
    Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
    Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error)
    Health(ctx context.Context) HealthStatus
    Describe() ProviderMeta  // name/version/capability/source
}
```

### 3.3 ModelRouter Port

```go
//===== Model Router Port (Domain Layer) =====
type ModelRouter interface {
    //Resolve returns the provider instance that should be selected for this request + the downgrade order
    Resolve(ctx context.Context, req ChatRequest) RouteDecision
}

type RouteDecision struct {
    Preferred     string   // model_id
    FallbackChain []string //Downgrade chain model_id list (sorted by priority)
    Reason        string   // cost/quota/latency/capability
}
```

### 3.4 ModelCatalog Port

```go
//===== Model directory Port (domain layer) =====
type ModelCatalog interface {
    Get(modelID string) (ModelCard, bool)
    ListByCapability(cap string, tenantID string) []ModelCard
    UpdateHealth(modelID string, h HealthStatus)
}

//Model Card (§4.4.5 Field)
type ModelCard struct {
    ModelID       string    `json:"model_id"`
    Source        string    `json:"source"`          // self_hosted / third_party
    Capability    string    `json:"capability"`      // chat/embedding/rerank/vision/audio
    ContextWindow int       `json:"context_window"`
    PriceIn       float64   `json:"price_in"`        //Every 1M tokens (self-hosted based on internal conversion)
    PriceOut      float64   `json:"price_out"`
    LatencySLA    int       `json:"latency_sla_ms"`
    TPS           int       `json:"tps"`
    RateLimit     RateLimit `json:"rate_limit"`
    Health        string    `json:"health"`          // healthy/degraded/down
    TenantAccess  []string  `json:"tenant_access"`   //Whitelist; empty = all tenants
}

type RateLimit struct {
    QPSPerTenant int `json:"qps_per_tenant"`
    TPMPerTenant int `json:"tpm_per_tenant"` // tokens per minute
}
```

### 3.5 Delay budget constraint

| Processing Phase | Budget (p95) | Over-Budget Behavior |
| --- | --- | --- |
| Access layer + JWT authentication | ≤15ms | WARN trace |
| Basic risk control (injection/PII) | ≤10ms | WARN trace |
| Cache query (optional) | ≤5ms | If not hit, go to the normal link |
| Routing decision | ≤2ms | WARN trace |
| Quota/current limit (Redis Lua) | ≤3ms | WARN trace |
| SPI call (to third party) | ≤1800ms | Over-threshold trigger degradation |
| Metering/auditing | ≤5ms (asynchronous) | Does not block the main path |
| **End-to-end p95** | **≤2000ms** | Consistent with §4.4.5 degradation threshold |

### 3.6 Gateway SPI（v1.0.0）

`Gateway` SPI is exposed to the upper layer by the repository itself as an implementation. This repository does not rely on external `Gateway` instances - Higress is the data plane forwarding layer, not the SPI implementation.

---

## §6 Adapter and SPI Ecosystem

### 6.1 SPI complete port matrix

| SPI port | Version | Repository role | External component (bom.yaml) | Default | Alternative | Adapter implementation |
| --- | --- | --- | --- | --- | --- | --- |
| `LLMProvider` | 1.0.0 | Consumer | Qwen/OpenAI/Claude (core) · Self-hosted vLLM/TGI (optional) | ✅ | Alternative | `ThirdPartyAdapter` / `SelfHostedAdapter` |
| `Gateway` | 1.0.0 | Implementer | Higress (core, data plane) | ✅ | — | The data plane is forwarded by Higress, and the control logic is in this repository |
| `Auth` | 1.0.0 | Consumer | Keycloak (core) | ✅ | — | `AuthAdapter` (JWT local verification) |
| `Cache` | 1.0.0 | Consumer | Redis (core) / Valkey (optional, OSI replacement) | ✅ | Alternative | `CacheAdapter` (Unified Cache Interface) |
| `Tracing` | 1.0.0 | Consumer | Langfuse (optional) / OTel (core) | ✅ | Alternative | `TracingAdapter` |

### 6.2 LLMProvider Adapter Details

| Adapter | Applicable Provider | Source Protocol | Normalized Difficulty | Default State | Stage Enabled |
| --- | --- | --- | --- | --- | --- |
| `ThirdPartyAdapter (OpenAI)` | OpenAI GPT Series | OpenAI Chat Completions API | Low (baseline protocol) | core enabled | Phase 1+ |
| `ThirdPartyAdapter (Claude)` | Anthropic Claude | Anthropic Messages API | Medium (messages format difference + system prompt processing) | core enabled | Phase 1+ |
| `ThirdPartyAdapter (Qwen)` | Tongyi Qianwen | DashScope API | Medium (parameter naming mapping) | core enabled | Phase 1+ |
| `SelfHostedAdapter (vLLM)` | vLLM self-hosted inference | OpenAI-compatible (gRPC/HTTP internally) | Low | optional | Phase 4 |
| `SelfHostedAdapter (TGI)` | HuggingFace TGI | TGI Generate API | Medium (requires chat template) | optional | Phase 4 |

### 6.3 Anti-corrosion layer design (ACL)

Each Adapter assumes the responsibility of protocol normalization and ensures the purity of the domain layer:

- **Claude Adapter**: messages array format → internal `[]Message`; system prompt independent injection vs first message distinction
- **Qwen Adapter**: DashScope parameter naming (`top_p` / `repetition_penalty`) → internal standard field
- **OpenAI Adapter**: Baseline protocol, minimal coverage (direct mapping)
- **vLLM Adapter**: Intranet direct connection (gRPC/HTTP), plus connection pool management
- **TGI Adapter**: chat template layer packaging + `generate_stream` SSE parsing

### 6.4 Connection pool and resource management

Each Provider Adapter maintains an independent connection pool:

- HTTP: `MaxConnsPerHost: 100`、`MaxIdleConns: 50`、IdleTimeout 60s
- gRPC: `MaxConcurrentStreams` + connection keepalive
- Connections that have been disconnected during circuit breaker are removed from the pool
- Regular health detection writeback `ModelCatalog.UpdateHealth`

### 6.5 Principle of coexistence of multiple implementations of the same type

Multiple Adapters can be online at the same time behind `LLMProvider` (§10.4):

- `ModelRouter` routes to different Adapters based on request/tenant/capability
- Circuit breaker isolation: Each Provider instance has an independent circuit breaker, and the failure of one Provider does not affect other
- Cost-aware routing: High-frequency traffic prioritizes self-hosted Adapters, and long-tail supplements third-party Adapters.
- Canary update: 5% canary traffic of the new model (the split point is at ModelRouter, to be aligned with `ai-platform-api`)

### 6.6 Higress collaborative segmentation

| Responsibilities | Level | Description |
| --- | --- | --- |
| JWT authentication | Higress Wasm | Lightweight front-end to reduce invalid requests entering the gateway |
| Global concurrent current limit | Higress Wasm | Global current limit to prevent avalanches |
| Audit header collection | Higress Wasm | Request header information collection |
| Routing decision | ai-gateway-core | Model selection, degradation chain |
| Circuit breaker/downgrade | ai-gateway-core | Business logic cannot be sunk |
| Protocol normalization | ai-gateway-core Adapter | Provider differential convergence |

### 6.7 SPI version alignment and consistency

| Consistency report item | bom.yaml version | This repository implementation version | Status |
| --- | --- | --- | --- |
| `LLMProvider` | `1.0.0` | `1.0.0` | Alignment |
| `Gateway` | `1.0.0` | `1.2.0` | Alignment (D3 fixed `ModelGateway→Gateway`) |
| `Auth` | `1.0.0` | `1.0.0` | Alignment |
| `Cache` | `1.0.0` | `1.0.0` | Alignment |
| `Tracing` | `1.0.0` | `1.0.0` | Alignment |

### 6.8 Stage introduction matrix

| Stage | Profile | Enable Adapter |
| --- | --- | --- |
| 1~3 | starter / standard | `ThirdPartyAdapter` (OpenAI + Qwen + Claude) + `AuthAdapter` + `CacheAdapter` + `TracingAdapter` (OTel only) |
| Four | advanced / full | + `SelfHostedAdapter` (vLLM/TGI) + Langfuse + Valkey (alternative Cache) |

Unenabled Adapters are eliminated at compile time via Wire DI, with zero runtime overhead.

---

## Request path panorama

```
Client(Portal / SDK / CLI) → POST /v1/chat/completions (JWT)
  → Higress Data plane（Wasm plug-in: JWT Authentication / Global throttling / Audit header collection）
    → ai-gateway-core access layer handler [Hertz/go-zero]
      → Tenant context resolution（Keycloak JWT → tenant_id / role）
        → Basic risk control: Injection attack scan + PII Detection + Token bucket current limit
          → [intercept] → 4xx + Audit log
          → [release] → cache query（Semantics/accurate，optional，Redis Vector Search）
            → [hit] → Return cached response directly（≤5ms）
            → [miss] → ModelRouter.Resolve（routing decisions: default + fallback + costAware）
              → tenant×Model quota/Current limit check（Redis Lua Atomic deduction）
                → [excess] → 429 or downgrade to an alternative model
                → [pass] → LLMProvider.Chat / ChatStream（through Adapter → third party API / self-hosted inference）
                  → Circuit breaker protection: Error rate exceeds threshold 50% → cool down 30s → Half-exploration
                    → [Master model failed] → according to fallback_chain Retry the next one in sequence
                    → [All failed] → 5xx + Report + audit
                    → [success] → Measurement instrumentation（Asynchronous reporting ai-billing-service）
                      → audit（PostgreSQL append-only）+ OTel trace
                        → streaming/non-streaming response → Higress → Client
```

---

> **Associated documents**: This repository `docs/DESIGN.md` · `docs/SKILLS.md` · `docs/SPECS.md`
> **Architecture Reference**: §4.4.1 (AI Unified Gateway) · §4.4.4–4.4.6 (Model Supply) · §4.7.4 (Basic Risk Control) · §9 (K8s Deployment) · §10.4 (SPI Multiple Implementation) · §10.6 (Component Registry) · §15.5 (DDD Layer/Technology Stack) · §16 (BOM)
