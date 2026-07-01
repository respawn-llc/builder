package runtimeview

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

type projectionFastClient struct{}

func (projectionFastClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (projectionFastClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
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
	store, err := session.Create(dir, "ws", dir)
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

func appendRuntimeViewMessages(t *testing.T, store *session.Store, count int, text func(int) string) {
	t.Helper()
	for i := range count {
		if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: text(i), Phase: llm.MessagePhaseFinal}); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
}

func TestEventFromRuntimeProjectsReasoningAndBackground(t *testing.T) {
	exitCode := 17
	view := EventFromRuntime(runtime.Event{
		Kind:                       runtime.EventBackgroundUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		AssistantDelta:             "delta",
		AssistantDeltaPhase:        llm.MessagePhaseFinal,
		ReasoningDelta:             &llm.ReasoningSummaryDelta{Key: "k", Role: "reasoning", Text: "thinking"},
		RunState:                   &runtime.RunState{Lifecycle: runtime.RunningRunLifecycle(runtime.RunModeTurn), RunID: "run-1", Status: runtime.RunStatusRunning},
		Background: &runtime.BackgroundShellEvent{
			Type:              "completed",
			ID:                "123",
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
	if view.Kind != "background_updated" || view.StepID != "step-1" || view.AssistantDelta != "delta" {
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
	if view.RunState.RunID != "run-1" || view.RunState.Status != "running" {
		t.Fatalf("expected run identity in projected run state, got %+v", view.RunState)
	}
	if view.RunState.Lifecycle.Phase != clientui.RunLifecycleRunning || view.RunState.Lifecycle.Mode != clientui.RunModeTurn {
		t.Fatalf("server/client run lifecycle projection mismatch: %+v", view.RunState.Lifecycle)
	}
	if view.Background == nil || view.Background.ID != "123" {
		t.Fatalf("expected background projection, got %+v", view.Background)
	}
	if view.Background.ExitCode == nil || *view.Background.ExitCode != 17 {
		t.Fatalf("expected copied exit code, got %+v", view.Background.ExitCode)
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
					ID:        " goal-1 ",
					Objective: "ship feature",
					Status:    session.GoalStatusActive,
				}},
			},
			want: &clientui.RuntimeGoalStatusUpdate{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
		},
		{
			name: "paused",
			evt: runtime.Event{
				Kind: runtime.EventGoalStatusUpdated,
				GoalStatus: &runtime.GoalStatusUpdate{State: session.GoalState{
					ID:        "goal-1",
					Objective: "ship feature",
					Status:    session.GoalStatusPaused,
				}},
			},
			want: &clientui.RuntimeGoalStatusUpdate{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
		},
		{
			name: "complete",
			evt: runtime.Event{
				Kind: runtime.EventGoalStatusUpdated,
				GoalStatus: &runtime.GoalStatusUpdate{State: session.GoalState{
					ID:        "goal-1",
					Objective: "ship feature",
					Status:    session.GoalStatusComplete,
				}},
			},
			want: &clientui.RuntimeGoalStatusUpdate{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete},
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
	engine := newRuntimeViewEngine(t, newRuntimeViewStore(t), projectionFastClient{})
	if _, err := engine.SetGoal("ship feature", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	status := StatusFromRuntime(engine)
	if status.Goal == nil || !status.Goal.Suspended {
		t.Fatalf("goal status = %+v, want suspended goal", status.Goal)
	}
}

func TestEventFromRuntimeProjectsLocalEntry(t *testing.T) {
	view := EventFromRuntime(runtime.Event{
		Kind:   runtime.EventLocalEntryAdded,
		StepID: "step-1",
		LocalEntry: &runtime.ChatEntry{
			Visibility:    transcript.EntryVisibilityAll,
			Role:          "reviewer_suggestions",
			Text:          "Supervisor suggested:\n1. Add verification notes.",
			CondensedText: "Supervisor made 1 suggestion.",
		},
	})

	if view.Kind != clientui.EventLocalEntryAdded || view.StepID != "step-1" {
		t.Fatalf("unexpected projected local entry event: %+v", view)
	}
	if len(view.TranscriptEntries) != 1 {
		t.Fatalf("expected one projected local entry, got %+v", view.TranscriptEntries)
	}
	entry := view.TranscriptEntries[0]
	if entry.Role != "reviewer_suggestions" || entry.Text != "Supervisor suggested:\n1. Add verification notes." || entry.CondensedText != "Supervisor made 1 suggestion." {
		t.Fatalf("unexpected projected local entry transcript: %+v", entry)
	}
	if entry.Visibility != clientui.EntryVisibilityAll {
		t.Fatalf("local entry visibility = %q, want all", entry.Visibility)
	}
}

func TestEventFromRuntimeLeavesCompactionStatusWithoutTranscriptEntriesUntilPersistedLocalEntry(t *testing.T) {
	testCases := []struct {
		name string
		evt  runtime.Event
	}{
		{
			name: "compaction completed",
			evt: runtime.Event{
				Kind:   runtime.EventCompactionCompleted,
				StepID: "step-1",
				Compaction: &runtime.CompactionStatus{
					Mode:  "auto",
					Count: 1,
				},
			},
		},
		{
			name: "compaction failed",
			evt: runtime.Event{
				Kind:   runtime.EventCompactionFailed,
				StepID: "step-1",
				Compaction: &runtime.CompactionStatus{
					Mode:  "manual",
					Error: "quota exceeded",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			view := EventFromRuntime(tc.evt)
			if tc.evt.Compaction == nil || view.Compaction == nil {
				t.Fatalf("expected compaction status projection, got %+v", view.Compaction)
			}
			if view.Compaction.Mode != tc.evt.Compaction.Mode || view.Compaction.Count != tc.evt.Compaction.Count || view.Compaction.Error != tc.evt.Compaction.Error {
				t.Fatalf("projected compaction = %+v, want %+v", view.Compaction, tc.evt.Compaction)
			}
			if len(view.TranscriptEntries) != 0 {
				t.Fatalf("expected no projected transcript entries before persisted local entry, got %+v", view.TranscriptEntries)
			}
		})
	}

	local := EventFromRuntime(runtime.Event{
		Kind:                       runtime.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		LocalEntry:                 &runtime.ChatEntry{Role: "compaction_notice", Text: "context compacted for the 1st time"},
	})
	if len(local.TranscriptEntries) != 1 {
		t.Fatalf("expected persisted local entry to remain the transcript source, got %+v", local.TranscriptEntries)
	}
	if got := local.TranscriptEntries[0].Role; got != "compaction_notice" {
		t.Fatalf("projected local entry role = %q, want compaction_notice", got)
	}
}

func TestRunViewFromRuntimeCopiesSnapshot(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Minute)
	finishedAt := time.Now().UTC()
	view := RunViewFromRuntime("session-1", &runtime.RunSnapshot{
		RunID:      "run-1",
		StepID:     "step-1",
		Status:     runtime.RunStatusCompleted,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	})
	if view == nil {
		t.Fatal("expected run view")
	}
	if view.RunID != "run-1" || view.SessionID != "session-1" || view.StepID != "step-1" {
		t.Fatalf("unexpected run view ids: %+v", view)
	}
	if view.Status != "completed" || !view.StartedAt.Equal(startedAt) || !view.FinishedAt.Equal(finishedAt) {
		t.Fatalf("unexpected run view timing/status: %+v", view)
	}
}

func TestMainViewFromRuntimeBundlesStatusAndSession(t *testing.T) {
	store := newRuntimeViewStore(t)
	if err := store.SetName("Session Name"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := store.SetParentSessionID("parent-123"); err != nil {
		t.Fatalf("set parent session id: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "final answer", Phase: llm.MessagePhaseFinal}); err != nil {
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

	view := MainViewFromRuntime(eng)
	if view.Session.SessionID != store.Meta().SessionID || view.Session.SessionName != "Session Name" {
		t.Fatalf("unexpected session hydration: %+v", view.Session)
	}
	if got := len(view.Session.Chat.Entries); got != 0 {
		t.Fatalf("expected main view to omit transcript payload, got %d entries", got)
	}
	if view.Status.ParentSessionID != "parent-123" || view.Status.LastCommittedAssistantFinalAnswer != "final answer" {
		t.Fatalf("unexpected status hydration: %+v", view.Status)
	}
	if view.Status.ThinkingLevel != "high" || !view.Status.FastModeEnabled || view.Status.AutoCompactionEnabled {
		t.Fatalf("unexpected runtime flags: %+v", view.Status)
	}
	if view.Status.ContextUsage.WindowTokens != 400_000 {
		t.Fatalf("context window tokens = %d, want 400000", view.Status.ContextUsage.WindowTokens)
	}
	if view.ActiveRun != nil {
		t.Fatalf("expected no active run in idle main view, got %+v", view.ActiveRun)
	}
}

func TestMainViewFromWorkflowRuntimeIncludesWorkflowStatus(t *testing.T) {
	store := newRuntimeViewStore(t)
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{
		Model: "gpt-5",
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
			Instructions: workflowruntime.TaskInstructions{
				TaskID:     "task-1",
				WorkflowID: "workflow-1",
			},
		},
	})
	view := MainViewFromRuntime(eng)
	if !view.Status.WorkflowActive || view.Status.WorkflowSession == nil {
		t.Fatalf("workflow status = %+v, want active workflow session", view.Status)
	}
	if view.Status.WorkflowSession.RunID != "run-1" || view.Status.WorkflowSession.TaskID != "task-1" || view.Status.WorkflowSession.WorkflowID != "workflow-1" {
		t.Fatalf("workflow session = %+v, want run/task/workflow ids", view.Status.WorkflowSession)
	}
}

func TestMainViewFromReopenedWorkflowSessionIncludesDurableWorkflowStatus(t *testing.T) {
	store := newRuntimeViewStore(t)
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{Model: "gpt-5"})
	view := MainViewFromRuntime(eng)
	if view.Status.WorkflowActive {
		t.Fatalf("workflow active = true, want false for reopened non-workflow runtime")
	}
	if view.Status.WorkflowSession == nil {
		t.Fatalf("workflow session = nil, status=%+v", view.Status)
	}
	if view.Status.WorkflowSession.RunID != "run-1" || view.Status.WorkflowSession.TaskID != "task-1" || view.Status.WorkflowSession.WorkflowID != "workflow-1" {
		t.Fatalf("workflow session = %+v, want run/task/workflow ids", view.Status.WorkflowSession)
	}
}

func TestSessionViewFromRuntimeOmitsTranscriptPayload(t *testing.T) {
	store := newRuntimeViewStore(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "local_entry", map[string]any{"role": "system", "text": "local note", "condensed_text": ""}); err != nil {
		t.Fatalf("append local entry: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: "warn"}); err != nil {
		t.Fatalf("append warning message: %v", err)
	}
	eng := newRuntimeViewEngine(t, store, projectionFastClient{})
	view := SessionViewFromRuntime(eng)
	if view.Transcript.Revision != eng.TranscriptRevision() {
		t.Fatalf("projected revision = %d, engine revision = %d", view.Transcript.Revision, eng.TranscriptRevision())
	}
	if got := len(view.Chat.Entries); got != 0 {
		t.Fatalf("session view chat entry count = %d, want 0", got)
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

func TestEventFromRuntimeCopiesCacheWarningLostInputTokens(t *testing.T) {
	event := EventFromRuntime(runtime.Event{
		Kind:                   runtime.EventCacheWarning,
		CacheWarningVisibility: transcript.EntryVisibilityAll,
		CacheWarning: &transcript.CacheWarning{
			Scope:           transcript.CacheWarningScopeReviewer,
			Reason:          transcript.CacheWarningReasonNonPostfix,
			CacheKey:        "reviewer-cache-key",
			LostInputTokens: 12_000,
		},
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
	if event.CacheWarningVisibility != clientui.EntryVisibilityAll {
		t.Fatalf("cache warning visibility = %q, want %q", event.CacheWarningVisibility, clientui.EntryVisibilityAll)
	}
	if len(event.TranscriptEntries) != 1 {
		t.Fatalf("expected one projected transcript entry, got %d", len(event.TranscriptEntries))
	}
	if entry := event.TranscriptEntries[0]; entry.Role != "cache_warning" || entry.Visibility != clientui.EntryVisibilityAll {
		t.Fatalf("unexpected projected cache warning entry: %+v", entry)
	}
}

func TestEventFromRuntimeProjectsDefaultCacheWarningAsVerbose(t *testing.T) {
	event := EventFromRuntime(runtime.Event{
		Kind:                   runtime.EventCacheWarning,
		CacheWarningVisibility: transcript.EntryVisibilityVerbose,
		CacheWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonNonPostfix,
		},
	})
	if event.CacheWarningVisibility != clientui.EntryVisibilityVerbose {
		t.Fatalf("cache warning visibility = %q, want %q", event.CacheWarningVisibility, clientui.EntryVisibilityVerbose)
	}
	if len(event.TranscriptEntries) != 1 {
		t.Fatalf("expected one projected transcript entry, got %d", len(event.TranscriptEntries))
	}
	if entry := event.TranscriptEntries[0]; entry.Role != "cache_warning" || entry.Visibility != clientui.EntryVisibilityVerbose {
		t.Fatalf("unexpected projected cache warning entry: %+v", entry)
	}
}

func TestChatSnapshotFromRuntimeCopiesEntries(t *testing.T) {
	toolCall := &transcript.ToolCallMeta{
		ToolName:    "shell",
		Suggestions: []string{"a", "b"},
	}
	snapshot := ChatSnapshotFromRuntime(runtime.ChatSnapshot{
		Entries: []runtime.ChatEntry{{
			Visibility:        transcript.EntryVisibilityVerbose,
			Role:              "assistant",
			Text:              "hello",
			CondensedText:     "hel",
			Phase:             llm.MessagePhaseFinal,
			MessageType:       llm.MessageTypeEnvironment,
			SourcePath:        "/tmp/source",
			CompactLabel:      "compact",
			ToolResultSummary: "summary",
			ToolCallID:        "call-1",
			ToolCall:          toolCall,
		}},
		Streaming:      "ongoing",
		StreamingError: "warn",
	})
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(snapshot.Entries))
	}
	entry := snapshot.Entries[0]
	if entry.Phase != string(llm.MessagePhaseFinal) || entry.ToolCall == nil || entry.ToolCall.ToolName != "shell" {
		t.Fatalf("unexpected projected entry: %+v", entry)
	}
	if entry.Visibility != clientui.EntryVisibilityVerbose {
		t.Fatalf("entry visibility = %q, want %q", entry.Visibility, clientui.EntryVisibilityVerbose)
	}
	if entry.MessageType != string(llm.MessageTypeEnvironment) || entry.SourcePath != "/tmp/source" || entry.CompactLabel != "compact" || entry.ToolResultSummary != "summary" {
		t.Fatalf("metadata was not projected: %+v", entry)
	}
	if len(entry.ToolCall.Suggestions) != 2 {
		t.Fatalf("expected copied suggestions, got %+v", entry.ToolCall.Suggestions)
	}
	toolCall.Suggestions[0] = "changed"
	if snapshot.Entries[0].ToolCall.Suggestions[0] != "a" {
		t.Fatalf("expected projection to copy suggestions, got %+v", snapshot.Entries[0].ToolCall.Suggestions)
	}
	if snapshot.Streaming != "ongoing" || snapshot.StreamingError != "warn" {
		t.Fatalf("unexpected snapshot projection: %+v", snapshot)
	}
}

func TestChatSnapshotFromRuntimeSuppressesNoopFinalAssistantState(t *testing.T) {
	snapshot := ChatSnapshotFromRuntime(runtime.ChatSnapshot{
		Entries: []runtime.ChatEntry{{
			Role:  "assistant",
			Text:  "NO_OP",
			Phase: llm.MessagePhaseFinal,
		}},
		Streaming:      "NO_OP",
		StreamingError: "warn",
	})
	if got := len(snapshot.Entries); got != 0 {
		t.Fatalf("noop final entry count = %d, want 0", got)
	}
	if got := snapshot.Streaming; got != "" {
		t.Fatalf("noop ongoing text = %q, want empty", got)
	}
	if got := snapshot.StreamingError; got != "warn" {
		t.Fatalf("ongoing error = %q, want warn", got)
	}
}

func TestTranscriptPageFromChatClonesPatchRender(t *testing.T) {
	snapshot := clientui.ChatSnapshot{Entries: []clientui.ChatEntry{{
		Role: "tool_call",
		ToolCall: &clientui.ToolCallMeta{
			PatchRender: &patchformat.RenderedPatch{
				SummaryLines: []patchformat.RenderedLine{{Text: "before"}},
			},
		},
	}}}

	entries := cloneChatEntries(snapshot.Entries)
	if len(entries) != 1 || entries[0].ToolCall == nil || entries[0].ToolCall.PatchRender == nil {
		t.Fatalf("expected patch render copied into cloned entries, got %+v", entries)
	}
	snapshot.Entries[0].ToolCall.PatchRender.SummaryLines[0].Text = "after"
	if entries[0].ToolCall.PatchRender.SummaryLines[0].Text != "before" {
		t.Fatalf("expected cloned entries to deep copy patch render, got %+v", entries[0].ToolCall.PatchRender.SummaryLines)
	}
}

func TestTranscriptPageFromRuntimeReturnsNewestSegment(t *testing.T) {
	store := newRuntimeViewStore(t)
	appendRuntimeViewMessages(t, store, 600, func(i int) string { return fmt.Sprintf("reply-%03d", i) })
	eng := newRuntimeViewEngine(t, store, projectionFastClient{})

	page, err := TranscriptPageFromRuntime(eng, clientui.TranscriptPageRequest{})
	if err != nil {
		t.Fatalf("transcript page from runtime: %v", err)
	}
	if page.HasMoreAbove {
		t.Fatalf("never-compacted session must not report more above, got %+v", page)
	}
	if page.OlderCursor != 0 {
		t.Fatalf("never-compacted older cursor = %d, want 0", page.OlderCursor)
	}
	if len(page.Entries) != 600 {
		t.Fatalf("entries = %d, want 600 (whole segment)", len(page.Entries))
	}
	if page.Entries[0].Text != "reply-000" || page.Entries[599].Text != "reply-599" {
		t.Fatalf("unexpected segment bounds: %q..%q", page.Entries[0].Text, page.Entries[599].Text)
	}
}
