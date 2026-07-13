package runtime

import (
	"encoding/json"
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflowruntime"
	"core/shared/toolspec"
)

func backgroundShellEventTypeForTest(eventType shelltool.EventType) BackgroundShellEventType {
	switch eventType {
	case shelltool.EventBackgrounded:
		return BackgroundShellEventBackgrounded
	case shelltool.EventCompleted:
		return BackgroundShellEventCompleted
	case shelltool.EventKilled:
		return BackgroundShellEventKilled
	default:
		panic("unknown shell event type in runtime test")
	}
}

func userMessageSeqAt(t *testing.T, store *session.Store, n int) int64 {
	t.Helper()
	window, err := store.ReadRecentEvents(10_000)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	visible := 0
	for _, evt := range window.Events {
		if evt.Kind != "message" {
			continue
		}
		var msg struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			continue
		}
		if msg.Role == "user" {
			visible++
			if visible == n {
				return evt.Seq
			}
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(window.Events))
	return 0
}

func mustCreateTestSession(t *testing.T, workspaceRoot ...string) *session.Store {
	t.Helper()
	root := t.TempDir()
	workspace := root
	if len(workspaceRoot) > 0 {
		workspace = workspaceRoot[0]
	}
	return mustCreateNamedTestSessionAt(t, root, "ws", workspace)
}

func mustCreateTestSessionAt(t *testing.T, root string, options ...session.StoreOption) *session.Store {
	t.Helper()
	return mustCreateNamedTestSessionAt(t, root, "ws", root, options...)
}

func mustCreateNamedTestSession(t *testing.T, workspaceContainerName string, workspaceRoot string, options ...session.StoreOption) *session.Store {
	t.Helper()
	return mustCreateNamedTestSessionAt(t, t.TempDir(), workspaceContainerName, workspaceRoot, options...)
}

func mustCreateNamedTestSessionAt(t *testing.T, root string, workspaceContainerName string, workspaceRoot string, options ...session.StoreOption) *session.Store {
	t.Helper()
	store, err := session.Create(root, workspaceContainerName, workspaceRoot, options...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func mustOpenTestSession(t *testing.T, dir string) *session.Store {
	t.Helper()
	store, err := session.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}

func mustNewTestEngine(t *testing.T, store *session.Store, client llm.Client, registry *tools.Registry, cfg Config) *Engine {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = "gpt-5"
	}
	engine, err := New(store, client, registry, cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return engine
}

func mustNewFakeToolEngine(t *testing.T, store *session.Store, client llm.Client, cfg Config, toolIDs ...toolspec.ID) *Engine {
	t.Helper()
	handlers := make([]tools.HandlerRegistration, 0, len(toolIDs))
	for _, id := range toolIDs {
		handlers = append(handlers, tools.HandlerRegistration{ID: id, Handler: fakeTool{name: id}})
	}
	return mustNewTestEngine(t, store, client, tools.NewRegistry(handlers...), cfg)
}

func mustNewExecTestEngine(t *testing.T, store *session.Store, client llm.Client, cfg Config) *Engine {
	t.Helper()
	return mustNewFakeToolEngine(t, store, client, cfg, toolspec.ToolExecCommand)
}

func mustNewHandoffTestEngine(t *testing.T, store *session.Store, client llm.Client, cfg Config) *Engine {
	t.Helper()
	if cfg.CompactionMode == "" {
		cfg.CompactionMode = "local"
	}
	cfg.EnabledTools = []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff}
	return mustNewExecTestEngine(t, store, client, cfg)
}

func mustNewWorkflowTestEngine(t *testing.T, store *session.Store, client llm.Client, workflowCfg *workflowruntime.Config, cfg Config) *Engine {
	t.Helper()
	cfg.WorkflowRun = workflowCfg
	return mustNewExecTestEngine(t, store, client, cfg)
}

func mustSetWorktreeReminderState(t *testing.T, store *session.Store, state session.WorktreeReminderState) session.WorktreeReminderState {
	t.Helper()
	if err := store.SetWorktreeReminderState(&state); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	persisted := store.Meta().WorktreeReminder
	if persisted == nil {
		t.Fatal("worktree reminder state was not persisted")
	}
	return *session.CloneWorktreeReminderState(persisted)
}

func testWorktreeReminderState(mode session.WorktreeReminderMode, branch, worktreePath, workspaceRoot, effectiveCWD string) session.WorktreeReminderState {
	return session.WorktreeReminderState{
		Mode: mode,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch(branch),
			WorktreePath:  worktreePath,
			WorkspaceRoot: workspaceRoot,
			EffectiveCwd:  effectiveCWD,
		},
	}
}

func finalTextResponse(content string) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: content},
		Usage:     llm.Usage{WindowTokens: 200000},
	}
}

func finalOutputItemResponse(content string) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: content},
		OutputItems: []llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    llm.RoleAssistant,
			Phase:   llm.MessagePhaseFinal,
			Content: content,
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}
}

func commentaryResponse(content string, toolCalls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: content, Phase: llm.MessagePhaseCommentary, ToolCalls: toolCalls},
		ToolCalls: toolCalls,
		Usage:     llm.Usage{WindowTokens: 200000},
	}
}

func assertModelCallCount(t *testing.T, client *fakeClient, want int) {
	t.Helper()
	if len(client.calls) != want {
		t.Fatalf("model calls = %d, want %d", len(client.calls), want)
	}
}

type expectedChatEntry struct {
	Role string
	Text string
}

type chatEntryCase struct {
	name string
	seed func(*chatStore)
	want []expectedChatEntry
}

func runChatEntryCases(t *testing.T, cases []chatEntryCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newChatStore()
			tc.seed(store)
			assertChatEntries(t, store.snapshotWithMetadata().Snapshot.Entries, tc.want)
		})
	}
}

func assertChatEntries(t *testing.T, got []ChatEntry, want []expectedChatEntry) {
	t.Helper()
	normalized := make([]expectedChatEntry, 0, len(got))
	for _, entry := range got {
		normalized = append(normalized, expectedChatEntry{Role: entry.Role, Text: entry.Text})
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("chat entries mismatch\n got: %+v\nwant: %+v", normalized, want)
	}
}
