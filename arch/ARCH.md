# ai-gateway-core · Architecture（架构总览）

> **摘自** `design/DESIGN.md` §1 定位与边界 · §2 职责清单 · §3 核心接口 · §6 适配器
> **语言·框架**: Go · Hertz/go-zero（热路径）+ Gin + Cobra + Wire（编译期 DI）
> **领域**: runtime（模型服务层 / AI 统一网关）
> **optional**: false（core · 核心必选）
> **平台版本**: v1.4.0

---

## §1 定位与边界（Scope）

### 1.1 一句话定位

`ai-gateway-core` 是 OpenStrata 的**数据面入口 + 模型供给中枢**，承载架构 §4.4.1「AI 统一网关」与 §4.4.4–4.4.6「模型供给体系」。它把"对模型能力的调用"收敛为**单一、标准、可治理的入口**，对上层（Agent 引擎、Portal、SDK、CLI）完全屏蔽模型来源差异（第三方 API / 自托管推理）。

### 1.2 解决的核心问题

把"N 个模型供应方 × M 种调用协议 × K 类治理诉求（限流/降级/成本/审计）"收敛为一个 **OpenAI-compatible 的、可路由、可熔断、可计量、可审计的统一平面**。

### 1.3 必选性与适用场景

- **必选**: core（核心最小自立组合之一，§4.4.1 / §10.2）
- **最小场景**: 即便只接第三方 LLM API（阶段一~三），本仓也足以成立
- **扩展场景**: 阶段四 full 档启用自托管 `SelfHostedAdapter`（vLLM/TGI）
- **不可关闭**: 本仓是数据面入口，关闭后所有 AI 调用失效

### 1.4 架构角色

| 维度 | 说明 |
| --- | --- |
| **层次** | DDD 四层：`domain/`（Port 接口）· `application/`（用例：路由/限流/缓存）· `infrastructure/`（适配器/Wire DI）· `interfaces/`（handler） |
| **热路径框架** | Hertz（CloudWeGo）或 go-zero，高并发低 GC |
| **控制面框架** | Gin（管理 API） |
| **DI 方案** | Wire（编译期依赖注入），零反射开销 |
| **数据面入口** | Higress（Wasm 插件：JWT 鉴权、全局限流、审计头采集）→ 本仓 handler |
| **协议承诺** | 对外 OpenAI-compatible，对内 gRPC/HTTP |

### 1.5 与其他 Go 组件的分工

| 组件 | 关系类型 | 说明 |
| --- | --- | --- |
| `ai-tool-registry` | 请求链路串联 | 网关负责"模型调用"数据面；工具注册/调用由 tool-registry 经 `ToolRegistry` SPI 承载。二者在请求链路中串联（Agent 调网关→网关转发工具→工具再经网关调模型），但职责不重叠。 |
| `ai-platform-api` | 控制面 vs 数据面 | `ai-platform-api`（Java）是控制面编排 + 租户/用户/计量汇总；网关是**热路径运行时**，不做跨租户结算，只做 `tenant×model` 实时配额/限流与原始计量上报。 |
| `ai-sandbox-manager` | 独立数据面 | 代码执行（沙箱）是另一条数据面，由 `ai-sandbox-manager` 经 `Sandbox` SPI 承载（§4.3.3），网关不执行代码。 |
| `ai-cli` | client/server | `aictl` 是调用方，通过本仓暴露的 OpenAI-compatible API / CLI 子命令驱动部署。 |

### 1.6 边界约束

| 约束 | 说明 |
| --- | --- |
| **不跨租户结算** | 仅做实时配额，汇总计费由 `ai-platform-api` + `ai-billing-service` 负责 |
| **不执行代码** | 代码执行是 `ai-sandbox-manager` 的职责 |
| **不管理工具** | 工具注册/调用由 `ai-tool-registry` 治理 |
| **不做模型训练/微调** | 本仓仅做推理路由，训练见 `ai-provisioning-engine` |

---

## §2 职责清单

### 2.1 完整职责表

| # | 职责 | 必选/可选 | 触发条件 | 说明 |
| --- | --- | --- | --- | --- |
| R1 | **OpenAI-compatible API 接入** | core | 每次模型调用请求 | `/v1/chat/completions`、`/v1/embeddings`、`/v1/rerank`、`/v1/models` 等，归一化上游差异（§4.4.1） |
| R2 | **请求路由 / 模型选择** | core | `ModelRouter.Resolve` 调用 | 按 `default + fallback_chain` 选源，支持 capability 匹配（§4.4.5） |
| R3 | **限流与配额** | core | 每次请求通过风控后 | `tenant×model` 的 QPS / Token 配额；供应商级限流（§4.4.5、§4.7.4） |
| R4 | **降级 / 故障转移** | core | 主模型不健康/超时/配额超限 | 降级链顺序切换；配合熔断器（§4.4.5、§4.4.6） |
| R5 | **成本感知路由** | optional→core（默认开） | `costAware: true` 时 | 高频流量优先自托管、长尾/稀缺补位第三方（§4.4.5） |
| R6 | **模型目录（ModelCatalog）** | core | 路由决策时查询 | 模型卡片登记：能力/上下文/价格/SLA/健康/租户白名单（§4.4.5） |
| R7 | **语义/精确缓存** | optional（默认关） | `cache.enabled: true` 时 | Redis Vector Search 语义缓存（§4.3.4） |
| R8 | **密钥托管与出境管控** | core | 第三方 Provider 调用前 | Provider API Key 存 Vault/K8s Secret，不向租户暴露；PII 脱敏（§4.4.6） |
| R9 | **基础风控（注入/PII/限流）** | core | 请求进入 handler 后 | 注入检测 + PII 扫描 + 令牌桶限流，默认开（§4.7.4） |
| R10 | **计量埋点（原始用量上报）** | core | 每次模型调用完成 | Token 输入/输出、调用次数、延迟，异步上报 `ai-billing-service`（§4.7.2） |
| R11 | **OTel traces + 不可变审计** | core | 每次请求全链路 | 基础可观测性默认开（§4.8） |

### 2.2 职责分级

| 级别 | 职责编号 | 数量 | 说明 |
| --- | --- | --- | --- |
| **core（不可关闭）** | R1, R2, R3, R4, R6, R8, R9, R10, R11 | 9 | 网关最小可行集 |
| **默认开（可配置关）** | R5 | 1 | 成本感知路由（`costAware: true`） |
| **默认关（optional）** | R7 | 1 | 语义缓存（`cache.enabled: false`） |

### 2.3 职责边界

- **不包含**: 模型训练、模型评估、数据标注、用户管理、租户结算、代码执行
- **本仓独占**: 统一协议接入、多 Provider 路由、实时熔断/降级、模型级成本感知

---

## §3 核心接口与抽象

### 3.1 设计原则

领域层（`domain/`）只定义 **Port（接口）**，不依赖具体 Provider。所有上游差异在 Adapter 层（`infrastructure/`）收敛。SPI 版本与 `bom.yaml` `interface_versions` 对齐。本仓遵循 DDD 经典四层架构（§15.6.2）。

### 3.2 LLMProvider SPI（v1.0.0）

与来源无关的模型能力抽象，Provider 适配器统一契约。

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
    Model         string    `json:"model"`             // 目标 model_id（可经路由改写）
    Messages      []Message `json:"messages"`
    Temperature   float32   `json:"temperature,omitempty"`
    MaxTokens     int       `json:"max_tokens,omitempty"`
    Stream        bool      `json:"stream"`            // SSE 流式
    TenantID      string    `json:"-"`                 // 由网关中间件注入，不入网
    FallbackChain []string  `json:"-"`                 // AgentSpec.model_binding.fallback_chain
    Capability    string    `json:"-"`                 // chat/embedding/rerank/vision/audio
}

type ChatResponse struct {
    Model        string     `json:"model"`             // 实际命中的 model_id
    Content      string     `json:"content"`
    FinishReason string     `json:"finish_reason"`
    Usage        TokenUsage `json:"usage"`
    RoutedFrom   string     `json:"-"`                 // 命中前的首选 model（用于诊断）
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
// ===== 模型路由器 Port（领域层）=====
type ModelRouter interface {
    // Resolve 返回本次请求应选中的 provider 实例 + 降级顺序
    Resolve(ctx context.Context, req ChatRequest) RouteDecision
}

type RouteDecision struct {
    Preferred     string   // model_id
    FallbackChain []string // 降级链 model_id 列表（按优先级排序）
    Reason        string   // cost/quota/latency/capability
}
```

### 3.4 ModelCatalog Port

```go
// ===== 模型目录 Port（领域层）=====
type ModelCatalog interface {
    Get(modelID string) (ModelCard, bool)
    ListByCapability(cap string, tenantID string) []ModelCard
    UpdateHealth(modelID string, h HealthStatus)
}

// 模型卡片（§4.4.5 字段）
type ModelCard struct {
    ModelID       string    `json:"model_id"`
    Source        string    `json:"source"`          // self_hosted / third_party
    Capability    string    `json:"capability"`      // chat/embedding/rerank/vision/audio
    ContextWindow int       `json:"context_window"`
    PriceIn       float64   `json:"price_in"`        // 每 1M tokens（自托管按内部折算）
    PriceOut      float64   `json:"price_out"`
    LatencySLA    int       `json:"latency_sla_ms"`
    TPS           int       `json:"tps"`
    RateLimit     RateLimit `json:"rate_limit"`
    Health        string    `json:"health"`          // healthy/degraded/down
    TenantAccess  []string  `json:"tenant_access"`   // 白名单；空=全租户
}

type RateLimit struct {
    QPSPerTenant int `json:"qps_per_tenant"`
    TPMPerTenant int `json:"tpm_per_tenant"` // tokens per minute
}
```

### 3.5 延迟预算约束

| 处理阶段 | 预算（p95） | 超预算行为 |
| --- | --- | --- |
| 接入层 + JWT 鉴权 | ≤15ms | WARN trace |
| 基础风控（注入/PII） | ≤10ms | WARN trace |
| 缓存查询（optional） | ≤5ms | 未命中走正常链路 |
| 路由决策 | ≤2ms | WARN trace |
| 配额/限流（Redis Lua） | ≤3ms | WARN trace |
| SPI 调用（到第三方） | ≤1800ms | 超阈触发降级 |
| 计量/审计 | ≤5ms（异步） | 不阻塞主路径 |
| **端到端 p95** | **≤2000ms** | 与 §4.4.5 降级阈值一致 |

### 3.6 Gateway SPI（v1.2.0）

`Gateway` SPI 由本仓**自身作为实现**暴露给上层。本仓不依赖外部 `Gateway` 实例 —— Higress 是数据面转发层，非 SPI 实现方。

---

## §6 适配器与 SPI 生态

### 6.1 SPI 完整端口矩阵

| SPI 端口 | 版本 | 本仓角色 | 外部组件（bom.yaml） | 默认 | 备选 | Adapter 实现 |
| --- | --- | --- | --- | --- | --- | --- |
| `LLMProvider` | 1.0.0 | 消费方 | Qwen/OpenAI/Claude（core）· 自托管 vLLM/TGI（optional） | ✅ | 备选 | `ThirdPartyAdapter` / `SelfHostedAdapter` |
| `Gateway` | 1.2.0 | 实现方 | Higress（core，数据面） | ✅ | — | 数据面由 Higress 转发，控制逻辑在本仓 |
| `Auth` | 1.0.0 | 消费方 | Keycloak（core） | ✅ | — | `AuthAdapter`（JWT 本地校验） |
| `Cache` | 1.0.0 | 消费方 | Redis（core）/ Valkey（optional，OSI 替代） | ✅ | 备选 | `CacheAdapter`（统一缓存接口） |
| `Tracing` | 1.0.0 | 消费方 | Langfuse（optional）/ OTel（core） | ✅ | 备选 | `TracingAdapter` |

### 6.2 LLMProvider Adapter 详情

| Adapter | 适用 Provider | 源协议 | 归一化难度 | 默认状态 | 阶段启用 |
| --- | --- | --- | --- | --- | --- |
| `ThirdPartyAdapter (OpenAI)` | OpenAI GPT 系列 | OpenAI Chat Completions API | 低（基准协议） | core 启用 | 阶段一+ |
| `ThirdPartyAdapter (Claude)` | Anthropic Claude | Anthropic Messages API | 中（messages 格式差异 + system prompt 处理） | core 启用 | 阶段一+ |
| `ThirdPartyAdapter (Qwen)` | 通义千问 | DashScope API | 中（参数命名映射） | core 启用 | 阶段一+ |
| `SelfHostedAdapter (vLLM)` | vLLM 自托管推理 | OpenAI-compatible（内部 gRPC/HTTP） | 低 | optional | 阶段四 |
| `SelfHostedAdapter (TGI)` | HuggingFace TGI | TGI Generate API | 中（需 chat template） | optional | 阶段四 |

### 6.3 防腐层设计（ACL）

每个 Adapter 承担协议归一职责，保证领域层纯净：

- **Claude Adapter**: messages 数组格式 → 内部 `[]Message`；system prompt 独立注入 vs 首条消息区分
- **Qwen Adapter**: DashScope 参数命名（`top_p` / `repetition_penalty`）→ 内部标准字段
- **OpenAI Adapter**: 基准协议，最小覆盖（直接映射）
- **vLLM Adapter**: 内网直连（gRPC/HTTP），加连接池管理
- **TGI Adapter**: chat template 层包装 + `generate_stream` SSE 解析

### 6.4 连接池与资源管理

每个 Provider Adapter 维持独立连接池：

- HTTP: `MaxConnsPerHost: 100`、`MaxIdleConns: 50`、IdleTimeout 60s
- gRPC: `MaxConcurrentStreams` + connection keepalive
- 熔断期间已断开连接从池中剔除
- 定期健康探测回写 `ModelCatalog.UpdateHealth`

### 6.5 同类多实现并存原则

`LLMProvider` 背后可同时在线多个 Adapter（§10.4）：

- `ModelRouter` 按请求/租户/能力路由到不同 Adapter
- 熔断隔离: 每 Provider 实例独立熔断器，一个 Provider 故障不影响其他
- 成本感知路由: 高频流量优先自托管 Adapter，长尾补位第三方 Adapter
- 灰度上新: 新模型 5% 流量灰度（切分点在 ModelRouter，待与 `ai-platform-api` 对齐）

### 6.6 Higress 协作切分

| 职责 | 所在层 | 说明 |
| --- | --- | --- |
| JWT 鉴权 | Higress Wasm | 轻量前置，减少无效请求进入网关 |
| 全局并发限流 | Higress Wasm | 全局限流，防止雪崩 |
| 审计头采集 | Higress Wasm | 请求头元信息采集 |
| 路由决策 | ai-gateway-core | 模型选择、降级链 |
| 熔断/降级 | ai-gateway-core | 业务逻辑不可下沉 |
| 协议归一 | ai-gateway-core Adapter | Provider 差异收敛 |

### 6.7 SPI 版本对齐与一致性

| 一致性报告项 | bom.yaml 版本 | 本仓实现版本 | 状态 |
| --- | --- | --- | --- |
| `LLMProvider` | `1.0.0` | `1.0.0` | 对齐 |
| `Gateway` | `1.2.0` | `1.2.0` | 对齐（D3 已修正 `ModelGateway→Gateway`） |
| `Auth` | `1.0.0` | `1.0.0` | 对齐 |
| `Cache` | `1.0.0` | `1.0.0` | 对齐 |
| `Tracing` | `1.0.0` | `1.0.0` | 对齐 |

### 6.8 阶段引入矩阵

| 阶段 | 配置档 | 启用 Adapter |
| --- | --- | --- |
| 一~三 | starter / standard | `ThirdPartyAdapter`（OpenAI + Qwen + Claude）+ `AuthAdapter` + `CacheAdapter` + `TracingAdapter`（OTel only） |
| 四 | advanced / full | + `SelfHostedAdapter`（vLLM/TGI）+ Langfuse + Valkey（备选 Cache） |

不启用的 Adapter 通过 Wire DI 编译期排除，零运行时开销。

---

## 请求路径全景

```
Client(Portal / SDK / CLI) → POST /v1/chat/completions (JWT)
  → Higress 数据面（Wasm 插件: JWT 鉴权 / 全局限流 / 审计头采集）
    → ai-gateway-core 接入层 handler [Hertz/go-zero]
      → 租户上下文解析（Keycloak JWT → tenant_id / role）
        → 基础风控: 注入攻击扫描 + PII 检测 + 令牌桶限流
          → [拦截] → 4xx + 审计日志
          → [放行] → 缓存查询（语义/精确，optional，Redis Vector Search）
            → [命中] → 直接返回缓存响应（≤5ms）
            → [未命中] → ModelRouter.Resolve（路由决策: default + fallback + costAware）
              → 租户×模型配额/限流检查（Redis Lua 原子扣减）
                → [超额] → 429 或降级到备选模型
                → [通过] → LLMProvider.Chat / ChatStream（经 Adapter → 第三方 API / 自托管推理）
                  → 熔断保护: 错误率超阈 50% → 冷却 30s → 半开探活
                    → [主模型失败] → 按 fallback_chain 顺序重试下一个
                    → [全部失败] → 5xx + 上报 + 审计
                    → [成功] → 计量埋点（异步上报 ai-billing-service）
                      → 审计（PostgreSQL append-only）+ OTel trace
                        → 流式/非流式响应 → Higress → Client
```

---

> **关联文档**: 本仓 `design/DESIGN.md` · `skills/SKILLS.md` · `specs/SPECS.md`
> **架构引用**: §4.4.1（AI统一网关）· §4.4.4–4.4.6（模型供给）· §4.7.4（基础风控）· §9（K8s 部署）· §10.4（SPI多实现）· §10.6（Component Registry）· §15.6（DDD分层/技术栈）· §16（BOM）
