package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestExclusiveStepLifecycleFinalizesProviderFailureOnceBeforeTerminalPublication(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("provider failed after dispatch")
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			engine.agentStepBoundary(stepID).MarkDispatched()
			return providerErr
		},
	); !errors.Is(err, providerErr) {
		t.Fatalf("lifecycle error = %v, want provider failure", err)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read finalization records: %v", err)
	}
	boundaries := 0
	runErrors := 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			boundaries++
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				runErrors++
			}
		}
	}
	if boundaries != 1 {
		t.Fatalf("agent step boundaries = %d, want one", boundaries)
	}
	if runErrors != 1 {
		t.Fatalf("persisted run errors = %d, want one", runErrors)
	}
	if !engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("provider failure boundary did not enable manual compaction")
	}
}

func TestUncommittedBoundaryFailureDoesNotPersistFallbackRunError(t *testing.T) {
	providerErr := errors.New("provider failed before finalization append")
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	blocker := mustBlockTestEventLogAppends(t, store)
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}

	err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			engine.agentStepBoundary(stepID).MarkDispatched()
			return providerErr
		},
	)
	if !errors.Is(err, providerErr) || !errors.Is(err, errAgentStepBoundaryUncommitted) {
		t.Fatalf("lifecycle error = %v, want provider and uncommitted-finalization errors", err)
	}
	if restoreErr := blocker.Restore(); restoreErr != nil {
		t.Fatalf("restore event log: %v", restoreErr)
	}

	engine.surfaceRunError(err)
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if readErr != nil {
		t.Fatalf("read finalization records: %v", readErr)
	}
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			t.Fatalf("uncommitted boundary persisted: %+v", payload)
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				t.Fatalf("fallback run error persisted without boundary: %+v", payload)
			}
		}
	}
	if snapshot := engine.ChatSnapshot(); snapshot.StreamingError == "" {
		t.Fatal("uncommitted boundary failure did not surface a streaming error")
	}
}

func TestSubmitUserMessagePersistsProviderFailureWithOneBoundaryAndOneRunError(t *testing.T) {
	t.Parallel()

	providerErr := &llm.ProviderAPIError{
		ProviderID: "test",
		StatusCode: 400,
		Message:    "provider failed",
	}
	store := mustCreateTestSession(t)
	client := &fakeClient{errors: []error{providerErr}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})

	if _, err := engine.SubmitUserMessage(context.Background(), "input"); !errors.Is(err, providerErr) {
		t.Fatalf("submit error = %#+v, want provider error", err)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read finalization records: %v", err)
	}
	boundaries := 0
	runErrors := 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			boundaries++
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				runErrors++
			}
		}
	}
	if boundaries != 1 || runErrors != 1 {
		t.Fatalf("provider failure durable facts = boundaries:%d run-errors:%d, want 1/1", boundaries, runErrors)
	}
}

func TestInterruptedDispatchedStepCommitsOneBoundaryWithoutRunError(t *testing.T) {
	t.Parallel()

	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	result := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "interrupt me")
		result <- err
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider dispatch did not start")
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	close(client.releaseC)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted submit error = %v, want context cancellation", err)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read interruption finalization records: %v", err)
	}
	boundaries := 0
	runErrors := 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			boundaries++
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				runErrors++
			}
		}
	}
	if boundaries != 1 || runErrors != 0 {
		t.Fatalf("interruption durable facts = boundaries:%d run-errors:%d, want 1/0", boundaries, runErrors)
	}
}

type terminalToolError struct{}

func (terminalToolError) Call(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, errors.New("tool execution failed")
}

func TestToolExecutionFailureFinalizesOneBoundaryAndOneRunError(t *testing.T) {
	t.Parallel()

	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("working"),
			Phase:   textutil.Value(llm.MessagePhaseCommentary),
		},
		ToolCalls: []llm.ToolCall{{
			ID:    "tool-failure-call",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"false"}`),
		}},
	}}}
	registry := tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: terminalToolError{},
	})
	engine := mustNewTestEngine(t, store, client, registry, Config{
		Model:        "gpt-5",
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
	})

	if _, err := engine.SubmitUserMessage(context.Background(), "run the tool"); err == nil {
		t.Fatal("tool failure submit unexpectedly succeeded")
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read tool failure finalization records: %v", err)
	}
	boundaries := 0
	runErrors := 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			boundaries++
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				runErrors++
			}
		}
	}
	if boundaries != 1 || runErrors != 1 {
		t.Fatalf("tool failure durable facts = boundaries:%d run-errors:%d, want 1/1", boundaries, runErrors)
	}
}
