# MCP From Scratch

[English](README.md) | 中文

这是一个用 Go 从零理解 Model Context Protocol 工具调用链路的小型学习项目。
第一阶段刻意不使用 MCP SDK，让 host 和 server 的边界、JSON-RPC 消息形状、
stdio 传输方式都保持可见。

它不是完整 MCP 实现。当前里程碑建模了 MCP `2026-07-28` 在 JSON-RPC over
stdio 和无状态 Streamable HTTP 上的一个小型子集：

- 无状态 `server/discover`
- 每个请求携带协议版本、客户端 identity 和客户端 capabilities
- 完整结果信封，以及 discovery/list 结果的缓存提示
- 一个无状态 MRTR form elicitation 流程，并校验 request state 完整性
- 一个只接受 POST 的 Streamable HTTP endpoint，并校验现代传输头
- 通过 `subscriptions/listen` 按需投递 tool list 变更
- 协商后的 `io.modelcontextprotocol/tasks`，包含 `deferred_echo`、轮询、更新和任务通知
- richer tool result 和 JSON Schema 2020-12 输入/输出校验
- `resources/list`、`resources/read`、`prompts/list`、`prompts/get`
- 三类 catalog 的 opaque cursor 分页和 list-change 事件
- 严格的双时代 stdio 探测，以及隔离的 legacy lifecycle
- namespaced extension capability negotiation
- string 和 integer JSON-RPC request ID
- request-scoped progress、协作式 cancellation、trace context 和日志
- OAuth protected-resource metadata、Bearer 校验、发现、PKCE 和有界 scope upgrade
- OpenAI-compatible 模型 adapter，执行一次 MCP tool call 并把结果回填给模型
- JSON-RPC parse error、invalid request error、method-not-found error 和
  invalid-params error

## 协议基线

项目最初从 MCP `2025-06-18` 的一个子集开始，历史实现保留在 Git 中。当前可执行
代码已经迁移到 `2026-07-28` 协议版本，对应 issue
[#10](https://github.com/shychee/mcp-from-scratch/issues/10) 到
[#24](https://github.com/shychee/mcp-from-scratch/issues/24)。

核心迁移的前五步已经完成：无状态 `server/discover` 已替代会话初始化，每个请求都
会被独立校验，成功响应使用完整结果信封。`confirm_preview` 工具通过内嵌
`elicitation/create` input request 演示 MRTR；host 原样带回 request state 和显式
input response 后，server 才完成调用。同一个 dispatcher 现在也能通过无状态
Streamable HTTP 使用；host 也可以在收到已确认的 `subscriptions/listen` 事件后刷新
registry。Tasks、request observability、OAuth resource-server/host 和真实模型流程已经
建立在核心协议之上；最终官方 peer 互操作验证与 support matrix 是最后一组 roadmap 工作。

迁移顺序、兼容边界和官方规范链接见
[学习路线](docs/learning-roadmap.md)。

## 心智模型

一个 agent 工具集成里有两个不同角色：

- host 负责模型对话、启动 tool server、发现工具、发送工具调用、把结果交回模型。
- server 通过标准协议形状暴露工具，并把工具调用翻译成真实工作。

在这个项目里：

```text
cmd/mcp-host
  启动 cmd/mcp-server 子进程
  向 server stdin 写 JSON-RPC request
  从 server stdout 读 JSON-RPC response

cmd/mcp-server
  从 stdin 读取 newline-delimited JSON-RPC request
  验证 JSON-RPC envelope
  处理 server/discover、tools/list 和 tools/call
  当 confirm_preview 需要用户输入时返回 input_required result
  向 stdout 写 JSON-RPC response

cmd/mcp-http-demo
  在临时本地 HTTP endpoint 上启动进程内 server
  每条 JSON-RPC request 使用独立 POST
  在标准头中镜像协议版本、method 和 tool name
  运行与 stdio host 相同的 discovery 和 tool-call 流程

cmd/mcp-subscription-demo
  打开 HTTP subscriptions/listen SSE response
  验证第一条消息是带 tag 的 acknowledgement
  注册 late_echo 并接收一条带 tag 的 tools/list_changed notification
  由 host 刷新 tools/list

cmd/mcp-task-demo
  通过 HTTP 创建 durable deferred_echo task
  轮询 tasks/get、通过 tasks/update 审批，并读取 notifications/tasks

cmd/mcp-oauth-demo
  从本地 resource server 发布 protected-resource metadata
  发现本地 fake authorization server 并执行 S256 PKCE
  校验 token audience/scope 后重放一次受保护请求

cmd/mcp-model-demo
  把发现到的 MCP tool schema 发给 OpenAI-compatible endpoint
  通过 MCP 执行模型返回的 function call，并把结果回填给模型
  默认 demo 使用本地、无凭据的模型 fixture
```

## 运行 Demo

```bash
make demo

# 演示 modern probe、单次 legacy fallback、initialize 和 legacy tools/list。
make demo-legacy

# 通过无状态 Streamable HTTP 运行相同流程。
make demo-http

# 展示 request-scoped SSE progress，随后返回最终工具结果。
make demo-progress

# 展示 acknowledgement、registry 变更和 host 刷新。
make demo-subscriptions

# 创建、审批、轮询并观察一个 deferred task。
make demo-task

# 无凭据运行本地 resource-server、discovery、PKCE 和 replay 流程。
make demo-oauth

# 使用本地模型 fixture 验证真实 OpenAI-compatible HTTP adapter。
make demo-model
```

demo 会打印每一次 request 和 response：

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

=== confirm_preview input required request ===
{ ... }

=== confirm_preview input required response ===
{ ... "resultType": "input_required" ... }

=== confirm_preview retry request ===
{ ... "inputResponses": { ... }, "requestState": "..." ... }

=== confirm_preview retry response ===
{ ... "resultType": "complete" ... }
```

## 运行测试

```bash
make test
```

测试按学习边界拆开：

- `internal/mcpserver` 直接测试 server 的协议行为。
- `internal/host` 验证真实 stdio 子进程和 HTTP/SSE 往返。

## 当前实现了什么

当前实现的是一个刻意缩小的 JSON-RPC 模型：

- request 和 response envelope
- string 和 integer request ID，以及 parse error response 里的显式 `null` ID
- 项目目前用到的 JSON-RPC 标准错误码
- malformed JSON 和 invalid request envelope 校验
- 不需要 response 的 JSON-RPC notification
- 针对 `2026-07-28` 协议版本的无状态 request metadata 校验
- 带协商数据的标准 `-32022` unsupported-version error
- `server/discover`、`tools/list`、`tools/call` method dispatch
- 每个成功结果都携带 `resultType: complete` 和 server identity metadata
- discovery 和 tool list 结果携带 public cache hints
- tool list 按工具名稳定排序
- host 兼容缺少 `resultType` 的旧版成功结果
- `resultType: input_required` 和内嵌的 form-mode
  `elicitation/create` request
- capability-gated elicitation，以及使用新 JSON-RPC ID、`inputResponses` 和未修改
  opaque `requestState` 的重试
- 请求依赖客户端未声明的 capability 时返回标准 `-32021` 错误
- request state 校验绑定 tool name 和 preview arguments，新 server instance 无需隐藏
  session state 即可完成重试
- 只接受 POST 的 Streamable HTTP dispatch，并按 JSON-RPC body 校验
  `MCP-Protocol-Version`、`Mcp-Method` 和 named method 的 `Mcp-Name`
- 校验 JSON media negotiation 和单消息 framing；host 同时支持 JSON 与请求级 SSE response
- header mismatch、unsupported version、method not found 使用现代 HTTP 状态映射，
  server 不创建或回显 session ID
- ACK-first `subscriptions/listen` stream 复用 listen request ID，只投递显式接受的
  tool-list 事件，并支持并发 ID
- 合并慢 listener 尚未消费的 list-change signal，避免阻塞 registry update
- discovery 通过 `tools.listChanged: true` 声明已实现的事件能力
- HTTP disconnect 和 stdio `notifications/cancelled` 都会清理订阅；server 也可在关闭
  stream 前发送 graceful complete response
- host 收到带 tag 的 `notifications/tools/list_changed` 后刷新 `tools/list`
- 仅在双方协商 `io.modelcontextprotocol/tasks` 后创建 `resultType: task`
- `tasks/get`、`tasks/update`、`tasks/cancel` 的 owner 和 TTL 校验
- task-id 过滤的 `subscriptions/listen` ACK 子集和完整 `notifications/tasks`
- 在返回 task handle 前先持久化的 bounded `deferred_echo` workflow
- 使用密码学随机 task ID；`tasks/list` 和 `tasks/result` 保持 method-not-found
- RFC 9728 protected-resource metadata 和注入式 Bearer token 校验
- MCP dispatch 前校验 issuer、audience、expiry、subject 和 method/name scope
- 401/403 Bearer challenge、只接受 header token，且不向 tool 转发 token
- host 侧 resource/authorization-server discovery、S256 PKCE、state/issuer 精确校验、
  Client ID Metadata Documents、有界 DCR fallback、内存 token store 和一次 scope upgrade
- `tools/list` 和 `tools/call` 由一个小型 server-side registry 驱动
- 对 missing、unknown、malformed tool call arguments 做防御性校验
- host-side tool discovery 和 OpenAI-compatible Chat Completions tool schema
- 一次有界 model tool-call、MCP 执行和 model result-feedback 回合

Tasks 的 Memory repository 只保证单进程演示所需的生命周期语义，是明确的
`TaskRepository` 生产边界，不等同于生产持久化。多实例部署必须提供共享存储，
并从认证 principal 获取 owner，而不是把 `clientInfo.name` 当作真实身份。OAuth host
同样只使用内存凭据和注入式 browser callback；生产持久化与 authorization server
实现不在本项目范围。模型 adapter 接受注入的 HTTPS endpoint、model name、HTTP client
和可选 API key；默认 smoke 路径只使用本地 fixture。

## 当前 Tool

server 暴露了两个玩具工具：`echo` 直接返回文本；`confirm_preview` 经过一次 MRTR
显式确认后返回无副作用 preview。

这个学习 demo 内置的 request-state HMAC key 只用于展示跨 server instance 的无状态
完整性检查，不是生产 secret 或安全边界。真实部署必须注入并轮换受保护的 key，并把
state 绑定到经过认证的 principal、有效期和原始请求。

`echo` 的定义如下：

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

调用它：

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

响应：

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

## 后续学习步骤

见 [docs/learning-roadmap.md](docs/learning-roadmap.md)。

## License

MIT. 见 [LICENSE](LICENSE)。
