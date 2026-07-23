package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestSubmitUserShellCommandPersistsErrorWithoutRegisteredHandler(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	result, err := engine.SubmitUserShellCommand(context.Background(), "pwd")
	if !errors.Is(err, errUnknownTool) {
		t.Fatalf("submit shell command error = %v, want unknown tool", err)
	}
	if result.CallID == "" ||
		result.Name != toolspec.ToolExecCommand ||
		!result.IsError {
		t.Fatalf("unknown shell handler result = %+v", result)
	}
	if len(client.calls) != 0 {
		t.Fatalf("unknown shell handler dispatched model calls = %d", len(client.calls))
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read bounded shell command records: %v", err)
	}
	completions := 0
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if !ok || completion.CallID != result.CallID {
			continue
		}
		completions++
		if completion.Name != string(toolspec.ToolExecCommand) || !completion.IsError {
			t.Fatalf("persisted unknown shell handler completion = %+v", completion)
		}
	}
	if completions != 1 {
		t.Fatalf("persisted unknown shell handler completions = %d, want one", completions)
	}
}
