# MCP From Scratch

[English](README.md) | 中文

这是一个用 Go 从零理解 Model Context Protocol 工具调用链路的小型学习项目。
第一阶段刻意不使用 MCP SDK，让 host 和 server 的边界、JSON-RPC 消息形状、
stdio 传输方式都保持可见。

它不是完整 MCP 实现。当前里程碑只建模了 JSON-RPC over stdio 的一个很小子集：

- `initialize`
- `notifications/initialized`
- `tools/list`
- `tools/call`
- JSON-RPC parse error、invalid request error、method-not-found error 和
  invalid-params error

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
  处理 initialize、tools/list 和 tools/call
  向 stdout 写 JSON-RPC response
```

## 运行 Demo

```bash
make demo
```

demo 会打印每一次 request 和 response：

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
- 通过 `notifications/initialized` 跟踪 initialize lifecycle
- 类 MCP 的 `initialize`、`tools/list`、`tools/call` method dispatch
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
