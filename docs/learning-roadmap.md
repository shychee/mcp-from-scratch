# MCP From Scratch Learning Roadmap

This roadmap keeps the project aligned with the learning goal: rebuild the MCP
tool-use path from the wire up, one narrow protocol question at a time.

Each block should be implemented and committed separately so the git history
shows the protocol becoming more complete.

## Protocol Baseline And Target

The historical baseline was a deliberately small subset of MCP `2025-06-18`:
JSON-RPC over stdio, `initialize`, `notifications/initialized`, `tools/list`,
and `tools/call`. The executable code now targets MCP `2026-07-28` and uses
stateless discovery, per-request metadata, and complete/cacheable result
envelopes.

The target changes the project's protocol model in several important ways:

- requests are stateless and carry protocol version, client identity, and
  client capabilities in `_meta`
- `server/discover` replaces the initialization handshake
- successful results declare a `resultType`; discovery and list results also
  carry cache hints
- model-requested tool results (MRTR) replace the old Sampling direction
- `subscriptions/listen` provides a general streaming subscription mechanism
- extensions are negotiated through capabilities instead of being assumed
- Streamable HTTP is the target network transport; legacy HTTP+SSE is only a
  compatibility concern

Primary references:

- [MCP 2026-07-28 specification](https://modelcontextprotocol.io/specification/2026-07-28)
- [MCP 2026-07-28 key changes](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
- [Versioning and compatibility](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning)
- [`server/discover`](https://modelcontextprotocol.io/specification/2026-07-28/server/discover)
- [Multi Round-Trip Requests (MRTR)](https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/mrtr)
- [MCP 2026-07-28 TypeScript schema](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/schema/2026-07-28/schema.ts)
- [MCP specification repository](https://github.com/modelcontextprotocol/modelcontextprotocol)

## Ordered 2026-07-28 Migration

### Core protocol path

1. [x] [#11 Replace initialization with stateless discovery](https://github.com/shychee/mcp-from-scratch/issues/11)
2. [x] [#12 Adopt complete and cacheable result envelopes](https://github.com/shychee/mcp-from-scratch/issues/12)
3. [x] [#13 Implement one stateless MRTR elicitation flow](https://github.com/shychee/mcp-from-scratch/issues/13)
4. [x] [#14 Add a stateless Streamable HTTP tool-call path](https://github.com/shychee/mcp-from-scratch/issues/14)
5. [x] [#15 Deliver tool list changes through subscriptions/listen](https://github.com/shychee/mcp-from-scratch/issues/15)

This order first makes the stdio protocol correct, then adds one model-facing
request/response path, and only then adds network transport and streaming.

### Compatibility and correctness

- [x] [#16](https://github.com/shychee/mcp-from-scratch/issues/16) isolates
  `2025-06-18` compatibility from the modern implementation. Removed
  initialization and deprecated Roots, Sampling, and Logging behavior belong
  there, not in the modern core.
- [x] [#17](https://github.com/shychee/mcp-from-scratch/issues/17) upgrades the
  teaching validator to JSON Schema 2020-12.
- [#24](https://github.com/shychee/mcp-from-scratch/issues/24) verifies the
  result against independent MCP implementations and publishes a support
  matrix.

### Optional standards-oriented enhancements

- [x] [#18](https://github.com/shychee/mcp-from-scratch/issues/18) provides generic
  extension negotiation, including the capability foundation for MCP Apps.
- [#19](https://github.com/shychee/mcp-from-scratch/issues/19) implements one
  durable Tasks extension workflow.
- [#20](https://github.com/shychee/mcp-from-scratch/issues/20) makes the HTTP
  server an OAuth-protected resource server; it does not add an authorization
  server to this repository.
- [#21](https://github.com/shychee/mcp-from-scratch/issues/21) adds OAuth
  discovery and PKCE to the HTTP host.
- [#22](https://github.com/shychee/mcp-from-scratch/issues/22) adds progress and
  cancellation.
- [#23](https://github.com/shychee/mcp-from-scratch/issues/23) propagates trace
  context and adds request-scoped logging.

Resources and prompts remain planned protocol surfaces. Their list methods must
follow the modern request metadata, complete/cacheable result, pagination, and
subscription rules. They must not reintroduce an initialization lifecycle.
The real model adapter remains after the protocol and interoperability work so
model integration does not hide wire-level mistakes.

## Completed Foundation Milestones

The following milestones explain how the current `2025-06-18` teaching
baseline was assembled. They remain useful history while the modern migration
replaces their lifecycle assumptions.

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

Historical note: this milestone describes the `2025-06-18` baseline. The
modern path removes this session state in favor of `server/discover` and
per-request metadata.

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

Target behavior after the core migration:

- declare the `resources` capability through `server/discover`
- implement `resources/list`
- implement `resources/read`
- use URI-based resource identity such as `demo://...`

Why it matters:

Tools are actions. Resources are data. A model may need logs, files, schemas, or
application state as context without invoking an operation that changes the
world.

## 9. Prompts

Question: how does a server expose reusable prompt templates?

Target behavior after the core migration:

- declare the `prompts` capability through `server/discover`
- implement `prompts/list`
- implement `prompts/get`
- render prompt messages from simple arguments

Why it matters:

Prompts are user-controlled workflows or templates. They let a server teach a
host how to ask good domain-specific questions without hard-coding those
prompts into the host.

## 10. Pagination And List Subscriptions

Question: what happens when a server has more tools, resources, or prompts than
fit comfortably in one response?

Target behavior:

- accept optional `cursor` params for list methods
- return optional `nextCursor`
- return complete, cacheable list results
- expose list changes through `subscriptions/listen`

Why it matters:

Real MCP servers may expose dynamic or large catalogs. Pagination and change
notifications keep discovery explicit without requiring clients to constantly
reload everything.

## 11. Stateless Protocol And Transport Hardening

Question: what protocol rules should the server enforce before it is treated as
a real MCP server?

Target behavior:

- validate protocol version and client capabilities on every request
- reject unsupported protocol versions with the standard MCP error
- keep stdout strictly JSON-RPC-only and use stderr for logs
- preserve newline-delimited stdio framing
- add Streamable HTTP without introducing server-side session state

Why it matters:

The server is not only method handlers. A usable modern MCP server also owns
per-request version/capability validation and transport discipline.

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
server-side protocol surface and interoperability matrix are more complete.
