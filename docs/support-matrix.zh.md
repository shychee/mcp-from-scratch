# MCP 支持矩阵

本仓库以 MCP `2026-07-28` 为目标版本。生产包中的协议行为均为从零实现；官方 Go SDK 固定为 `v1.7.0`，只在 `internal/interop` 和 `cmd/mcp-official-fixture` 中作为互操作验证 peer。

矩阵修订日期：`2026-08-13`。

## 验证

在干净 checkout 中运行完整的无凭据验证：

```bash
make smoke
```

只运行官方 peer 互操作验证：

```bash
make interop
```

互操作测试通过 stdio 和 Streamable HTTP 验证两个方向：官方 client 可以发现、列出并调用本仓库 server；本仓库 host 可以发现、列出、调用官方 fixture，并完成一次 MRTR elicitation。同时也会执行文档约定的 legacy stdio fallback。

## 协议与传输

| 范围 | 状态 | 证据与边界 |
| --- | --- | --- |
| MCP `2026-07-28` 元数据与 `server/discover` | 支持 | `internal/mcpserver/server_test.go`、`internal/interop` 官方 client 测试 |
| JSON-RPC 字符串/整数 ID 与 result type | 支持 | `internal/protocol/types_test.go`；没有 `resultType` 的 legacy result 按 complete 解码 |
| stdio NDJSON | 支持 | `internal/mcpserver/stdio_test.go`、官方双向互操作 |
| 无状态 Streamable HTTP | 支持 | JSON 或 request-scoped SSE；官方双向互操作 |
| 有状态 HTTP session、独立 SSE GET、恢复 | 不支持 | 已从 `2026-07-28` 移除；GET/DELETE 返回 405，session ID 被忽略 |
| Legacy stdio initialize 生命周期 | 兼容支持 | 独立 `--legacy` server 和 host 单次 fallback；不会混入 modern dispatch |
| HTTP 负向状态映射 | 支持 | `internal/interop` 覆盖 `-32020`、`-32022`、`-32601`、`-32602` 与 HTTP 400/404/200 |

## Server 能力

| 能力 | 状态 | 证据与边界 |
| --- | --- | --- |
| Tools | 支持 | 发现、分页、JSON Schema 2020-12 输入/输出校验、rich ordered content、`isError`、structured content |
| Resources | 支持 | list/read、稳定分页、缓存元数据、列表变更订阅 |
| Prompts | 支持 | list/get 与参数渲染、稳定分页、缓存元数据、列表变更订阅 |
| MRTR elicitation | 支持 | 签名 opaque request state 与官方 fixture round trip；内置示例只实现 form elicitation |
| 缓存语义 | 支持 | cacheable result 包含 `ttlMs` 和 `cacheScope`；没有持久化缓存实现 |
| Subscriptions | 支持 | 显式 filter、ACK-first、subscription tag、断开清理、并发 demultiplex |
| Progress 与 cancellation | 支持 | request-scoped 有序 progress；stdio cancellation notification 与 HTTP disconnect cancellation |
| Trace context | 支持 | 合法 W3C `traceparent`、`tracestate`、`baggage` 进入 request context |
| Request logging | 部分支持，已 deprecated | opt-in request-scoped wire message 与脱敏；没有 `logging/setLevel`，没有 exporter |

## 扩展、授权与 Host

| 能力 | 状态 | 证据与边界 |
| --- | --- | --- |
| 通用扩展协商 | 支持 | namespaced opaque settings、交集启用与 core fallback |
| Tasks 扩展 | 部分支持 | durable repository interface 与完整内存生命周期；没有生产持久化 backend |
| OAuth resource server | HTTP 支持 | RFC 9728 元数据、注入式 validator、issuer/audience/expiry/scope 校验；不实现 authorization server |
| OAuth host | 支持 | 元数据发现、PKCE S256、state/issuer/resource 绑定、注册 fallback、有界 step-up；token store 仅在内存 |
| 真实模型 adapter | 部分支持 | OpenAI-compatible Chat Completions tool loop；每轮一个 tool call，smoke 使用本地 fake server，不支持 streaming |
| MCP Apps UI runtime | 不支持 | 通用扩展协商不包含 iframe、CSP、permissions 或 `ui://` 渲染 |

## 安全边界

- `make smoke` 不需要真实凭据；OAuth 和 model demo 都使用本地 fixture。
- Bearer token 只允许通过 `Authorization` header 传入，不会转发给 MCP handler，也不会写入日志。
- OAuth discovery 拒绝 userinfo、fragment、不安全 redirect、private/link-local/special-use 目标与 DNS rebinding 变化。显式 loopback 只用于本地测试和 demo。
- JSON Schema remote reference 被禁用，并限制 payload 大小和嵌套深度。
- 内存 task repository 与 OAuth store 是演示边界，不提供 crash-safe persistence。
