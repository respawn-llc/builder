package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestGoalSetPersistsGoalAndDeveloperPrompt(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	goal, err := engine.SetGoal("ship goal mode", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if goal.Status != session.GoalStatusActive {
		t.Fatalf("goal status = %q", goal.Status)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Kind != "goal_set" {
		t.Fatalf("first event kind = %q, want goal_set", events[0].Kind)
	}
	if events[1].Kind != "message" {
		t.Fatalf("second event kind = %q, want message", events[1].Kind)
	}
	var msg llm.Message
	if err := json.Unmarshal(events[1].Payload, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.Role != llm.RoleDeveloper || msg.MessageType != llm.MessageTypeGoal {
		t.Fatalf("message role/type = %q/%q, want developer/goal", msg.Role, msg.MessageType)
	}
	if msg.Content != prompts.RenderGoalSetPrompt("ship goal mode") {
		t.Fatalf("message content = %q", msg.Content)
	}
	if msg.CompactContent != `Goal set: "ship goal mode"` {
		t.Fatalf("compact content = %q, want goal set preview", msg.CompactContent)
	}
	if !strings.Contains(msg.CompactContent, "ship goal mode") {
		t.Fatalf("compact content = %q, want objective preview", msg.CompactContent)
	}
}

func TestGoalSetEmitsCommittedGoalFeedbackEvent(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	events := make([]Event, 0, 1)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2: %+v", len(events), events)
	}
	evt := events[0]
	if evt.Kind != EventConversationUpdated || !evt.CommittedTranscriptChanged {
		t.Fatalf("event = %+v, want committed conversation update", evt)
	}
	entries := TranscriptEntriesFromEvent(evt)
	if len(entries) != 1 {
		t.Fatalf("event transcript entries len = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Role != string(transcript.EntryRoleGoalFeedback) || entry.CondensedText != `Goal set: "ship goal mode"` {
		t.Fatalf("event transcript entry = %+v, want goal feedback", entry)
	}
	if !evt.CommittedEntryStartSet || evt.CommittedEntryStart != 0 || evt.CommittedEntryCount != 1 {
		t.Fatalf("event committed range start=%d set=%t count=%d, want start 0 count 1", evt.CommittedEntryStart, evt.CommittedEntryStartSet, evt.CommittedEntryCount)
	}
	statusEvt := events[1]
	if statusEvt.Kind != EventGoalStatusUpdated || statusEvt.GoalStatus == nil {
		t.Fatalf("status event = %+v, want goal status update", statusEvt)
	}
	if statusEvt.GoalStatus.Cleared || statusEvt.GoalStatus.State.Objective != "ship goal mode" || statusEvt.GoalStatus.State.Status != session.GoalStatusActive {
		t.Fatalf("status payload = %+v, want active goal", statusEvt.GoalStatus)
	}
}

func TestGoalMutationsEmitGoalStatusEventsAfterFeedback(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	events := make([]Event, 0, 10)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	set, err := engine.SetGoal("ship goal mode", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, 0, set, false)

	paused, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser)
	if err != nil {
		t.Fatalf("pause goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, 2, paused, false)

	active, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser)
	if err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, 4, active, false)

	complete, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent)
	if err != nil {
		t.Fatalf("complete goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, 6, complete, false)

	cleared, err := engine.ClearGoal(session.GoalActorUser)
	if err != nil {
		t.Fatalf("clear goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, 8, cleared, true)
}

func TestConcurrentGoalMutationsDoNotInterleaveBetweenMetadataAndStatusEvent(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	events := make([]Event, 0, 4)
	var eventsMu sync.Mutex
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
		},
	})

	engine.outputMutationMu.Lock()
	outputLocked := true
	defer func() {
		if outputLocked {
			engine.outputMutationMu.Unlock()
		}
	}()
	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.SetGoal("first goal", session.GoalActorUser)
		firstDone <- err
	}()
	waitForGoalObjective(t, store, "first goal")

	secondDone := make(chan error, 1)
	go func() {
		_, err := engine.SetGoal("second goal", session.GoalActorUser)
		secondDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if goal := store.Meta().Goal; goal == nil || goal.Objective != "first goal" {
		t.Fatalf("second mutation interleaved before first status event completed: %+v", goal)
	}
	eventsMu.Lock()
	if len(events) != 0 {
		t.Fatalf("events emitted while output boundary was blocked: %+v", events)
	}
	eventsMu.Unlock()

	engine.outputMutationMu.Unlock()
	outputLocked = false
	if err := <-firstDone; err != nil {
		t.Fatalf("first SetGoal: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second SetGoal: %v", err)
	}

	eventsMu.Lock()
	gotEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	if len(gotEvents) != 4 {
		t.Fatalf("events len = %d, want 4: %+v", len(gotEvents), gotEvents)
	}
	assertGoalStatusEventObjective(t, gotEvents, 0, "first goal")
	assertGoalStatusEventObjective(t, gotEvents, 2, "second goal")
}

func waitForGoalObjective(t *testing.T, store *session.Store, objective string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if goal := store.Meta().Goal; goal != nil && goal.Objective == objective {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for goal objective %q, got %+v", objective, store.Meta().Goal)
}

func assertGoalStatusEventObjective(t *testing.T, events []Event, start int, objective string) {
	t.Helper()
	if events[start].Kind != EventConversationUpdated || !events[start].CommittedTranscriptChanged {
		t.Fatalf("event[%d] = %+v, want committed feedback", start, events[start])
	}
	status := events[start+1]
	if status.Kind != EventGoalStatusUpdated || status.GoalStatus == nil || status.GoalStatus.State.Objective != objective {
		t.Fatalf("event[%d] = %+v, want goal status objective %q", start+1, status, objective)
	}
}

func assertGoalFeedbackThenStatusEvent(t *testing.T, events []Event, start int, goal session.GoalState, cleared bool) {
	t.Helper()
	if len(events) < start+2 {
		t.Fatalf("events len = %d, want at least %d: %+v", len(events), start+2, events)
	}
	feedback := events[start]
	if feedback.Kind != EventConversationUpdated || !feedback.CommittedTranscriptChanged {
		t.Fatalf("event[%d] = %+v, want committed goal feedback", start, feedback)
	}
	status := events[start+1]
	if status.Kind != EventGoalStatusUpdated || status.GoalStatus == nil {
		t.Fatalf("event[%d] = %+v, want goal status event", start+1, status)
	}
	if status.GoalStatus.Cleared != cleared {
		t.Fatalf("cleared = %t, want %t", status.GoalStatus.Cleared, cleared)
	}
	if cleared {
		return
	}
	if status.GoalStatus.State.ID != goal.ID || status.GoalStatus.State.Objective != goal.Objective || status.GoalStatus.State.Status != goal.Status {
		t.Fatalf("goal status state = %+v, want %+v", status.GoalStatus.State, goal)
	}
}

func TestGoalStatusAndClearPersistDeveloperPrompts(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause goal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser); err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent); err != nil {
		t.Fatalf("complete goal: %v", err)
	}
	if _, err := engine.ClearGoal(session.GoalActorUser); err != nil {
		t.Fatalf("clear goal: %v", err)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	messages := goalDeveloperMessages(t, events)
	if len(messages) != 5 {
		t.Fatalf("goal developer messages len = %d, want 5", len(messages))
	}
	if messages[1].Content != prompts.GoalPausePrompt {
		t.Fatalf("pause prompt = %q", messages[1].Content)
	}
	if messages[2].Content != prompts.RenderGoalResumePrompt("ship goal mode") {
		t.Fatalf("resume prompt = %q", messages[2].Content)
	}
	if messages[3].Content != prompts.GoalCompletePrompt {
		t.Fatalf("complete prompt = %q", messages[3].Content)
	}
	if messages[4].Content != prompts.GoalClearPrompt {
		t.Fatalf("clear prompt = %q", messages[4].Content)
	}
}

func TestGoalLifecycleMessagesProjectAsSingleGoalFeedbackEntry(t *testing.T) {
	tests := []struct {
		name    string
		message llm.Message
		ongoing string
	}{
		{
			name:    "set",
			message: llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.RenderGoalSetPrompt("ship goal mode"), CompactContent: "Goal set: \"ship goal mode\""},
			ongoing: "Goal set: \"ship goal mode\"",
		},
		{
			name:    "pause",
			message: llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.GoalPausePrompt, CompactContent: "Goal paused"},
			ongoing: "Goal paused",
		},
		{
			name:    "resume",
			message: llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.RenderGoalResumePrompt("ship goal mode"), CompactContent: "Goal resumed: \"ship goal mode\""},
			ongoing: "Goal resumed: \"ship goal mode\"",
		},
		{
			name:    "complete",
			message: llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.GoalCompletePrompt, CompactContent: "Goal complete. Cooked for 31m"},
			ongoing: "Goal complete. Cooked for 31m",
		},
		{
			name:    "clear",
			message: llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.GoalClearPrompt, CompactContent: "Goal cleared"},
			ongoing: "Goal cleared",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := VisibleChatEntriesFromMessage(tt.message)
			if len(entries) != 1 {
				t.Fatalf("entries len = %d, want exactly one", len(entries))
			}
			entry := entries[0]
			if entry.Role != string(transcript.EntryRoleGoalFeedback) {
				t.Fatalf("role = %q, want %q", entry.Role, transcript.EntryRoleGoalFeedback)
			}
			if entry.CondensedText != tt.ongoing {
				t.Fatalf("condensed text = %q, want %q", entry.CondensedText, tt.ongoing)
			}
		})
	}
}

func TestGoalCompleteCompactTextIncludesCookDuration(t *testing.T) {
	createdAt := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{name: "hours minutes seconds", duration: 5*time.Hour + 32*time.Minute + 9*time.Second, want: "Goal complete. Cooked for 5h32m9s"},
		{name: "minutes only", duration: 31 * time.Minute, want: "Goal complete. Cooked for 31m"},
		{name: "seconds only", duration: 9 * time.Second, want: "Goal complete. Cooked for 9s"},
		{name: "zero", duration: 0, want: "Goal complete. Cooked for 0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goal := session.GoalState{Status: session.GoalStatusComplete, CreatedAt: createdAt, UpdatedAt: createdAt.Add(tt.duration)}
			if got := goalStatusCompactText(goal); got != tt.want {
				t.Fatalf("goal compact text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActiveGoalRequiresAskQuestionToolVisibilityBeforeModelTurn(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolExecCommand}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	_, err := engine.runStepLoop(t.Context(), "step-1")
	if !errors.Is(err, ErrGoalRequiresAskQuestion) {
		t.Fatalf("runStepLoop error = %v, want ErrGoalRequiresAskQuestion", err)
	}
	assertModelCallCount(t, client, 0)
}

func TestWorkflowActiveGoalRequiresAskQuestionToolVisibilityBeforeModelTurn(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
		WorkflowRun:  &workflowruntime.Config{Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("workflow-run-1")}},
	})
	engine.SetQuestionsEnabled(false)
	if _, err := engine.SetGoal("ship workflow goal", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if err := engine.requireAskQuestionForActiveGoal(); !errors.Is(err, ErrGoalRequiresAskQuestion) {
		t.Fatalf("workflow active goal preflight error = %v, want ErrGoalRequiresAskQuestion", err)
	}
}

func TestActiveGoalAllowsModelTurnWithAskQuestionEnabled(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if _, err := engine.runStepLoop(t.Context(), "step-1"); err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	assertModelCallCount(t, client, 1)
}

func TestActiveGoalAllowsModelTurnWithQuestionsDisabledWhenAskQuestionToolVisible(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	engine.SetQuestionsEnabled(false)
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if _, err := engine.runStepLoop(t.Context(), "step-1"); err != nil {
		t.Fatalf("runStepLoop with questions disabled: %v", err)
	}
	assertModelCallCount(t, client, 1)
}

func TestGoalResumeRequiresAskQuestionToolVisibilityAtEngineBoundary(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
	})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause goal: %v", err)
	}

	if _, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser); !errors.Is(err, ErrGoalRequiresAskQuestion) {
		t.Fatalf("resume goal error = %v, want ErrGoalRequiresAskQuestion", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusPaused {
		t.Fatalf("goal after failed resume = %+v, want paused", goal)
	}
}

func TestGoalResumeAllowsQuestionsDisabledWhenAskQuestionToolVisible(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause goal: %v", err)
	}
	engine.SetQuestionsEnabled(false)

	if _, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser); err != nil {
		t.Fatalf("resume goal with questions disabled: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("goal after resume = %+v, want active", goal)
	}
}

func TestGoalTurnAppendsNudgePromptAndRunsModel(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if _, err := engine.runGoalTurn(t.Context(), true); err != nil {
		t.Fatalf("runGoalTurn: %v", err)
	}
	assertModelCallCount(t, client, 1)
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	messages := goalDeveloperMessages(t, events)
	if len(messages) < 2 {
		t.Fatalf("goal developer messages len = %d, want at least 2", len(messages))
	}
	if got := messages[1].Content; got != prompts.RenderGoalNudgePrompt("ship goal mode", "active") {
		t.Fatalf("nudge prompt = %q", got)
	}
}

func TestGoalTurnRejectsNoopFinalWithoutAppendingExtraNudge(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{
		finalTextResponse("NO_OP"),
		finalTextResponse("working"),
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	msg, err := engine.runGoalTurn(t.Context(), true)
	if err != nil {
		t.Fatalf("runGoalTurn: %v", err)
	}
	if msg.Content != "working" {
		t.Fatalf("assistant content = %q, want working", msg.Content)
	}
	assertModelCallCount(t, client, 2)
	secondReq := requestMessages(client.calls[1])
	foundWarning := false
	for _, reqMsg := range secondReq {
		if reqMsg.Role == llm.RoleDeveloper && reqMsg.Content == goalNoopFinalWarning {
			if reqMsg.MessageType != llm.MessageTypeErrorFeedback {
				t.Fatalf("NO_OP warning message type = %q, want error_feedback", reqMsg.MessageType)
			}
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected NO_OP warning in second request, got %+v", secondReq)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	messages := goalDeveloperMessages(t, events)
	if len(messages) != 2 {
		t.Fatalf("goal developer messages len = %d, want set+nudge only: %+v", len(messages), messages)
	}
	for _, msg := range messages {
		if msg.Content == goalNoopFinalWarning {
			t.Fatalf("NO_OP rejection should use error feedback, not goal feedback: %+v", msg)
		}
	}
}

func TestGoalDeveloperMessageVisibleInOngoingWithDetailPrompt(t *testing.T) {
	msg := llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    llm.MessageTypeGoal,
		Content:        prompts.RenderGoalNudgePrompt("ship goal mode", "active"),
		CompactContent: "Continue active goal: \"ship goal mode\"",
	}

	entries := VisibleChatEntriesFromMessage(msg)
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Role != string(transcript.EntryRoleGoalFeedback) {
		t.Fatalf("goal role = %q, want %q", entry.Role, transcript.EntryRoleGoalFeedback)
	}
	if entry.Visibility != "all" {
		t.Fatalf("goal visibility = %q, want all", entry.Visibility)
	}
	if entry.Text != msg.Content {
		t.Fatalf("goal detail text = %q, want full prompt", entry.Text)
	}
	if entry.CondensedText != msg.CompactContent {
		t.Fatalf("goal condensed text = %q, want compact", entry.CondensedText)
	}
}

func TestRecordGoalLoopErrorPersistsOperatorFeedback(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	engine.recordGoalLoopError(errors.New("provider down"))

	snapshot := engine.ChatSnapshot()
	found := false
	for _, entry := range snapshot.Entries {
		if entry.Role == "developer_error_feedback" && strings.Contains(entry.Text, "Goal loop stopped: provider down") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected goal loop error entry, got %+v", snapshot.Entries)
	}
}

func TestGoalLoopStopsAfterPauseOrClearDuringActiveTurn(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Engine) error
	}{
		{
			name: "pause",
			mutate: func(engine *Engine) error {
				_, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser)
				return err
			},
		},
		{
			name: "clear",
			mutate: func(engine *Engine) error {
				_, err := engine.ClearGoal(session.GoalActorUser)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
			client := newScriptedGoalLoopClient()
			engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
			if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
				t.Fatalf("SetGoal: %v", err)
			}
			if err := engine.StartGoalLoop(); err != nil {
				t.Fatalf("StartGoalLoop: %v", err)
			}
			client.waitStarted(t, 1)

			if err := tt.mutate(engine); err != nil {
				t.Fatalf("mutate goal: %v", err)
			}
			client.releaseCall(1)
			waitGoalLoopRunning(t, engine, false)
			if got := client.callCount(); got != 1 {
				t.Fatalf("model calls = %d, want 1", got)
			}
		})
	}
}

func TestGoalLoopInterruptSuspendsUntilResumeRestarts(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 2 {
			_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)

	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	waitGoalLoopRunning(t, engine, false)
	if got := client.callCount(); got != 1 {
		t.Fatalf("model calls after interrupt = %d, want 1", got)
	}

	if _, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser); err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop after resume: %v", err)
	}
	client.waitStarted(t, 2)
	client.releaseCall(2)
	waitGoalLoopRunning(t, engine, false)
	if got := client.callCount(); got != 2 {
		t.Fatalf("model calls after resume = %d, want 2", got)
	}
}

func TestGoalLoopResumeDuringInterruptedTurnDoesNotLaunchDuplicateLoop(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	client.ignoreCancelUntilRelease = true
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 2 {
			_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)

	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser); err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop after resume: %v", err)
	}
	client.assertNotStarted(t, 2)

	client.releaseCall(1)
	client.waitStarted(t, 2)
	client.releaseCall(2)
	waitGoalLoopRunning(t, engine, false)
	if got := client.callCount(); got != 2 {
		t.Fatalf("model calls after resumed interrupted turn = %d, want 2", got)
	}
}

func TestGoalLoopRetriesWhenExclusiveStepIsBusy(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	baseLifecycle := engine.stepLifecycle
	attempts := 0
	engine.stepLifecycle = &stubExclusiveStepLifecycle{runFn: func(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
		attempts++
		if attempts == 1 {
			return errExclusiveStepBusy
		}
		return baseLifecycle.Run(ctx, options, fn)
	}}
	client.beforeReturn = func(call int) {
		if call == 1 {
			_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)
	client.releaseCall(1)
	waitGoalLoopRunning(t, engine, false)
	if attempts < 2 {
		t.Fatalf("goal loop attempts = %d, want retry after busy step lifecycle", attempts)
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
	for _, entry := range engine.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) && strings.Contains(entry.Text, errExclusiveStepBusy.Error()) {
			t.Fatalf("did not expect busy retry to persist goal-loop error, entries=%+v", engine.ChatSnapshot().Entries)
		}
	}
}

func TestNewDoesNotRestartPersistedActiveGoalLoop(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	if _, err := store.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	reopenedStore := mustOpenTestSession(t, store.Dir())
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, reopenedStore, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	defer func() { _ = engine.Close() }()
	waitGoalLoopRunning(t, engine, false)
	if got := client.callCount(); got != 0 {
		t.Fatalf("model calls after reopen = %d, want 0", got)
	}
	events, err := sessiontest.CollectEvents(reopenedStore)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for _, msg := range goalDeveloperMessages(t, events) {
		if msg.Content == prompts.RenderGoalNudgePrompt("ship goal mode", "active") {
			t.Fatalf("did not expect reopened session to append goal nudge: %+v", msg)
		}
	}
}

func TestNewOpensPersistedActiveGoalWhenAskQuestionDisabled(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	if _, err := store.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	reopenedStore := mustOpenTestSession(t, store.Dir())
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, reopenedStore, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolExecCommand}})
	defer func() { _ = engine.Close() }()

	goal := engine.Goal()
	if goal == nil || goal.Status != session.GoalStatusActive || goal.Objective != "ship goal mode" {
		t.Fatalf("goal after reopen = %+v", goal)
	}
	if engine.GoalLoopSuspended() {
		t.Fatal("did not expect reopened active goal to be reported suspended before an explicit start attempt")
	}
	waitGoalLoopRunning(t, engine, false)
	if got := client.callCount(); got != 0 {
		t.Fatalf("model calls = %d, want 0", got)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause goal after soft reopen: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusPaused {
		t.Fatalf("goal after pause = %+v", goal)
	}
	if _, err := engine.ClearGoal(session.GoalActorUser); err != nil {
		t.Fatalf("clear goal after soft reopen: %v", err)
	}
	if goal := engine.Goal(); goal != nil {
		t.Fatalf("goal after clear = %+v, want nil", goal)
	}
}

func goalDeveloperMessages(t *testing.T, events []session.Event) []llm.Message {
	t.Helper()
	out := []llm.Message{}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeGoal {
			out = append(out, msg)
		}
	}
	return out
}

type scriptedGoalLoopClient struct {
	mu                       sync.Mutex
	calls                    int
	started                  map[int]chan struct{}
	release                  map[int]chan struct{}
	beforeReturn             func(int)
	ignoreCancelUntilRelease bool
}

func newScriptedGoalLoopClient() *scriptedGoalLoopClient {
	return &scriptedGoalLoopClient{
		started: map[int]chan struct{}{},
		release: map[int]chan struct{}{},
	}
}

func (c *scriptedGoalLoopClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	started := c.channelLocked(c.started, call)
	release := c.channelLocked(c.release, call)
	beforeReturn := c.beforeReturn
	close(started)
	c.mu.Unlock()

	if c.ignoreCancelUntilRelease {
		<-release
	} else {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-release:
		}
	}
	if beforeReturn != nil {
		beforeReturn(call)
	}
	return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal}}, nil
}

func (c *scriptedGoalLoopClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func (c *scriptedGoalLoopClient) waitStarted(t *testing.T, call int) {
	t.Helper()
	c.mu.Lock()
	started := c.channelLocked(c.started, call)
	c.mu.Unlock()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for goal loop call %d to start", call)
	}
}

func (c *scriptedGoalLoopClient) assertNotStarted(t *testing.T, call int) {
	t.Helper()
	c.mu.Lock()
	started := c.channelLocked(c.started, call)
	c.mu.Unlock()
	select {
	case <-started:
		t.Fatalf("goal loop call %d started before previous interrupted turn finished", call)
	case <-time.After(50 * time.Millisecond):
	}
}

func (c *scriptedGoalLoopClient) releaseCall(call int) {
	c.mu.Lock()
	release := c.channelLocked(c.release, call)
	c.mu.Unlock()
	close(release)
}

func (c *scriptedGoalLoopClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *scriptedGoalLoopClient) channelLocked(channels map[int]chan struct{}, call int) chan struct{} {
	ch, ok := channels[call]
	if !ok {
		ch = make(chan struct{})
		channels[call] = ch
	}
	return ch
}

func waitGoalLoopRunning(t *testing.T, engine *Engine, want bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		running := engine.goalLoopState().Running()
		if running == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("goalLoopRunning = %t, want %t", running, want)
		case <-ticker.C:
		}
	}
}
