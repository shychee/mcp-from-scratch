package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const defaultModelResponseLimit = 1 << 20

type ModelToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ModelMessage struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ModelToolCall
}

type ModelRequest struct {
	Messages []ModelMessage
	Tools    []ToolDescription
}

type ModelAdapter interface {
	Complete(context.Context, ModelRequest) (ModelMessage, error)
}

type OpenAICompatibleConfig struct {
	Endpoint         string
	Model            string
	APIKey           string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type OpenAICompatibleAdapter struct {
	config OpenAICompatibleConfig
}

func NewOpenAICompatibleAdapter(config OpenAICompatibleConfig) (*OpenAICompatibleAdapter, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || !endpoint.IsAbs() || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("model endpoint must be an absolute HTTP URL")
	}
	if endpoint.User != nil || endpoint.Fragment != "" {
		return nil, fmt.Errorf("model endpoint must not contain userinfo or fragment")
	}
	if endpoint.Scheme == "http" && !isLoopbackModelHost(endpoint.Hostname()) {
		return nil, fmt.Errorf("model endpoint must use https or loopback http")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	client := *config.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTPClient = &client
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = defaultModelResponseLimit
	}
	if config.MaxResponseBytes < 1 {
		return nil, fmt.Errorf("model response limit must be positive")
	}
	return &OpenAICompatibleAdapter{config: config}, nil
}

func isLoopbackModelHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (adapter *OpenAICompatibleAdapter) Complete(ctx context.Context, request ModelRequest) (ModelMessage, error) {
	body, err := json.Marshal(openAIChatRequest{
		Model:    adapter.config.Model,
		Messages: openAIMessages(request.Messages),
		Tools:    openAIToolsFromToolDescriptions(request.Tools),
	})
	if err != nil {
		return ModelMessage{}, fmt.Errorf("encode model request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return ModelMessage{}, fmt.Errorf("create model request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if adapter.config.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+adapter.config.APIKey)
	}
	response, err := adapter.config.HTTPClient.Do(httpRequest)
	if err != nil {
		return ModelMessage{}, fmt.Errorf("send model request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, adapter.config.MaxResponseBytes+1))
	if err != nil {
		return ModelMessage{}, fmt.Errorf("read model response: %w", err)
	}
	if int64(len(data)) > adapter.config.MaxResponseBytes {
		return ModelMessage{}, fmt.Errorf("model response exceeds %d bytes", adapter.config.MaxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelMessage{}, fmt.Errorf("model request failed with status %d", response.StatusCode)
	}
	var completion openAIChatResponse
	if err := json.Unmarshal(data, &completion); err != nil {
		return ModelMessage{}, fmt.Errorf("decode model response: %w", err)
	}
	if len(completion.Choices) != 1 {
		return ModelMessage{}, fmt.Errorf("model response must contain exactly one choice")
	}
	return modelMessageFromOpenAI(completion.Choices[0].Message)
}

type ModelTranscript struct {
	Prompt       string
	Tools        []ToolDescription
	FirstModel   ModelMessage
	ToolRequest  protocol.Request
	ToolResponse protocol.Response
	FinalModel   ModelMessage
}

func RunHTTPModelDemo(ctx context.Context, endpoint string, adapter ModelAdapter, prompt string) (ModelTranscript, error) {
	if adapter == nil {
		return ModelTranscript{}, fmt.Errorf("model adapter is required")
	}
	client := &httpRPCClient{ctx: ctx, endpoint: endpoint, client: http.DefaultClient}
	requestParamsJSON, err := json.Marshal(protocol.RequestParams{Meta: clientRequestMeta()})
	if err != nil {
		return ModelTranscript{}, fmt.Errorf("encode request metadata: %w", err)
	}
	discovery, err := client.call(protocol.Request{JSONRPC: "2.0", ID: protocol.ID(1), Method: "server/discover", Params: requestParamsJSON})
	if err != nil {
		return ModelTranscript{}, fmt.Errorf("server/discover: %w", err)
	}
	if err := decodeCompleteResult(discovery.Result, &struct{}{}); err != nil {
		return ModelTranscript{}, fmt.Errorf("decode server/discover result: %w", err)
	}
	tools, _, _, err := listAllTools(client, requestParamsJSON, 2)
	if err != nil {
		return ModelTranscript{}, fmt.Errorf("tools/list: %w", err)
	}
	firstRequest := ModelRequest{Messages: []ModelMessage{{Role: "user", Content: prompt}}, Tools: tools}
	firstModel, err := adapter.Complete(ctx, firstRequest)
	if err != nil {
		return ModelTranscript{}, fmt.Errorf("first model turn: %w", err)
	}
	if len(firstModel.ToolCalls) != 1 {
		return ModelTranscript{}, fmt.Errorf("first model turn must contain exactly one tool call")
	}
	call := firstModel.ToolCalls[0]
	if call.ID == "" || !containsTool(tools, call.Name) || !validJSONObject(call.Arguments) {
		return ModelTranscript{}, fmt.Errorf("model returned an invalid tool call")
	}
	params, err := json.Marshal(toolCallRequestParams{
		RequestParams: protocol.RequestParams{Meta: clientRequestMeta()},
		Name:          call.Name,
		Arguments:     call.Arguments,
	})
	if err != nil {
		return ModelTranscript{}, fmt.Errorf("encode tools/call params: %w", err)
	}
	toolRequest := protocol.Request{JSONRPC: "2.0", ID: protocol.ID(3), Method: "tools/call", Params: params}
	toolResponse, err := client.call(toolRequest)
	if err != nil && toolResponse.Error == nil {
		return ModelTranscript{}, fmt.Errorf("tools/call: %w", err)
	}
	toolContent := string(toolResponse.Result)
	if toolResponse.Error != nil {
		encodedError, encodeErr := json.Marshal(toolResponse.Error)
		if encodeErr != nil {
			return ModelTranscript{}, fmt.Errorf("encode tools/call error: %w", encodeErr)
		}
		toolContent = string(encodedError)
	} else if err := decodeCompleteResult(toolResponse.Result, &struct{}{}); err != nil {
		return ModelTranscript{}, fmt.Errorf("decode tools/call result: %w", err)
	}
	secondRequest := ModelRequest{
		Messages: []ModelMessage{
			{Role: "user", Content: prompt},
			firstModel,
			{Role: "tool", ToolCallID: call.ID, Content: toolContent},
		},
		Tools: tools,
	}
	finalModel, err := adapter.Complete(ctx, secondRequest)
	if err != nil {
		return ModelTranscript{}, fmt.Errorf("final model turn: %w", err)
	}
	if len(finalModel.ToolCalls) != 0 || finalModel.Content == "" {
		return ModelTranscript{}, fmt.Errorf("final model turn must contain text and no tool call")
	}
	return ModelTranscript{Prompt: prompt, Tools: tools, FirstModel: firstModel, ToolRequest: toolRequest, ToolResponse: toolResponse, FinalModel: finalModel}, nil
}

func containsTool(tools []ToolDescription, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func validJSONObject(raw json.RawMessage) bool {
	var value map[string]any
	return len(raw) != 0 && json.Unmarshal(raw, &value) == nil && value != nil
}

type openAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIFunctionCallWire `json:"function"`
}

type openAIFunctionCallWire struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
}

func openAIMessages(messages []ModelMessage) []openAIMessage {
	result := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		wire := openAIMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			wire.ToolCalls = append(wire.ToolCalls, openAIToolCall{ID: call.ID, Type: "function", Function: openAIFunctionCallWire{Name: call.Name, Arguments: string(call.Arguments)}})
		}
		result = append(result, wire)
	}
	return result
}

func modelMessageFromOpenAI(message openAIMessage) (ModelMessage, error) {
	if message.Role != "assistant" {
		return ModelMessage{}, fmt.Errorf("model response role must be assistant")
	}
	result := ModelMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
	for _, call := range message.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return ModelMessage{}, fmt.Errorf("model response contains an invalid function call")
		}
		result.ToolCalls = append(result.ToolCalls, ModelToolCall{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)})
	}
	return result, nil
}
