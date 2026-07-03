package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunDemoTalksToServerProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	transcript, err := RunDemo(ctx, ServerCommand{
		Name: "go",
		Args: []string{"run", "./cmd/mcp-server"},
		Dir:  projectRoot(t),
	})
	if err != nil {
		t.Fatalf("RunDemo() error = %v, want nil", err)
	}

	if transcript.Initialize.Error != nil {
		t.Fatalf("initialize error = %v, want nil", transcript.Initialize.Error)
	}
	if transcript.ToolsList.Error != nil {
		t.Fatalf("tools/list error = %v, want nil", transcript.ToolsList.Error)
	}
	if transcript.EchoCall.Error != nil {
		t.Fatalf("tools/call error = %v, want nil", transcript.EchoCall.Error)
	}
	if len(transcript.Exchanges) != 4 {
		t.Fatalf("exchange count = %d, want 4", len(transcript.Exchanges))
	}
	if transcript.Exchanges[0].Request.Method != "initialize" {
		t.Fatalf("first exchange method = %q, want initialize", transcript.Exchanges[0].Request.Method)
	}
	if transcript.Exchanges[1].Request.Method != "notifications/initialized" {
		t.Fatalf("second exchange method = %q, want notifications/initialized", transcript.Exchanges[1].Request.Method)
	}
	if transcript.Exchanges[1].Request.ID != nil {
		t.Fatalf("initialized notification id = %v, want nil", *transcript.Exchanges[1].Request.ID)
	}
	if transcript.Exchanges[1].Response != nil {
		t.Fatalf("initialized notification response = %v, want nil", transcript.Exchanges[1].Response)
	}
	if len(transcript.DiscoveredTools) != 1 {
		t.Fatalf("discovered tool count = %d, want 1", len(transcript.DiscoveredTools))
	}
	if transcript.DiscoveredTools[0].Name != "echo" {
		t.Fatalf("discovered tool name = %q, want echo", transcript.DiscoveredTools[0].Name)
	}

	var echo struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(transcript.EchoCall.Result, &echo); err != nil {
		t.Fatalf("unmarshal echo result: %v", err)
	}
	if len(echo.Content) != 1 {
		t.Fatalf("echo content count = %d, want 1", len(echo.Content))
	}
	if echo.Content[0].Text != "hello from fake model" {
		t.Fatalf("echo text = %q, want hello from fake model", echo.Content[0].Text)
	}
}

func TestFakeModelDecisionChoosesEchoTool(t *testing.T) {
	t.Parallel()

	decision, err := fakeModelDecision(
		[]ToolDescription{
			{
				Name: "echo",
			},
		},
		"hello from fake model",
	)
	if err != nil {
		t.Fatalf("fakeModelDecision() error = %v, want nil", err)
	}

	if decision.ToolName != "echo" {
		t.Fatalf("tool name = %q, want echo", decision.ToolName)
	}

	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(decision.Arguments, &args); err != nil {
		t.Fatalf("unmarshal arguments: %v", err)
	}
	if args.Text != "hello from fake model" {
		t.Fatalf("argument text = %q, want hello from fake model", args.Text)
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate current test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
