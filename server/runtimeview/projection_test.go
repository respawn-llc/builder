package runtimeview

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"
)

const (
	projectionWorkspaceID  = "10000000-0000-4000-8000-000000000001"
	projectionRunID        = "10000000-0000-4000-8000-000000000002"
	projectionStepID       = "10000000-0000-4000-8000-000000000003"
	projectionBackgroundID = "10000000-0000-4000-8000-000000000004"
	projectionGoalID       = "10000000-0000-4000-8000-000000000005"
	projectionParentID     = "10000000-0000-4000-8000-000000000006"
	projectionWorkflowRun  = "10000000-0000-4000-8000-000000000007"
	projectionWorkflowTask = "10000000-0000-4000-8000-000000000008"
	projectionWorkflowID   = "10000000-0000-4000-8000-000000000009"
)

type projectionFastClient struct{}

func (projectionFastClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (projectionFastClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}

type projectionBlockingClient struct {
	started chan struct{}
	release chan struct{}
}

func newProjectionBlockingClient() *projectionBlockingClient {
	return &projectionBlockingClient{started: make(chan struct{}), release: make(chan struct{})}
}

func (c *projectionBlockingClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-c.release:
		return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal}}, nil
	}
}

func (c *projectionBlockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}

type projectionPreciseClient struct {
	inputTokens int
}

func (c projectionPreciseClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{InputTokens: 900, OutputTokens: 100, WindowTokens: 400_000},
	}, nil
}

func (c projectionPreciseClient) CountRequestInputTokens(context.Context, llm.Request) (int, error) {
	return c.inputTokens, nil
}

func (c projectionPreciseClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsRequestInputTokenCount: true, IsOpenAIFirstParty: true}, nil
}

func newRuntimeViewStore(t *testing.T) *session.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := session.Create(dir, projectionWorkspaceID, dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func newRuntimeViewEngine(t *testing.T, store *session.Store, client llm.Client, cfg ...runtime.Config) *runtime.Engine {
	t.Helper()
	engineConfig := runtime.Config{Model: "gpt-5"}
	if len(cfg) > 0 {
		engineConfig = cfg[0]
	}
	engine, err := runtime.New(store, client, tools.NewRegistry(), engineConfig)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestEventFromRuntimeProjectsReasoningBackgroundAndRunState(t *testing.T) {
	exitCode := 17
	view := EventFromRuntime(runtime.Event{
		Kind:                       runtime.EventBackgroundUpdated,
		StepID:                     projectionStepID,
		CommittedTranscriptChanged: true,
		AssistantDelta:             "delta",
		AssistantDeltaPhase:        llm.MessagePhaseFinal,
		ReasoningDelta:             &llm.ReasoningSummaryDelta{Key: "k", Role: "reasoning", Text: "thinking"},
		RunState:                   &runtime.RunState{Lifecycle: runtime.RunningRunLifecycle(runtime.RunModeTurn), RunID: projectionRunID, ActiveKind: runtime.ActiveKindUserTurn, Status: runtime.RunStatusRunning},
		Background: &runtime.BackgroundShellEvent{
			Type:              "completed",
			ID:                projectionBackgroundID,
			State:             "completed",
			Command:           "echo hi",
			Workdir:           "/tmp/work",
			LogPath:           "/tmp/work/run.log",
			NoticeText:        "done",
			CompactText:       "done compact",
			Preview:           "hi",
			Removed:           2,
			ExitCode:          &exitCode,
			UserRequestedKill: true,
			NoticeSuppressed:  true,
		},
	})
	if view.Kind != clientui.EventBackgroundUpdated || view.StepID != projectionStepID || view.AssistantDelta != "delta" {
		t.Fatalf("unexpected projected event: %+v", view)
	}
	if !view.CommittedTranscriptChanged {
		t.Fatalf("expected committed transcript change flag projected, got %+v", view)
	}
	if view.ReasoningDelta == nil || view.ReasoningDelta.Text != "thinking" {
		t.Fatalf("expected reasoning delta projection, got %+v", view.ReasoningDelta)
	}
	if view.AssistantDeltaPhase != clientui.MessagePhaseFinal {
		t.Fatalf("expected assistant delta phase projection, got %q", view.AssistantDeltaPhase)
	}
	if view.RunState == nil || !view.RunState.Lifecycle.IsRunning() {
		t.Fatalf("expected busy run state, got %+v", view.RunState)
	}
	if view.RunState.RunID != projectionRunID || view.RunState.Status != clientui.RunStatusRunning {
		t.Fatalf("expected run identity in projected run state, got %+v", view.RunState)
	}
	if view.RunState.ActiveKind != clientui.RuntimeActivityActiveKindUserTurn {
		t.Fatalf("expected active kind projection, got %+v", view.RunState)
	}
	if view.RunState.Lifecycle.Phase != clientui.RunLifecycleRunning || view.RunState.Lifecycle.Mode != clientui.RunModeTurn {
		t.Fatalf("server/client run lifecycle projection mismatch: %+v", view.RunState.Lifecycle)
	}
	if view.Background == nil || view.Background.ID != projectionBackgroundID {
		t.Fatalf("expected background projection, got %+v", view.Background)
	}
	if view.Background.ExitCode == nil || *view.Background.ExitCode != 17 {
		t.Fatalf("expected copied exit code, got %+v", view.Background.ExitCode)
	}
}

func TestActivityFromRuntimeSnapshotCopiesRuntimeOwnedActiveKinds(t *testing.T) {
	tests := []struct {
		name string
		kind runtime.ActiveKind
		want clientui.RuntimeActivityActiveKind
	}{
		{name: "user turn", kind: runtime.ActiveKindUserTurn, want: clientui.RuntimeActivityActiveKindUserTurn},
		{name: "workflow turn", kind: runtime.ActiveKindWorkflowTurn, want: clientui.RuntimeActivityActiveKindWorkflowTurn},
		{name: "goal loop", kind: runtime.ActiveKindGoalLoop, want: clientui.RuntimeActivityActiveKindGoalLoop},
		{name: "compaction", kind: runtime.ActiveKindCompaction, want: clientui.RuntimeActivityActiveKindCompaction},
		{name: "pre-submit compaction", kind: runtime.ActiveKindPreSubmitCompaction, want: clientui.RuntimeActivityActiveKindPreSubmitCompaction},
		{name: "user shell", kind: runtime.ActiveKindUserShell, want: clientui.RuntimeActivityActiveKindUserShell},
		{name: "background", kind: runtime.ActiveKindBackground, want: clientui.RuntimeActivityActiveKindBackground},
		{name: "runtime maintenance", kind: runtime.ActiveKindRuntimeMaintenance, want: clientui.RuntimeActivityActiveKindRuntimeMaintenance},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := ActivityFromRuntimeSnapshot(&runtime.RunSnapshot{
				RunID:      projectionRunID,
				StepID:     projectionStepID,
				Status:     runtime.RunStatusRunning,
				ActiveKind: tt.kind,
			}, true)
			if activity.State != clientui.RuntimeActivityRunning || activity.ActiveKind != tt.want {
				t.Fatalf("activity = %+v, want running %q", activity, tt.want)
			}
			if !activity.QueueAccepting {
				t.Fatalf("queue accepting was not preserved in activity: %+v", activity)
			}
		})
	}
}

func TestEventFromRuntimeProjectsGoalStatusUpdated(t *testing.T) {
	testCases := []struct {
		name string
		evt  runtime.Event
		want *clientui.RuntimeGoalStatusUpdate
	}{
		{
			name: "active",
			evt: runtime.Event{
				Kind: runtime.EventGoalStatusUpdated,
				GoalStatus: &runtime.GoalStatusUpdate{State: session.GoalState{
					ID:        " " + projectionGoalID + " ",
					Objective: "ship feature",
					Status:    session.GoalStatusActive,
				}},
			},
			want: &clientui.RuntimeGoalStatusUpdate{ID: projectionGoalID, Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
		},
		{
			name: "paused",
			evt: runtime.Event{
				Kind: runtime.EventGoalStatusUpdated,
				GoalStatus: &runtime.GoalStatusUpdate{State: session.GoalState{
					ID:        projectionGoalID,
					Objective: "ship feature",
					Status:    session.GoalStatusPaused,
				}},
			},
			want: &clientui.RuntimeGoalStatusUpdate{ID: projectionGoalID, Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
		},
		{
			name: "complete",
			evt: runtime.Event{
				Kind: runtime.EventGoalStatusUpdated,
				GoalStatus: &runtime.GoalStatusUpdate{State: session.GoalState{
					ID:        projectionGoalID,
					Objective: "ship feature",
					Status:    session.GoalStatusComplete,
				}},
			},
			want: &clientui.RuntimeGoalStatusUpdate{ID: projectionGoalID, Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete},
		},
		{
			name: "clear",
			evt:  runtime.Event{Kind: runtime.EventGoalStatusUpdated, GoalStatus: &runtime.GoalStatusUpdate{Cleared: true}},
			want: &clientui.RuntimeGoalStatusUpdate{Cleared: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			view := EventFromRuntime(tc.evt)
			if view.Kind != clientui.EventGoalStatusUpdated {
				t.Fatalf("kind = %q, want %q", view.Kind, clientui.EventGoalStatusUpdated)
			}
			if view.GoalStatus == nil {
				t.Fatal("expected projected goal status payload")
			}
			if *view.GoalStatus != *tc.want {
				t.Fatalf("goal status = %+v, want %+v", view.GoalStatus, tc.want)
			}
		})
	}
}

func TestStatusFromRuntimeIncludesSuspendedGoal(t *testing.T) {
	client := newProjectionBlockingClient()
	engine := newRuntimeViewEngine(t, newRuntimeViewStore(t), client, runtime.Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship feature", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("start goal loop: %v", err)
	}
	<-client.started
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	close(client.release)

	status := StatusFromRuntime(engine)
	if status.Goal == nil || !status.Goal.Suspended {
		t.Fatalf("goal status = %+v, want suspended goal", status.Goal)
	}
}

func TestMainViewFromRuntimeBundlesStatusAndSession(t *testing.T) {
	store := newRuntimeViewStore(t)
	if err := store.SetName("Session Name"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := store.SetParentSessionID(projectionParentID); err != nil {
		t.Fatalf("set parent session id: %v", err)
	}
	if _, _, err := store.AppendEvent(projectionStepID, "message", llm.Message{Role: llm.RoleAssistant, Content: "final answer", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	if changed, err := eng.SetFastModeEnabled(true); err != nil {
		t.Fatalf("enable fast mode: %v", err)
	} else if !changed {
		t.Fatal("expected fast mode enable to report changed=true")
	}
	if changed, enabled := eng.SetAutoCompactionEnabled(false); !changed || enabled {
		t.Fatalf("expected auto-compaction disabled, changed=%v enabled=%v", changed, enabled)
	}

	view := mainViewFromRuntimeForTest(t, eng)
	if view.Session.SessionID != store.Meta().SessionID || view.Session.SessionName != "Session Name" {
		t.Fatalf("unexpected session hydration: %+v", view.Session)
	}
	if view.Status.ParentSessionID != projectionParentID || view.Status.LastCommittedAssistantFinalAnswer != "final answer" {
		t.Fatalf("unexpected status hydration: %+v", view.Status)
	}
	if view.Status.ThinkingLevel != "high" || !view.Status.FastModeEnabled || view.Status.AutoCompactionEnabled {
		t.Fatalf("unexpected runtime flags: %+v", view.Status)
	}
	if view.Status.ContextUsage.WindowTokens != 400_000 {
		t.Fatalf("context window tokens = %d, want 400000", view.Status.ContextUsage.WindowTokens)
	}
	if view.Activity.ActiveForControl() {
		t.Fatalf("expected idle activity in idle main view, got %+v", view.Activity)
	}
}

func mainViewFromRuntimeForTest(t *testing.T, eng *runtime.Engine) clientui.RuntimeMainView {
	t.Helper()
	version := clientui.ReadModelVersion{Epoch: "runtimeview-test", Generation: 1, Sequence: 1}
	activity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	return MainViewFromRuntimeActivity(eng, version, activity)
}

func TestMainViewFromWorkflowRuntimeIncludesWorkflowStatus(t *testing.T) {
	store := newRuntimeViewStore(t)
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{
		Model: "gpt-5",
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID(projectionWorkflowRun)},
			Instructions: workflowruntime.TaskInstructions{
				TaskID:     projectionWorkflowTask,
				WorkflowID: projectionWorkflowID,
			},
		},
	})
	view := mainViewFromRuntimeForTest(t, eng)
	if !view.Status.WorkflowActive || view.Status.WorkflowSession == nil {
		t.Fatalf("workflow status = %+v, want active workflow session", view.Status)
	}
	if view.Status.WorkflowSession.RunID != projectionWorkflowRun || view.Status.WorkflowSession.TaskID != projectionWorkflowTask || view.Status.WorkflowSession.WorkflowID != projectionWorkflowID {
		t.Fatalf("workflow session = %+v, want run/task/workflow ids", view.Status.WorkflowSession)
	}
}

func TestMainViewFromReopenedWorkflowSessionIncludesDurableWorkflowStatus(t *testing.T) {
	store := newRuntimeViewStore(t)
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: projectionWorkflowRun, TaskID: projectionWorkflowTask, WorkflowID: projectionWorkflowID}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{Model: "gpt-5"})
	view := mainViewFromRuntimeForTest(t, eng)
	if view.Status.WorkflowActive {
		t.Fatalf("workflow active = true, want false for reopened non-workflow runtime")
	}
	if view.Status.WorkflowSession == nil {
		t.Fatalf("workflow session = nil, status=%+v", view.Status)
	}
	if view.Status.WorkflowSession.RunID != projectionWorkflowRun || view.Status.WorkflowSession.TaskID != projectionWorkflowTask || view.Status.WorkflowSession.WorkflowID != projectionWorkflowID {
		t.Fatalf("workflow session = %+v, want run/task/workflow ids", view.Status.WorkflowSession)
	}
}

func TestStatusFromRuntimeUsesFreshPreciseCurrentTokens(t *testing.T) {
	eng := newRuntimeViewEngine(t, newRuntimeViewStore(t), projectionPreciseClient{inputTokens: 180}, runtime.Config{
		Model:                         "gpt-5",
		ContextWindowTokens:           400_000,
		AutoCompactTokenLimit:         1_000,
		PreSubmitCompactionLeadTokens: 100,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "prompt"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if _, err := eng.ShouldCompactBeforeUserMessage(context.Background(), "follow-up"); err != nil {
		t.Fatalf("warm exact count: %v", err)
	}
	view := StatusFromRuntime(eng)
	if view.ContextUsage.UsedTokens != 180 {
		t.Fatalf("projected used tokens=%d, want exact 180", view.ContextUsage.UsedTokens)
	}
}

func TestEventFromRuntimeCopiesContextUsage(t *testing.T) {
	projected := EventFromRuntime(runtime.Event{
		Kind: runtime.EventModelResponse,
		ContextUsage: &runtime.ContextUsage{
			UsedTokens:            420,
			WindowTokens:          1_000,
			CacheHitPercent:       25,
			HasCacheHitPercentage: true,
		},
	})
	if projected.ContextUsage == nil {
		t.Fatal("expected projected event to carry context usage")
	}
	if projected.ContextUsage.UsedTokens != 420 || projected.ContextUsage.WindowTokens != 1_000 {
		t.Fatalf("projected context usage = %+v", projected.ContextUsage)
	}
	if projected.ContextUsage.CacheHitPercent != 25 || !projected.ContextUsage.HasCacheHitPercentage {
		t.Fatalf("projected cache hit usage = %+v", projected.ContextUsage)
	}
}

func TestEventFromRuntimeCopiesCacheWarning(t *testing.T) {
	source := &transcript.CacheWarning{
		Scope:           transcript.CacheWarningScopeReviewer,
		Reason:          transcript.CacheWarningReasonNonPostfix,
		CacheKey:        "reviewer-cache-key",
		LostInputTokens: 12_000,
	}
	event := EventFromRuntime(runtime.Event{
		Kind:                   runtime.EventCacheWarning,
		CacheWarningVisibility: transcript.EntryVisibilityOngoing,
		CacheWarning:           source,
	})
	if event.CacheWarning == nil {
		t.Fatal("expected projected cache warning")
	}
	if event.CacheWarning.LostInputTokens != 12_000 {
		t.Fatalf("cache warning lost input tokens = %d, want 12000", event.CacheWarning.LostInputTokens)
	}
	if event.CacheWarning.Scope != transcript.CacheWarningScopeReviewer {
		t.Fatalf("cache warning scope = %q, want %q", event.CacheWarning.Scope, transcript.CacheWarningScopeReviewer)
	}
	if event.CacheWarningVisibility != clientui.EntryVisibilityOngoing {
		t.Fatalf("cache warning visibility = %q, want %q", event.CacheWarningVisibility, clientui.EntryVisibilityOngoing)
	}
	source.LostInputTokens = 99
	if event.CacheWarning.LostInputTokens != 12_000 {
		t.Fatalf("cache warning projection aliased source: %+v", event.CacheWarning)
	}
}

func TestEventFromRuntimeProjectsDefaultCacheWarningAsDetail(t *testing.T) {
	event := EventFromRuntime(runtime.Event{
		Kind:                   runtime.EventCacheWarning,
		CacheWarningVisibility: transcript.EntryVisibilityDetail,
		CacheWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonNonPostfix,
		},
	})
	if event.CacheWarningVisibility != clientui.EntryVisibilityDetail {
		t.Fatalf("cache warning visibility = %q, want %q", event.CacheWarningVisibility, clientui.EntryVisibilityDetail)
	}
	if event.CacheWarning == nil || event.CacheWarning.Scope != transcript.CacheWarningScopeConversation {
		t.Fatalf("unexpected projected cache warning: %+v", event.CacheWarning)
	}
}
