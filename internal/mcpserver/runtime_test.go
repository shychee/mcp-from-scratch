package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func TestReportProgressEnforcesTokenMonotonicityAndCompletion(t *testing.T) {
	var notifications []protocol.Notification
	ctx := withRequestRuntime(context.Background(), json.RawMessage(`{"_meta":{"progressToken":"job-1"}}`), func(notification protocol.Notification) bool {
		notifications = append(notifications, notification)
		return true
	})
	if !ReportProgress(ctx, 1, 3, "start") {
		t.Fatal("first progress report = false")
	}
	if ReportProgress(ctx, 0, 3, "regression") {
		t.Fatal("regressing progress report = true")
	}
	if !ReportProgress(ctx, 3, 3, "done") {
		t.Fatal("final progress report = false")
	}
	finishRequestRuntime(ctx)
	if ReportProgress(ctx, 4, 3, "after final") {
		t.Fatal("post-final progress report = true")
	}
	if len(notifications) != 2 {
		t.Fatalf("notification count = %d, want 2", len(notifications))
	}
	var params struct {
		Meta struct {
			ProgressToken string `json:"progressToken"`
		} `json:"_meta"`
		Progress float64 `json:"progress"`
	}
	if err := json.Unmarshal(notifications[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Meta.ProgressToken != "job-1" || params.Progress != 1 {
		t.Fatalf("progress params = %#v", params)
	}
}

func TestReportProgressSupportsIntegerToken(t *testing.T) {
	var notification protocol.Notification
	ctx := withRequestRuntime(context.Background(), json.RawMessage(`{"_meta":{"progressToken":7}}`), func(value protocol.Notification) bool {
		notification = value
		return true
	})
	if !ReportProgress(ctx, 0, 0, "ready") {
		t.Fatal("integer token report = false")
	}
	var params struct {
		Meta struct {
			ProgressToken int `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Meta.ProgressToken != 7 {
		t.Fatalf("progress token = %d, want 7", params.Meta.ProgressToken)
	}
}
