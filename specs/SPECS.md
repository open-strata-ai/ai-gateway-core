# ai-gateway-core · Specifications

> **规格层** — API/CLI 接口面、数据模型、部署配置
> **源文档**: design/DESIGN.md §7 / §8 / §11
> **平台版本**: v1.4.0

---

## 7. API / CLI 接口面

### 7.1 对外 HTTP API（OpenAI-compatible）

经 Higress 数据面暴露：

| 方法 | 路径 | 说明 | Stream | 鉴权 |
| --- | --- | --- | --- | --- |
| POST | `/v1/chat/completions` | 对话补全 | 支持 SSE | Keycloak JWT |
| POST | `/v1/embeddings` | 向量化文本 | 否 | Keycloak JWT |
| POST | `/v1/rerank` | 重排序 | 否 | Keycloak JWT |
| GET | `/v1/models` | 列出租户可见 model_id | 否 | Keycloak JWT |
| GET | `/v1/healthz` | 存活探针 | 否 | 无 |
| GET | `/metrics` | Prometheus 指标 | 否 | 内网 |

#### Chat Completions 请求/响应模型

**请求体 (JSON)**:
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

**非流式响应体 (JSON)**:
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

**流式响应体 (SSE)**:
```
data: {"delta": "Docker", "done": false}
data: {"delta": " is", "done": false}
...
data: {"delta": "", "usage": {"prompt_tokens": 42, "completion_tokens": 128}, "done": true}
```

### 7.2 内部/管控 API

| 方法 | 路径 | 说明 | 协议 | 鉴权 |
| --- | --- | --- | --- | --- |
| GET | `/internal/catalog/models` | 模型目录管理 | gRPC/HTTP | 内网 |
| POST | `/internal/routing/policy` | 路由策略下发 | gRPC/HTTP | 内网 |
| PUT | `/internal/provider/{id}/health` | 探活回写 | HTTP | 内网 |
| POST | `/internal/metering/report` | 计量聚合（对接 billing） | gRPC | 内网 |
| GET | `/internal/ready` | 就绪探针（校验 PG/Redis/至少一个 provider healthy） | HTTP | 内网 |

### 7.3 CLI

`ai-gateway-core` 自身不发布 CLI；平台级 CLI 见 `ai-cli`（`aictl`）。运维可用 `--config` 启动参数。

---

## 8. 数据模型

### 8.1 持久化存储

| 存储 | 角色 | 数据内容 |
| --- | --- | --- |
| PostgreSQL（core） | 权威存储 | `model_catalog`（模型卡片）、`routing_policy`（租户路由策略）、`tenant_entitlement`（模型白名单）、`audit_log`（不可变审计） |
| Redis（core） | 热数据 | 限流计数器（QPS/TPM 滑动窗口）、语义缓存向量、路由表热副本、熔断状态 |
| Valkey（optional） | OSI 替代 Redis | 同 Redis，by bom.yaml `Cache` |

### 8.2 核心表: `model_catalog`

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
  tenant_access  JSONB DEFAULT '[]'    -- 白名单；空=全租户
);
```

**列说明**:

| 列 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| model_id | TEXT | PK | 模型唯一标识，如 `cloud-qwen-max` |
| source | TEXT | NOT NULL | `self_hosted` 或 `third_party` |
| capability | TEXT | NOT NULL | `chat`/`embedding`/`rerank`/`vision`/`audio` |
| context_window | INT | | 最大上下文 token 数 |
| price_in | NUMERIC | | 输入价格（每 1M tokens） |
| price_out | NUMERIC | | 输出价格（每 1M tokens） |
| latency_sla_ms | INT | | SLA 延迟上限 |
| tps | INT | | 吞吐量 tokens/second |
| rate_limit | JSONB | | `{qps_per_tenant, tpm_per_tenant}` |
| health | TEXT | 'healthy' | 健康状态 |
| tenant_access | JSONB | '[]' | 租户白名单 |

### 8.3 Redis 键设计

| Key Pattern | 用途 | TTL |
| --- | --- | --- |
| `ratelimit:{tenant}:{model}:qps` | QPS 令牌桶计数 | 1s |
| `ratelimit:{tenant}:{model}:tpm` | TPM 滑动窗口 | 60s |
| `cache:semantic:{embedding_hash}` | 语义缓存响应 | 3600s |
| `router:table` | 路由表热副本 | 无（实时同步） |
| `circuit:{provider}` | 熔断器状态 | 与冷却窗口一致 |

---

## 11. 配置与部署

### 11.1 部署形态

| 属性 | 值 |
| --- | --- |
| 必选性 | core（非 optional） |
| 命名空间 | `ai-system`（§9.2） |
| 部署方式 | Docker Compose（starter）/ K8s Deployment（standard） |
| 镜像 | 单二进制（`cmd/` + Wire 装配） |
| GPU 需求 | 否（自托管推理 vLLM 解耦到 GPU 节点组） |

### 11.2 K8s 资源配置

```yaml
resources:
  requests:
    cpu: 500m
    memory: 512Mi
  limits:
    cpu: 2
    memory: 2Gi
```

### 11.3 探针配置

| 探针 | 路径 | 说明 | initialDelaySeconds | periodSeconds |
| --- | --- | --- | --- | --- |
| 存活 | `GET /v1/healthz` | 快速返回 200 | 5 | 10 |
| 就绪 | `GET /internal/ready` | 校验 PG + Redis + ≥1 provider healthy | 5 | 10 |

### 11.4 滚动更新策略

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxSurge: 1
    maxUnavailable: 0
```

### 11.5 配置键完整列表

**文件位置**: `infrastructure/config/`

```yaml
gateway:
  listen: "0.0.0.0:8080"           # 监听地址
  upstream: "higress://ai-system"   # 上游转发目标

modelRouting:
  default: "cloud-qwen-max"         # 默认模型
  fallbackChain:                     # 降级链
    - "cloud-gpt-4o"
  costAware: true                    # 成本感知路由

ratelimit:
  backend: "redis"                   # 限流后端
  defaultQPSPerTenant: 20            # 默认每租户 QPS
  defaultTPMPerTenant: 200000        # 默认每租户 TPM

circuitBreaker:
  errorThreshold: 0.5                # 错误率阈值
  cooldownMs: 30000                  # 冷却窗口 ms

cache:
  enabled: false                     # 语义缓存开关
  semanticThreshold: 0.95            # 相似度阈值

egress:
  piiScan: true                      # PII 扫描开关
  denyEgressTenants: []              # 强制自托管租户列表
```

**配置键说明**:

| 键 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `gateway.listen` | string | `0.0.0.0:8080` | 服务监听地址 |
| `gateway.upstream` | string | `higress://ai-system` | Higress 上游地址 |
| `modelRouting.default` | string | `cloud-qwen-max` | 默认路由模型 |
| `modelRouting.fallbackChain` | []string | `[cloud-gpt-4o]` | 降级链 model_id 列表 |
| `modelRouting.costAware` | bool | `true` | 是否启用成本感知路由 |
| `ratelimit.backend` | string | `redis` | 限流实现后端 |
| `ratelimit.defaultQPSPerTenant` | int | `20` | 每租户默认 QPS |
| `ratelimit.defaultTPMPerTenant` | int | `200000` | 每租户默认 TPM |
| `circuitBreaker.errorThreshold` | float | `0.5` | 熔断错误率阈值 |
| `circuitBreaker.cooldownMs` | int | `30000` | 熔断冷却时间（毫秒） |
| `cache.enabled` | bool | `false` | 启用语义缓存 |
| `cache.semanticThreshold` | float | `0.95` | 语义缓存命中相似度阈值 |
| `egress.piiScan` | bool | `true` | 出境前 PII 脱敏 |
| `egress.denyEgressTenants` | []string | `[]` | 禁止出境的租户列表 |

### 11.6 阶段引入策略

| 阶段 | 组件 | 配置状态 |
| --- | --- | --- |
| 一~三（starter/standard） | core | 全部 core 功能开启；自托管 Adapter 关闭 |
| 四（advanced/full） | core + SelfHostedAdapter | `SelfHostedAdapter` 由 profiles `optional_disabled` 控制启用 |

### 11.7 依赖组件

| 组件 | 类型 | 必选 | 说明 |
| --- | --- | --- | --- |
| Higress | 数据面 | core | Wasm 插件: 鉴权/限流/审计 |
| Keycloak | 认证 | core | JWT 签发与校验 |
| PostgreSQL | 存储 | core | 模型目录/路由策略/审计 |
| Redis | 缓存/计数 | core | 限流/缓存/路由热副本 |
| Vault | 密钥管理 | core | Provider API Key 托管 |

---

## 追溯矩阵

| 章节 | 源文档 DESIGN.md 对应 |
| --- | --- |
| 7 API/CLI/配置接口面 | §7 |
| 8 数据模型与存储 | §8 |
| 11 配置与部署 | §11 |

> **变更记录**: v0.1 | 2026-07-17 | 初稿（从 DESIGN.md §7/§8/§11 提取）
