package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestSteerWorktreeTransitionFailureRejectsInvalidOutcomeBeforeRuntimeMutation(t *testing.T) {
	engine := &Engine{}
	err := engine.SteerWorktreeTransitionFailure(clientui.WorktreeTransitionOutcome{
		Transition: clientui.WorktreeTransitionEnter,
		State:      clientui.WorktreeTransitionFailed,
		Failure:    &clientui.WorktreeTransitionFailure{Diagnostic: "failed"},
	})
	if err == nil {
		t.Fatal("invalid worktree transition outcome was accepted")
	}
}

func TestPersistedWorktreeContextRejectsDuplicateSourcePath(t *testing.T) {
	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/typed-only",
		"/tmp/worktree",
		"/tmp/workspace",
		"/tmp/worktree",
	))
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:            llm.RoleDeveloper,
		MessageType:     llm.MessageTypeWorktreeMode,
		SourcePath:      target.EffectiveCwd,
		WorktreeContext: &target.WorktreeContext,
		Content:         "typed worktree context",
	}}))
	if err == nil {
		t.Fatal("expected duplicate worktree source path to be rejected")
	}
}

func TestFirstMetaInjectionUsesPendingWorktreeCWD(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}} {{cwd}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	worktree := t.TempDir()
	worktreeSubdir := filepath.Join(worktree, "pkg")
	if err := os.MkdirAll(worktreeSubdir, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree subdir: %v", err)
	}
	writeTestFile(t, filepath.Join(workspace, agentsFileName), "stale workspace instruction")
	writeTestFile(t, filepath.Join(worktree, agentsFileName), "active worktree instruction")
	store := mustCreateTestSession(t, workspace)
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/new",
		worktree,
		workspace,
		worktreeSubdir,
	))

	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "start in the new worktree"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	messages := requestMessages(client.calls[0])
	if len(messages) < 3 {
		t.Fatalf("expected environment, agents, and user messages, got %+v", messages)
	}
	envMsg := messages[0]
	if envMsg.Role != llm.RoleDeveloper || envMsg.MessageType != llm.MessageTypeEnvironment {
		t.Fatalf("expected environment context first, got %+v", envMsg)
	}
	if !strings.Contains(envMsg.Content, "\nCWD: "+worktreeSubdir+"\n") {
		t.Fatalf("expected environment cwd to use pending worktree subdir %q, got %q", worktreeSubdir, envMsg.Content)
	}
	if strings.Contains(envMsg.Content, "\nCWD: "+workspace+"\n") {
		t.Fatalf("expected environment cwd not to use stale workspace %q, got %q", workspace, envMsg.Content)
	}
	agentsMsg := messages[1]
	if agentsMsg.Role != llm.RoleDeveloper || agentsMsg.MessageType != llm.MessageTypeAgentsMD || !strings.Contains(agentsMsg.Content, "source: "+filepath.Join(worktree, agentsFileName)) {
		t.Fatalf("expected active worktree AGENTS context second, got %+v", agentsMsg)
	}
	if strings.Contains(agentsMsg.Content, "stale workspace instruction") {
		t.Fatalf("expected stale workspace AGENTS context to be excluded, got %q", agentsMsg.Content)
	}
}

func TestSubmitUserMessageInjectsPendingWorktreeEnterReminder(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}} {{cwd}} {{worktree_path}} {{workspace_root}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/enter",
		"/tmp/wt-enter",
		"/tmp/workspace",
		"/tmp/wt-enter/pkg",
	))

	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	assertModelCallCount(t, client, 1)
	messages := requestMessages(client.calls[0])
	reminderIdx := -1
	for i, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeMode {
			reminderIdx = i
			if msg.WorktreeContext == nil || !session.WorktreeContextEqual(*msg.WorktreeContext, target.WorktreeContext) {
				t.Fatalf("worktree context = %+v, want %+v", msg.WorktreeContext, target)
			}
		}
	}
	if reminderIdx < 0 {
		t.Fatalf("expected worktree enter reminder, messages=%+v", messages)
	}
	state := store.Meta().WorktreeReminder
	if state == nil || !session.WorktreeReminderStateEqual(*state, target) {
		t.Fatalf("unexpected persisted reminder state after submit: %+v", state)
	}
	var entry *ChatEntry
	for idx := range eng.ChatSnapshot().Entries {
		if eng.ChatSnapshot().Entries[idx].MessageType == llm.MessageTypeWorktreeMode {
			entry = &eng.ChatSnapshot().Entries[idx]
			break
		}
	}
	if entry == nil {
		t.Fatal("expected worktree reminder transcript entry")
	}
	if entry.Visibility != transcript.EntryVisibilityOngoing {
		t.Fatalf("worktree reminder visibility = %q, want ongoing", entry.Visibility)
	}
	if entry.WorktreeContext == nil || !session.WorktreeContextEqual(*entry.WorktreeContext, target.WorktreeContext) {
		t.Fatalf("transcript worktree context = %+v, want %+v", entry.WorktreeContext, target)
	}
	if entry.CondensedText != "" || entry.CompactLabel != "" {
		t.Fatalf("server-authored worktree presentation leaked into transcript: ongoing=%q compact=%q", entry.CondensedText, entry.CompactLabel)
	}
	if entry.SourcePath != "" {
		t.Fatalf("worktree source path = %q, want typed context only", entry.SourcePath)
	}
}

func TestRunStepLoopMaterializesPendingWorktreeReminder(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/direct",
		"/tmp/wt-direct",
		"/tmp/workspace",
		"/tmp/wt-direct",
	))
	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.runStepLoop(context.Background(), "step-1"); err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}

	messages := requestMessages(client.calls[0])
	reminderCount := 0
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeMode {
			reminderCount++
			if msg.WorktreeContext == nil || !session.WorktreeContextEqual(*msg.WorktreeContext, target.WorktreeContext) {
				t.Fatalf("worktree context = %+v, want %+v", msg.WorktreeContext, target)
			}
			if msg.CompactContent != "" {
				t.Fatalf("server-authored worktree compact content = %q, want empty", msg.CompactContent)
			}
		}
	}
	if reminderCount != 1 {
		t.Fatalf("expected one worktree reminder, got %d messages=%+v", reminderCount, messages)
	}
	state := store.Meta().WorktreeReminder
	if state == nil || !session.WorktreeReminderStateEqual(*state, target) {
		t.Fatalf("unexpected reminder target state: %+v", state)
	}
}

func TestRunStepLoopCountsPendingWorktreeReminderBeforeAutoCompaction(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/compact",
		"/tmp/wt-compact",
		"/tmp/workspace",
		"/tmp/wt-compact",
	))

	sawReminderDuringPreCompactionCount := false
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant:   llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok"},
			OutputItems: []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok"}},
			Usage:       llm.Usage{WindowTokens: 2_000},
		}},
		inputTokenCountFn: func(req llm.Request) int {
			hasReminder := requestHasWorktreeReminder(req)
			if hasReminder && !requestHasCompactionCheckpoint(req) {
				sawReminderDuringPreCompactionCount = true
				return 1_000
			}
			return 100
		},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "compacted seed"},
				{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
			},
			Usage: llm.Usage{InputTokens: 100, WindowTokens: 2_000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "native",
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 999, WindowTokens: 2_000})

	if _, err := eng.runStepLoop(context.Background(), "step-1"); err != nil {
		t.Fatalf("runStepLoop: %v", err)
	}
	if !sawReminderDuringPreCompactionCount {
		t.Fatal("expected auto-compaction token count to include pending worktree reminder")
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected one auto-compaction call, got %d", len(client.compactionCalls))
	}
	if !requestHasWorktreeReminder(client.calls[0]) {
		t.Fatalf("expected post-compaction model request to include worktree reminder, messages=%+v", requestMessages(client.calls[0]))
	}
	state := store.Meta().WorktreeReminder
	if state == nil || !session.WorktreeReminderStateEqual(*state, target) {
		t.Fatalf("unexpected reminder target after compaction: %+v", state)
	}
}

func TestManualCompactionReinjectsWorktreeReminderExactlyOnce(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/manual-compact",
		"/tmp/wt-manual-compact",
		"/tmp/workspace",
		"/tmp/wt-manual-compact",
	))
	client := &fakeCompactionClient{responses: []llm.Response{
		finalOutputItemResponse("before compaction"),
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "compacted summary"},
			Usage:     llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "start"); err != nil {
		t.Fatalf("submit before compaction: %v", err)
	}
	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	compactedMessages := eng.transcriptRuntimeState().SnapshotMessages()
	if got := worktreeReminderMessageCount(compactedMessages); got != 1 {
		t.Fatalf("worktree reminders after compaction = %d, want 1 messages=%+v", got, compactedMessages)
	}
	assertLatestWorktreeContext(t, compactedMessages, target)
	if err := eng.Close(); err != nil {
		t.Fatalf("close compacted engine: %v", err)
	}

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen compacted session: %v", err)
	}
	resumedClient := &fakeClient{responses: []llm.Response{finalOutputItemResponse("after compaction")}}
	resumedEngine := mustNewTestEngine(t, reopenedStore, resumedClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	if _, err := resumedEngine.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after compaction resume: %v", err)
	}
	if len(resumedClient.calls) != 1 {
		t.Fatalf("resumed model calls = %d, want 1", len(resumedClient.calls))
	}
	resumedMessages := requestMessages(resumedClient.calls[0])
	if got := worktreeReminderMessageCount(resumedMessages); got != 1 {
		t.Fatalf("worktree reminders in resumed post-compaction request = %d, want 1 messages=%+v", got, resumedMessages)
	}
	assertLatestWorktreeContext(t, resumedMessages, target)
}

func worktreeReminderMessageCount(messages []llm.Message) int {
	count := 0
	for _, message := range messages {
		if message.Role == llm.RoleDeveloper &&
			(message.MessageType == llm.MessageTypeWorktreeMode || message.MessageType == llm.MessageTypeWorktreeModeExit) {
			count++
		}
	}
	return count
}

func requestHasWorktreeReminder(req llm.Request) bool {
	return worktreeReminderMessageCount(requestMessages(req)) > 0
}

func assertLatestWorktreeContext(t *testing.T, messages []llm.Message, want session.WorktreeReminderState) {
	t.Helper()
	var latest *session.WorktreeContext
	for _, message := range messages {
		if message.MessageType != llm.MessageTypeWorktreeMode && message.MessageType != llm.MessageTypeWorktreeModeExit {
			continue
		}
		latest = message.WorktreeContext
	}
	if latest == nil || !session.WorktreeContextEqual(*latest, want.WorktreeContext) {
		t.Fatalf("latest worktree context = %+v, want %+v messages=%+v", latest, want, messages)
	}
}

func requestHasCompactionCheckpoint(req llm.Request) bool {
	for _, item := range req.Items {
		if item.Type == llm.ResponseItemTypeCompaction || item.MessageType == llm.MessageTypeCompactionSummary {
			return true
		}
	}
	return false
}

func TestRepeatedSubmissionsDoNotDuplicateMaterializedWorktreeReminder(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateNamedTestSession(t, "ws", t.TempDir())
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/stable",
		"/tmp/wt-stable",
		"/tmp/workspace",
		"/tmp/wt-stable",
	))
	client := &fakeClient{responses: []llm.Response{
		finalOutputItemResponse("ok-1"),
		finalOutputItemResponse("ok-2"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "continue again"); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	assertModelCallCount(t, client, 2)
	assertWorktreeReminderEntryCount(t, eng.ChatSnapshot(), 1)
	if got := worktreeReminderMessageCount(requestMessages(client.calls[1])); got != 1 {
		t.Fatalf("worktree reminders in second request = %d, want 1", got)
	}
}

func TestSameCWDChangedWorktreeTargetMaterializesNewContext(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	const sharedCWD = "/tmp/shared-cwd"
	store := mustCreateNamedTestSession(t, "ws", t.TempDir())
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/first",
		"/tmp/worktree-first",
		"/tmp/workspace",
		sharedCWD,
	))
	client := &fakeClient{responses: []llm.Response{
		finalOutputItemResponse("ok-1"),
		finalOutputItemResponse("ok-2"),
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "first target"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	secondTarget := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/second",
		"/tmp/worktree-second",
		"/tmp/workspace",
		sharedCWD,
	))
	if _, err := eng.SubmitUserMessage(context.Background(), "second target"); err != nil {
		t.Fatalf("second submit: %v", err)
	}

	messages := requestMessages(client.calls[1])
	if got := worktreeReminderMessageCount(messages); got != 2 {
		t.Fatalf("worktree contexts in second request = %d, want 2 messages=%+v", got, messages)
	}
	var latest *llm.Message
	for idx := range messages {
		if messages[idx].MessageType == llm.MessageTypeWorktreeMode {
			latest = &messages[idx]
		}
	}
	if latest == nil ||
		latest.WorktreeContext == nil ||
		!session.WorktreeContextEqual(*latest.WorktreeContext, secondTarget.WorktreeContext) {
		t.Fatalf("latest worktree context = %+v, want %+v", latest, secondTarget)
	}
	assertWorktreeReminderEntryCount(t, eng.ChatSnapshot(), 2)
}

func TestConfirmedSameCWDTargetChangeBypassesLegacyFallback(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	const sharedCWD = "/tmp/legacy-shared-cwd"
	store := mustCreateNamedTestSession(t, "ws", t.TempDir())
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/legacy",
		"/tmp/worktree-legacy",
		"/tmp/workspace",
		sharedCWD,
	))
	if _, _, err := store.AppendEvent("legacy-worktree", "message", llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeWorktreeMode,
		SourcePath:  sharedCWD,
		Content:     "legacy worktree context",
	}); err != nil {
		t.Fatalf("persist legacy worktree context: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	changedTarget := testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/changed",
		"/tmp/worktree-changed",
		"/tmp/workspace",
		sharedCWD,
	)
	if err := eng.SetWorktreeReminderState(&changedTarget); err != nil {
		t.Fatalf("set confirmed target change: %v", err)
	}
	persistedChangedTarget := store.Meta().WorktreeReminder
	if persistedChangedTarget == nil || persistedChangedTarget.ContextID == nil {
		t.Fatalf("confirmed target change has no context id: %+v", persistedChangedTarget)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "continue after target change"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	messages := requestMessages(client.calls[0])
	if got := worktreeReminderMessageCount(messages); got != 2 {
		t.Fatalf("worktree contexts after confirmed legacy target change = %d, want 2 messages=%+v", got, messages)
	}
	assertLatestWorktreeContext(t, messages, *persistedChangedTarget)
}

func TestLegacyWorktreeFallbackOnlyMatchesUnversionedTarget(t *testing.T) {
	const sharedCWD = "/tmp/legacy-fallback-cwd"
	legacyItems := []llm.ResponseItem{{
		Type:        llm.ResponseItemTypeMessage,
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeWorktreeMode,
		SourcePath:  sharedCWD,
		Content:     "legacy worktree context",
	}}
	desired := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeWorktreeMode,
		WorktreeContext: &session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/legacy"),
			WorktreePath:  "/tmp/worktree-legacy",
			WorkspaceRoot: "/tmp/workspace",
			EffectiveCwd:  sharedCWD,
		},
		Content: "current worktree context",
	}
	if !latestActiveMetaContextMatches(legacyItems, desired) {
		t.Fatal("unversioned target did not match legacy same-cwd context")
	}

	contextID := uuid.New()
	desired.WorktreeContext.ContextID = &contextID
	if latestActiveMetaContextMatches(legacyItems, desired) {
		t.Fatal("versioned target change was suppressed by legacy same-cwd fallback")
	}
}

func TestLegacyCompactionWithoutWorktreeReminderSelfHealsOnceAcrossResume(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateNamedTestSession(t, "ws", t.TempDir())
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/resume",
		"/tmp/wt-resume",
		"/tmp/workspace",
		"/tmp/wt-resume",
	))
	legacyItems := llm.ItemsFromMessages([]llm.Message{
		{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeCompactionSummary,
			Content:     "legacy compacted summary",
		},
		{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeEnvironment,
			Content:     "legacy environment context",
		},
		{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeSkills,
			Content:     "legacy skills context",
		},
		{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeAgentsMD,
			SourcePath:  "/tmp/workspace/AGENTS.md",
			Content:     "legacy AGENTS context",
		},
	})
	if _, _, err := store.AppendEvent("legacy-compact", "history_replaced", historyReplacementPayload{
		Engine:           "local",
		Mode:             string(compactionModeManual),
		CompactionNumber: 1,
		Items:            legacyItems,
	}); err != nil {
		t.Fatalf("append legacy history replacement: %v", err)
	}
	firstClient := &fakeClient{responses: []llm.Response{finalOutputItemResponse("before resume")}}
	firstEngine := mustNewTestEngine(t, store, firstClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})
	if _, err := firstEngine.SubmitUserMessage(context.Background(), "resume legacy generation"); err != nil {
		t.Fatalf("legacy resume submit: %v", err)
	}
	assertMessageTypesInOrder(t, requestMessages(firstClient.calls[0]),
		llm.MessageTypeCompactionSummary,
		llm.MessageTypeEnvironment,
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeWorktreeMode,
	)
	assertWorktreeReminderEntryCount(t, firstEngine.ChatSnapshot(), 1)
	if err := firstEngine.Close(); err != nil {
		t.Fatalf("close initial engine: %v", err)
	}
	prompts.WorktreeModePrompt = "updated enter {{branch}}"

	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if err := reopenedStore.SetWorktreeReminderState(&target); err != nil {
		t.Fatalf("reapply unchanged worktree target: %v", err)
	}
	resumedClient := &fakeClient{responses: []llm.Response{finalOutputItemResponse("after resume")}}
	resumedEngine := mustNewTestEngine(t, reopenedStore, resumedClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})
	if _, err := resumedEngine.SubmitUserMessage(context.Background(), "resume"); err != nil {
		t.Fatalf("resumed submit: %v", err)
	}

	assertModelCallCount(t, resumedClient, 1)
	if got := worktreeReminderMessageCount(requestMessages(resumedClient.calls[0])); got != 1 {
		t.Fatalf("worktree reminders after resume = %d, want 1 messages=%+v", got, requestMessages(resumedClient.calls[0]))
	}
	assertMessageTypesInOrder(t, requestMessages(resumedClient.calls[0]),
		llm.MessageTypeCompactionSummary,
		llm.MessageTypeEnvironment,
		llm.MessageTypeSkills,
		llm.MessageTypeAgentsMD,
		llm.MessageTypeWorktreeMode,
	)
	assertWorktreeReminderEntryCount(t, resumedEngine.ChatSnapshot(), 1)
}

func assertMessageTypesInOrder(t *testing.T, messages []llm.Message, expected ...llm.MessageType) {
	t.Helper()
	previousIndex := -1
	for _, messageType := range expected {
		foundIndex := -1
		count := 0
		for idx, message := range messages {
			if message.Role != llm.RoleDeveloper || message.MessageType != messageType {
				continue
			}
			count++
			foundIndex = idx
		}
		if count != 1 {
			t.Fatalf("message type %q count = %d, want 1 messages=%+v", messageType, count, messages)
		}
		if foundIndex <= previousIndex {
			t.Fatalf("message type %q index = %d after previous index %d, want canonical order", messageType, foundIndex, previousIndex)
		}
		previousIndex = foundIndex
	}
}

func assertWorktreeReminderEntryCount(t *testing.T, snapshot ChatSnapshot, want int) {
	t.Helper()
	got := 0
	for _, entry := range snapshot.Entries {
		if entry.MessageType == llm.MessageTypeWorktreeMode || entry.MessageType == llm.MessageTypeWorktreeModeExit {
			got++
		}
	}
	if got != want {
		t.Fatalf("worktree reminder entry count = %d, want %d entries=%+v", got, want, snapshot.Entries)
	}
}

func TestSubmitUserMessageInjectsPendingWorktreeExitReminder(t *testing.T) {
	prevPrompt := prompts.WorktreeModeExitPrompt
	prompts.WorktreeModeExitPrompt = "exit {{branch}} {{cwd}} {{worktree_path}} {{workspace_root}}"
	defer func() { prompts.WorktreeModeExitPrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	target := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeExit,
		"feature/exit",
		"/tmp/wt-exit",
		"/tmp/workspace",
		"/tmp/workspace/pkg",
	))

	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	messages := requestMessages(client.calls[0])
	found := false
	for _, msg := range messages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeModeExit {
			found = true
			if msg.WorktreeContext == nil || !session.WorktreeContextEqual(*msg.WorktreeContext, target.WorktreeContext) {
				t.Fatalf("worktree exit context = %+v, want %+v", msg.WorktreeContext, target)
			}
		}
	}
	if !found {
		t.Fatalf("expected worktree exit reminder, messages=%+v", messages)
	}
}

func TestSubmitUserMessageMaterializesWorktreeReminderBeforeModelFailure(t *testing.T) {
	withGenerateRetryDelays(t, nil)

	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/retry",
		"/tmp/wt-retry",
		"/tmp/workspace",
		"/tmp/wt-retry",
	))

	failingClient := &hookClient{beforeReturn: func() error { return context.DeadlineExceeded }}
	eng := mustNewTestEngine(t, store, failingClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err == nil {
		t.Fatal("expected submit failure")
	}
	assertWorktreeReminderEntryCount(t, eng.ChatSnapshot(), 1)

	successClient := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng.llm = successClient

	if _, err := eng.SubmitUserMessage(context.Background(), "continue again"); err != nil {
		t.Fatalf("submit retry: %v", err)
	}
	assertModelCallCount(t, successClient, 1)
	reminderCount := 0
	for _, msg := range requestMessages(successClient.calls[0]) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeMode {
			reminderCount++
		}
	}
	if reminderCount != 1 {
		t.Fatalf("expected materialized reminder after failed submit, got %d messages=%+v", reminderCount, requestMessages(successClient.calls[0]))
	}
}

func TestSubmitUserMessageUsesLatestPendingWorktreeReminder(t *testing.T) {
	prevPrompt := prompts.WorktreeModePrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	defer func() { prompts.WorktreeModePrompt = prevPrompt }()

	store := mustCreateTestSession(t)
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/old",
		"/tmp/wt-old",
		"/tmp/workspace",
		"/tmp/wt-old",
	))
	latestTarget := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/new",
		"/tmp/wt-new",
		"/tmp/workspace",
		"/tmp/wt-new",
	))

	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	messages := requestMessages(client.calls[0])
	for _, msg := range messages {
		if msg.Role != llm.RoleDeveloper || msg.MessageType != llm.MessageTypeWorktreeMode {
			continue
		}
		if msg.WorktreeContext == nil || !session.WorktreeContextEqual(*msg.WorktreeContext, latestTarget.WorktreeContext) {
			t.Fatalf("latest worktree context = %+v, want %+v", msg.WorktreeContext, latestTarget)
		}
		return
	}
	t.Fatalf("expected worktree reminder, messages=%+v", messages)
}

func TestSubmitUserMessagePreservesHistoricalWorktreeRemindersInRequest(t *testing.T) {
	prevEnterPrompt := prompts.WorktreeModePrompt
	prevExitPrompt := prompts.WorktreeModeExitPrompt
	prompts.WorktreeModePrompt = "enter {{branch}}"
	prompts.WorktreeModeExitPrompt = "exit {{branch}}"
	defer func() {
		prompts.WorktreeModePrompt = prevEnterPrompt
		prompts.WorktreeModeExitPrompt = prevExitPrompt
	}()

	store := mustCreateTestSession(t)
	firstOutput := llm.ResponseItem{Type: llm.ResponseItemTypeMessage, Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok-1"}
	secondOutput := llm.ResponseItem{Type: llm.ResponseItemTypeMessage, Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok-2"}
	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok-1"}, OutputItems: []llm.ResponseItem{firstOutput}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "ok-2"}, OutputItems: []llm.ResponseItem{secondOutput}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	enterTarget := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(session.WorktreeReminderModeEnter, "feature/enter", "/tmp/wt-enter", "/tmp/workspace", "/tmp/wt-enter"))
	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	exitTarget := mustSetWorktreeReminderState(t, store, testWorktreeReminderState(session.WorktreeReminderModeExit, "feature/exit", "/tmp/wt-exit", "/tmp/workspace", "/tmp/workspace"))
	if _, err := eng.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit second: %v", err)
	}

	assertModelCallCount(t, client, 2)
	exitMessage, ok := worktreeModeExitMetaMessage(exitTarget)
	if !ok {
		t.Fatal("expected exit reminder message")
	}
	expectedSecondItems := llm.CloneResponseItems(client.calls[0].Items)
	expectedSecondItems = append(expectedSecondItems, llm.PrepareOpenAIInputItems([]llm.ResponseItem{firstOutput})...)
	expectedSecondItems = append(expectedSecondItems, llm.ItemsFromMessages([]llm.Message{
		exitMessage,
		{Role: llm.RoleUser, Content: "second"},
	})...)
	if !reflect.DeepEqual(client.calls[1].Items, expectedSecondItems) {
		t.Fatalf("second request items changed historical order/content\nwant=%+v\n got=%+v", expectedSecondItems, client.calls[1].Items)
	}
	firstMessages := requestMessages(client.calls[0])
	firstCount := 0
	for _, msg := range firstMessages {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeMode {
			firstCount++
		}
	}
	if firstCount != 1 {
		t.Fatalf("expected one enter reminder in first request, got %d messages=%+v", firstCount, firstMessages)
	}
	secondMessages := requestMessages(client.calls[1])
	enterCount := 0
	exitCount := 0
	for _, msg := range secondMessages {
		switch {
		case msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeMode:
			enterCount++
		case msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeWorktreeModeExit:
			exitCount++
		}
	}
	if enterCount != 1 || exitCount != 1 {
		t.Fatalf("expected historical enter and latest exit reminders in second request, got enter=%d exit=%d messages=%+v", enterCount, exitCount, secondMessages)
	}
	snapshot := eng.ChatSnapshot()
	detailEntries := 0
	for _, entry := range snapshot.Entries {
		if entry.Role != string(transcript.EntryRoleDeveloperContext) {
			continue
		}
		if entry.WorktreeContext != nil &&
			(session.WorktreeContextEqual(*entry.WorktreeContext, enterTarget.WorktreeContext) ||
				session.WorktreeContextEqual(*entry.WorktreeContext, exitTarget.WorktreeContext)) {
			detailEntries++
		}
	}
	if detailEntries != 2 {
		t.Fatalf("expected detail transcript to retain both reminder rows, got %d entries=%+v", detailEntries, snapshot.Entries)
	}
}
