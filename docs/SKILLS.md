# ai-gateway-core · Skills & Rules

> **Skill Rules Layer** — executable rules for key algorithms, concurrency/performance models, and security policies
> **Source document**: docs/DESIGN.md §5 / §9 / §12
> **Platform version**: v1.0.0

---

## Algorithm rules (§5)

### RULE-GW-001: Model routing priority

**Trigger**: Received `POST /v1/chat/completions` request

**constraint**:
1. If the request specifies `capability` (such as vision) and the first choice does not support it → jump directly to the model that supports the capability
2. If `health != healthy` or `quota_exceeded` is preferred → take the first available one in the downgrade chain
3. If `costAware=true` and third-party is preferred and self-hosting has margin → self-hosting is preferred
4. Return `RouteDecision{Preferred, FallbackChain, Reason}`, and the caller will retry up to N times in chain order.

**Example**:
```
Input:  ChatRequest{Model:"cloud-qwen-max", FallbackChain:["cloud-gpt-4o"], Capability:"chat"}
State:  cloud-qwen-max health=degraded, cloud-gpt-4o healthy
Output: RouteDecision{Preferred:"cloud-gpt-4o", FallbackChain:[], Reason:"health"}
```

### RULE-GW-002: Circuit breaker state machine

**Trigger**: Provider instance error rate/latency exceeds threshold

**constraint**:
- State machine: `Closed → Open (error rate ≥50% & cooling window 30s) → HalfOpen (release for exploration) → Closed/Open`
- During the circuit breaker period, the router directly skips the provider and takes the downgrade chain.
- Each provider instance has an independent circuit breaker and does not affect each other.
- The default cooling window is `30s`, configurable `circuitBreaker.cooldownMs`

**Example**:
```
Input:  Provider P1 continuous5error rate=60%
Action: P1 circuit breaker Closed → Open
Route:  Subsequent requests are skipped P1，route to fallback P2
After:  30s automatically enter after HalfOpen，release1job search request
```

### RULE-GW-003: Rate limiting (token bucket + sliding window)

**Trigger**: After each model call request passes the route

**constraint**:
- Dimension: `tenant×model` QPS (token bucket) and TPM (sliding window count)
- Implementation: Redis Lua script atomic deduction; local Goroutine-level cache warm-up reduces Redis round-trips
- Over-limit processing: Return `429` or downgrade to an alternative with a looser quota according to the policy (only if the alternative exists and does not exceed the limit)
-Default value: QPS=20/tenant, TPM=200000/tenant

**Example**:
```
Input:  tenant=T1, model=M1, current TPM=195000, request tokens=6000
Check:  195000 + 6000 = 201000 > 200000 (TPM limit)
Action: 429 Too Many Requests or downgrade to M2（like M2 Not exceeding the limit）
Redis:  EVAL lua_script 1 tenant:T1:model:M1:tpm 6000 200000 60
```

### RULE-GW-004: Semantic cache hit

**Trigger**: Cache switch `cache.enabled=true`, after the request passes the risk control

**constraint**:
- Request normalization (remove random parameters) → Query Embedding (BGE-M3) → Redis Vector Search
- Similarity threshold: `> 0.95` hit
- Hit result asynchronous TTL=1h write back
- It is turned off by default (optional). After it is turned on, cross-tenant semantic reuse needs to be evaluated for compliance.

**Example**:
```
Input:  ChatRequest{Model:"gpt-4o", Messages:[{Role:"user", Content:"What is Docker?"}]}
Embed:  [0.12, -0.34, 0.56, ...] (BGE-M3 1024-dim)
Search: Redis FT.SEARCH idx:semantic "*=>[KNN 5 @embedding $vec]" RETURN 3 content similarity
Hit:    similarity=0.972 > 0.95 → Return cached response directly
Miss:   similarity=0.87 < 0.95 → Go normal SPI call link
```

### RULE-GW-005: Exit Control

**Trigger**: The request is routed to a third-party Provider (`source=third_party`)

**constraint**:
- Pre-call PII detection and desensitization (regular + classifier)
- If the tenant policy is `deny_egress`, force only route self-hosting (Phase 4)
- Platform API Key is not exposed to tenants (Vault hosting)
- Tenants in the `egress.denyEgressTenants` list are forced to be self-hosted

**Example**:
```
Input:  tenant=T2, model=cloud-gpt-4o, tenant in denyEgressTenants=[T2]
Check:  cloud-gpt-4o source=third_party, but T2 requires deny_egress
Action: 403 Forbidden (egress denied) — Need to configure self-hosted model or move out deny list
```

---

## Concurrency and Performance Rules (§9)

### RULE-GW-006: Hot path frame selection

**Trigger**: Code initialization phase

**constraint**:
- Hot path (chat/embed/rerank): Hertz (CloudWeGo) or go-zero, high concurrency and low GC
- Control plane (management API): Gin
- Do not allow hot paths to use Gin (significant performance difference)

### RULE-GW-007: Goroutine model

**Trigger**: Every time a request arrives

**constraint**:
- One goroutine per request (Hertz default model)
- Streaming responses use independent goroutine to drive SSE shard writing
- Metering/auditing passes `chan` + background worker pool is placed asynchronously without blocking the main path
- Disable synchronous writing of audit logs in the main goroutine

### RULE-GW-008: Back pressure protection

**Trigger**: Upstream Provider current limit/circuit trigger or concurrency increase

**constraint**:
- Local token bucket + `weighted semaphore` to limit the number of requests in transit
- Configure the global concurrency upper limit on the Higress side
- When the number of requests in transit exceeds the limit, `503 Service Unavailable` will be returned and no queue will be queued.
- Prevent avalanches: circuit breaker > semaphore > rate limiting, layer by layer

**Example**:
```
Config:  maxConcurrentRequests=1000
State:   Currently in transit 1000，New request arrives
Action:  503 Service Unavailable（Don't wait in line）
Metric:  gateway_concurrent_requests{status="rejected"} +1
```

### RULE-GW-009: Connection pool configuration

**Trigger**: Each Provider Adapter is initialized

**constraint**:
- Independent HTTP/gRPC connection pool, parameters: `MaxConnsPerHost`, `MaxIdleConns`
- Avoid jitter during connection establishment: Idle connection maintained >= 60s
- Connections that have been disconnected during the circuit breaker period do not participate in connection pool allocation.

### RULE-GW-010: Horizontal expansion

**Trigger**: Deploy configuration

**constraint**:
- There is no local stateful memory in this repository (except for the rebuildable routing hot copy)
- Horizontally scalable (multi-copy Deployment)
- When expanding capacity `maxSurge:1, maxUnavailable:0`
- Linear throughput growth when the number of replicas increases (target: 2→4 replica throughput doubles)

---

## Safety Rules (§12)

### RULE-GW-011: Key Security

**Trigger**: Provider configuration loading

**constraint**:
- Provider API Key is stored in Vault / K8s Secret
- Does not expose the original Key to the tenant (the tenant can only enable/disable the model)
- Do not allow clear text storage of Key in configuration files/environment variables
- Audit logs do not record API Key content

**Example**:
```
Good:  apiKeyFrom: vault://providers/openai
Bad:   apiKey: "sk-abc123..."  (plain text)
```

### RULE-GW-012: PII Detection and Desensitization

**Trigger**: The request body passes through the basic risk control module

**constraint**:
- Force PII scanning before calling third party (default enabled `egress.piiScan: true`)
- Detection type: mobile phone number, ID card, email, bank card, IP address
- Desensitization policy: replace with `[REDACTED]` or hash
- Heavy model asynchronous classifier is only enabled in high confidence scenarios

### RULE-GW-013: Supplier Authorization

**Trigger**: Model routing decision

**constraint**:
- Administrators open third-party models by tenant whitelist (`tenant_access`)
- Empty `tenant_access` = visible to all tenants
- Non-whitelisted tenant requests return `403 Forbidden`
- Do not record model request-response pairs outside the whitelist

### RULE-GW-014: Basic risk control

**Trigger**: After each request enters the access layer

**constraint**:
- Injection attack detection (prompt injection regular matching)
- PII/sensitive word scanning (on by default)
- Token bucket current limit (enabled by default)
- Return `4xx` + audit log after interception (not silently discarded)
- Do not enter subsequent routing/SPI calls after rejection

### RULE-GW-015: Audit Immutability

**Trigger**: Each time the request is completed (success/failure/interception)

**constraint**:
- Audit logs are written to the PostgreSQL `audit_log` table
- Immutable: INSERT only, no UPDATE/DELETE permissions
- Fields: tenant_id, model_id, action, status, tokens, latency_ms, timestamp
- Asynchronous writing, does not block the main path

### RULE-GW-016: Cost Measurement

**Trigger**: Every time the model call is completed

**constraint**:
- Token input/output, number of calls, delayed asynchronous reporting `ai-billing-service`
- Self-hosted GPU-hour internal conversion + third-party Token billing, unified reporting
- Metering failure does not affect the main path response (best-effort)

---

## Observability rules

- OTel traces are enabled by default (core): full link trace for each request
- Prometheus metrics: QPS, p50/p95/p99 latency, Token consumption, error rate, number of circuit breakers, number of downgrades
- Langfuse (optional): LLM special observation (prompt/response/token/cost)
- All middleware is marked with a delay budget, and a WARN trace is displayed when the budget exceeds the budget.

---

## Traceability matrix

| Rules | Source Document DESIGN.md |
| --- | --- |
| RULE-GW-001~005 | §5 Key Algorithm |
| RULE-GW-006~010 | §9 Concurrency and Performance |
| RULE-GW-011~016 | §12 Observability/Security |

> **Change Log**: v0.1 | 2026-07-17 | First draft (extracted from DESIGN.md §5/§9/§12)
