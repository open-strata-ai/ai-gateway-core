# ai-gateway-core · Specifications

> **Specification layer** — API/CLI interface, data model, deployment configuration
> **Source document**: design/DESIGN.md §7 / ​​§8 / §11
> **Platform version**: v1.0.0

---

## 7. API / CLI interface

### 7.1 External HTTP API (OpenAI-compatible)

Exposed via Higress data plane:

| Method | Path | Description | Stream | Authentication |
| --- | --- | --- | --- | --- |
| POST | `/v1/chat/completions` | Conversation completion | SSE supported | Keycloak JWT |
| POST | `/v1/embeddings` | Vectorized text | No | Keycloak JWT |
| POST | `/v1/rerank` | Rerank | No | Keycloak JWT |
| GET | `/v1/models` | List tenant-visible model_id | No | Keycloak JWT |
| GET | `/v1/healthz` | Liveness probe | NO | None |
| GET | `/metrics` | Prometheus metrics | No | Intranet |

#### Chat Completions request/response model

**Request body (JSON)**:
```json
{
  "model": "cloud-qwen-max",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Explain Docker in 3 sentences."}
  ],
  "temperature": 0.7,
  "max_tokens": 500,
  "stream": false
}
```

**Non-streaming response body (JSON)**:
```json
{
  "model": "cloud-qwen-max",
  "content": "Docker is...",
  "finish_reason": "stop",
  "usage": {
    "prompt_tokens": 42,
    "completion_tokens": 128,
    "total_tokens": 170
  }
}
```

**Streaming Response Body (SSE)**:
```
data: {"delta": "Docker", "done": false}
data: {"delta": " is", "done": false}
...
data: {"delta": "", "usage": {"prompt_tokens": 42, "completion_tokens": 128}, "done": true}
```

### 7.2 Internal/Control API

| Method | Path | Description | Protocol | Authentication |
| --- | --- | --- | --- | --- |
| GET | `/internal/catalog/models` | Model catalog management | gRPC/HTTP | Intranet |
| POST | `/internal/routing/policy` | Routing policy delivery | gRPC/HTTP | Intranet |
| PUT | `/internal/provider/{id}/health` | Exploring writeback | HTTP | Intranet |
| POST | `/internal/metering/report` | Metering aggregation (connected to billing) | gRPC | Intranet |
| GET | `/internal/ready` | Readiness probe (verify that PG/Redis/at least one provider is healthy) | HTTP | Intranet |

### 7.3 CLI

`ai-gateway-core` does not publish a CLI itself; see `ai-cli` (`aictl`) for platform-level CLI. The `--config` startup parameter can be used for operation and maintenance.

---

## 8. Data model

### 8.1 Persistent storage

| Storage | Role | Data Content |
| --- | --- | --- |
| PostgreSQL (core) | Authoritative storage | `model_catalog` (model card), `routing_policy` (tenant routing policy), `tenant_entitlement` (model whitelist), `audit_log` (immutable audit) |
| Redis (core) | Hot data | Current limit counter (QPS/TPM sliding window), semantic cache vector, routing table hot copy, circuit breaker status |
| Valkey (optional) | OSI replacement for Redis | Same as Redis, by bom.yaml `Cache` |

### 8.2 Core table: `model_catalog`

```sql
CREATE TABLE model_catalog (
  model_id       TEXT PRIMARY KEY,
  source         TEXT NOT NULL,         -- 'self_hosted' | 'third_party'
  capability     TEXT NOT NULL,         -- 'chat'|'embedding'|'rerank'|'vision'|'audio'
  context_window INT,
  price_in       NUMERIC,              -- per 1M tokens in
  price_out      NUMERIC,              -- per 1M tokens out
  latency_sla_ms INT,
  tps            INT,                  -- tokens per second
  rate_limit     JSONB,                -- {"qps_per_tenant": N, "tpm_per_tenant": N}
  health         TEXT DEFAULT 'healthy', -- 'healthy'|'degraded'|'down'
  tenant_access  JSONB DEFAULT '[]'    -- whitelist；null=fully tenanted
);
```

**Column Description**:

| Column | Type | Constraint | Description |
| --- | --- | --- | --- |
| model_id | TEXT | PK | The unique identifier of the model, such as `cloud-qwen-max` |
| source | TEXT | NOT NULL | `self_hosted` or `third_party` |
| capability | TEXT | NOT NULL | `chat`/`embedding`/`rerank`/`vision`/`audio` |
| context_window | INT | | Maximum number of context tokens |
| price_in | NUMERIC | | Enter price (per 1M tokens) |
| price_out | NUMERIC | | Output price (per 1M tokens) |
| latency_sla_ms | INT | | SLA latency cap |
| tps | INT | | Throughput tokens/second |
| rate_limit | JSONB | | `{qps_per_tenant, tpm_per_tenant}` |
| health | TEXT | 'healthy' | health status |
| tenant_access | JSONB | '[]' | Tenant whitelist |

### 8.3 Redis key design

| Key Pattern | Purpose | TTL |
| --- | --- | --- |
| `ratelimit:{tenant}:{model}:qps` | QPS token bucket count | 1s |
| `ratelimit:{tenant}:{model}:tpm` | TPM sliding window | 60s |
| `cache:semantic:{embedding_hash}` | Semantic cache response | 3600s |
| `router:table` | Hot copy of routing table | None (real-time synchronization) |
| `circuit:{provider}` | circuit breaker status | consistent with cooling window |

---

## 11. Configuration and deployment

### 11.1 Deployment form

| Properties | Values ​​|
| --- | --- |
| Required | core (not optional) |
| namespace | `ai-system` (§9.2) |
| Deployment method | Docker Compose (starter)/K8s Deployment (standard) |
| Mirror | Single binary (`cmd/` + Wire assembly) |
| GPU Requirements | No (self-hosted inference vLLM decoupled to GPU node group) |

### 11.2 K8s resource configuration

```yaml
resources:
  requests:
    cpu: 500m
    memory: 512Mi
  limits:
    cpu: 2
    memory: 2Gi
```

### 11.3 Probe configuration

| probe | path | description | initialDelaySeconds | periodSeconds |
| --- | --- | --- | --- | --- |
| Alive | `GET /v1/healthz` | Quick return 200 | 5 | 10 |
| Ready | `GET /internal/ready` | Verify PG + Redis + ≥1 provider healthy | 5 | 10 |

### 11.4 Rolling update strategy

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxSurge: 1
    maxUnavailable: 0
```

### 11.5 Complete list of configuration keys

**File location**: `infrastructure/config/`

```yaml
gateway:
  listen: "0.0.0.0:8080"           #listening address
  upstream: "higress://ai-system" # upstream forwarding target

modelRouting:
  default: "cloud-qwen-max"         #Default model
  fallbackChain:                     #Downgrade chain
    - "cloud-gpt-4o"
  costAware: true                    #cost aware routing

ratelimit:
  backend: "redis"                   #Rate limiting backend
  defaultQPSPerTenant: 20            #Default QPS per tenant
  defaultTPMPerTenant: 200000        #Default per-tenant TPM

circuitBreaker:
  errorThreshold: 0.5                #error rate threshold
  cooldownMs: 30000                  #Cooling window ms

cache:
  enabled: false                     #Semantic caching switch
  semanticThreshold: 0.95            #similarity threshold

egress:
  piiScan: true                      #PII scan switch
  denyEgressTenants: []              #Force self-hosted tenant list
```

**Configuration key description**:

| key | type | default value | description |
| --- | --- | --- | --- |
| `gateway.listen` | string | `0.0.0.0:8080` | Service listening address |
| `gateway.upstream` | string | `higress://ai-system` | Higress upstream address |
| `modelRouting.default` | string | `cloud-qwen-max` | Default routing model |
| `modelRouting.fallbackChain` | []string | `[cloud-gpt-4o]` | Fallback chain model_id list |
| `modelRouting.costAware` | bool | `true` | Whether to enable cost-aware routing |
| `ratelimit.backend` | string | `redis` | Rate limiting implementation backend |
| `ratelimit.defaultQPSPerTenant` | int | `20` | Default QPS per tenant |
| `ratelimit.defaultTPMPerTenant` | int | `200000` | Per-tenant default TPM |
| `circuitBreaker.errorThreshold` | float | `0.5` | circuit breaker error rate threshold |
| `circuitBreaker.cooldownMs` | int | `30000` | Circuit break cooling time (milliseconds) |
| `cache.enabled` | bool | `false` | Enable semantic caching |
| `cache.semanticThreshold` | float | `0.95` | Semantic cache hit similarity threshold |
| `egress.piiScan` | bool | `true` | Pre-exit PII desensitization |
| `egress.denyEgressTenants` | []string | `[]` | List of tenants prohibited from leaving the country |

### 11.6 Stage introduction strategy

| Stages | Components | Configuration Status |
| --- | --- | --- |
| One to three (starter/standard) | core | All core functions are enabled; self-hosted Adapter is disabled |
| Four (advanced/full) | core + SelfHostedAdapter | `SelfHostedAdapter` is enabled by profiles `optional_disabled` control |

### 11.7 Dependent components

| Component | Type | Required | Description |
| --- | --- | --- | --- |
| Higress | Data plane | core | Wasm plug-in: authentication/rate limiting/auditing |
| Keycloak | Authentication | core | JWT issuance and verification |
| PostgreSQL | storage | core | model directory/routing policy/auditing |
| Redis | cache/counting | core | rate limiting/caching/routing hot copy |
| Vault | Key management | core | Provider API Key hosting |

---

## Traceability matrix

| Chapter | Source document DESIGN.md corresponding |
| --- | --- |
| 7 API/CLI/Configuration Interface | §7 |
| 8 Data Model and Storage | §8 |
| 11 Configuration and Deployment | §11 |

> **Change Record**: v0.1 | 2026-07-17 | First draft (extracted from DESIGN.md §7/§8/§11)
