package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const (
	protocolVersion    = protocol.Version20260728
	discoveryTTLMillis = 60 * 60 * 1000
	toolsListTTLMillis = 5 * 60 * 1000
	serverName         = "mcp-from-scratch"
	serverVersion      = "0.1.0"
)

// Tool is an MCP tool implementation.
type Tool interface {
	Definition() ToolDefinition
	Call(ToolInvocation) (ToolResult, error)
}

// ToolDefinition is the public description returned by tools/list.
type ToolDefinition struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
}

// ToolInvocation is the validated input passed to a tool handler.
type ToolInvocation struct {
	Context            context.Context
	Arguments          json.RawMessage
	InputResponses     map[string]json.RawMessage
	RequestState       string
	ClientCapabilities map[string]any
}

// ToolResult is the result returned by an MCP tool handler.
type ToolResult struct {
	protocol.Result
	Content           []ContentBlock          `json:"content,omitempty"`
	StructuredContent any                     `json:"structuredContent,omitempty"`
	IsError           bool                    `json:"isError,omitempty"`
	InputRequests     map[string]inputRequest `json:"inputRequests,omitempty"`
	RequestState      string                  `json:"requestState,omitempty"`
}

// ContentBlock is a content item returned by a tool.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Internal aliases preserve the original in-package API while exposing the
// same concrete types to consumers.
type tool = ToolDefinition
type toolCallInvocation = ToolInvocation
type toolCallResult = ToolResult
type contentBlock = ContentBlock

type registeredTool struct {
	tool         Tool
	definition   ToolDefinition
	inputSchema  *jsonschema.Resolved
	outputSchema *jsonschema.Resolved
}

type Server struct {
	mu            sync.RWMutex
	tools         map[string]registeredTool
	catalogs      catalogRegistry
	subscriptions map[*subscription]struct{}
	extensions    protocol.Extensions
	tasks         TaskRepository
}

type discoverResult struct {
	protocol.CacheableResult
	SupportedVersions []string     `json:"supportedVersions"`
	Capabilities      capabilities `json:"capabilities"`
}

type capabilities struct {
	Tools      map[string]any      `json:"tools"`
	Resources  map[string]any      `json:"resources,omitempty"`
	Prompts    map[string]any      `json:"prompts,omitempty"`
	Logging    map[string]any      `json:"logging"`
	Extensions protocol.Extensions `json:"extensions,omitempty"`
}

type unsupportedProtocolVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}

type toolsListResult struct {
	protocol.CacheableResult
	Tools []ToolDefinition `json:"tools"`
}

type toolCallParams struct {
	protocol.RequestParams
	Name           string                     `json:"name"`
	Arguments      json.RawMessage            `json:"arguments"`
	InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
	RequestState   string                     `json:"requestState,omitempty"`
}

type echoArguments struct {
	Text string `json:"text"`
}

func New(tools ...Tool) *Server {
	server, err := NewServer(tools...)
	if err != nil {
		panic(err)
	}
	return server
}

// NewServer constructs a server and returns registration errors instead of
// silently dropping an invalid tool.
func NewServer(tools ...Tool) (*Server, error) {
	if len(tools) == 0 {
		tools = []Tool{newEchoTool("echo"), confirmPreviewTool{}}
	}
	server := &Server{
		tools:         make(map[string]registeredTool),
		subscriptions: make(map[*subscription]struct{}),
		tasks:         NewMemoryTaskRepository(nil),
	}
	for _, tool := range tools {
		if err := server.RegisterTool(tool); err != nil {
			return nil, err
		}
	}
	return server, nil
}

// SetExtensions replaces the server's advertised extension settings.
func (s *Server) SetExtensions(extensions protocol.Extensions) error {
	if err := extensions.Validate(); err != nil {
		return err
	}
	cloned := protocol.IntersectExtensions(extensions, extensions)
	s.mu.Lock()
	s.extensions = cloned
	s.mu.Unlock()
	return nil
}

func (s *Server) extensionSnapshot() protocol.Extensions {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return protocol.IntersectExtensions(s.extensions, s.extensions)
}

// RegisterTool validates and atomically adds a tool to the name-indexed
// registry. Invalid or duplicate tools never become visible to callers.
func (s *Server) RegisterTool(tool Tool) error {
	entry, err := prepareTool(tool)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if _, exists := s.tools[entry.definition.Name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("register tool: duplicate tool %q", entry.definition.Name)
	}
	s.tools[entry.definition.Name] = entry
	s.mu.Unlock()
	s.publishListChanged(toolListChange)
	return nil
}

// Handle dispatches valid JSON-RPC requests to the MCP method implementation.
func (s *Server) Handle(ctx context.Context, request protocol.Request) protocol.Response {
	response := protocol.Response{
		JSONRPC: "2.0",
		ID:      request.ID,
	}
	if requestError := validateRequestMetadata(request.Params); requestError != nil {
		response.Error = requestError
		return response
	}
	ctx = withObservabilityContext(ctx, request.Params)

	switch request.Method {
	case "server/discover":
		resources, prompts := s.catalogCapabilities()
		response.Result = mustMarshal(discoverResult{
			CacheableResult: protocol.CacheableResult{
				Result:     newResult(),
				TTLMillis:  discoveryTTLMillis,
				CacheScope: protocol.CacheScopePublic,
			},
			SupportedVersions: []string{protocolVersion},
			Capabilities: capabilities{
				Tools:      map[string]any{"listChanged": true},
				Resources:  resources,
				Prompts:    prompts,
				Logging:    map[string]any{},
				Extensions: s.extensionSnapshot(),
			},
		})
	case "tools/list":
		result, err := s.listTools(request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "resources/list":
		result, err := s.listResources(request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "resources/read":
		result, err := s.readResource(request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "prompts/list":
		result, err := s.listPrompts(request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "prompts/get":
		result, err := s.getPrompt(request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "tools/call":
		if taskResult, taskError, handled := s.callDeferredEchoTask(ctx, request.Params); handled {
			if taskError != nil {
				response.Error = taskError
				return response
			}
			response.Result = taskResult
			return response
		}
		result, err := s.callTool(ctx, request.Params)
		if err != nil {
			if protocolError, ok := err.(*protocol.Error); ok {
				response.Error = protocolError
			} else {
				response.Error = protocol.NewError(protocol.CodeInvalidParams, err.Error())
			}
			return response
		}
		if result.ResultType == "" {
			result.ResultType = protocol.ResultTypeComplete
		}
		result.Meta = newResult().Meta
		response.Result = mustMarshal(result)
	case "tasks/get":
		result, err := s.getTask(ctx, request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "tasks/update":
		result, err := s.updateTask(ctx, request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	case "tasks/cancel":
		result, err := s.cancelTask(ctx, request.Params)
		if err != nil {
			response.Error = err
			return response
		}
		response.Result = result
	default:
		response.Error = protocol.NewError(protocol.CodeMethodNotFound, "method not found")
	}

	return response
}

func newResult() protocol.Result {
	return protocol.Result{
		ResultType: protocol.ResultTypeComplete,
		Meta: protocol.ResultMeta{
			ServerInfo: protocol.Implementation{
				Name:    serverName,
				Version: serverVersion,
			},
		},
	}
}

func validateRequestMetadata(raw json.RawMessage) *protocol.Error {
	var params protocol.RequestParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return protocol.NewError(protocol.CodeInvalidParams, "missing or invalid request metadata")
	}
	if params.Meta.ProtocolVersion == "" || params.Meta.ClientCapabilities == nil {
		return protocol.NewError(protocol.CodeInvalidParams, "missing or invalid request metadata")
	}
	if params.Meta.ProtocolVersion != protocolVersion {
		return protocol.NewErrorWithData(
			protocol.CodeUnsupportedProtocolVersion,
			"unsupported protocol version",
			unsupportedProtocolVersionData{
				Supported: []string{protocolVersion},
				Requested: params.Meta.ProtocolVersion,
			},
		)
	}
	if params.Meta.LogLevel != "" {
		if _, ok := logLevelRank(params.Meta.LogLevel); !ok {
			return protocol.NewError(protocol.CodeInvalidParams, "invalid request log level")
		}
	}
	var rawParams struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if json.Unmarshal(raw, &rawParams) == nil && len(rawParams.Meta.ProgressToken) != 0 &&
		!validProgressToken(rawParams.Meta.ProgressToken) {
		return protocol.NewError(protocol.CodeInvalidParams, "invalid progress token")
	}
	return nil
}

type echoTool struct {
	name string
}

func newEchoTool(name string) Tool {
	return echoTool{name: name}
}

func (t echoTool) Definition() tool {
	return tool{
		Name:        t.name,
		Description: "Return the text argument back to the caller.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Text to return.",
				},
			},
			"required": []string{"text"},
		},
	}
}

func (echoTool) Call(invocation toolCallInvocation) (toolCallResult, error) {
	var args echoArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return toolCallResult{}, fmt.Errorf("decode echo arguments: %w", err)
	}
	return toolCallResult{
		Content: []contentBlock{
			{
				Type: "text",
				Text: args.Text,
			},
		},
	}, nil
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (toolCallResult, error) {
	var params toolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return toolCallResult{}, fmt.Errorf("decode tool call params: %w", err)
	}
	if params.Name == "" {
		return toolCallResult{}, fmt.Errorf("missing tool name")
	}

	registeredTool, ok := s.tool(params.Name)
	if !ok {
		return toolCallResult{}, fmt.Errorf("unknown tool %q", params.Name)
	}
	if err := validateToolArguments(registeredTool.inputSchema, params.Arguments); err != nil {
		return toolCallResult{}, err
	}
	result, err := registeredTool.tool.Call(toolCallInvocation{
		Context:            ctx,
		Arguments:          params.Arguments,
		InputResponses:     params.InputResponses,
		RequestState:       params.RequestState,
		ClientCapabilities: params.Meta.ClientCapabilities,
	})
	if err != nil {
		return toolCallResult{}, err
	}
	if registeredTool.outputSchema != nil {
		if result.StructuredContent == nil {
			return toolCallResult{}, protocol.NewErrorWithData(
				protocol.CodeInternalError,
				"tool output is missing structured content required by output schema",
				map[string]any{"tool": params.Name},
			)
		}
		if err := registeredTool.outputSchema.Validate(result.StructuredContent); err != nil {
			return toolCallResult{}, protocol.NewErrorWithData(
				protocol.CodeInternalError,
				"tool output does not match output schema",
				map[string]any{"tool": params.Name},
			)
		}
	}
	return result, nil
}

func (s *Server) tool(name string) (registeredTool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tool, ok := s.tools[name]
	return tool, ok
}

func (s *Server) toolSnapshot() []registeredTool {
	s.mu.RLock()
	tools := make([]registeredTool, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, tool)
	}
	s.mu.RUnlock()
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].definition.Name < tools[j].definition.Name
	})
	return tools
}

func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
