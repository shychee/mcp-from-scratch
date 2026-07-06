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

## 6. Server-Side Tool Schema Enforcement

Question: what is the difference between publishing a schema and enforcing it?

Target behavior:

- keep `inputSchema` in `tools/list` as the public contract
- validate `tools/call` arguments against each tool's schema before dispatch
- return `CodeInvalidParams` for schema violations
- keep tool-specific business validation inside the tool implementation

Why it matters:

Schemas are not just hints for the model. The server is the trust boundary, so
it still needs to reject malformed or unsafe inputs even when a client or model
claims the call is valid.

## 7. Rich Tool Results

Question: what can a tool return besides one text block?

Target behavior:

- support multiple text content blocks in `tools/call` results
- add `isError` for tool execution failures that are not protocol errors
- add `structuredContent` for machine-readable output
- optionally describe `outputSchema` in tool definitions

Why it matters:

MCP separates protocol errors from tool execution errors. It also lets tools
return both human-readable content and structured data that clients and models
can handle more reliably.

## 8. Resources

Question: how does a server expose readable context instead of executable
actions?

Target behavior:

- declare the `resources` capability during `initialize`
- implement `resources/list`
- implement `resources/read`
- use URI-based resource identity such as `demo://...`

Why it matters:

Tools are actions. Resources are data. A model may need logs, files, schemas, or
application state as context without invoking an operation that changes the
world.

## 9. Prompts

Question: how does a server expose reusable prompt templates?

Target behavior:

- declare the `prompts` capability during `initialize`
- implement `prompts/list`
- implement `prompts/get`
- render prompt messages from simple arguments

Why it matters:

Prompts are user-controlled workflows or templates. They let a server teach a
host how to ask good domain-specific questions without hard-coding those
prompts into the host.

## 10. Pagination And List Change Notifications

Question: what happens when a server has more tools, resources, or prompts than
fit comfortably in one response?

Target behavior:

- accept optional `cursor` params for list methods
- return optional `nextCursor`
- advertise `listChanged` only when the server can send matching notifications
- implement one list-changed notification path after a registry update

Why it matters:

Real MCP servers may expose dynamic or large catalogs. Pagination and change
notifications keep discovery explicit without requiring clients to constantly
reload everything.

## 11. Lifecycle And Transport Hardening

Question: what protocol rules should the server enforce before it is treated as
a real MCP server?

Target behavior:

- reject normal requests before `initialize`, except allowed lifecycle probes
- negotiate or reject unsupported protocol versions deliberately
- keep stdout strictly JSON-RPC-only and use stderr for logs
- preserve newline-delimited stdio framing

Why it matters:

The server is not only method handlers. A usable MCP server also owns session
state, version/capability negotiation, and transport discipline.

## 12. Real Model Adapter

Question: how do model-native tool calls and MCP fit together?

Target behavior:

- convert MCP tool descriptions into model-facing tool schemas
- send a user prompt and tool list to a real model
- convert the model tool call into MCP `tools/call`
- feed the MCP result back into the model conversation

Why it matters:

Function calling standardizes how the model asks for a tool. MCP standardizes
how the host talks to the tool server. This is useful, but it can wait until the
server-side protocol surface is more complete.
