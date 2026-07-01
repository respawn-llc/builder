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
	assertEqual(t, "transcript revision", live.Session.Transcript.Revision, dormant.Session.Transcript.Revision)
	assertEqual(t, "execution target", live.Session.ExecutionTarget, dormant.Session.ExecutionTarget)
	assertEqual(t, "parent session id", live.Status.ParentSessionID, dormant.Status.ParentSessionID)
	assertEqual(t, "last committed final", live.Status.LastCommittedAssistantFinalAnswer, dormant.Status.LastCommittedAssistantFinalAnswer)
	assertEqual(t, "update status", live.Status.Update, dormant.Status.Update)
	assertEqual(t, "activity available", live.Activity.State != "", true)
	assertEqual(t, "dormant activity", dormant.Activity.State, clientui.RuntimeActivityUnavailable)
}

func TestSessionSnapshotSourcesParityForTranscriptQueries(t *testing.T) {
	fixture := newSessionSnapshotParityFixture(t, config.CacheWarningModeVerbose)
	pageRequests := map[string]serverapi.SessionTranscriptPageRequest{
		"default": {SessionID: fixture.sessionID},
	}
	for name, req := range pageRequests {
		t.Run(name, func(t *testing.T) {
			live := mustTranscriptPage(t, fixture.live, req)
			dormant := mustTranscriptPage(t, fixture.dormant, req)
			assertEqual(t, "transcript page", normalizedTranscriptPage(live), normalizedTranscriptPage(dormant))
		})
	}

	suffixReq := serverapi.SessionCommittedTranscriptSuffixRequest{SessionID: fixture.sessionID}
	liveSuffix := mustCommittedSuffix(t, fixture.live, suffixReq)
	dormantSuffix := mustCommittedSuffix(t, fixture.dormant, suffixReq)
	assertEqual(t, "committed suffix", normalizedCommittedSuffix(liveSuffix), normalizedCommittedSuffix(dormantSuffix))
}

func TestSessionSnapshotSourcesParityForForwardCursorPaging(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	appendParityMessage(t, store, llm.RoleUser, "u1")
	appendParityMessage(t, store, llm.RoleAssistant, "a1")
	if _, _, err := store.AppendEvent("step", "history_replaced", map[string]any{
		"engine": "compaction",
		"items":  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}
	appendParityMessage(t, store, llm.RoleUser, "u2")
	appendParityMessage(t, store, llm.RoleAssistant, "a2")

	engine, err := runtime.New(store, &serviceFakeLLM{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	target := staticExecutionTargetResolver{target: clientui.SessionExecutionTarget{WorkspaceID: "workspace-1", WorkspaceRoot: dir, CwdRelpath: ".", EffectiveWorkdir: dir}}
	live := NewService(NewStaticSessionResolver(store), NewStaticRuntimeResolver(engine), target)
	dormant := NewService(NewStaticSessionResolver(store), nil, target)
	sessionID := store.Meta().SessionID

	tail := mustTranscriptPage(t, live, serverapi.SessionTranscriptPageRequest{SessionID: sessionID})
	if tail.OlderCursor <= 0 {
		t.Fatalf("tail must page above a compaction, got cursor %d", tail.OlderCursor)
	}
	older := mustTranscriptPage(t, live, serverapi.SessionTranscriptPageRequest{SessionID: sessionID, Cursor: tail.OlderCursor})
	if older.NewerCursor <= 0 || !older.HasMoreBelow {
		t.Fatalf("older segment must expose a forward cursor, got cursor=%d below=%t", older.NewerCursor, older.HasMoreBelow)
	}

	forwardReq := serverapi.SessionTranscriptPageRequest{SessionID: sessionID, NewerCursor: older.NewerCursor}
	liveForward := mustTranscriptPage(t, live, forwardReq)
	dormantForward := mustTranscriptPage(t, dormant, forwardReq)
	assertEqual(t, "forward transcript page", normalizedTranscriptPage(liveForward), normalizedTranscriptPage(dormantForward))

	dormantOlder := mustTranscriptPage(t, dormant, serverapi.SessionTranscriptPageRequest{SessionID: sessionID, Cursor: tail.OlderCursor})
	assertEqual(t, "older transcript page", normalizedTranscriptPage(older), normalizedTranscriptPage(dormantOlder))
}

func appendParityMessage(t *testing.T, store *session.Store, role llm.Role, content string) {
	t.Helper()
	if _, _, err := store.AppendEvent("step", "message", llm.Message{Role: role, Content: content}); err != nil {
		t.Fatalf("append %q: %v", content, err)
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
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
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
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
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

func mustTranscriptPage(t *testing.T, svc *Service, req serverapi.SessionTranscriptPageRequest) clientui.TranscriptPage {
	t.Helper()
	resp, err := svc.GetSessionTranscriptPage(context.Background(), req)
	if err != nil {
		t.Fatalf("get transcript page: %v", err)
	}
	return resp.Transcript
}

func mustCommittedSuffix(t *testing.T, svc *Service, req serverapi.SessionCommittedTranscriptSuffixRequest) clientui.CommittedTranscriptSuffix {
	t.Helper()
	resp, err := svc.GetSessionCommittedTranscriptSuffix(context.Background(), req)
	if err != nil {
		t.Fatalf("get committed suffix: %v", err)
	}
	return resp.Suffix
}

type comparableTranscriptPage struct {
	SessionID             string
	SessionName           string
	ConversationFreshness clientui.ConversationFreshness
	Revision              int64
	OlderCursor           int64
	HasMoreAbove          bool
	NewerCursor           int64
	HasMoreBelow          bool
	Entries               []comparableChatEntry
}

func normalizedTranscriptPage(page clientui.TranscriptPage) comparableTranscriptPage {
	return comparableTranscriptPage{
		SessionID:             page.SessionID,
		SessionName:           page.SessionName,
		ConversationFreshness: page.ConversationFreshness,
		Revision:              page.Revision,
		OlderCursor:           page.OlderCursor,
		HasMoreAbove:          page.HasMoreAbove,
		NewerCursor:           page.NewerCursor,
		HasMoreBelow:          page.HasMoreBelow,
		Entries:               normalizedChatEntries(page.Entries),
	}
}

type comparableCommittedSuffix struct {
	SessionID               string
	SessionName             string
	ConversationFreshness   clientui.ConversationFreshness
	Revision                int64
	HasMoreCommittedEntries bool
	Entries                 []comparableChatEntry
}

func normalizedCommittedSuffix(suffix clientui.CommittedTranscriptSuffix) comparableCommittedSuffix {
	return comparableCommittedSuffix{
		SessionID:               suffix.SessionID,
		SessionName:             suffix.SessionName,
		ConversationFreshness:   suffix.ConversationFreshness,
		Revision:                suffix.Revision,
		HasMoreCommittedEntries: suffix.HasMoreCommittedEntries,
		Entries:                 normalizedChatEntries(suffix.Entries),
	}
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

func normalizedChatEntries(entries []clientui.ChatEntry) []comparableChatEntry {
	out := make([]comparableChatEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, comparableChatEntry{
			Visibility:    entry.Visibility,
			Role:          entry.Role,
			Text:          entry.Text,
			CondensedText: entry.CondensedText,
			Phase:         entry.Phase,
			MessageType:   entry.MessageType,
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
