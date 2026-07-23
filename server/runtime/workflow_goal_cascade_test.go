package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestWorkflowTerminalCascadeRacesUserGoalMutationWithoutDeadlock(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	started := make(chan struct{})
	release := make(chan struct{})
	client := &hookClient{
		response: structuredFinalResponse(`{"commentary":"complete","summary":"done"}`),
		beforeReturn: func() error {
			close(started)
			<-release
			return nil
		},
	}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := eng.SetGoal("race objective", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitWorkflowTurn(context.Background())
		submitDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow turn to start")
	}

	mutateDone := make(chan struct{})
	go func() {
		_, _ = eng.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser)
		close(mutateDone)
	}()
	close(release)

	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("SubmitWorkflowTurn: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: workflow turn did not finish racing the goal mutation")
	}
	select {
	case <-mutateDone:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: user goal mutation did not finish")
	}
}

func TestWorkflowToolModeCascadeSkipsGoalPausedDuringRace(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	active, _, err := store.SetGoal("stay paused through completion", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, _, _, err := store.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause: %v", err)
	}
	controller := &fakeWorkflowController{}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("complete", completeNodeCall("call_complete", json.RawMessage(`{"commentary":"complete","summary":"done"}`))),
	}}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	goal := eng.Goal()
	if goal == nil || goal.ID != active.ID || goal.Status != session.GoalStatusPaused {
		t.Fatalf("paused goal after workflow completion = %+v, want left paused", goal)
	}
}

func TestWorkflowToolModeCascadeEmitsGoalCompletionAfterHostedToolResult(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	completion := json.RawMessage(`{"commentary":"complete","summary":"done"}`)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal), ToolCalls: []llm.ToolCall{completeNodeCall("call_complete", completion)}},
			ToolCalls: []llm.ToolCall{completeNodeCall("call_complete", completion)},
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeOther, Raw: json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`)},
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleAssistant), Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("done")},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsNativeWebSearch: true, IsOpenAIFirstParty: true}
	eng := mustNewWorkflowTestEngine(t, store, client, testWorkflowConfig(controller, config.WorkflowCompletionModeTool), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolWebSearch, toolspec.ToolAskQuestion},
		HeadlessMode:  true,
	})
	if _, err := eng.SetGoal("finish via tool completion with web search", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	assertModelCallCount(t, client, 1)
	messages := requestMessages(client.calls[0])
	if len(workflowPromptMessages(messages)) == 0 {
		t.Fatalf("workflow prompt missing: messages=%+v", messages)
	}
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper &&
			msg.MessageType != nil &&
			*msg.MessageType == llm.MessageTypeHeadlessMode {
			t.Fatalf("headless prompt should not be injected during workflow runs: %+v", messages)
		}
		if msg.Role == llm.RoleUser {
			t.Fatalf("workflow run should not inject user prompt: %+v", messages)
		}
	}
	if got := controller.completed.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	if terminal := eng.WorkflowTerminalState(); !terminal.Completed || terminal.Source != WorkflowCompletionSourceTool {
		t.Fatalf("terminal state = %+v, want tool completion", terminal)
	}
	if goal := eng.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after hosted+tool completion = %+v, want auto-completed", goal)
	}

	entries := eng.ChatSnapshot().Entries
	hostedResultIdx, completeResultIdx, goalCompleteIdx := -1, -1, -1
	for i, entry := range entries {
		if entry.ToolCallID == "ws_1" {
			hostedResultIdx = i
		}
		if entry.ToolCallID == "call_complete" {
			completeResultIdx = i
		}
		if entry.MessageType == llm.MessageTypeGoal {
			goalCompleteIdx = i
		}
	}
	if hostedResultIdx < 0 || completeResultIdx < 0 || goalCompleteIdx < 0 {
		t.Fatalf("missing entries: hosted=%d complete=%d goal=%d entries=%+v", hostedResultIdx, completeResultIdx, goalCompleteIdx, entries)
	}
	if goalCompleteIdx < hostedResultIdx || goalCompleteIdx < completeResultIdx {
		t.Fatalf("goal-completion (idx %d) precedes a tool result (hosted=%d complete=%d); interleaves tool outputs", goalCompleteIdx, hostedResultIdx, completeResultIdx)
	}

	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	hostedCallPersisted := false
	hostedResultPersisted := false
	for _, record := range records {
		if kind, kindErr := record.Kind(); kindErr != nil {
			t.Fatalf("read record kind: %v", kindErr)
		} else if kind != session.EventKindMessage {
			continue
		}
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read message record: %v", payloadErr)
		}
		persisted, ok := payload.(session.MessageRecord)
		if !ok {
			t.Fatalf("message payload type = %T, want session.MessageRecord", payload)
		}
		if persisted.Role == session.MessageRoleAssistant {
			for _, call := range persisted.ToolCalls {
				hostedCallPersisted = hostedCallPersisted || call.CallID == "ws_1"
			}
		}
		hostedResultPersisted = hostedResultPersisted ||
			persisted.Role == session.MessageRoleTool &&
				persisted.ToolCallID != nil &&
				*persisted.ToolCallID == "ws_1"
	}
	if !hostedCallPersisted || !hostedResultPersisted {
		t.Fatalf("hosted call/result persisted = %v/%v, want both", hostedCallPersisted, hostedResultPersisted)
	}
}

func TestWorkflowObservedDurableCompletionCascadeCompletesActiveGoal(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	controller := &fakeWorkflowController{}
	controller.completedExternally.Store(true)
	eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := eng.SetGoal("finish via observed completion", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := eng.SubmitWorkflowTurn(context.Background()); err != nil {
		t.Fatalf("SubmitWorkflowTurn: %v", err)
	}
	if terminal := eng.WorkflowTerminalState(); !terminal.Completed || terminal.Source != WorkflowCompletionSourceObserved {
		t.Fatalf("terminal state = %+v, want observed completion", terminal)
	}
	if goal := eng.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after observed completion = %+v, want auto-completed", goal)
	}
}

func TestWorkflowCascadePublishesStatusAfterCommittedNoticeObserverFailure(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("goal completion notice observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x", session.WithPersistenceObserver(gate))
	events := make([]Event, 0, 4)
	controller := &fakeWorkflowController{}
	eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, testWorkflowConfig(controller, config.WorkflowCompletionModeUnstructured), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		OnEvent:      func(event Event) { events = append(events, event) },
	})
	if _, err := eng.SetGoal("finish with a failed observer", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	events = events[:0]
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.Goal != nil &&
			snapshot.Meta.Goal.Status == session.GoalStatusComplete &&
			snapshot.Meta.LastSequence == 2
	}, observerErr)

	eng.setWorkflowTerminalState(WorkflowCompletionSourceObserved)

	if goal := eng.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after workflow completion = %+v, want complete", goal)
	}
	if len(events) < 2 || events[0].Kind != EventConversationUpdated || events[1].Kind != EventGoalStatusUpdated {
		t.Fatalf("events after committed completion notice observer failure = %+v, want notice then status", events)
	}
}
