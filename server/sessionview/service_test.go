package sessionview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type serviceFakeLLM struct {
	responses []llm.Response
}

func (f *serviceFakeLLM) Generate(context.Context, llm.Request) (llm.Response, error) {
	if len(f.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *serviceFakeLLM) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}

type serviceBlockingTool struct {
	started chan struct{}
	release chan struct{}
}

type staticExecutionTargetResolver struct {
	target clientui.SessionExecutionTarget
}

func (r staticExecutionTargetResolver) ResolveSessionExecutionTarget(context.Context, string) (clientui.SessionExecutionTarget, error) {
	return r.target, nil
}

type failingSessionStoreResolver struct {
	err error
}

func (r failingSessionStoreResolver) ResolveSessionStore(context.Context, string) (*session.Store, error) {
	return nil, r.err
}

func (t serviceBlockingTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	<-t.release
	out, _ := json.Marshal(map[string]any{"ok": true})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

func TestServiceGetSessionMainViewUsesLiveRuntimeWhenAttached(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	started := make(chan struct{})
	release := make(chan struct{})
	client := &serviceFakeLLM{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng, err := runtime.New(store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: serviceBlockingTool{started: started, release: release}}), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), newTestRuntimeResolver(eng), nil)

	done := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "run tools")
		done <- submitErr
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active run")
	}

	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Activity.State != clientui.RuntimeActivityRunning {
		t.Fatalf("expected live running activity, got %+v", resp.MainView.Activity)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
}

func TestServiceGetSessionMainViewDoesNotRequireStoreResolutionForLiveRuntime(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	engine, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	response, err := NewService(failingSessionStoreResolver{err: errors.New("store unavailable")}, newTestRuntimeResolver(engine), nil).
		GetSessionMainView(t.Context(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get live main view: %v", err)
	}
	if response.MainView.Activity.State != clientui.RuntimeActivityRegisteredIdle {
		t.Fatalf("live activity = %+v, want registered idle", response.MainView.Activity)
	}
}

func TestServiceGetSessionMainViewFallsBackToDurableSessionState(t *testing.T) {
	dir := t.TempDir()
	store, parentSessionID := newSessionViewParentAgentChild(t, dir, "ws", dir)
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, err := store.SetGoal("ship dormant goal", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "final answer", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), nil, nil)
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Session.SessionID != store.Meta().SessionID || resp.MainView.Session.SessionName != "incident triage" {
		t.Fatalf("unexpected dormant session view: %+v", resp.MainView.Session)
	}
	if resp.MainView.Status.ParentAgentSessionID == nil || resp.MainView.Status.ParentAgentSessionID.String() != parentSessionID ||
		resp.MainView.Status.NavigationTargetSessionID == nil || resp.MainView.Status.NavigationTargetSessionID.String() != parentSessionID ||
		resp.MainView.Status.LastCommittedAssistantFinalAnswer != "final answer" {
		t.Fatalf("unexpected dormant status: %+v", resp.MainView.Status)
	}
	if resp.MainView.Status.Goal == nil || resp.MainView.Status.Goal.Status != clientui.RuntimeGoalStatusActive || resp.MainView.Status.Goal.Objective != "ship dormant goal" {
		t.Fatalf("unexpected dormant goal status: %+v", resp.MainView.Status.Goal)
	}
	if resp.MainView.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("dormant activity = %+v, want unavailable", resp.MainView.Activity)
	}
}

func TestServiceGetSessionMainViewFallsBackToDurableWorkflowSessionState(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), nil, nil)
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	if resp.MainView.Status.WorkflowSession == nil {
		t.Fatalf("workflow session = nil, status=%+v", resp.MainView.Status)
	}
	if resp.MainView.Status.WorkflowActive {
		t.Fatalf("workflow active = true, want false for reopened non-workflow runtime")
	}
	if resp.MainView.Status.WorkflowSession.RunID != "run-1" || resp.MainView.Status.WorkflowSession.TaskID != "task-1" || resp.MainView.Status.WorkflowSession.WorkflowID != "workflow-1" {
		t.Fatalf("workflow session = %+v, want run/task/workflow ids", resp.MainView.Status.WorkflowSession)
	}
}

func TestServiceGetSessionMainViewMergesDurableWorkflowSessionStateIntoLiveRuntime(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	eng, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), newTestRuntimeResolver(eng), nil)
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	if resp.MainView.Status.WorkflowSession == nil {
		t.Fatalf("workflow session = nil, status=%+v", resp.MainView.Status)
	}
	if resp.MainView.Status.WorkflowSession.RunID != "run-1" || resp.MainView.Status.WorkflowSession.TaskID != "task-1" || resp.MainView.Status.WorkflowSession.WorkflowID != "workflow-1" {
		t.Fatalf("workflow session = %+v, want run/task/workflow ids", resp.MainView.Status.WorkflowSession)
	}
}

func TestServiceGetSessionMainViewIncludesExecutionTarget(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	target := clientui.SessionExecutionTarget{
		WorkspaceID:      "workspace-1",
		WorkspaceRoot:    dir,
		CwdRelpath:       ".",
		EffectiveWorkdir: dir,
	}
	svc := NewService(newTestSessionResolver(store), nil, staticExecutionTargetResolver{target: target})

	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Session.ExecutionTarget.WorkspaceID != "workspace-1" {
		t.Fatalf("workspace id = %q, want workspace-1", resp.MainView.Session.ExecutionTarget.WorkspaceID)
	}
	if resp.MainView.Session.ExecutionTarget.EffectiveWorkdir != dir {
		t.Fatalf("effective workdir = %q, want %q", resp.MainView.Session.ExecutionTarget.EffectiveWorkdir, dir)
	}
}

func TestServiceGetSessionMainViewReconcilesPendingOperationRefs(t *testing.T) {
	store := newSessionViewStore(t, t.TempDir(), "ws", t.TempDir())
	ref := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindSubmit,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}
	operations := runtimeops.NewCoordinator()
	operations.RecordCommitted(store.Meta().SessionID, ref)
	response, err := NewService(newTestSessionResolver(store), nil, nil).
		WithOperationCoordinator(operations).
		GetSessionMainView(t.Context(), serverapi.SessionMainViewRequest{
			SessionID:            store.Meta().SessionID,
			PendingOperationRefs: []clientui.RuntimeOperationRef{ref},
		})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	got := response.MainView.InputReconciliation.Operations
	if len(got) != 1 || got[0].Operation != ref || got[0].State != clientui.RuntimeInputReconciliationCommitted {
		t.Fatalf("input reconciliation = %+v, want committed ref %+v", got, ref)
	}
}

func TestServiceRequiresSessionStoreResolverForDormantReads(t *testing.T) {
	svc := NewService(nil, nil, nil)

	if _, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: "session-1"}); err == nil || !errors.Is(err, errSessionStoreResolverRequired) {
		t.Fatalf("expected explicit session store resolver error for main view, got %v", err)
	}
	if _, err := svc.SessionTranscriptTailEntries(context.Background(), "session-1"); err == nil || !errors.Is(err, errSessionStoreResolverRequired) {
		t.Fatalf("expected explicit session store resolver error for transcript tail entries, got %v", err)
	}
}

func TestServiceWithCacheWarningModeChangesSubsequentDormantReads(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if _, _, err := store.AppendEvent("step-1", "cache_warning", transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix}); err != nil {
		t.Fatalf("append cache warning: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), nil, nil)

	first, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get dormant transcript tail entries default: %v", err)
	}
	if got := first[0].Visibility; got != clientui.EntryVisibilityDetail {
		t.Fatalf("default cache warning visibility = %q, want %q", got, clientui.EntryVisibilityDetail)
	}

	svc.WithCacheWarningMode(config.CacheWarningModeVerbose)
	second, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get dormant transcript tail entries verbose: %v", err)
	}
	if got := second[0].Visibility; got != clientui.EntryVisibilityOngoing {
		t.Fatalf("verbose cache warning visibility = %q, want %q", got, clientui.EntryVisibilityOngoing)
	}
}

func TestServiceSessionTranscriptTailEntriesObservesRevisionAdvance(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if _, _, err := store.AppendEvent("11111111-1111-4111-8111-111111111111", "message", llm.Message{Role: llm.RoleAssistant, Content: "line 0", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append first message: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), nil, nil)

	first, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get first transcript tail entries: %v", err)
	}
	if got := len(first); got != 1 {
		t.Fatalf("first entry count = %d, want 1", got)
	}

	if _, _, err := store.AppendEvent("22222222-2222-4222-8222-222222222222", "message", llm.Message{Role: llm.RoleAssistant, Content: "line 1", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append second message: %v", err)
	}
	second, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get second transcript tail entries: %v", err)
	}
	if got := len(second); got != 2 {
		t.Fatalf("updated entry count = %d, want 2", got)
	}
}

func TestServiceDormantReviewerRollbackIsIgnoredOnRead(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "rolled back final", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "history_replaced", map[string]any{
		"engine": "reviewer_rollback",
		"items":  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "u1"}}),
	}); err != nil {
		t.Fatalf("append reviewer rollback: %v", err)
	}

	svc := NewService(newTestSessionResolver(store), nil, nil)

	entries, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get session transcript tail entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(entries))
	}
	if got := entries[0].Text; got != "u1" {
		t.Fatalf("first visible transcript entry = %+v, want u1", entries)
	}
	if got := entries[1].Text; got != "rolled back final" {
		t.Fatalf("second visible transcript entry = %+v, want rolled back final", entries)
	}
	if got := entries[2].Text; got != "u2" {
		t.Fatalf("third visible transcript entry = %+v, want u2", entries)
	}

	mainViewResp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if got := mainViewResp.MainView.Status.LastCommittedAssistantFinalAnswer; got != "" {
		t.Fatalf("last committed assistant final answer = %q, want empty because later user message supersedes it", got)
	}
}

func TestServiceSessionTranscriptTailEntriesKeepsDormantCompactionSummaryAndCarryover(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "before compaction"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "history_replaced", map[string]any{
		"engine": "local",
		"mode":   "manual",
		"items":  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "condensed provider summary", MessageType: llm.MessageTypeCompactionSummary}}),
	}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "local_entry", map[string]any{"role": "compaction_summary", "text": "condensed summary"}); err != nil {
		t.Fatalf("append compaction summary entry: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeManualCompactionCarryover, Content: "Last user message before handoff\n\ncarry this forward"}); err != nil {
		t.Fatalf("append manual carryover: %v", err)
	}
	svc := NewService(newTestSessionResolver(store), nil, nil)

	entries, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get session transcript tail entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (%+v)", len(entries), entries)
	}
	if entries[0].Role != "compaction_summary" || entries[0].Text != "condensed provider summary" {
		t.Fatalf("expected projected provider compaction summary entry, got %+v", entries[0])
	}
	if entries[1].Role != "compaction_summary" || entries[1].Text != "condensed summary" {
		t.Fatalf("expected persisted compaction summary entry, got %+v", entries[1])
	}
	if entries[2].Role != "manual_compaction_carryover" {
		t.Fatalf("expected manual carryover entry, got %+v", entries[2])
	}
}

func TestServiceDormantReadsDoNotMutatePersistedEvents(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	const stepID = "11111111-1111-4111-8111-111111111111"
	if _, _, err := store.AppendEvent(stepID, "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{RecoveryID: "recovery-1", StepID: stepID, Reason: "test", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	beforeSequence := store.Meta().LastSequence
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file before: %v", err)
	}

	svc := NewService(newTestSessionResolver(store), nil, nil)
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("dormant activity = %+v, want unavailable", resp.MainView.Activity)
	}
	if _, err := svc.GetSessionTranscriptPage(t.Context(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID}); err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if _, err := svc.SessionTranscriptTailEntries(t.Context(), store.Meta().SessionID); err != nil {
		t.Fatalf("get session transcript tail entries: %v", err)
	}

	afterEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file after: %v", err)
	}
	if string(beforeEvents) != string(afterEvents) {
		t.Fatalf("events file mutated during read\nbefore=%s\nafter=%s", string(beforeEvents), string(afterEvents))
	}
	if got := store.Meta().LastSequence; got != beforeSequence {
		t.Fatalf("session sequence mutated during read: got %d want %d", got, beforeSequence)
	}
}
