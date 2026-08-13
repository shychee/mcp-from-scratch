# MCP Support Matrix

This repository targets MCP `2026-07-28`. Protocol behavior in production packages is implemented from scratch. The official Go SDK is pinned to `v1.7.0` only as an interoperability peer in `internal/interop` and `cmd/mcp-official-fixture`.

Matrix revision: `2026-08-13`.

## Verification

Run the complete credential-free gate from a clean checkout:

```bash
make smoke
```

Run only official-peer interoperability checks:

```bash
make interop
```

The interoperability suite proves both directions over stdio and Streamable HTTP: the official client discovers, lists, and calls this server; this host discovers, lists, calls, and completes an MRTR elicitation against an official fixture. It also exercises the documented legacy stdio fallback.

## Protocol and Transport

| Area | Status | Evidence and boundary |
| --- | --- | --- |
| MCP `2026-07-28` metadata and `server/discover` | Supported | `internal/mcpserver/server_test.go`, official client tests in `internal/interop` |
| JSON-RPC string/integer IDs and result types | Supported | `internal/protocol/types_test.go`; legacy results without `resultType` decode as complete |
| stdio NDJSON | Supported | `internal/mcpserver/stdio_test.go`, official bidirectional interop |
| Stateless Streamable HTTP | Supported | JSON or request-scoped SSE; official bidirectional interop |
| Stateful HTTP sessions, standalone SSE GET, resumability | Unsupported | Removed by `2026-07-28`; GET/DELETE return 405 and session IDs are ignored |
| Legacy stdio initialize lifecycle | Supported for compatibility | Isolated `--legacy` server and one-time host fallback; not mixed into modern dispatch |
| HTTP negative mapping | Supported | `internal/interop`: `-32020`, `-32022`, `-32601`, `-32602`, HTTP 400/404/200 mapping |

## Server Features

| Feature | Status | Evidence and boundary |
| --- | --- | --- |
| Tools | Supported | Discovery, pagination, JSON Schema 2020-12 input/output validation, rich ordered content, `isError`, structured content |
| Resources | Supported | List/read, stable pagination, cache metadata, list-change subscriptions |
| Prompts | Supported | List/get with arguments, stable pagination, cache metadata, list-change subscriptions |
| MRTR elicitation | Supported | Signed opaque request state and official fixture round trip; form elicitation only in the built-in example |
| Cache semantics | Supported | `ttlMs` and `cacheScope` on cacheable results; no persistent cache implementation |
| Subscriptions | Supported | Explicit filters, ACK-first, subscription tagging, disconnect cleanup, concurrent demultiplexing |
| Progress and cancellation | Supported | Request-scoped ordered progress; stdio cancellation notification and HTTP disconnect cancellation |
| Trace context | Supported | Valid W3C `traceparent`, `tracestate`, and `baggage` reach request context |
| Request logging | Partial, deprecated | Opt-in request-scoped wire messages with redaction; no `logging/setLevel`, no exporter |

## Extensions, Authorization, and Host

| Feature | Status | Evidence and boundary |
| --- | --- | --- |
| Generic extension negotiation | Supported | Namespaced opaque settings; intersection activation and core fallback |
| Tasks extension | Partial | Durable repository interface and complete in-memory lifecycle; no persistent production backend |
| OAuth resource server | Supported for HTTP | RFC 9728 metadata, injected validator, issuer/audience/expiry/scope checks; no authorization server |
| OAuth host | Supported | Metadata discovery, PKCE S256, state/issuer/resource binding, registration fallbacks, bounded step-up; in-memory token store only |
| Real model adapter | Partial | OpenAI-compatible Chat Completions tool loop; one tool call per turn, local fake server in smoke, no streaming |
| MCP Apps UI runtime | Unsupported | Generic extension negotiation does not implement iframe, CSP, permissions, or `ui://` rendering |

## Security Boundaries

- No real credentials are needed by `make smoke`; OAuth and model demos use local fixtures.
- Bearer tokens are accepted only in the `Authorization` header, are not forwarded to MCP handlers, and are not logged.
- OAuth discovery rejects userinfo, fragments, unsafe redirects, private/link-local/special-use destinations, and DNS rebinding changes. Explicit loopback support is limited to local tests and demos.
- JSON Schema remote references are disabled; payload size and nesting are bounded.
- The in-memory task repository and OAuth store are demonstration boundaries, not crash-safe persistence.
