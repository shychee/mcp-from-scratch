# MCP From Scratch

English | [中文](README.zh.md)

This is a small Go learning project for understanding the moving parts behind
Model Context Protocol style tool use. It intentionally avoids MCP SDKs in the
first stage so the host/server boundary stays visible.

It is not a complete MCP implementation. The first milestone only models a tiny
subset of JSON-RPC over stdio:

- `initialize`
- `notifications/initialized`
- `tools/list`
- `tools/call`
- JSON-RPC parse errors, invalid request errors, method-not-found errors, and
  invalid-params errors

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
  handles initialize, tools/list, and tools/call
  writes JSON-RPC responses to stdout
```

## Run It

```bash
make demo
```

The demo prints each request and response:

```text
=== initialize request ===
{ ... }

=== initialize response ===
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
- initialize lifecycle tracking through `notifications/initialized`
- MCP-like `initialize`, `tools/list`, and `tools/call` method dispatch
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
