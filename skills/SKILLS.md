# ai-gateway-core · Skills & Rules

> **技能规则层** — 关键算法、并发/性能模型、安全策略的可执行规则
> **源文档**: design/DESIGN.md §5 / §9 / §12
> **平台版本**: v1.4.0

---

## 算法规则（§5）

### RULE-GW-001: 模型路由优先级

**触发**: 收到 `POST /v1/chat/completions` 请求

**约束**:
1. 若请求指定 `capability`（如 vision）而首选不支持 → 直接跳到支持该能力的模型
2. 若首选 `health != healthy` 或 `quota_exceeded` → 取降级链第一个可用者
3. 若 `costAware=true` 且首选为第三方而自托管有余量 → 优先自托管
4. 返回 `RouteDecision{Preferred, FallbackChain, Reason}`，调用方按链顺序重试至多 N 次

**示例**:
```
Input:  ChatRequest{Model:"cloud-qwen-max", FallbackChain:["cloud-gpt-4o"], Capability:"chat"}
State:  cloud-qwen-max health=degraded, cloud-gpt-4o healthy
Output: RouteDecision{Preferred:"cloud-gpt-4o", FallbackChain:[], Reason:"health"}
```

### RULE-GW-002: 熔断器状态机

**触发**: Provider 实例错误率/延迟超过阈值

**约束**:
- 状态机: `Closed → Open（错误率≥50% & 冷却窗口30s）→ HalfOpen（放行探活）→ Closed/Open`
- 熔断期间路由直接跳过该 provider，走降级链
- 每 provider 实例独立熔断器，互不影响
- 冷却窗口默认 `30s`，可配置 `circuitBreaker.cooldownMs`

**示例**:
```
Input:  Provider P1 连续5次错误率=60%
Action: P1 熔断器 Closed → Open
Route:  后续请求跳过 P1，路由到 fallback P2
After:  30s 后自动进入 HalfOpen，放行1个探活请求
```

### RULE-GW-003: 限流（令牌桶 + 滑动窗口）

**触发**: 每次模型调用请求通过路由后

**约束**:
- 维度: `tenant×model` QPS（令牌桶）与 TPM（滑动窗口计数）
- 实现: Redis Lua 脚本原子扣减；本地 Goroutine 级缓存预热减少 Redis 往返
- 超限处理: 返回 `429` 或按策略降级到配额更宽松的备选（仅当备选存在且未超限）
- 默认值: QPS=20/租户，TPM=200000/租户

**示例**:
```
Input:  tenant=T1, model=M1, current TPM=195000, request tokens=6000
Check:  195000 + 6000 = 201000 > 200000 (TPM limit)
Action: 429 Too Many Requests 或降级到 M2（若 M2 未超限）
Redis:  EVAL lua_script 1 tenant:T1:model:M1:tpm 6000 200000 60
```

### RULE-GW-004: 语义缓存命中

**触发**: 缓存开关 `cache.enabled=true`，请求通过风控后

**约束**:
- 请求标准化（去随机参数）→ Query Embedding（BGE-M3）→ Redis Vector Search
- 相似度阈值: `> 0.95` 命中
- 命中结果异步 TTL=1h 写回
- 默认关闭（optional），开启后跨租户语义复用需评估合规

**示例**:
```
Input:  ChatRequest{Model:"gpt-4o", Messages:[{Role:"user", Content:"What is Docker?"}]}
Embed:  [0.12, -0.34, 0.56, ...] (BGE-M3 1024-dim)
Search: Redis FT.SEARCH idx:semantic "*=>[KNN 5 @embedding $vec]" RETURN 3 content similarity
Hit:    similarity=0.972 > 0.95 → 直接返回缓存响应
Miss:   similarity=0.87 < 0.95 → 走正常 SPI 调用链路
```

### RULE-GW-005: 出境管控

**触发**: 请求路由到第三方 Provider（`source=third_party`）

**约束**:
- 调用前 PII 检测与脱敏（正则 + 分类器）
- 若租户策略为 `deny_egress`，强制仅路由自托管（阶段四）
- 平台 API Key 不向租户暴露（Vault 托管）
- `egress.denyEgressTenants` 列表中的租户强制自托管

**示例**:
```
Input:  tenant=T2, model=cloud-gpt-4o, tenant in denyEgressTenants=[T2]
Check:  cloud-gpt-4o source=third_party, but T2 requires deny_egress
Action: 403 Forbidden (egress denied) — 需配置自托管模型或移出 deny 列表
```

---

## 并发与性能规则（§9）

### RULE-GW-006: 热路径框架选择

**触发**: 代码初始化阶段

**约束**:
- 热路径（chat/embed/rerank）：Hertz（CloudWeGo）或 go-zero，高并发低 GC
- 控制面（管理 API）：Gin
- 不允许热路径使用 Gin（性能差异显著）

### RULE-GW-007: Goroutine 模型

**触发**: 每次请求到达

**约束**:
- 每请求一个 goroutine（Hertz 默认模型）
- 流式响应用独立 goroutine 驱动 SSE 分片写入
- 计量/审计通过 `chan` + 后台 worker 池异步落盘，不阻塞主路径
- 禁止在主 goroutine 中同步写审计日志

### RULE-GW-008: 背压保护

**触发**: 上游 Provider 限流/熔断触发或并发升高

**约束**:
- 本地令牌桶 + `weighted semaphore` 限制在途请求数
- Higress 侧配置全局并发上限
- 在途请求数超限时返回 `503 Service Unavailable`，不排队
- 防止雪崩：熔断 > 信号量 > 限流，逐层兜底

**示例**:
```
Config:  maxConcurrentRequests=1000
State:   当前在途 1000，新请求到达
Action:  503 Service Unavailable（不排队等待）
Metric:  gateway_concurrent_requests{status="rejected"} +1
```

### RULE-GW-009: 连接池配置

**触发**: 每个 Provider Adapter 初始化

**约束**:
- 独立 HTTP/gRPC 连接池，参数: `MaxConnsPerHost`、`MaxIdleConns`
- 避免建连抖动：Idle 连接保持 >= 60s
- 熔断期间已断开连接不参与连接池分配

### RULE-GW-010: 水平扩展

**触发**: 部署配置

**约束**:
- 本仓无本地有状态内存（除可重建的路由热副本）
- 可水平扩缩（多副本 Deployment）
- 扩容时 `maxSurge:1, maxUnavailable:0`
- 副本数增加时线性吞吐增长（目标: 2→4 副本吞吐翻倍）

---

## 安全规则（§12）

### RULE-GW-011: 密钥安全

**触发**: Provider 配置加载

**约束**:
- Provider API Key 存储于 Vault / K8s Secret
- 不向租户暴露原始 Key（租户只能启用/禁用模型）
- 不允许在配置文件 / 环境变量明文存储 Key
- 审计日志不记录 API Key 内容

**示例**:
```
Good:  apiKeyFrom: vault://providers/openai
Bad:   apiKey: "sk-abc123..."  (明文)
```

### RULE-GW-012: PII 检测与脱敏

**触发**: 请求体经过基础风控模块

**约束**:
- 调用第三方前强制 PII 扫描（默认开 `egress.piiScan: true`）
- 检测类型: 手机号、身份证、邮箱、银行卡、IP 地址
- 脱敏策略: 替换为 `[REDACTED]` 或哈希
- 重模型异步分类器仅在高置信度场景启用

### RULE-GW-013: 供应商授权

**触发**: 模型路由决策

**约束**:
- 管理员按租户白名单开放第三方模型（`tenant_access`）
- 空 `tenant_access` = 全租户可见
- 非白名单租户请求返回 `403 Forbidden`
- 不记录白名单外的模型请求响应对

### RULE-GW-014: 基础风控

**触发**: 每次请求进入接入层之后

**约束**:
- 注入攻击检测（prompt injection 正则匹配）
- PII/敏感词扫描（默认开）
- 令牌桶限流（默认开）
- 拦截后返回 `4xx` + 审计日志（不静默丢弃）
- 拒绝后不进入后续路由/SPI 调用

### RULE-GW-015: 审计不可变性

**触发**: 每次请求完成（成功/失败/拦截）

**约束**:
- 审计日志写入 PostgreSQL `audit_log` 表
- 不可变: 仅 INSERT，无 UPDATE/DELETE 权限
- 字段: tenant_id, model_id, action, status, tokens, latency_ms, timestamp
- 异步写入，不阻塞主路径

### RULE-GW-016: 成本计量

**触发**: 每次模型调用完成

**约束**:
- Token 输入/输出、调用次数、延迟异步上报 `ai-billing-service`
- 自托管 GPU-hour 内部折算 + 第三方 Token 计费，统一上报
- 计量失败不影响主路径响应（best-effort）

---

## 可观测性规则

- OTel traces 默认开（core）：每个请求全链路 trace
- Prometheus 指标: QPS, p50/p95/p99 延迟, Token 消耗, 错误率, 熔断次数, 降级次数
- Langfuse（optional）: LLM 专项观测（prompt/response/token/cost）
- 所有中间件标注延迟预算，超预算阶段打 WARN trace

---

## 追溯矩阵

| 规则 | 源文档 DESIGN.md |
| --- | --- |
| RULE-GW-001~005 | §5 关键算法 |
| RULE-GW-006~010 | §9 并发与性能 |
| RULE-GW-011~016 | §12 可观测性/安全 |

> **变更记录**: v0.1 | 2026-07-17 | 初稿（从 DESIGN.md §5/§9/§12 提取）
