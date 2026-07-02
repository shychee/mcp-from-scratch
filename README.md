# MCP From Scratch

This is a small Go learning project for understanding the moving parts behind
Model Context Protocol style tool use. It intentionally avoids MCP SDKs in the
first stage so the host/server boundary stays visible.

It is not a complete MCP implementation. The first milestone only models a tiny
subset of JSON-RPC over stdio:

- `initialize`
- `tools/list`
- `tools/call`

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

1. Add a second tool such as `add` to see how `tools/list` schema maps to
   `tools/call` arguments.
2. Add request validation and JSON-RPC error cases.
3. Replace the mock host decision with an OpenAI function calling adapter.
4. Compare this hand-written version with the official MCP SDK.
