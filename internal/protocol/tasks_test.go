package protocol

import (
	"encoding/json"
	"testing"
)

func TestDetailedTaskTTLMSCanBeNull(t *testing.T) {
	var task DetailedTask
	if err := json.Unmarshal([]byte(`{"taskId":"task-1","status":"working","createdAt":"2026-08-12T12:00:00Z","lastUpdatedAt":"2026-08-12T12:00:00Z","ttlMs":null}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.TTLMS != nil {
		t.Fatalf("ttlMs = %v, want nil", *task.TTLMS)
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || string(encoded) == "null" {
		t.Fatalf("encoded task = %s", encoded)
	}
}

func TestTaskStatusValidation(t *testing.T) {
	for _, status := range []TaskStatus{TaskStatusWorking, TaskStatusInputRequired, TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled} {
		if !status.Valid() {
			t.Errorf("status %q reported invalid", status)
		}
	}
	if TaskStatus("unknown").Valid() {
		t.Fatal("unknown status reported valid")
	}
}

func TestMissingTaskCapabilityError(t *testing.T) {
	err := MissingTaskCapabilityError()
	if err.Code != CodeMissingRequiredTaskCapability {
		t.Fatalf("error code = %d, want %d", err.Code, CodeMissingRequiredTaskCapability)
	}
	data, errMarshal := json.Marshal(err.Data)
	if errMarshal != nil || string(data) == "{}" {
		t.Fatalf("error data = %s, marshal error = %v", data, errMarshal)
	}
}
