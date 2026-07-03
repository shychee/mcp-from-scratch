# MCP From Scratch Learning Roadmap

This roadmap keeps the project aligned with the learning goal: rebuild the MCP
tool-use path from the wire up, one narrow protocol question at a time.

Each block should be implemented and committed separately so the git history
shows the protocol becoming more complete.

## 1. JSON-RPC Notifications

Question: what changes when a JSON-RPC request has no `id`?

Target behavior:

- accept notification-shaped requests
- do not send a response for notifications
- keep parse error `id: null` distinct from notification requests with no `id`

Why it matters:

JSON-RPC request IDs are not just correlation IDs. Their presence also decides
whether the sender expects a response.

## 2. Initialize Lifecycle

Question: what happens after `initialize` succeeds?

Target behavior:

- keep `initialize` as request/response
- add `notifications/initialized` as a no-response notification
- track whether the server has completed initialization

Why it matters:

MCP has a lifecycle, not just independent method calls. The host and server need
to agree when the session is ready.

## 3. Tool Registry

Question: how does a server move from one hard-coded tool to registered tools?

Target behavior:

- introduce a small `Tool` interface
- register `echo` through the server constructor
- derive `tools/list` from the registry
- dispatch `tools/call` through the registry

Why it matters:

`tools/list` and `tools/call` should describe and invoke the same source of
truth. A registry makes that relationship visible.

## 4. Tool Argument Validation

Question: who protects the server when tool arguments are missing or malformed?

Target behavior:

- reject `tools/call` requests with missing tool names
- reject unknown tools with a clear JSON-RPC error
- reject malformed `echo` arguments with `CodeInvalidParams`

Why it matters:

`inputSchema` helps the host and model produce arguments, but the server still
owns defensive validation.

## 5. Host Tool Discovery And Dispatch

Question: what does the host do with `tools/list` before calling a tool?

Target behavior:

- parse `tools/list` into host-side tool descriptions
- add a fake model decision function
- convert the fake model decision into `tools/call`
- keep a transcript showing every host/server exchange

Why it matters:

The host is the bridge between model-facing tool calls and MCP-facing tool
calls. This step makes that bridge explicit without requiring a real LLM.

## 6. Real Model Adapter

Question: how do OpenAI function calling and MCP fit together?

Target behavior:

- convert MCP tool descriptions into model-facing tool schemas
- send a user prompt and tool list to a real model
- convert the model tool call into MCP `tools/call`
- feed the MCP result back into the model conversation

Why it matters:

Function calling standardizes how the model asks for a tool. MCP standardizes
how the host talks to the tool server.
