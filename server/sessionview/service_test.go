package sessionview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
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

type staticUpdateStatusProvider struct {
	status clientui.UpdateStatus
}

func (p staticUpdateStatusProvider) Status(context.Context) clientui.UpdateStatus {
	return p.status
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
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
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
	svc := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(eng), nil)

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
	if resp.MainView.ActiveRun == nil || resp.MainView.ActiveRun.Status != "running" {
		t.Fatalf("expected live active run, got %+v", resp.MainView.ActiveRun)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
}

func TestServiceGetSessionMainViewIncludesUpdateStatus(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil).WithUpdateStatusProvider(staticUpdateStatusProvider{
		status: clientui.UpdateStatus{Checked: true, Available: true, LatestVersion: "1.2.3"},
	})

	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Status.Update.LatestVersion != "1.2.3" || !resp.MainView.Status.Update.Available {
		t.Fatalf("unexpected update status: %+v", resp.MainView.Status.Update)
	}
}

func TestServiceGetSessionMainViewFallsBackToDurableSessionState(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := store.SetParentSessionID("parent-1"); err != nil {
		t.Fatalf("set parent session id: %v", err)
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
	startedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := store.AppendRunStarted(session.RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: startedAt}); err != nil {
		t.Fatalf("append run start: %v", err)
	}

	svc := NewService(NewStaticSessionResolver(store), nil, nil)
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.Session.SessionID != store.Meta().SessionID || resp.MainView.Session.SessionName != "incident triage" {
		t.Fatalf("unexpected dormant session view: %+v", resp.MainView.Session)
	}
	if len(resp.MainView.Session.Chat.Entries) != 0 {
		t.Fatalf("expected main view to omit transcript payload, got %+v", resp.MainView.Session.Chat)
	}
	if resp.MainView.Status.ParentSessionID != "parent-1" || resp.MainView.Status.LastCommittedAssistantFinalAnswer != "final answer" {
		t.Fatalf("unexpected dormant status: %+v", resp.MainView.Status)
	}
	if resp.MainView.Status.Goal == nil || resp.MainView.Status.Goal.Status != clientui.RuntimeGoalStatusActive || resp.MainView.Status.Goal.Objective != "ship dormant goal" {
		t.Fatalf("unexpected dormant goal status: %+v", resp.MainView.Status.Goal)
	}
	if resp.MainView.ActiveRun == nil || resp.MainView.ActiveRun.RunID != "run-1" || resp.MainView.ActiveRun.Status != "running" {
		t.Fatalf("expected durable running active run, got %+v", resp.MainView.ActiveRun)
	}
	if resp.MainView.Session.Transcript.Revision != store.Meta().LastSequence {
		t.Fatalf("transcript revision = %d, want %d", resp.MainView.Session.Transcript.Revision, store.Meta().LastSequence)
	}
}

func TestServiceGetSessionMainViewFallsBackToDurableWorkflowSessionState(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)
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
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	eng, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	svc := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(eng), nil)
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
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	target := clientui.SessionExecutionTarget{
		WorkspaceID:      "workspace-1",
		WorkspaceRoot:    dir,
		CwdRelpath:       ".",
		EffectiveWorkdir: dir,
	}
	svc := NewService(NewStaticSessionResolver(store), nil, staticExecutionTargetResolver{target: target})

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

func TestServiceRequiresSessionStoreResolverForDormantReads(t *testing.T) {
	svc := NewService(nil, nil, nil)

	if _, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: "session-1"}); err == nil || !errors.Is(err, errSessionStoreResolverRequired) {
		t.Fatalf("expected explicit session store resolver error for main view, got %v", err)
	}
	if _, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: "session-1"}); err == nil || !errors.Is(err, errSessionStoreResolverRequired) {
		t.Fatalf("expected explicit session store resolver error for transcript page, got %v", err)
	}
}

func TestServiceGetSessionTranscriptPageUsesLiveRuntimeWhenAttached(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "one", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	eng.AppendCommittedEntry("assistant", "two")
	svc := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(eng), nil)

	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if resp.Transcript.SessionName != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", resp.Transcript.SessionName)
	}
	if resp.Transcript.Revision != store.Meta().LastSequence {
		t.Fatalf("revision = %d, want %d", resp.Transcript.Revision, store.Meta().LastSequence)
	}
	if len(resp.Transcript.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(resp.Transcript.Entries))
	}
	if resp.Transcript.Entries[2].Text != "two" {
		t.Fatalf("unexpected tail entry: %+v", resp.Transcript.Entries[2])
	}
}

func TestServiceGetSessionTranscriptPageUsesConfiguredCacheWarningModeForDormantTail(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "cache_warning", transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix}); err != nil {
		t.Fatalf("append cache warning: %v", err)
	}

	tests := []struct {
		name string
		mode config.CacheWarningMode
		want clientui.EntryVisibility
	}{
		{name: "default", mode: config.CacheWarningModeDefault, want: clientui.EntryVisibilityVerbose},
		{name: "verbose", mode: config.CacheWarningModeVerbose, want: clientui.EntryVisibilityAll},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(NewStaticSessionResolver(store), nil, nil).WithCacheWarningMode(tt.mode)
			resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
			if err != nil {
				t.Fatalf("get dormant transcript page: %v", err)
			}
			if len(resp.Transcript.Entries) != 1 {
				t.Fatalf("entry count = %d, want 1", len(resp.Transcript.Entries))
			}
			if got := resp.Transcript.Entries[0].Visibility; got != tt.want {
				t.Fatalf("cache warning visibility = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceWithCacheWarningModeInvalidatesDormantCache(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "cache_warning", transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix}); err != nil {
		t.Fatalf("append cache warning: %v", err)
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	first, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get dormant transcript page default: %v", err)
	}
	if got := first.Transcript.Entries[0].Visibility; got != clientui.EntryVisibilityVerbose {
		t.Fatalf("default cache warning visibility = %q, want %q", got, clientui.EntryVisibilityVerbose)
	}

	secondSvc := svc.WithCacheWarningMode(config.CacheWarningModeVerbose)
	if secondSvc != svc {
		t.Fatal("expected WithCacheWarningMode to mutate service in place")
	}
	second, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get dormant transcript page verbose: %v", err)
	}
	if got := second.Transcript.Entries[0].Visibility; got != clientui.EntryVisibilityAll {
		t.Fatalf("verbose cache warning visibility = %q, want %q", got, clientui.EntryVisibilityAll)
	}
}

func TestServiceGetSessionTranscriptPageSupportsPagination(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	entries := []llm.Message{
		{Role: llm.RoleUser, Content: "u1"},
		{Role: llm.RoleAssistant, Content: "a1", Phase: llm.MessagePhaseFinal},
		{Role: llm.RoleUser, Content: "u2"},
		{Role: llm.RoleAssistant, Content: "a2", Phase: llm.MessagePhaseFinal},
	}
	for i, entry := range entries {
		if _, _, err := store.AppendEvent("step-1", "message", entry); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if resp.Transcript.HasMoreAbove {
		t.Fatalf("never-compacted session must not report more above: %+v", resp.Transcript)
	}
	if len(resp.Transcript.Entries) != 4 {
		t.Fatalf("entries = %d, want 4 (whole segment)", len(resp.Transcript.Entries))
	}
	if resp.Transcript.Entries[0].Text != "u1" || resp.Transcript.Entries[3].Text != "a2" {
		t.Fatalf("unexpected transcript page entries: %+v", resp.Transcript.Entries)
	}
}

func TestServiceGetSessionTranscriptPageDormantPageCacheInvalidatesOnRename(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := appendDormantTranscriptMessages(store, 510); err != nil {
		t.Fatalf("append transcript messages: %v", err)
	}
	if err := store.SetName("before rename"); err != nil {
		t.Fatalf("set initial name: %v", err)
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	first, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get first transcript page: %v", err)
	}
	if got := first.Transcript.SessionName; got != "before rename" {
		t.Fatalf("first session name = %q, want before rename", got)
	}

	if err := store.SetName("after rename"); err != nil {
		t.Fatalf("rename session: %v", err)
	}
	second, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get second transcript page: %v", err)
	}
	if got := second.Transcript.SessionName; got != "after rename" {
		t.Fatalf("cached session name = %q, want after rename", got)
	}
}

func TestServiceGetSessionTranscriptPageDormantPageCacheInvalidatesOnRevisionBoundary(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := appendDormantTranscriptMessages(store, 510); err != nil {
		t.Fatalf("append transcript messages: %v", err)
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	first, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get first transcript page: %v", err)
	}
	if got := len(first.Transcript.Entries); got != 510 {
		t.Fatalf("first entry count = %d, want 510", got)
	}

	if _, _, err := store.AppendEvent("step-extra", "message", llm.Message{Role: llm.RoleAssistant, Content: "line 510", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append revision boundary message: %v", err)
	}
	second, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get second transcript page: %v", err)
	}
	if got := len(second.Transcript.Entries); got != 511 {
		t.Fatalf("cached entry count = %d, want 511", got)
	}
	if second.Transcript.Revision <= first.Transcript.Revision {
		t.Fatalf("revision did not advance: first=%d second=%d", first.Transcript.Revision, second.Transcript.Revision)
	}
}

func appendDormantTranscriptMessages(store *session.Store, count int) error {
	for i := 0; i < count; i++ {
		if _, _, err := store.AppendEvent("step-seed", "message", llm.Message{Role: llm.RoleAssistant, Content: fmt.Sprintf("line %d", i), Phase: llm.MessagePhaseFinal}); err != nil {
			return err
		}
	}
	return nil
}

func TestServiceGetSessionTranscriptPageUsesDormantRecentTailByDefault(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	for i := 0; i < runtimeview.RecentTailEntryLimit+20; i++ {
		entry := llm.Message{Role: llm.RoleUser, Content: "u" + strconv.Itoa(i)}
		if _, _, err := store.AppendEvent("step-1", "message", entry); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	total := runtimeview.RecentTailEntryLimit + 20
	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if resp.Transcript.HasMoreAbove {
		t.Fatalf("never-compacted session must not report more above: %+v", resp.Transcript)
	}
	if len(resp.Transcript.Entries) != total {
		t.Fatalf("entries = %d, want %d (whole segment)", len(resp.Transcript.Entries), total)
	}
	if first := resp.Transcript.Entries[0].Text; first != "u0" {
		t.Fatalf("first dormant segment entry = %q, want u0", first)
	}
	if last := resp.Transcript.Entries[len(resp.Transcript.Entries)-1].Text; last != fmt.Sprintf("u%d", total-1) {
		t.Fatalf("last dormant segment entry = %q", last)
	}
}

func TestServiceDormantReviewerRollbackIsIgnoredOnRead(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
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

	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	transcriptResp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{
		SessionID: store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if transcriptResp.Transcript.HasMoreAbove {
		t.Fatalf("legacy reviewer rollback must not act as a segment boundary: %+v", transcriptResp.Transcript)
	}
	if len(transcriptResp.Transcript.Entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(transcriptResp.Transcript.Entries))
	}
	if got := transcriptResp.Transcript.Entries[0].Text; got != "u1" {
		t.Fatalf("first visible transcript entry = %+v, want u1", transcriptResp.Transcript.Entries)
	}
	if got := transcriptResp.Transcript.Entries[1].Text; got != "rolled back final" {
		t.Fatalf("second visible transcript entry = %+v, want rolled back final", transcriptResp.Transcript.Entries)
	}
	if got := transcriptResp.Transcript.Entries[2].Text; got != "u2" {
		t.Fatalf("third visible transcript entry = %+v, want u2", transcriptResp.Transcript.Entries)
	}

	mainViewResp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if got := mainViewResp.MainView.Status.LastCommittedAssistantFinalAnswer; got != "" {
		t.Fatalf("last committed assistant final answer = %q, want empty because later user message supersedes it", got)
	}
}

func TestServiceGetSessionTranscriptPageKeepsDormantCompactionSummaryAndCarryover(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
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
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if len(resp.Transcript.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (%+v)", len(resp.Transcript.Entries), resp.Transcript.Entries)
	}
	if resp.Transcript.Entries[0].Role != "compaction_summary" || resp.Transcript.Entries[0].Text != "condensed provider summary" {
		t.Fatalf("expected projected provider compaction summary entry, got %+v", resp.Transcript.Entries[0])
	}
	if resp.Transcript.Entries[1].Role != "compaction_summary" || resp.Transcript.Entries[1].Text != "condensed summary" {
		t.Fatalf("expected persisted compaction summary entry, got %+v", resp.Transcript.Entries[1])
	}
	if resp.Transcript.Entries[2].Role != "manual_compaction_carryover" {
		t.Fatalf("expected manual carryover entry, got %+v", resp.Transcript.Entries[2])
	}
}

func TestServiceGetSessionTranscriptPagePreservesHistoryAcrossActiveCompaction(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
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
	if _, _, err := store.AppendEvent("step-1", "local_entry", map[string]any{"role": "compaction_notice", "text": "after replace notice"}); err != nil {
		t.Fatalf("append compaction notice entry: %v", err)
	}
	eng, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	eng.AppendCommittedEntry("assistant", "live local")
	svc := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(eng), nil)

	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if !resp.Transcript.HasMoreAbove || resp.Transcript.OlderCursor <= 0 {
		t.Fatalf("newest segment after compaction must report more above: %+v", resp.Transcript)
	}
	if len(resp.Transcript.Entries) != 3 {
		t.Fatalf("newest segment entries = %d, want 3 (%+v)", len(resp.Transcript.Entries), resp.Transcript.Entries)
	}
	if resp.Transcript.Entries[0].Role != "compaction_summary" || resp.Transcript.Entries[0].Text != "condensed provider summary" || resp.Transcript.Entries[0].CompactLabel != "Context compacted" || resp.Transcript.Entries[0].CondensedText != "Context compacted" {
		t.Fatalf("expected projected compaction summary, got %+v", resp.Transcript.Entries[0])
	}
	if resp.Transcript.Entries[1].Role != "compaction_notice" || resp.Transcript.Entries[1].Text != "after replace notice" {
		t.Fatalf("expected legacy local entry preserved without special handling, got %+v", resp.Transcript.Entries[1])
	}
	if resp.Transcript.Entries[2].Role != "assistant" || resp.Transcript.Entries[2].Text != "live local" {
		t.Fatalf("expected live local entry after compaction, got %+v", resp.Transcript.Entries[2])
	}

	older, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID, Cursor: resp.Transcript.OlderCursor})
	if err != nil {
		t.Fatalf("get older transcript page: %v", err)
	}
	if older.Transcript.HasMoreAbove {
		t.Fatalf("oldest segment must not report more above: %+v", older.Transcript)
	}
	if len(older.Transcript.Entries) != 1 || older.Transcript.Entries[0].Text != "before compaction" {
		t.Fatalf("expected pre-compaction segment via scroll-up, got %+v", older.Transcript.Entries)
	}
}

func TestServiceGetSessionTranscriptPagePaginatesBeforeActiveCompactionBoundary(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "before-1"}); err != nil {
		t.Fatalf("append first user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "before-2", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "history_replaced", map[string]any{
		"engine": "local",
		"mode":   "manual",
		"items":  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "condensed provider summary", MessageType: llm.MessageTypeCompactionSummary}}),
	}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "local_entry", map[string]any{"role": "compaction_notice", "text": "after replace notice"}); err != nil {
		t.Fatalf("append compaction notice entry: %v", err)
	}
	eng, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	eng.AppendCommittedEntry("assistant", "live local")
	svc := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(eng), nil)

	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get paginated session transcript page: %v", err)
	}
	if !resp.Transcript.HasMoreAbove || resp.Transcript.OlderCursor <= 0 {
		t.Fatalf("newest segment must report more above: %+v", resp.Transcript)
	}
	older, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{SessionID: store.Meta().SessionID, Cursor: resp.Transcript.OlderCursor})
	if err != nil {
		t.Fatalf("get pre-compaction segment: %v", err)
	}
	if older.Transcript.HasMoreAbove {
		t.Fatalf("oldest segment must not report more above: %+v", older.Transcript)
	}
	if len(older.Transcript.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (%+v)", len(older.Transcript.Entries), older.Transcript.Entries)
	}
	if older.Transcript.Entries[0].Role != "user" || older.Transcript.Entries[0].Text != "before-1" {
		t.Fatalf("expected first pre-compaction entry, got %+v", older.Transcript.Entries[0])
	}
	if older.Transcript.Entries[1].Role != "assistant" || older.Transcript.Entries[1].Text != "before-2" {
		t.Fatalf("expected second pre-compaction entry, got %+v", older.Transcript.Entries[1])
	}
}

func TestServiceGetSessionTranscriptPageUsesDormantRecentTailWindow(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	for i := 0; i < runtimeview.RecentTailEntryLimit+20; i++ {
		if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
	svc := NewService(NewStaticSessionResolver(store), nil, nil)

	total := runtimeview.RecentTailEntryLimit + 20
	resp, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{
		SessionID: store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("get session transcript page: %v", err)
	}
	if resp.Transcript.HasMoreAbove {
		t.Fatalf("never-compacted session must not report more above: %+v", resp.Transcript)
	}
	if len(resp.Transcript.Entries) != total {
		t.Fatalf("entries = %d, want %d (whole segment)", len(resp.Transcript.Entries), total)
	}
	if first := resp.Transcript.Entries[0].Text; first != "u0" {
		t.Fatalf("first tail entry = %q, want u0", first)
	}
	if last := resp.Transcript.Entries[len(resp.Transcript.Entries)-1].Text; last != fmt.Sprintf("u%d", total-1) {
		t.Fatalf("last tail entry = %q", last)
	}
}

func TestServiceGetSessionMainViewDoesNotMutatePersistedSessionFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Minute)
	if _, err := store.AppendRunStarted(session.RunRecord{RunID: "run-1", StepID: "step-1", StartedAt: startedAt}); err != nil {
		t.Fatalf("append run start: %v", err)
	}
	if err := store.MarkInFlight(true); err != nil {
		t.Fatalf("mark in-flight: %v", err)
	}

	sessionPath := filepath.Join(store.Dir(), "session.json")
	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	beforeSession, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session file before: %v", err)
	}
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file before: %v", err)
	}

	svc := NewService(NewStaticSessionResolver(store), nil, nil)
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if resp.MainView.ActiveRun == nil || resp.MainView.ActiveRun.RunID != "run-1" {
		t.Fatalf("expected durable running active run, got %+v", resp.MainView.ActiveRun)
	}

	afterSession, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session file after: %v", err)
	}
	afterEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file after: %v", err)
	}
	if string(beforeSession) != string(afterSession) {
		t.Fatalf("session file mutated during read\nbefore=%s\nafter=%s", string(beforeSession), string(afterSession))
	}
	if string(beforeEvents) != string(afterEvents) {
		t.Fatalf("events file mutated during read\nbefore=%s\nafter=%s", string(beforeEvents), string(afterEvents))
	}
}
