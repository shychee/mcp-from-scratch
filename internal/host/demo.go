package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

type ServerCommand struct {
	Name string
	Args []string
	Dir  string
}

type ToolDescription struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolsListResult struct {
	Tools []ToolDescription `json:"tools"`
}

type toolCallRequestParams struct {
	Meta      protocol.RequestMeta `json:"_meta"`
	Name      string               `json:"name"`
	Arguments json.RawMessage      `json:"arguments"`
}

type ToolCallDecision struct {
	ToolName  string
	Arguments json.RawMessage
}

type Transcript struct {
	Discovery       protocol.Response
	ToolsList       protocol.Response
	EchoCall        protocol.Response
	Exchanges       []Exchange
	DiscoveredTools []ToolDescription
}

type Exchange struct {
	Name     string             `json:"name"`
	Request  protocol.Request   `json:"request"`
	Response *protocol.Response `json:"response,omitempty"`
}

func RunDemo(ctx context.Context, serverCommand ServerCommand) (Transcript, error) {
	cmd := exec.CommandContext(ctx, serverCommand.Name, serverCommand.Args...)
	cmd.Dir = serverCommand.Dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Transcript{}, fmt.Errorf("open server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Transcript{}, fmt.Errorf("open server stdout: %w", err)
	}

	var stderrBuffer bytes.Buffer
	cmd.Stderr = &stderrBuffer

	if err := cmd.Start(); err != nil {
		return Transcript{}, fmt.Errorf("start server: %w", err)
	}

	client := rpcClient{
		encoder: json.NewEncoder(stdin),
		decoder: json.NewDecoder(stdout),
	}

	transcript, err := runProtocolDemo(&client)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if err != nil {
		return Transcript{}, err
	}
	if closeErr != nil {
		return Transcript{}, fmt.Errorf("close server stdin: %w", closeErr)
	}
	if waitErr != nil {
		return Transcript{}, fmt.Errorf("wait for server: %w; stderr: %s", waitErr, stderrBuffer.String())
	}

	return transcript, nil
}

type rpcClient struct {
	encoder *json.Encoder
	decoder *json.Decoder
}

func runProtocolDemo(client *rpcClient) (Transcript, error) {
	requestParamsJSON, err := json.Marshal(protocol.RequestParams{
		Meta: clientRequestMeta(),
	})
	if err != nil {
		return Transcript{}, fmt.Errorf("encode request metadata: %w", err)
	}

	discoveryRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(1),
		Method:  "server/discover",
		Params:  requestParamsJSON,
	}
	discovery, err := client.call(discoveryRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("server/discover: %w", err)
	}

	toolsListRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(2),
		Method:  "tools/list",
		Params:  requestParamsJSON,
	}
	toolsList, err := client.call(toolsListRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/list: %w", err)
	}

	var listedTools toolsListResult
	if err := json.Unmarshal(toolsList.Result, &listedTools); err != nil {
		return Transcript{}, fmt.Errorf("decode tools/list result: %w", err)
	}

	decision, err := fakeModelDecision(listedTools.Tools, "hello from fake model")
	if err != nil {
		return Transcript{}, fmt.Errorf("fake model decision: %w", err)
	}
	toolCallParams := toolCallRequestParams{
		Meta:      clientRequestMeta(),
		Name:      decision.ToolName,
		Arguments: decision.Arguments,
	}
	toolCallParamsJSON, err := json.Marshal(toolCallParams)
	if err != nil {
		return Transcript{}, fmt.Errorf("encode tools/call params: %w", err)
	}
	echoCallRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      protocol.ID(3),
		Method:  "tools/call",
		Params:  toolCallParamsJSON,
	}
	echoCall, err := client.call(echoCallRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/call: %w", err)
	}

	return Transcript{
		Discovery:       discovery,
		ToolsList:       toolsList,
		EchoCall:        echoCall,
		DiscoveredTools: listedTools.Tools,
		Exchanges: []Exchange{
			{Name: "server/discover", Request: discoveryRequest, Response: &discovery},
			{Name: "tools/list", Request: toolsListRequest, Response: &toolsList},
			{Name: "tools/call", Request: echoCallRequest, Response: &echoCall},
		},
	}, nil
}

func clientRequestMeta() protocol.RequestMeta {
	return protocol.RequestMeta{
		ProtocolVersion: protocol.Version20260728,
		ClientInfo: &protocol.Implementation{
			Name:    "mcp-from-scratch-host",
			Version: "0.1.0",
		},
		ClientCapabilities: map[string]any{},
	}
}

func (c *rpcClient) call(request protocol.Request) (protocol.Response, error) {
	if err := c.encoder.Encode(request); err != nil {
		return protocol.Response{}, fmt.Errorf("encode request: %w", err)
	}

	var response protocol.Response
	if err := c.decoder.Decode(&response); err != nil {
		return protocol.Response{}, fmt.Errorf("decode response: %w", err)
	}
	return response, nil
}

func openAIToolsFromToolDescriptions(tools []ToolDescription) []openAITool {
	openAITools := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		openAITools = append(openAITools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return openAITools
}

func fakeModelDecision(tools []ToolDescription, userText string) (ToolCallDecision, error) {
	for _, tool := range tools {
		if tool.Name == "echo" {
			arguments, err := json.Marshal(map[string]string{
				"text": userText,
			})
			if err != nil {
				return ToolCallDecision{}, fmt.Errorf("encode echo arguments: %w", err)
			}
			return ToolCallDecision{
				ToolName:  tool.Name,
				Arguments: arguments,
			}, nil
		}
	}

	return ToolCallDecision{}, fmt.Errorf("no echo tool discovered")
}
