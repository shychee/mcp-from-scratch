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

type Transcript struct {
	Initialize protocol.Response
	ToolsList  protocol.Response
	EchoCall   protocol.Response
	Exchanges  []Exchange
}

type Exchange struct {
	Name     string            `json:"name"`
	Request  protocol.Request  `json:"request"`
	Response protocol.Response `json:"response"`
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
	initializeRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	}
	initialize, err := client.call(initializeRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("initialize: %w", err)
	}

	toolsListRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}
	toolsList, err := client.call(toolsListRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/list: %w", err)
	}

	echoCallRequest := protocol.Request{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"text":"hello from host"}}`),
	}
	echoCall, err := client.call(echoCallRequest)
	if err != nil {
		return Transcript{}, fmt.Errorf("tools/call: %w", err)
	}

	return Transcript{
		Initialize: initialize,
		ToolsList:  toolsList,
		EchoCall:   echoCall,
		Exchanges: []Exchange{
			{Name: "initialize", Request: initializeRequest, Response: initialize},
			{Name: "tools/list", Request: toolsListRequest, Response: toolsList},
			{Name: "tools/call", Request: echoCallRequest, Response: echoCall},
		},
	}, nil
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
