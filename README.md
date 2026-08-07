# MCP From Scratch

English | [中文](README.zh.md)

This is a small Go learning project for understanding the moving parts behind
Model Context Protocol style tool use. It intentionally avoids MCP SDKs in the
first stage so the host/server boundary stays visible.

It is not a complete MCP implementation. The current milestone models a small
subset of MCP `2026-07-28` over JSON-RPC and stdio:

- stateless `server/discover`
- protocol version, client identity, and client capabilities on every request
- complete result envelopes, with cache hints on discovery and list results
- `tools/list`
- `tools/call`
- JSON-RPC parse errors, invalid request errors, method-not-found errors, and
  invalid-params errors

## Protocol Baseline

The project started from a subset of MCP `2025-06-18`; that history remains in
git. The executable code now targets the `2026-07-28` protocol revision,
tracked in issues
[#10](https://github.com/shychee/mcp-from-scratch/issues/10) through
[#24](https://github.com/shychee/mcp-from-scratch/issues/24).

The first two core steps are complete: session initialization has been replaced
by stateless `server/discover`, every request is validated independently, and
successful responses use complete result envelopes. MRTR is next, followed by
Streamable HTTP and `subscriptions/listen`. OAuth, Tasks, extensions, tracing,
and interoperability work build on that core instead of blocking it.

See [the learning roadmap](docs/learning-roadmap.md) for the ordered migration,
compatibility boundaries, and links to the official specification.

## Mental Model

An agent integration has two different jobs:

- The host owns the model conversation, starts tool servers, discovers tools,
  sends tool calls, and feeds results back to the model.
- The server exposes tools through a standard protocol shape and translates
  tool calls into real work.

In this project:

```text
cmd/mcp-host
  starts cmd/mcp-server as a child process
  writes JSON-RPC requests to server stdin
  reads JSON-RPC responses from server stdout

cmd/mcp-server
  reads newline-delimited JSON-RPC requests from stdin
  validates the JSON-RPC envelope
  handles server/discover, tools/list, and tools/call
  writes JSON-RPC responses to stdout
```

## Run It

```bash
make demo
```

The demo prints each request and response:

```text
=== server/discover request ===
{ ... }

=== server/discover response ===
{ ... }

=== tools/list request ===
{ ... }

=== tools/list response ===
{ ... }

=== tools/call request ===
{ ... }

=== tools/call response ===
{ ... }
```

## Test It

```bash
make test
```

The tests are intentionally split by learning boundary:

- `internal/mcpserver` tests the server protocol behavior directly.
- `internal/host` starts a real server subprocess and verifies stdio JSON-RPC
  round trips.

## What This Implements

This project currently implements a deliberately small JSON-RPC model:

- request and response envelopes
- integer request IDs plus explicit `null` response IDs for parse errors
- standard JSON-RPC error codes used by this project
- validation for malformed JSON and invalid request envelopes
- no-response JSON-RPC notifications
- stateless request metadata validation for the `2026-07-28` protocol version
- the standard `-32022` unsupported-version error with negotiation data
- `server/discover`, `tools/list`, and `tools/call` method dispatch
- `resultType: complete` and server identity metadata on every successful result
- public cache hints for discovery and tool-list results
- deterministic tool-list ordering by tool name
- host compatibility with legacy successful results that omit `resultType`
- tool descriptions and calls backed by a small server-side registry
- defensive validation for missing, unknown, and malformed tool call arguments
- host-side tool discovery, fake model tool selection, and a transcript of
  host/server exchanges

It does not yet implement full JSON Schema validation or a real model adapter.

## Current Tool

The server exposes one toy tool:

```json
{
  "name": "echo",
  "description": "Return the text argument back to the caller.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "text": {
        "type": "string",
        "description": "Text to return."
      }
    },
    "required": ["text"]
  }
}
```

Calling it:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {
        "name": "mcp-from-scratch-host",
        "version": "0.1.0"
      },
      "io.modelcontextprotocol/clientCapabilities": {}
    },
    "name": "echo",
    "arguments": {
      "text": "hello from host"
    }
  }
}
```

Response:

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "resultType": "complete",
    "_meta": {
      "io.modelcontextprotocol/serverInfo": {
        "name": "mcp-from-scratch",
        "version": "0.1.0"
      }
    },
    "content": [
      {
        "type": "text",
        "text": "hello from host"
      }
    ]
  }
}
```

## Next Learning Steps

See [docs/learning-roadmap.md](docs/learning-roadmap.md).

## License

MIT. See [LICENSE](LICENSE).
