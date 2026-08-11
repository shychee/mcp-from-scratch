package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const (
	protocolVersion    = protocol.Version20260728
	discoveryTTLMillis = 60 * 60 * 1000
	toolsListTTLMillis = 5 * 60 * 1000
	serverName         = "mcp-from-scratch"
	serverVersion      = "0.1.0"
)

type Tool interface {
	Definition() tool
	Call(toolCallInvocation) (toolCallResult, error)
}

type Server struct {
	tools []Tool
}

type discoverResult struct {
	protocol.CacheableResult
	SupportedVersions []string     `json:"supportedVersions"`
	Capabilities      capabilities `json:"capabilities"`
}

type capabilities struct {
	Tools map[string]any `json:"tools"`
}

type unsupportedProtocolVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}

type toolsListResult struct {
	protocol.CacheableResult
	Tools []tool `json:"tools"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolCallResult struct {
	protocol.Result
	Content       []contentBlock          `json:"content,omitempty"`
	InputRequests map[string]inputRequest `json:"inputRequests,omitempty"`
	RequestState  string                  `json:"requestState,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallParams struct {
	protocol.RequestParams
	Name           string                     `json:"name"`
	Arguments      json.RawMessage            `json:"arguments"`
	InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
	RequestState   string                     `json:"requestState,omitempty"`
}

type toolCallInvocation struct {
	Arguments          json.RawMessage
	InputResponses     map[string]json.RawMessage
	RequestState       string
	ClientCapabilities map[string]any
}

type echoArguments struct {
	Text string `json:"text"`
}

func New(tools ...Tool) *Server {
	if len(tools) == 0 {
		tools = []Tool{echoTool{}, confirmPreviewTool{}}
	}
	return &Server{
		tools: tools,
	}
}

// Handle dispatches valid JSON-RPC requests to the MCP method implementation.
func (s *Server) Handle(_ context.Context, request protocol.Request) protocol.Response {
	response := protocol.Response{
		JSONRPC: "2.0",
		ID:      request.ID,
	}
	if requestError := validateRequestMetadata(request.Params); requestError != nil {
		response.Error = requestError
		return response
	}

	switch request.Method {
	case "server/discover":
		response.Result = mustMarshal(discoverResult{
			CacheableResult: protocol.CacheableResult{
				Result:     newResult(),
				TTLMillis:  discoveryTTLMillis,
				CacheScope: protocol.CacheScopePublic,
			},
			SupportedVersions: []string{protocolVersion},
			Capabilities: capabilities{
				Tools: map[string]any{},
			},
		})
	case "tools/list":
		tools := make([]tool, 0, len(s.tools))
		for _, t := range s.tools {
			tools = append(tools, t.Definition())
		}
		sort.Slice(tools, func(i, j int) bool {
			return tools[i].Name < tools[j].Name
		})
		response.Result = mustMarshal(toolsListResult{
			CacheableResult: protocol.CacheableResult{
				Result:     newResult(),
				TTLMillis:  toolsListTTLMillis,
				CacheScope: protocol.CacheScopePublic,
			},
			Tools: tools,
		})
	case "tools/call":
		result, err := s.callTool(request.Params)
		if err != nil {
			response.Error = protocol.NewError(protocol.CodeInvalidParams, err.Error())
			return response
		}
		if result.ResultType == "" {
			result.ResultType = protocol.ResultTypeComplete
		}
		result.Meta = newResult().Meta
		response.Result = mustMarshal(result)
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
	return nil
}

type echoTool struct{}

func (echoTool) Definition() tool {
	return tool{
		Name:        "echo",
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

func (s *Server) callTool(raw json.RawMessage) (toolCallResult, error) {
	var params toolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return toolCallResult{}, fmt.Errorf("decode tool call params: %w", err)
	}
	if params.Name == "" {
		return toolCallResult{}, fmt.Errorf("missing tool name")
	}

	for _, registeredTool := range s.tools {
		definition := registeredTool.Definition()
		if definition.Name == params.Name {
			if err := validateToolArguments(definition, params.Arguments); err != nil {
				return toolCallResult{}, err
			}
			return registeredTool.Call(toolCallInvocation{
				Arguments:          params.Arguments,
				InputResponses:     params.InputResponses,
				RequestState:       params.RequestState,
				ClientCapabilities: params.Meta.ClientCapabilities,
			})
		}
	}

	return toolCallResult{}, fmt.Errorf("unknown tool %q", params.Name)
}

func validateToolArguments(definition tool, raw json.RawMessage) error {
	schema := definition.InputSchema
	if !requiresObjectArguments(schema) {
		return nil
	}

	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return fmt.Errorf("tool arguments must be an object")
	}

	for _, name := range requiredProperties(schema) {
		if _, ok := arguments[name]; !ok {
			return fmt.Errorf("missing required argument %q", name)
		}
	}

	for name, rawValue := range arguments {
		expectedType, ok := propertyType(schema, name)
		if !ok {
			continue
		}
		if expectedType == "string" && !isJSONString(rawValue) {
			return fmt.Errorf("argument %q must be a string", name)
		}
	}
	return nil
}

func requiresObjectArguments(schema map[string]any) bool {
	return schemaType(schema) == "object" || len(requiredProperties(schema)) > 0
}

func propertyType(schema map[string]any, name string) (string, bool) {
	rawProperties, ok := schema["properties"].(map[string]any)
	if !ok {
		return "", false
	}

	rawProperty, ok := rawProperties[name].(map[string]any)
	if !ok {
		return "", false
	}

	propertyType, ok := rawProperty["type"].(string)
	return propertyType, ok
}

func isJSONString(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func schemaType(schema map[string]any) string {
	schemaType, _ := schema["type"].(string)
	return schemaType
}

func requiredProperties(schema map[string]any) []string {
	rawRequired, ok := schema["required"]
	if !ok {
		return nil
	}

	required, ok := rawRequired.([]string)
	if ok {
		return required
	}

	values, ok := rawRequired.([]any)
	if !ok {
		return nil
	}

	names := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if ok {
			names = append(names, name)
		}
	}
	return names
}

func mustMarshal(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
