# MCP From Scratch

[English](README.md) | 中文

这是一个用 Go 从零理解 Model Context Protocol 工具调用链路的小型学习项目。
第一阶段刻意不使用 MCP SDK，让 host 和 server 的边界、JSON-RPC 消息形状、
stdio 传输方式都保持可见。

它不是完整 MCP 实现。当前里程碑建模了 MCP `2026-07-28` 在 JSON-RPC over
stdio 上的一个小型子集：

- 无状态 `server/discover`
- 每个请求携带协议版本、客户端 identity 和客户端 capabilities
- 完整结果信封，以及 discovery/list 结果的缓存提示
- `tools/list`
- `tools/call`
- JSON-RPC parse error、invalid request error、method-not-found error 和
  invalid-params error

## 协议基线

项目最初从 MCP `2025-06-18` 的一个子集开始，历史实现保留在 Git 中。当前可执行
代码已经迁移到 `2026-07-28` 协议版本，对应 issue
[#10](https://github.com/shychee/mcp-from-scratch/issues/10) 到
[#24](https://github.com/shychee/mcp-from-scratch/issues/24)。

核心迁移的前两步已经完成：无状态 `server/discover` 已替代会话初始化，每个请求都
会被独立校验，成功响应使用完整结果信封。下一步是 MRTR，之后依次实现 Streamable
HTTP 和 `subscriptions/listen`。OAuth、Tasks、扩展、trace 和互操作性验证建立在
核心协议之上，不阻塞核心迁移。

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
  向 stdout 写 JSON-RPC response
```

## 运行 Demo

```bash
make demo
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
```

## 运行测试

```bash
make test
```

测试按学习边界拆开：

- `internal/mcpserver` 直接测试 server 的协议行为。
- `internal/host` 启动真实 server 子进程，验证 stdio JSON-RPC 往返。

## 当前实现了什么

当前实现的是一个刻意缩小的 JSON-RPC 模型：

- request 和 response envelope
- integer request ID，以及 parse error response 里的显式 `null` ID
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
- `tools/list` 和 `tools/call` 由一个小型 server-side registry 驱动
- 对 missing、unknown、malformed tool call arguments 做防御性校验
- host-side tool discovery、fake model tool selection，以及 host/server
  exchange transcript

还没有实现完整 JSON Schema 校验，也没有接入真实模型 adapter。

## 当前 Tool

server 暴露了一个玩具工具：

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
