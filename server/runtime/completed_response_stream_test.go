package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestCompletedResponseExternalWorkflowCompletionDiscardsActiveStreamWithoutPersistence(t *testing.T) {
	controller := &externallyCompletedWorkflowController{}
	step := scriptedllm.ToolBatch("", llm.ToolCall{
		ID:    "stale-call",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{}`),
	})
	step.StreamDeltas = []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseCommentary}}
	step.AfterResponse = func(context.Context) error {
		controller.completed.Store(true)
		return nil
	}
	var events []Event
	runID := workflow.RunID("workflow-run")
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{step}}),
		Config{
			Model: "gpt-5",
			WorkflowRun: &workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}

	var delta, reset *Event
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple assistant deltas: %+v", events)
			}
			delta = event
		case EventAssistantDeltaReset:
			if reset != nil {
				t.Fatalf("multiple assistant delta resets: %+v", events)
			}
			reset = event
		case EventToolCallStarted:
			t.Fatalf("externally completed workflow executed a stale tool call: %+v", event)
		}
	}
	if delta == nil || reset == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		reset.AssistantTranscriptStreamID == nil ||
		*delta.AssistantTranscriptStreamID != *reset.AssistantTranscriptStreamID ||
		reset.AssistantStreamAbortReason != string(AssistantStreamAbortSuperseded) {
		t.Fatalf("stale stream disposal = delta:%+v reset:%+v", delta, reset)
	}

	window, err := mustMaterializeTestEventLog(t, engine.store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded workflow records: %v", err)
	}
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID == "stale-call" {
				t.Fatalf("stale workflow response persisted a tool completion: %+v", payload)
			}
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore persisted message: %v", restoreErr)
			}
			for _, call := range message.ToolCalls {
				if call.ID == "stale-call" {
					t.Fatalf("stale workflow response persisted a tool call: %+v", message)
				}
			}
		}
	}
}

func TestWorkflowDurableCompletionBeforeModelTurnStopsWithoutRequest(t *testing.T) {
	controller := &externallyCompletedWorkflowController{}
	controller.completed.Store(true)
	runID := workflow.RunID("workflow-run")
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant},
	}}}
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		Config{
			Model: "gpt-5",
			WorkflowRun: &workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if calls := len(client.calls); calls != 0 {
		t.Fatalf("durably completed workflow dispatched %d model requests", calls)
	}
	terminal := engine.WorkflowTerminalState()
	if !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceObserved ||
		terminal.RunID != string(runID) ||
		terminal.CompletedAt.IsZero() {
		t.Fatalf("workflow terminal state after durable completion = %+v", terminal)
	}
}

func TestWorkflowDelayedDurableCompletionObservedBeforeNextModelTurn(t *testing.T) {
	const callID = "workflow-delayed-completion-call"
	controller := &externallyCompletedWorkflowController{completeAfterObservations: 4}
	runID := workflow.RunID("workflow-run")
	toolCall := llm.ToolCall{
		ID:    callID,
		Name:  string(toolspec.ToolExecCommand),
		Input: mustJSON(map[string]any{"cmd": "pwd"}),
	}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:      llm.RoleAssistant,
			Phase:     textutil.Value(llm.MessagePhaseCommentary),
			Content:   textutil.Value("working"),
			ToolCalls: []llm.ToolCall{toolCall},
		},
		ToolCalls: []llm.ToolCall{toolCall},
		Usage:     llm.Usage{InputTokens: 100, WindowTokens: 2_000},
	}}}
	store := mustCreateTestSession(t)
	engine := mustNewExecTestEngine(
		t,
		store,
		client,
		Config{
			Model:        "gpt-5",
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			WorkflowRun: &workflowruntime.Config{
				RunID:          runID,
				Contract:       workflowruntime.CompletionContract{RunID: runID},
				CompletionMode: workflowruntime.CompletionModeShellCommand,
				Controller:     controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if calls := len(client.calls); calls != 1 {
		t.Fatalf("delayed workflow completion dispatched %d model requests, want 1", calls)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded workflow records: %v", err)
	}
	foundCompletion := false
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if !ok || completion.CallID != callID {
			continue
		}
		if completion.IsError || completion.Name != string(toolspec.ToolExecCommand) {
			t.Fatalf("workflow tool completion is an error: %+v", completion)
		}
		foundCompletion = true
	}
	if !foundCompletion {
		t.Fatalf("bounded workflow records contain no tool completion for %q", callID)
	}
	terminal := engine.WorkflowTerminalState()
	if !terminal.Completed ||
		terminal.Source != WorkflowCompletionSourceObserved ||
		terminal.RunID != string(runID) {
		t.Fatalf("workflow terminal state after delayed completion = %+v", terminal)
	}
	if observations := controller.observations.Load(); observations < controller.completeAfterObservations {
		t.Fatalf(
			"completion observations = %d, want at least %d",
			observations,
			controller.completeAfterObservations,
		)
	}
}

func TestWorkflowInvalidCompletionFailClosedWhenConfiguredCapInvalid(t *testing.T) {
	runID := workflow.RunID("workflow-run")
	controller := &interruptingWorkflowProtocolViolationController{}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("invalid workflow completion"),
		},
	}}}
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		Config{
			Model: "gpt-5",
			WorkflowRun: &workflowruntime.Config{
				RunID:                        runID,
				Contract:                     workflowruntime.CompletionContract{RunID: runID},
				CompletionMode:               workflowruntime.CompletionModeTool,
				MaxInvalidCompletionAttempts: 0,
				Controller:                   controller,
			},
		},
	)

	if _, err := engine.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("submit workflow turn: %v", err)
	}
	if len(controller.violations) != 1 {
		t.Fatalf("workflow protocol violations = %+v", controller.violations)
	}
	violation := controller.violations[0]
	if violation.Kind != workflowruntime.ViolationKindInvalidCompletion ||
		violation.MaxCount != 1 {
		t.Fatalf("workflow protocol violation = %+v", violation)
	}
	if controller.result.Count != 1 || !controller.result.Interrupted {
		t.Fatalf("workflow protocol violation result = %+v", controller.result)
	}
}

func TestCompletedResponseFinalizationUsesActiveSegmentCoordinatesAfterCompaction(t *testing.T) {
	first := scriptedllm.FinalAnswer("first")
	first.StreamDeltas = []llm.AssistantDelta{{Text: "first", Phase: llm.MessagePhaseFinal}}
	second := scriptedllm.FinalAnswer("second")
	second.StreamDeltas = []llm.AssistantDelta{{Text: "second", Phase: llm.MessagePhaseFinal}}

	var events []Event
	engine := mustNewExecTestEngine(
		t,
		mustCreateTestSession(t),
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{first, second}}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "first input"); err != nil {
		t.Fatalf("submit pre-compaction message: %v", err)
	}
	if err := engine.steer(
		"compaction",
		steerHistoryReplacementIntent("local", compactionModeAuto, "", 1, "", "", nil),
	); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}
	activeSegmentStart := engine.CommittedTranscriptEntryCount()
	events = nil

	if _, err := engine.SubmitUserMessage(context.Background(), "second input"); err != nil {
		t.Fatalf("submit post-compaction message: %v", err)
	}

	var delta, finalized *Event
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case EventAssistantDelta:
			if delta != nil {
				t.Fatalf("multiple post-compaction assistant deltas: %+v", events)
			}
			delta = event
		case EventAssistantMessage:
			if finalized != nil {
				t.Fatalf("multiple post-compaction finalized assistant messages: %+v", events)
			}
			finalized = event
		}
	}
	if delta == nil || finalized == nil ||
		delta.AssistantTranscriptStreamID == nil ||
		finalized.AssistantTranscriptStreamID == nil ||
		delta.AssistantStreamMetadata == nil ||
		!finalized.CommittedEntryStartSet {
		t.Fatalf("post-compaction stream finalization events = %+v", events)
	}
	if delta.AssistantStreamMetadata.BaseCommittedEntryCount != finalized.CommittedEntryStart ||
		finalized.CommittedEntryStart < activeSegmentStart ||
		*delta.AssistantTranscriptStreamID != *finalized.AssistantTranscriptStreamID {
		t.Fatalf(
			"post-compaction stream coordinates = delta:%+v finalized:%+v active_segment_start=%d",
			delta,
			finalized,
			activeSegmentStart,
		)
	}

	var hydratedAssistant *TranscriptCommittedRowFact
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		for index := range snapshot.CommittedRows {
			row := &snapshot.CommittedRows[index]
			if row.Kind != TranscriptCommittedRowFactAssistant {
				continue
			}
			if hydratedAssistant != nil {
				t.Fatalf("multiple hydrated assistant rows after compaction: %+v", snapshot.CommittedRows)
			}
			hydratedAssistant = row
		}
		return nil
	}); err != nil {
		t.Fatalf("hydrate active segment: %v", err)
	}
	if hydratedAssistant == nil ||
		hydratedAssistant.Assistant == nil ||
		hydratedAssistant.Assistant.StreamID == nil ||
		*hydratedAssistant.Assistant.StreamID != *finalized.AssistantTranscriptStreamID {
		t.Fatalf(
			"hydrated post-compaction assistant = %+v, finalized stream=%+v",
			hydratedAssistant,
			finalized.AssistantTranscriptStreamID,
		)
	}
}

type externallyCompletedWorkflowController struct {
	completed                 atomic.Bool
	observations              atomic.Int32
	completeAfterObservations int32
}

type interruptingWorkflowProtocolViolationController struct {
	externallyCompletedWorkflowController
	violations []workflowruntime.ViolationRequest
	result     workflowruntime.ViolationResult
}

func (c *interruptingWorkflowProtocolViolationController) RecordWorkflowProtocolViolation(
	_ context.Context,
	request workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	c.violations = append(c.violations, request)
	c.result = workflowruntime.ViolationResult{Count: 1, Interrupted: true}
	return c.result, nil
}

func (c *externallyCompletedWorkflowController) CompleteWorkflowRun(
	context.Context,
	workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	return workflowruntime.CompletionResult{}, nil
}

func (c *externallyCompletedWorkflowController) RecordWorkflowProtocolViolation(
	context.Context,
	workflowruntime.ViolationRequest,
) (workflowruntime.ViolationResult, error) {
	return workflowruntime.ViolationResult{}, nil
}

func (c *externallyCompletedWorkflowController) ResetWorkflowProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return nil
}

func (c *externallyCompletedWorkflowController) ObserveWorkflowRunCompletion(
	context.Context,
	workflowruntime.CompletionObservationRequest,
) (workflowruntime.CompletionObservationResult, error) {
	if count := c.observations.Add(1); c.completeAfterObservations > 0 && count >= c.completeAfterObservations {
		c.completed.Store(true)
	}
	return workflowruntime.CompletionObservationResult{Completed: c.completed.Load()}, nil
}
