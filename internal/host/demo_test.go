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
	if len(transcript.Exchanges) != 3 {
		t.Fatalf("exchange count = %d, want 3", len(transcript.Exchanges))
	}
	if transcript.Exchanges[0].Request.Method != "initialize" {
		t.Fatalf("first exchange method = %q, want initialize", transcript.Exchanges[0].Request.Method)
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
	if echo.Content[0].Text != "hello from host" {
		t.Fatalf("echo text = %q, want hello from host", echo.Content[0].Text)
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
