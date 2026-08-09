package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/runtimecommand"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestSubmitUserShellCommandPersistsErrorWithoutRegisteredHandler(t *testing.T) {
	t.Parallel()
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

func TestHeldUserShellProcessDoesNotBlockRuntimeEventAdmission(t *testing.T) {
	store := mustCreateTestSession(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer closeSignalOnce(release)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: blockingTool{
				name:    toolspec.ToolExecCommand,
				started: started,
				release: release,
			},
		}),
		Config{Model: "gpt-5"},
	)

	shellDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserShellCommand(context.Background(), "pwd")
		shellDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("user shell process did not start")
	}

	deferred, err := runtimecommand.Submit(
		context.Background(),
		engine.runtimeEvents,
		"unrelated",
		func(
			_ runtimecommand.Admission,
			value string,
			complete func(string, error),
		) error {
			complete(value, nil)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit unrelated Runtime Event: %v", err)
	}
	result, err := deferred.Await(context.Background())
	if err != nil || result != "unrelated" {
		t.Fatalf("unrelated Runtime Event = %q, %v", result, err)
	}

	closeSignalOnce(release)
	if err := <-shellDone; err != nil {
		t.Fatalf("user shell process: %v", err)
	}
}

func closeSignalOnce(signal chan struct{}) {
	select {
	case <-signal:
	default:
		close(signal)
	}
}
