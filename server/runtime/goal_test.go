package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

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

func mustGoalRunID(t *testing.T) runtimeids.RunID {
	t.Helper()
	id, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Goal Run ID: %v", err)
	}
	return id
}

func mustGoalStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse Goal Step ID: %v", err)
	}
	return id
}

func TestExactAgentGoalSetDrainsAfterToolCompletion(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	scopeID := runtimeids.NewExecutionScopeID()
	workflowConfig := &workflowruntime.CurrentNodeExecutionConfig{ScopeID: scopeID, CompletionMode: workflowruntime.CompletionModeTool}
	publishTestWorkflowExecution(t, engine, workflowConfig)
	runID := mustGoalRunID(t)
	stepID := mustGoalStepID(t)
	engine.stepLifecycle = &stubExclusiveStepLifecycle{activeStepID: stepID.String(), snapshot: &RunSnapshot{RunID: runID.String(), StepID: stepID.String()}}

	accepted, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, stepID, GoalMutation{
		Kind: GoalMutationSet, Objective: "queued goal", Actor: session.GoalActorAgent,
	})
	if err != nil || accepted.Disposition != GoalCommandQueued {
		t.Fatalf("ScheduleExactAgentGoalMutation result=%+v err=%v, want queued", accepted, err)
	}
	if g := engine.Goal(); g != nil {
		t.Fatalf("goal applied before drain: %+v", g)
	}
	assistant := llm.Message{
		Role:  llm.RoleAssistant,
		Phase: textutil.Value(llm.MessagePhaseCommentary),
		ToolCalls: []llm.ToolCall{{
			ID:   "call-shell",
			Name: string(toolspec.ToolExecCommand),
		}},
	}
	if err := engine.steer(stepID.String(), steerMessagesWithPersistenceIntent(steeringMessageEventNone, true, []llm.Message{assistant})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	result := tools.Result{
		CallID:  "call-shell",
		Name:    toolspec.ToolExecCommand,
		Output:  json.RawMessage(`{"output":"ok","exit_code":0,"truncated":false}`),
		Summary: textutil.Value("ok"),
	}
	if err := engine.steer(stepID.String(), steerToolCompletionIntent(result)); err != nil {
		t.Fatalf("append tool completion: %v", err)
	}
	engine.drainSteeringAtBoundary(context.Background(), stepID.String())

	if g := engine.Goal(); g == nil || g.Objective != "queued goal" || g.Status != session.GoalStatusActive {
		t.Fatalf("goal after drain = %+v, want active 'queued goal'", g)
	}
	messages := engine.transcriptRuntimeState().SnapshotMessages()
	assistantIdx, toolIdx, goalIdx := -1, -1, -1
	for idx, msg := range messages {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "call-shell" {
			assistantIdx = idx
		}
		if msg.Role == llm.RoleTool && msg.ToolCallID != nil && *msg.ToolCallID == "call-shell" {
			toolIdx = idx
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeGoal {
			goalIdx = idx
		}
	}
	if assistantIdx < 0 || toolIdx < 0 || goalIdx < 0 {
		t.Fatalf("message indexes assistant/tool/goal = %d/%d/%d, messages=%+v", assistantIdx, toolIdx, goalIdx, messages)
	}
	if !(assistantIdx < toolIdx && toolIdx < goalIdx) {
		t.Fatalf("message order assistant/tool/goal = %d/%d/%d, want tool result before goal mutation", assistantIdx, toolIdx, goalIdx)
	}
}

func TestExactAgentGoalCompleteSeesEarlierQueuedSet(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	scopeID := runtimeids.NewExecutionScopeID()
	workflowConfig := &workflowruntime.CurrentNodeExecutionConfig{ScopeID: scopeID, CompletionMode: workflowruntime.CompletionModeTool}
	publishTestWorkflowExecution(t, engine, workflowConfig)
	runID := mustGoalRunID(t)
	stepID := mustGoalStepID(t)
	engine.stepLifecycle = &stubExclusiveStepLifecycle{activeStepID: stepID.String(), snapshot: &RunSnapshot{RunID: runID.String(), StepID: stepID.String()}}

	if _, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, stepID, GoalMutation{Kind: GoalMutationSet, Objective: "queued goal", Actor: session.GoalActorAgent}); err != nil {
		t.Fatalf("schedule set: %v", err)
	}
	accepted, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, stepID, GoalMutation{Kind: GoalMutationStatus, Status: session.GoalStatusComplete, Actor: session.GoalActorAgent})
	if err != nil || accepted.Disposition != GoalCommandQueued {
		t.Fatalf("schedule complete result=%+v err=%v, want queued", accepted, err)
	}
	if accepted.Objective != "queued goal" || accepted.Status != session.GoalStatusComplete {
		t.Fatalf("accepted completion = %+v, want completed 'queued goal'", accepted)
	}
	engine.drainSteeringAtBoundary(context.Background(), stepID.String())
	if g := engine.Goal(); g == nil || g.Objective != "queued goal" || g.Status != session.GoalStatusComplete {
		t.Fatalf("goal after drain = %+v, want completed 'queued goal'", g)
	}
}

func TestExactAgentGoalSetRejectsEarlierProjectedActiveGoal(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	scopeID := runtimeids.NewExecutionScopeID()
	workflowConfig := &workflowruntime.CurrentNodeExecutionConfig{ScopeID: scopeID, CompletionMode: workflowruntime.CompletionModeTool}
	publishTestWorkflowExecution(t, engine, workflowConfig)
	runID := mustGoalRunID(t)
	stepID := mustGoalStepID(t)
	engine.stepLifecycle = &stubExclusiveStepLifecycle{activeStepID: stepID.String(), snapshot: &RunSnapshot{RunID: runID.String(), StepID: stepID.String()}}

	if _, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, stepID, GoalMutation{Kind: GoalMutationSet, Objective: "first goal", Actor: session.GoalActorAgent}); err != nil {
		t.Fatalf("schedule first: %v", err)
	}
	var blocked session.GoalAgentOverwriteBlockedError
	if _, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, stepID, GoalMutation{Kind: GoalMutationSet, Objective: "second goal", Actor: session.GoalActorAgent}); !errors.As(err, &blocked) {
		t.Fatalf("schedule second err=%T %[1]v, want overwrite blocked", err)
	}
	engine.drainSteeringAtBoundary(context.Background(), stepID.String())
	if g := engine.Goal(); g == nil || g.Objective != "first goal" || g.Status != session.GoalStatusActive {
		t.Fatalf("goal after drain = %+v, want active first goal", g)
	}
}

func TestExactAgentGoalSetForEndedStepIsRejected(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	scopeID := runtimeids.NewExecutionScopeID()
	workflowConfig := &workflowruntime.CurrentNodeExecutionConfig{ScopeID: scopeID, CompletionMode: workflowruntime.CompletionModeTool}
	publishTestWorkflowExecution(t, engine, workflowConfig)
	runID := mustGoalRunID(t)
	stepID := mustGoalStepID(t)
	otherStep, _ := runtimeids.ParseStepID("33333333-3333-4333-8333-333333333333")
	engine.stepLifecycle = &stubExclusiveStepLifecycle{activeStepID: otherStep.String(), snapshot: &RunSnapshot{RunID: runID.String(), StepID: otherStep.String()}}

	if _, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, stepID, GoalMutation{Kind: GoalMutationSet, Objective: "stale background goal", Actor: session.GoalActorAgent}); !errors.Is(err, ErrAgentGoalStepInactive) {
		t.Fatalf("schedule stale Goal err=%v, want inactive originating step", err)
	}
	if g := engine.Goal(); g != nil {
		t.Fatalf("stale background shell mutated goal: %+v", g)
	}
}

func TestUserGoalMutationsUseOrdinarySteeringDuringActiveStep(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	engine.stepLifecycle = &stubExclusiveStepLifecycle{activeStepID: "step-1", snapshot: &RunSnapshot{RunID: "run-1", StepID: "step-1"}}

	accepted, err := engine.ApplyGoalMutation(GoalMutation{Kind: GoalMutationSet, Objective: "queued user goal", Actor: session.GoalActorUser, StartLoop: true})
	if err != nil || accepted.Disposition != GoalCommandApplied {
		t.Fatalf("ApplyGoalMutation result=%+v err=%v, want applied after ordinary drain", accepted, err)
	}
	if accepted.Objective != "queued user goal" || accepted.Status != session.GoalStatusActive {
		t.Fatalf("accepted goal = %+v, want active queued user goal", accepted)
	}
	if g := engine.Goal(); g == nil || g.Objective != "queued user goal" || g.Status != session.GoalStatusActive {
		t.Fatalf("goal after drain = %+v, want active queued user goal", g)
	}
}

func TestOrdinaryActiveGoalResumeSchedulesRestart(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	engine.stepLifecycle = &stubExclusiveStepLifecycle{activeStepID: "step-1", snapshot: &RunSnapshot{RunID: "run-1", StepID: "step-1"}}
	if _, err := engine.SetGoal("queued resume goal", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	engine.goalLoopState().Suspend()

	accepted, err := engine.ApplyGoalMutation(GoalMutation{Kind: GoalMutationStatus, Status: session.GoalStatusActive, Actor: session.GoalActorUser, StartLoop: true})
	if err != nil {
		t.Fatalf("ApplyGoalMutation: %v", err)
	}
	if accepted.Status != session.GoalStatusActive {
		t.Fatalf("accepted status = %q, want active", accepted.Status)
	}
	if engine.pendingGoalLoopStart {
		t.Fatal("empty Steering drain must consume the pending Goal loop restart")
	}
}

func TestRetainedWorkflowControlRejectsExecutionStartingGoalsBeforeSteeringAcceptance(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	if _, err := engine.SetGoal("paused retained goal", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause Goal: %v", err)
	}
	engine.EnterRetainedWorkflowControl()
	if _, err := engine.ApplyGoalMutationDeferred(GoalMutation{Kind: GoalMutationSet, Objective: "replacement", Actor: session.GoalActorUser, StartLoop: true}); !errors.Is(err, ErrSteeringUnavailable) {
		t.Fatalf("retained Goal Set error = %v, want unavailable", err)
	}
	if _, err := engine.ApplyGoalMutationDeferred(GoalMutation{
		Kind: GoalMutationStatus, Status: session.GoalStatusActive, Actor: session.GoalActorUser, StartLoop: true,
	}); !errors.Is(err, ErrSteeringUnavailable) {
		t.Fatalf("retained Goal Resume error = %v, want unavailable", err)
	}
	if result, err := engine.SetGoalStatusWithoutGoalLoopStart(session.GoalStatusPaused, session.GoalActorUser); err != nil || result.Disposition != GoalCommandNoop {
		t.Fatal("retained Goal no-op")
	}
}

func TestExactGoalMutationDuringClosingActiveStepRemainsEligible(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	engine.stepLifecycle = lifecycle
	_, stepID, err := lifecycle.begin(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	lifecycle.closeActiveStepQueue(stepID)

	scopeID := runtimeids.NewExecutionScopeID()
	publishTestWorkflowExecution(t, engine, &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID:        scopeID,
		CompletionMode: workflowruntime.CompletionModeTool,
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "late-goal-task", "late-goal-node", nil),
		},
	})
	runID, err := runtimeids.ParseRunID(lifecycle.Snapshot().RunID)
	if err != nil {
		t.Fatalf("parse active Run ID: %v", err)
	}
	typedStepID, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		t.Fatalf("parse active Step ID: %v", err)
	}
	if _, err := engine.ScheduleExactAgentGoalMutation(scopeID, runID, typedStepID, GoalMutation{Kind: GoalMutationSet, Objective: "late goal", Actor: session.GoalActorAgent}); err != nil {
		t.Fatalf("ScheduleExactAgentGoalMutation during Step-closing: %v", err)
	}
	if g := engine.Goal(); g != nil {
		t.Fatalf("goal applied while active-step queue is closing: %+v", g)
	}
	lifecycle.end()
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
	assertGoalFeedbackThenStatusEvent(t, events, set.GoalState, false)

	paused, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser)
	if err != nil {
		t.Fatalf("pause goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, paused.GoalState, false)

	active, err := engine.SetGoalStatus(session.GoalStatusActive, session.GoalActorUser)
	if err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, active.GoalState, false)

	complete, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent)
	if err != nil {
		t.Fatalf("complete goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, complete.GoalState, false)

	cleared, err := engine.ClearGoal(session.GoalActorUser)
	if err != nil {
		t.Fatalf("clear goal: %v", err)
	}
	assertGoalFeedbackThenStatusEvent(t, events, cleared.GoalState, true)
}

func assertGoalFeedbackThenStatusEvent(t *testing.T, events []Event, goal session.GoalState, cleared bool) {
	t.Helper()
	statusIndex := -1
	for index := len(events) - 1; index >= 0; index-- {
		status := events[index]
		if status.Kind != EventGoalStatusUpdated || status.GoalStatus == nil ||
			status.GoalStatus.Cleared != cleared {
			continue
		}
		if !cleared &&
			(status.GoalStatus.State.ID != goal.ID ||
				status.GoalStatus.State.Objective != goal.Objective ||
				status.GoalStatus.State.Status != goal.Status) {
			continue
		}
		statusIndex = index
		break
	}
	if statusIndex < 0 {
		t.Fatalf("missing matching goal status event for %+v (cleared=%t): %+v", goal, cleared, events)
	}
	for index := statusIndex - 1; index >= 0; index-- {
		feedback := events[index]
		if feedback.Kind == EventConversationUpdated && feedback.CommittedTranscriptChanged &&
			feedback.Message.MessageType != nil && *feedback.Message.MessageType == llm.MessageTypeGoal {
			return
		}
	}
	t.Fatalf("goal status event at %d had no earlier committed goal feedback: %+v", statusIndex, events)
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
	})
	publishTestWorkflowExecution(t, engine, &workflowruntime.CurrentNodeExecutionConfig{ScopeID: runtimeids.NewExecutionScopeID()})
	engine.SetQuestionsEnabled(false)
	if _, err := engine.SetGoal("ship workflow goal", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if err := engine.requireAskQuestionWhenGoalActive(); !errors.Is(err, ErrGoalRequiresAskQuestion) {
		t.Fatalf("workflow active goal preflight error = %v, want ErrGoalRequiresAskQuestion", err)
	}
}

func TestActiveGoalAllowsModelTurnWithAskQuestionEnabled(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		activeStepID: "step-1",
		snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: "step-1"},
	}
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
	engine.stepLifecycle = &stubExclusiveStepLifecycle{
		activeStepID: "step-1",
		snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: "step-1"},
	}
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
	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	messages := goalDeveloperMessages(t, events)
	if len(messages) < 2 {
		t.Fatalf("goal developer messages len = %d, want at least 2", len(messages))
	}
	if got := messageContent(messages[1]); got != prompts.RenderGoalNudgePrompt("ship goal mode", "active") {
		t.Fatalf("nudge prompt = %q", got)
	}
	if got := messages[1].CompactContent; clientui.GoalNudgeCompactLabel == "" || got == nil || *got != clientui.GoalNudgeCompactLabel {
		t.Fatalf("nudge compact content = %v, want non-empty shared label %q", got, clientui.GoalNudgeCompactLabel)
	}
}

func TestGoalBlankFinalUsesRegularContinuationNudge(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := &fakeClient{responses: []llm.Response{
		finalTextResponse(""),
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
	if messageContent(msg) != "" {
		t.Fatalf("first assistant content = %q, want blank", messageContent(msg))
	}
	msg, err = engine.runGoalTurn(t.Context(), true)
	if err != nil {
		t.Fatalf("runGoalTurn continuation: %v", err)
	}
	if messageContent(msg) != "working" {
		t.Fatalf("assistant content = %q, want working", messageContent(msg))
	}
	assertModelCallCount(t, client, 2)
	secondReq := requestMessages(client.calls[1])
	foundNudge := false
	for _, reqMsg := range secondReq {
		if reqMsg.Role == llm.RoleDeveloper && messageContent(reqMsg) == prompts.RenderGoalNudgePrompt("ship goal mode", "active") {
			if reqMsg.MessageType == nil || *reqMsg.MessageType != llm.MessageTypeGoal {
				t.Fatalf("goal nudge message type = %v, want goal", reqMsg.MessageType)
			}
			foundNudge = true
		}
	}
	if !foundNudge {
		t.Fatalf("expected regular goal nudge in second request, got %+v", secondReq)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	messages := goalDeveloperMessages(t, events)
	if len(messages) != 3 {
		t.Fatalf("goal developer messages len = %d, want set plus two regular nudges: %+v", len(messages), messages)
	}
}

func TestGoalDeveloperMessageVisibleInOngoingWithDetailPrompt(t *testing.T) {
	msg := llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    textutil.Value(llm.MessageTypeGoal),
		Content:        textutil.Value(prompts.RenderGoalNudgePrompt("ship goal mode", "active")),
		CompactContent: textutil.Value(clientui.GoalNudgeCompactLabel),
	}

	entries := VisibleChatEntriesFromMessage(msg)
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Role != string(transcript.EntryRoleGoalFeedback) {
		t.Fatalf("goal role = %q, want %q", entry.Role, transcript.EntryRoleGoalFeedback)
	}
	if entry.Visibility != transcript.EntryVisibilityOngoing {
		t.Fatalf("goal visibility = %q, want ongoing", entry.Visibility)
	}
	if entry.Text != messageContent(msg) {
		t.Fatalf("goal detail text = %q, want full prompt", entry.Text)
	}
	if msg.CompactContent == nil || entry.CondensedText != *msg.CompactContent {
		t.Fatalf("goal condensed text = %q, want compact", entry.CondensedText)
	}
}
func TestSurfaceRunError(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	t.Run("ignores benign terminations", func(t *testing.T) {
		for _, benign := range []error{nil, context.Canceled, ErrAgentBusy, errGoalLoopInactive, ErrEngineClosed} {
			engine.surfaceRunError(benign)
		}

		snapshot := engine.ChatSnapshot()
		for _, entry := range snapshot.Entries {
			if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				t.Fatalf("benign termination surfaced an error entry: %+v", entry)
			}
		}
		if snapshot.StreamingError != "" {
			t.Fatalf("benign termination set a streaming error banner: %q", snapshot.StreamingError)
		}
	})

	t.Run("persists operator feedback", func(t *testing.T) {
		if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
			t.Fatalf("SetGoal: %v", err)
		}

		runErr := errors.New("provider down")
		engine.surfaceRunError(runErr)

		snapshot := engine.ChatSnapshot()
		found := false
		for _, entry := range snapshot.Entries {
			if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) && entry.Text == runErr.Error() {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected surfaced run error entry, got %+v", snapshot.Entries)
		}
		if snapshot.StreamingError == "" {
			t.Fatal("expected streaming error banner to be set")
		}
	})

	t.Run("prefers user-facing message for stall", func(t *testing.T) {
		engine.surfaceRunError(fmt.Errorf("model generation failed after retries: %w", llm.ErrModelStreamStalled))

		snapshot := engine.ChatSnapshot()
		want := llm.UserFacingError(llm.ErrModelStreamStalled)
		if want == "" {
			t.Fatal("expected stall sentinel to have a user-facing message")
		}
		for _, entry := range snapshot.Entries {
			if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) && entry.Text == want {
				return
			}
		}
		t.Fatalf("expected user-facing stall message entry, got %+v", snapshot.Entries)
	})
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

			mutateDone := make(chan error, 1)
			go func() {
				mutateDone <- tt.mutate(engine)
			}()
			select {
			case err := <-mutateDone:
				t.Fatalf("goal mutation applied during protected Step: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			client.releaseCall(1)
			if err := <-mutateDone; err != nil {
				t.Fatalf("mutate goal: %v", err)
			}
			waitGoalLoopRunning(t, engine, false)
			waitActiveLiveRunGroup(t, engine, false)
			if got := client.callCount(); got != 1 {
				t.Fatalf("model calls = %d, want 1", got)
			}
		})
	}
}

func TestGoalLoopKeepsLiveRunActiveAcrossAutoContinuingTurns(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 2 {
			completeGoalFromActiveStep(engine)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)
	waitActiveLiveRunGroup(t, engine, true)

	waitDone := make(chan error, 1)
	go func() {
		_, err := engine.WaitForActiveRunResult(context.Background())
		waitDone <- err
	}()

	client.releaseCall(1)
	client.waitStarted(t, 2)
	assertWaitStillBlocked(t, waitDone)
	waitActiveLiveRunGroup(t, engine, true)

	client.releaseCall(2)
	waitGoalLoopRunning(t, engine, false)
	waitActiveLiveRunGroup(t, engine, false)
	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrLiveRunNoFinalAnswer) {
			t.Fatalf("WaitForActiveRunResult error = %v, want %v", err, ErrLiveRunNoFinalAnswer)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for live run result")
	}
}

func TestGoalLoopInterruptSuspendsUntilResumeRestarts(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 2 {
			completeGoalFromActiveStep(engine)
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

func TestInterruptIdleActiveGoalDoesNotSuspendGoalLoop(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	engine := mustNewTestEngine(t, store, newScriptedGoalLoopClient(), tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if engine.GoalLoopSuspended() {
		t.Fatal("idle active goal must not be suspended by a no-op interrupt")
	}
}

func TestSuspendedGoalAutoResumesAfterSuccessfulUserTurnOnly(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 2 {
			completeGoalFromActiveStep(engine)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	engine.goalLoopState().Suspend()

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "continue")
		done <- err
	}()
	client.waitStarted(t, 1)
	if !engine.GoalLoopSuspended() {
		t.Fatal("suspended goal resumed before user turn completed")
	}
	client.releaseCall(1)
	if err := <-done; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	client.waitStarted(t, 2)
	client.releaseCall(2)
	waitGoalLoopRunning(t, engine, false)
	if engine.GoalLoopSuspended() {
		t.Fatal("suspended goal did not resume after successful user turn")
	}
}

func TestSuspendedGoalStaysSuspendedAfterInterruptedUserTurn(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	engine.goalLoopState().Suspend()

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "continue")
		done <- err
	}()
	client.waitStarted(t, 1)
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SubmitUserMessage err = %v, want context canceled", err)
	}
	if !engine.GoalLoopSuspended() {
		t.Fatal("interrupted user turn must leave goal suspended")
	}
	client.assertNotStarted(t, 2)
}

func TestGoalLoopResumeDuringInterruptedTurnDoesNotLaunchDuplicateLoop(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	client.ignoreCancelUntilRelease = true
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	t.Cleanup(func() {
		client.releaseCall(1)
		client.releaseCall(2)
	})
	client.beforeReturn = func(call int) {
		if call == 2 {
			completeGoalFromActiveStep(engine)
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
	resumed, err := engine.ApplyGoalMutationDeferred(GoalMutation{
		Kind:      GoalMutationStatus,
		Status:    session.GoalStatusActive,
		Actor:     session.GoalActorUser,
		StartLoop: true,
	})
	if err != nil {
		t.Fatalf("resume goal: %v", err)
	}
	if resumed.Disposition != GoalCommandQueued {
		t.Fatalf("resume disposition = %v, want queued", resumed.Disposition)
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

func TestGoalResumeWhileInterruptIsPublishingSchedulesRestart(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	client.ignoreCancelUntilRelease = true
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 2 {
			completeGoalFromActiveStep(engine)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)

	released := map[int]bool{}
	releaseCall := func(call int) {
		if released[call] {
			return
		}
		released[call] = true
		client.releaseCall(call)
	}
	defer releaseCall(2)
	defer releaseCall(1)

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- engine.Interrupt()
	}()
	waitGoalLoopContinuationEnforced(t, engine, false)

	entry := newOutputSteeringQueueEntry("", false, steeringIntent{items: []steeringMutation{
		&steeringGoalMutation{mutation: GoalMutation{
			Kind:      GoalMutationStatus,
			Status:    session.GoalStatusActive,
			Actor:     session.GoalActorUser,
			StartLoop: true,
		}},
	}})
	wake, err := engine.steering.append(entry)
	if err != nil {
		t.Fatalf("append Goal resume during in-flight interrupt: %v", err)
	}
	if wake {
		engine.wakeSteeringDrain()
	}

	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for interrupt publication")
	}

	releaseCall(1)
	client.waitStarted(t, 2)
	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	messages := goalDeveloperMessages(t, events)
	if len(messages) != 2 {
		t.Fatalf("goal developer messages after interrupt race resume = %d, want set+resume", len(messages))
	}
	releaseCall(2)
	waitGoalLoopRunning(t, engine, false)
}

func TestManualCompactionSubmittedDuringGoalTurnRunsBeforeNextGoalTurn(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	client.beforeReturn = func(call int) {
		if call == 3 {
			completeGoalFromActiveStep(engine)
		}
	}
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)

	compactDone := make(chan error, 2)
	go func() { compactDone <- engine.CompactContext(context.Background(), "preserve active goal") }()
	go func() { compactDone <- engine.CompactContext(context.Background(), "duplicate request") }()
	time.Sleep(75 * time.Millisecond)
	client.releaseCall(1)

	client.waitStarted(t, 2)
	if active := engine.ActiveRun(); active == nil || active.ActiveKind != ActiveKindCompaction {
		t.Fatalf("second model request active run = %+v, want compaction before the next goal turn", active)
	}
	client.releaseCall(2)
	first, second := <-compactDone, <-compactDone
	if (first == nil) == (second == nil) || (!errors.Is(first, ErrManualCompactionTooSoon) && !errors.Is(second, ErrManualCompactionTooSoon)) {
		t.Fatalf("duplicate compact errors = (%v, %v), want one success and one too-soon result", first, second)
	}
	if got := engine.CompactionCount(); got != 1 {
		t.Fatalf("compaction count = %d, want 1", got)
	}

	client.waitStarted(t, 3)
	client.releaseCall(3)
	waitGoalLoopRunning(t, engine, false)
}

func TestNewDoesNotRestartPersistedActiveGoalLoop(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	if _, _, err := store.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
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
	events, err := collectTestEventRecords(reopenedStore)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if messages := goalDeveloperMessages(t, events); len(messages) != 0 {
		t.Fatalf("reopened session appended goal messages: %+v", messages)
	}
}

func TestNewOpensPersistedActiveGoalWhenAskQuestionDisabled(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	if _, _, err := store.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
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

func goalDeveloperMessages(t *testing.T, events []testPersistedEvent) []llm.Message {
	t.Helper()
	out := []llm.Message{}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		msg := persistedMessageForTest(t, evt)
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil && *msg.MessageType == llm.MessageTypeGoal {
			out = append(out, msg)
		}
	}
	return out
}

func completeGoalFromActiveStep(engine *Engine) {
	active := engine.ActiveRun()
	if active == nil {
		return
	}
	_ = engine.steer(active.StepID, steeringIntent{items: []steeringMutation{
		&steeringGoalMutation{mutation: GoalMutation{
			Kind:   GoalMutationStatus,
			Status: session.GoalStatusComplete,
			Actor:  session.GoalActorAgent,
		}},
	}})
}

type scriptedGoalLoopClient struct {
	mu                       sync.Mutex
	calls                    int
	started                  map[int]chan struct{}
	release                  map[int]chan struct{}
	releaseOnce              map[int]*sync.Once
	beforeReturn             func(int)
	ignoreCancelUntilRelease bool
}

func newScriptedGoalLoopClient() *scriptedGoalLoopClient {
	return &scriptedGoalLoopClient{
		started:     map[int]chan struct{}{},
		release:     map[int]chan struct{}{},
		releaseOnce: map[int]*sync.Once{},
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
		if err := ctx.Err(); err != nil {
			return llm.Response{}, err
		}
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
	return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}}, nil
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
	case <-time.After(runtimeTestSynchronizationTimeout):
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
	once, ok := c.releaseOnce[call]
	if !ok {
		once = &sync.Once{}
		c.releaseOnce[call] = once
	}
	c.mu.Unlock()
	once.Do(func() { close(release) })
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
	deadline := time.After(runtimeTestSynchronizationTimeout)
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

func waitGoalLoopContinuationEnforced(t *testing.T, engine *Engine, want bool) {
	t.Helper()
	deadline := time.After(runtimeTestSynchronizationTimeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		enforced := engine.GoalLoopContinuationEnforced()
		if enforced == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("goalLoopContinuationEnforced = %t, want %t", enforced, want)
		case <-ticker.C:
		}
	}
}

func waitActiveLiveRunGroup(t *testing.T, engine *Engine, want bool) {
	t.Helper()
	deadline := time.After(runtimeTestSynchronizationTimeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		active := engine.HasActiveLiveRunGroup()
		if active == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("HasActiveLiveRunGroup = %t, want %t", active, want)
		case <-ticker.C:
		}
	}
}

func assertWaitStillBlocked(t *testing.T, waitDone <-chan error) {
	t.Helper()
	select {
	case err := <-waitDone:
		t.Fatalf("live wait completed before auto-continuing goal turn finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}
