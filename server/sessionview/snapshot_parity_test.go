package sessionview

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestSessionSnapshotSourcesParityForMainView(t *testing.T) {
	fixture := newSessionSnapshotParityFixture(t, config.CacheWarningModeVerbose)
	live := mustMainView(t, fixture.live, fixture.sessionID)
	dormant := mustMainView(t, fixture.dormant, fixture.sessionID)

	assertEqual(t, "session id", live.Session.SessionID, dormant.Session.SessionID)
	assertEqual(t, "session name", live.Session.SessionName, dormant.Session.SessionName)
	assertEqual(t, "freshness", live.Session.ConversationFreshness, dormant.Session.ConversationFreshness)
	assertEqual(t, "execution target", live.Session.ExecutionTarget, dormant.Session.ExecutionTarget)
	assertEqual(t, "parent session id", live.Status.ParentSessionID, dormant.Status.ParentSessionID)
	assertEqual(t, "last committed final", live.Status.LastCommittedAssistantFinalAnswer, dormant.Status.LastCommittedAssistantFinalAnswer)
	assertEqual(t, "update status", live.Status.Update, dormant.Status.Update)
	assertEqual(t, "activity available", live.Activity.State != "", true)
	assertEqual(t, "dormant activity", dormant.Activity.State, clientui.RuntimeActivityUnavailable)
}

func TestSessionSnapshotSourcesParityForTranscriptTailEntries(t *testing.T) {
	fixture := newSessionSnapshotParityFixture(t, config.CacheWarningModeVerbose)
	live := mustTranscriptTailEntries(t, fixture.live, fixture.sessionID)
	dormant := mustTranscriptTailEntries(t, fixture.dormant, fixture.sessionID)
	assertEqual(t, "transcript tail entries", normalizedChatEntries(live), normalizedChatEntries(dormant))
}

func TestSessionSnapshotSourcesParityForRollbackLocatorAcrossCandidateFreeCompactions(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	appended, err := store.AppendEventWithEndByteCursor(
		"user-step",
		"message",
		llm.Message{Role: llm.RoleUser, Content: "candidate before dormant compactions"},
	)
	if err != nil {
		t.Fatalf("append rollback candidate: %v", err)
	}
	if appended.EndByteCursor == nil {
		t.Fatal("rollback candidate append did not return a page cursor")
	}
	locator := rollbacktarget.CandidateLocator{
		UserMessageSeq:       appended.Event.Seq,
		CandidatePageEndByte: *appended.EndByteCursor,
	}
	for index := 0; index < 3; index++ {
		if _, _, err := store.AppendEvent("compact-step", "history_replaced", map[string]any{
			"engine":                    "local",
			"latest_rollback_candidate": locator,
			"items": llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleUser,
				MessageType: llm.MessageTypeCompactionSummary,
				Content:     "candidate-free summary",
			}}),
		}); err != nil {
			t.Fatalf("append history replacement %d: %v", index, err)
		}
	}
	engine, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close engine: %v", err)
		}
	})
	live := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(engine), nil)
	dormant := NewService(NewStaticSessionResolver(store), nil, nil)

	liveNewest := mustTranscriptPage(t, live, store.Meta().SessionID, nil)
	dormantNewest := mustTranscriptPage(t, dormant, store.Meta().SessionID, nil)
	assertEqual(t, "newest rollback locator", liveNewest.LatestRollbackCandidate, dormantNewest.LatestRollbackCandidate)
	if dormantNewest.LatestRollbackCandidate == nil || *dormantNewest.LatestRollbackCandidate != locator {
		t.Fatalf("dormant newest locator = %#v, want %#v", dormantNewest.LatestRollbackCandidate, locator)
	}

	cursor := locator.CandidatePageEndByte
	liveCandidate := mustTranscriptPage(t, live, store.Meta().SessionID, &cursor)
	dormantCandidate := mustTranscriptPage(t, dormant, store.Meta().SessionID, &cursor)
	assertEqual(t, "candidate-page rollback locator", liveCandidate.LatestRollbackCandidate, dormantCandidate.LatestRollbackCandidate)
	wantTarget := rollbacktarget.EncodeUserMessageSeq(locator.UserMessageSeq)
	found := false
	for _, row := range dormantCandidate.Entries {
		if row.User != nil && row.User.RollbackTargetID != nil && *row.User.RollbackTargetID == wantTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("dormant direct candidate page did not contain rollback target %q", wantTarget)
	}
}

func TestSessionSnapshotSourcesParityForActiveRunStatus(t *testing.T) {
	store, engine, release, done := startBlockingRuntimeRun(t)
	live := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(engine), nil)
	dormant := NewService(NewStaticSessionResolver(store), nil, nil)

	liveMain := mustMainView(t, live, store.Meta().SessionID)
	dormantMain := mustMainView(t, dormant, store.Meta().SessionID)
	if liveMain.Activity.State != clientui.RuntimeActivityRunning {
		t.Fatalf("live activity = %+v, want running", liveMain.Activity)
	}
	if dormantMain.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("dormant activity = %+v, want unavailable", dormantMain.Activity)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
}

func TestLiveRuntimeSnapshotReturnsActiveRunWithoutSessionStore(t *testing.T) {
	store, engine, release, done := startBlockingRuntimeRun(t)
	live := NewService(nil, NewStaticRuntimeResolver(engine), nil)
	liveMain := mustMainView(t, live, store.Meta().SessionID)
	if liveMain.Activity.State != clientui.RuntimeActivityRunning {
		t.Fatalf("expected running activity, got %+v", liveMain.Activity)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
}

type sessionSnapshotParityFixture struct {
	sessionID string
	live      *Service
	dormant   *Service
}

func newSessionSnapshotParityFixture(t *testing.T, cacheWarningMode config.CacheWarningMode) sessionSnapshotParityFixture {
	t.Helper()
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if err := store.SetName("parity session"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := store.SetParentSessionID("parent-session"); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append u1: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append a1: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "cache_warning", transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonNonPostfix}); err != nil {
		t.Fatalf("append cache warning: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "local_entry", map[string]any{"role": "compaction_summary", "text": "manual compacted summary"}); err != nil {
		t.Fatalf("append compaction summary: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append u2: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append a2: %v", err)
	}
	engine, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", CacheWarningMode: cacheWarningMode})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	target := staticExecutionTargetResolver{target: clientui.SessionExecutionTarget{
		WorkspaceID:      "workspace-1",
		WorkspaceRoot:    dir,
		CwdRelpath:       ".",
		EffectiveWorkdir: dir,
	}}
	update := staticUpdateStatusProvider{status: clientui.UpdateStatus{Checked: true, Available: true, CurrentVersion: "1.0.0", LatestVersion: "1.1.0"}}
	live := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(engine), target).WithCacheWarningMode(cacheWarningMode).WithUpdateStatusProvider(update)
	dormant := NewService(NewStaticSessionResolver(store), nil, target).WithCacheWarningMode(cacheWarningMode).WithUpdateStatusProvider(update)
	return sessionSnapshotParityFixture{sessionID: store.Meta().SessionID, live: live, dormant: dormant}
}

func startBlockingRuntimeRun(t *testing.T) (*session.Store, *runtime.Engine, chan struct{}, chan error) {
	t.Helper()
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	started := make(chan struct{})
	release := make(chan struct{})
	client := &serviceFakeLLM{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_shell_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: []byte(`{"command":"pwd"}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	engine, err := runtime.New(store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: serviceBlockingTool{started: started, release: release}}), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, submitErr := engine.SubmitUserMessage(context.Background(), "run tools")
		done <- submitErr
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active run")
	}
	return store, engine, release, done
}

func mustMainView(t *testing.T, svc *Service, sessionID string) clientui.RuntimeMainView {
	t.Helper()
	resp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("get main view: %v", err)
	}
	return resp.MainView
}

func mustTranscriptTailEntries(t *testing.T, svc *Service, sessionID string) []runtime.ChatEntry {
	t.Helper()
	entries, err := svc.SessionTranscriptTailEntries(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("get transcript tail entries: %v", err)
	}
	return entries
}

func mustTranscriptPage(t *testing.T, svc *Service, sessionID string, cursor *int64) clientui.TranscriptPage {
	t.Helper()
	response, err := svc.GetSessionTranscriptPage(context.Background(), serverapi.SessionTranscriptPageRequest{
		SessionID: sessionID,
		Cursor:    cursor,
	})
	if err != nil {
		t.Fatalf("get transcript page: %v", err)
	}
	return response.Transcript
}

type comparableChatEntry struct {
	Visibility    clientui.EntryVisibility
	Role          string
	Text          string
	CondensedText string
	Phase         string
	MessageType   string
	CompactLabel  string
}

func normalizedChatEntries(entries []runtime.ChatEntry) []comparableChatEntry {
	out := make([]comparableChatEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, comparableChatEntry{
			Visibility:    entry.Visibility,
			Role:          entry.Role,
			Text:          entry.Text,
			CondensedText: entry.CondensedText,
			Phase:         string(entry.Phase),
			MessageType:   string(entry.MessageType),
			CompactLabel:  entry.CompactLabel,
		})
	}
	return out
}

func assertEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s mismatch\nlive=%s\ndormant=%s", label, strings.TrimSpace(fmt.Sprintf("%+v", got)), strings.TrimSpace(fmt.Sprintf("%+v", want)))
	}
}
