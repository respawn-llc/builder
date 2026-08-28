package runtime

import (
	"context"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemPromptSnapshotUsesStoredWorkspaceRootWhenTranscriptWorkdirIsNested(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "pkg")
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	writeTestFile(t, filepath.Join(systemDir, systemPromptFileName), "workspace root system")

	store := mustCreateTestSession(t, workspace)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir: nested,
		ToolPreambles:        false,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := client.calls[0].SystemPrompt; got != "workspace root system" {
		t.Fatalf("system prompt = %q, want workspace root system", got)
	}
}

func TestSystemPromptSnapshotFallsBackWhenHomeDirUnavailable(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", "")
	if err := os.MkdirAll(filepath.Join(workspace, agentsGlobalDirName), 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	writeTestFile(t, filepath.Join(workspace, agentsGlobalDirName, systemPromptFileName), "local without home")

	template, sourcePath, ok, err := readSystemPromptTemplate(systemPromptSnapshotOptions{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("read system prompt template: %v", err)
	}
	if !ok || template != "local without home" {
		t.Fatalf("template = %q ok=%t, want local without home true", template, ok)
	}
	if want := filepath.Join(workspace, agentsGlobalDirName, systemPromptFileName); sourcePath != want {
		t.Fatalf("source path = %q, want %q", sourcePath, want)
	}
	template, sourcePath, ok, err = readSystemPromptTemplate(systemPromptSnapshotOptions{})
	if err != nil {
		t.Fatalf("read system prompt template without local prompt: %v", err)
	}
	if ok || template != "" || sourcePath != "" {
		t.Fatalf("template = %q sourcePath=%q ok=%t, want empty fallback", template, sourcePath, ok)
	}
}

func TestEnsureLockedWithSystemPromptAndTranscriptWorkingDirDoesNotDeadlock(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	writeTestFile(t, filepath.Join(systemDir, systemPromptFileName), "deadlock guard")

	store := mustCreateTestSession(t, workspace)
	eng := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir: workspace,
		ToolPreambles:        false,
	})
	done := make(chan struct {
		locked session.LockedContract
		err    error
	}, 1)
	go func() {
		locked, err := eng.ensureLocked()
		done <- struct {
			locked session.LockedContract
			err    error
		}{locked: locked, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ensureLocked: %v", got.err)
		}
		if got.locked.SystemPrompt != "deadlock guard" {
			t.Fatalf("system prompt = %q, want deadlock guard", got.locked.SystemPrompt)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("ensureLocked deadlocked while resolving SYSTEM.md from TranscriptWorkingDir")
	}
}

func TestBuildSystemPromptSnapshotForRootDoesNotUseMutexTakingWorkspaceAccessor(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	writeTestFile(t, filepath.Join(systemDir, systemPromptFileName), "locked helper guard")

	store := mustCreateTestSession(t)
	eng := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
	})
	done := make(chan struct {
		prompt string
		err    error
	}, 1)
	eng.mu.Lock()
	go func() {
		prompt, err := eng.buildSystemPromptSnapshotForRoot(session.LockedContract{
			Model:          "gpt-5",
			Temperature:    1,
			ContextWindow:  272_000,
			ContextPercent: 95,
			ToolPreambles: func() *bool {
				enabled := false
				return &enabled
			}(),
		}, workspace)
		done <- struct {
			prompt string
			err    error
		}{prompt: prompt, err: err}
	}()
	select {
	case got := <-done:
		eng.mu.Unlock()
		if got.err != nil {
			t.Fatalf("buildSystemPromptSnapshotForRoot: %v", got.err)
		}
		if got.prompt != "locked helper guard" {
			t.Fatalf("prompt = %q, want locked helper guard", got.prompt)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		eng.mu.Unlock()
		t.Fatal("buildSystemPromptSnapshotForRoot called a mutex-taking workspace accessor")
	}
}

func TestSystemPromptSnapshotUsesTranscriptWorkingDirForRetargetedSession(t *testing.T) {
	home := t.TempDir()
	canonical := t.TempDir()
	worktree := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{filepath.Join(canonical, agentsGlobalDirName), filepath.Join(worktree, agentsGlobalDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeTestFile(t, filepath.Join(canonical, agentsGlobalDirName, systemPromptFileName), "canonical system")
	writeTestFile(t, filepath.Join(worktree, agentsGlobalDirName, systemPromptFileName), "worktree system")

	store := mustCreateTestSession(t, canonical)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir: canonical,
		ToolPreambles:        false,
	})
	eng.SetTranscriptWorkingDir(worktree)
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := client.calls[0].SystemPrompt; got != "worktree system" {
		t.Fatalf("system prompt = %q, want worktree system", got)
	}
}

func TestEnvironmentContextUsesTranscriptWorkingDirWithoutWorktreeReminder(t *testing.T) {
	workspace := t.TempDir()
	worktree := t.TempDir()
	store := mustCreateTestSession(t, workspace)
	if store.Meta().WorktreeReminder != nil {
		t.Fatalf("worktree reminder = %+v, want none", store.Meta().WorktreeReminder)
	}
	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		Model:                 "gpt-5",
		EnabledTools:          []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir:  worktree,
		AutoCompactTokenLimit: 1_000_000_000,
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	messages := requestMessages(client.calls[0])
	for _, message := range messages {
		if message.MessageType == nil ||
			*message.MessageType != llm.MessageTypeEnvironment ||
			message.Content == nil {
			continue
		}
		if !strings.Contains(*message.Content, "\nCWD: "+worktree+"\n") {
			t.Fatalf("environment context = %q, want active runtime CWD %q", *message.Content, worktree)
		}
		if strings.Contains(*message.Content, "\nCWD: "+workspace+"\n") {
			t.Fatalf("environment context leaked persisted workspace CWD %q", workspace)
		}
		return
	}
	t.Fatalf("environment context missing from request: %+v", messages)
}

func TestLegacyLockedSessionBackfillsSystemPromptSnapshotOnce(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	systemPath := filepath.Join(systemDir, systemPromptFileName)
	writeTestFile(t, systemPath, "stale legacy {{.EstimatedToolCallsForContext}}")

	store := mustCreateTestSession(t, workspace)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
		ContextWindow:  272_000,
		ContextPercent: 95,
		ToolPreambles: func() *bool {
			enabled := false
			return &enabled
		}(),
	}); err != nil {
		t.Fatalf("mark locked: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok again")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir: workspace,
	})
	if snapshot := store.Meta().Locked.SystemPrompt; snapshot != "" {
		t.Fatalf("system prompt snapshot before first dispatch = %q, want empty", snapshot)
	}
	writeTestFile(t, systemPath, "legacy {{.EstimatedToolCallsForContext}}")
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	snapshot := store.Meta().Locked.SystemPrompt
	if snapshot != "legacy 185" {
		t.Fatalf("system prompt snapshot = %q, want legacy 185", snapshot)
	}
	writeTestFile(t, systemPath, "changed legacy")
	if got := client.calls[0].SystemPrompt; got != snapshot {
		t.Fatalf("request used changed system prompt\ngot: %q\nwant: %q", got, snapshot)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "again"); err != nil {
		t.Fatalf("submit again: %v", err)
	}
	if got := client.calls[1].SystemPrompt; got != snapshot {
		t.Fatalf("second request used changed system prompt\ngot: %q\nwant: %q", got, snapshot)
	}
	if got := store.Meta().Locked.SystemPrompt; got != snapshot {
		t.Fatalf("stored system prompt changed\ngot: %q\nwant: %q", got, snapshot)
	}
}

func TestChildSessionSnapshotsRoleSystemPromptOnFirstRequest(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	rolePrompt := filepath.Join(workspace, "code-review-system.md")
	writeTestFile(t, rolePrompt, "code review system prompt")
	toolPreambles := false
	root := t.TempDir()
	parent := mustCreateNamedTestSessionAt(t, root, "parent", workspace)
	if err := parent.MarkModelDispatchLocked(session.LockedContract{
		Model:             "locked-parent",
		EnabledTools:      []string{"shell"},
		ToolPreambles:     &toolPreambles,
		SystemPrompt:      "parent generic system prompt",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "parent reviewer prompt",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	child, err := session.NewLazy(root, "child", workspace, sessioncontract.SessionCategorySubagent, runtimeTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourceParentAgent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	if err := child.EnsureDurable(); err != nil {
		t.Fatalf("persist child: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewExecTestEngine(t, child, client, Config{
		Model:         "role-model",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
		SystemPromptFiles: []config.SystemPromptFile{
			{Path: rolePrompt, Scope: config.SystemPromptFileScopeSubagent},
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "review this"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if len(client.calls) != 1 {
		t.Fatalf("client calls = %d, want 1", len(client.calls))
	}
	if got := client.calls[0].SystemPrompt; got != "code review system prompt" {
		t.Fatalf("request system prompt = %q, want role system prompt", got)
	}
	if got := client.calls[0].Model; got != "role-model" {
		t.Fatalf("request model = %q, want role model", got)
	}
	if locked := child.Meta().Locked; locked == nil || locked.Model != "role-model" || !locked.HasSystemPrompt || locked.SystemPrompt != "code review system prompt" {
		t.Fatalf("child locked contract = %+v, want role model and prompt", locked)
	} else if locked.HasReviewerPrompt || locked.ReviewerPrompt != "" {
		t.Fatalf("child reviewer prompt lock = %+v, want no parent reviewer prompt inherited", locked)
	}
}

func TestEmptySystemPromptFileIsSkippedAndFallbackSnapshotIsReused(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	systemPath := filepath.Join(systemDir, systemPromptFileName)
	writeTestFile(t, systemPath, "   \n")

	store := mustCreateTestSession(t, workspace)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("still ok")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir: workspace,
		ToolPreambles:        false,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	firstPrompt := client.calls[0].SystemPrompt
	if strings.TrimSpace(firstPrompt) == "" || firstPrompt == "changed" {
		t.Fatalf("first system prompt = %q, want built-in fallback", firstPrompt)
	}
	if locked := store.Meta().Locked; locked == nil || !locked.HasSystemPrompt || locked.SystemPrompt != firstPrompt {
		t.Fatalf("locked system prompt snapshot = %+v, want built-in fallback snapshot", locked)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	writeTestFile(t, systemPath, "changed")
	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if locked := reopened.Meta().Locked; locked == nil || !locked.HasSystemPrompt || locked.SystemPrompt != firstPrompt {
		t.Fatalf("reopened locked system prompt snapshot = %+v, want built-in fallback snapshot", locked)
	}
	reopenedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("still ok")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reopenedEngine := mustNewExecTestEngine(t, reopened, reopenedClient, Config{
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		TranscriptWorkingDir: workspace,
		ToolPreambles:        false,
	})
	if _, err := reopenedEngine.SubmitUserMessage(context.Background(), "again"); err != nil {
		t.Fatalf("submit again: %v", err)
	}
	if got := reopenedClient.calls[0].SystemPrompt; got != firstPrompt {
		t.Fatalf("second system prompt = %q, want locked fallback snapshot %q", got, firstPrompt)
	}
	if locked := reopened.Meta().Locked; locked == nil || !locked.HasSystemPrompt || locked.SystemPrompt != firstPrompt {
		t.Fatalf("stored system prompt snapshot changed: %+v", locked)
	}
}

func TestLegacyLockedSessionBackfillsContextBudgetOnce(t *testing.T) {
	store := mustCreateTestSession(t)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
	}); err != nil {
		t.Fatalf("mark locked: %v", err)
	}

	firstEngine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:               "gpt-5",
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ContextWindowTokens: 272_000,
	})
	locked := store.Meta().Locked
	if locked == nil || locked.ContextWindow != 272_000 || locked.ContextPercent != 95 {
		t.Fatalf("expected legacy lock backfilled from first resume config, got %+v", locked)
	}
	if got := firstEngine.estimatedToolCallsForLockedContext(*locked); got != 185 {
		t.Fatalf("first estimated tool calls = %d, want 185", got)
	}

	secondEngine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:               "gpt-5",
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ContextWindowTokens: 400_000,
	})
	locked = store.Meta().Locked
	if locked == nil || locked.ContextWindow != 272_000 || locked.ContextPercent != 95 {
		t.Fatalf("expected legacy lock backfill to stay pinned, got %+v", locked)
	}
	if got := secondEngine.estimatedToolCallsForLockedContext(*locked); got != 185 {
		t.Fatalf("second estimated tool calls = %d, want 185", got)
	}
}

func TestThinkingLevelCanChangeAfterLock(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("one")}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("two")}, Usage: llm.Usage{WindowTokens: 200000}},
	}}

	eng := mustNewExecTestEngine(t, store, client, Config{
		Temperature:             1,
		ThinkingLevel:           "xhigh",
		SupportedThinkingValues: []string{"low", "xhigh"},
		EnabledTools:            []toolspec.ID{toolspec.ToolExecCommand},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if err := eng.SetThinkingLevel(t.Context(), "low"); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "again"); err != nil {
		t.Fatalf("submit second: %v", err)
	}

	if len(client.calls) != 2 {
		t.Fatalf("client calls = %d, want 2", len(client.calls))
	}
	if client.calls[0].ReasoningEffort != "xhigh" {
		t.Fatalf("first reasoning effort = %q, want xhigh", client.calls[0].ReasoningEffort)
	}
	if client.calls[1].ReasoningEffort != "low" {
		t.Fatalf("second reasoning effort = %q, want low", client.calls[1].ReasoningEffort)
	}
}

func TestRuntimeControlsRejectInvalidOrUnavailableChanges(t *testing.T) {
	eng := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{caps: llm.ProviderCapabilities{
			ProviderID:           "azure-openai",
			SupportsResponsesAPI: true,
			IsOpenAIFirstParty:   false,
		}},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:                   "gpt-5.3-codex",
			ThinkingLevel:           "high",
			SupportedThinkingValues: []string{"low", "medium", "high", "xhigh"},
			Reviewer: ReviewerConfig{
				Frequency:     "off",
				Model:         "gpt-5",
				ThinkingLevel: "low",
			},
		},
	)

	t.Run("blank thinking level", func(t *testing.T) {
		if err := eng.SetThinkingLevel(t.Context(), " "); err == nil {
			t.Fatal("expected blank thinking level error")
		}
		if got := eng.ThinkingLevel(); got != "high" {
			t.Fatalf("thinking level after blank set = %q, want high", got)
		}
	})

	t.Run("provider-specific thinking level", func(t *testing.T) {
		if err := eng.SetThinkingLevel(t.Context(), " provider-specific-depth "); err != nil {
			t.Fatalf("set provider-specific thinking level: %v", err)
		}
		if got := eng.ThinkingLevel(); got != "provider-specific-depth" {
			t.Fatalf("thinking level = %q, want provider-specific-depth", got)
		}
		meta := eng.store.Meta()
		if meta.ChatSettings == nil || meta.ChatSettings.Thinking == nil || *meta.ChatSettings.Thinking != "provider-specific-depth" {
			t.Fatalf("provider-specific Thinking override = %+v", meta.ChatSettings)
		}
	})

	t.Run("unsupported fast mode", func(t *testing.T) {
		changed, err := eng.SetFastModeEnabled(true)
		if err == nil {
			t.Fatal("expected fast mode unsupported error")
		}
		if changed {
			t.Fatal("did not expect changed=true for unsupported fast mode")
		}
		if eng.FastModeEnabled() {
			t.Fatal("did not expect fast mode enabled after failure")
		}
	})

	t.Run("missing reviewer client", func(t *testing.T) {
		changed, mode, err := eng.SetReviewerEnabled(true)
		if err == nil {
			t.Fatal("expected enable reviewer error when reviewer client is missing")
		}
		if changed {
			t.Fatal("did not expect changed=true when reviewer client is missing")
		}
		if mode != "off" {
			t.Fatalf("expected mode off on failure, got %q", mode)
		}
	})
}

func TestFastModeEnabledReportsFalseWhenProviderIsUnavailable(t *testing.T) {
	eng := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{caps: llm.ProviderCapabilities{
			ProviderID:           "azure-openai",
			SupportsResponsesAPI: true,
			IsOpenAIFirstParty:   false,
		}},
		tools.NewRegistry(),
		Config{
			Model:           "gpt-5.3-codex",
			FastModeEnabled: true,
		},
	)

	if eng.FastModeAvailable() {
		t.Fatal("expected fast mode to be unavailable for non-OpenAI provider")
	}
	if eng.FastModeEnabled() {
		t.Fatal("expected effective fast mode to be disabled when provider is unavailable")
	}
}

func TestNewRejectsUnavailableProviderCapabilities(t *testing.T) {
	store := mustCreateTestSession(t)
	capabilityErr := errors.New("provider capability lookup failed")

	_, err := New(
		store,
		mustMaterializeTestEventLog(t, store),
		&fakeClient{capsErr: capabilityErr},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	if !errors.Is(err, capabilityErr) {
		t.Fatalf("New error = %v, want provider capability error", err)
	}
}

func TestPoisonedLockedSessionFallsBackToModelReasoningSupport(t *testing.T) {
	store := mustCreateTestSession(t)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:          "gpt-5.4",
		Temperature:    1,
		MaxOutputToken: 0,
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                 "chatgpt-codex",
			SupportsResponsesAPI:       true,
			SupportsResponsesCompact:   true,
			SupportsNativeWebSearch:    true,
			SupportsReasoningEncrypted: true,
			IsOpenAIFirstParty:         true,
		},
	}); err != nil {
		t.Fatalf("mark locked: %v", err)
	}

	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")}}}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		Model:         "gpt-5.4",
		ThinkingLevel: "high",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("client calls = %d, want 1", len(client.calls))
	}
	if client.calls[0].ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want high", client.calls[0].ReasoningEffort)
	}
	if !client.calls[0].SupportsReasoningEffort {
		t.Fatal("expected request to preserve reasoning support fallback for poisoned locked session")
	}
}

func TestFastModeCanChangeAfterLock(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{
		responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("one")}, Usage: llm.Usage{WindowTokens: 200000}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("two")}, Usage: llm.Usage{WindowTokens: 200000}},
		},
		caps: llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true},
	}

	eng := mustNewExecTestEngine(t, store, client, Config{
		Model:         "gpt-5.3-codex",
		Temperature:   1,
		ThinkingLevel: "high",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	changed, err := eng.SetFastModeEnabled(true)
	if err != nil {
		t.Fatalf("set fast mode: %v", err)
	}
	if !changed {
		t.Fatal("expected fast mode change")
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "again"); err != nil {
		t.Fatalf("submit second: %v", err)
	}

	if len(client.calls) != 2 {
		t.Fatalf("client calls = %d, want 2", len(client.calls))
	}
	if client.calls[0].FastMode {
		t.Fatal("did not expect first request to enable fast mode")
	}
	if !client.calls[1].FastMode {
		t.Fatal("expected second request to enable fast mode")
	}
}

func TestSetFastModePersistsSessionSetting(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}}, Config{Model: "gpt-5.3-codex"})

	changed, err := eng.SetFastModeEnabled(true)
	if err != nil {
		t.Fatalf("enable fast mode: %v", err)
	}
	if !changed || !eng.FastModeEnabled() {
		t.Fatalf("expected fast mode enabled, changed=%v enabled=%v", changed, eng.FastModeEnabled())
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Fast == nil || !*meta.ChatSettings.Fast {
		t.Fatalf("Session Fast Mode override = %+v, want true", meta.ChatSettings)
	}
}

func TestSetAutoCompactionEnabledPersistsSessionSetting(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})

	changed, enabled, err := eng.SetAutoCompactionEnabled(t.Context(), false)
	if err != nil {
		t.Fatalf("disable auto-compaction: %v", err)
	}
	if !changed || enabled {
		t.Fatalf("expected changed=true enabled=false, got changed=%v enabled=%v", changed, enabled)
	}
	if got := eng.AutoCompactionEnabled(); got {
		t.Fatalf("expected runtime auto-compaction disabled, got %v", got)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.AutoCompaction == nil || *meta.ChatSettings.AutoCompaction {
		t.Fatalf("Session Auto-compaction override = %+v, want false", meta.ChatSettings)
	}
}

func TestSetAutoCompactionEnabledRejectsAfterClose(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})

	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	changed, enabled, err := eng.SetAutoCompactionEnabled(t.Context(), false)
	if !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("disable auto-compaction after close error = %v, want ErrEngineClosed", err)
	}
	if changed || !enabled {
		t.Fatalf("setting after close = changed %v, enabled %v; want unchanged and enabled", changed, enabled)
	}
}

func TestSetAutoCompactionDisabledDuringBusyStepAppliesImmediately(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
				ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
				Usage:     llm.Usage{InputTokens: 390000, OutputTokens: 1000, WindowTokens: 400000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
				Usage:     llm.Usage{WindowTokens: 400000},
			},
		},
		compactionResponses: []llm.CompactionResponse{
			{
				Checkpoint: llm.ResponseItem{
					Type:             llm.ResponseItemTypeCompaction,
					ID:               textutil.Value("cmp_1"),
					EncryptedContent: textutil.Value("enc_1"),
				},
				Usage: llm.Usage{InputTokens: 8000, OutputTokens: 500, WindowTokens: 400000},
			},
		},
	}

	started := make(chan struct{})
	release := make(chan struct{})
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: blockingTool{name: toolspec.ToolExecCommand, started: started, release: release}}), Config{
		Model:                 "gpt-5",
		AutoCompactTokenLimit: 350000,
	})

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "run tools")
		submitDone <- submitErr
	}()

	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for tool call to start")
	}
	type settingResult struct {
		changed bool
		enabled bool
		err     error
	}
	settingDone := make(chan settingResult, 1)
	go func() {
		changed, enabled, err := eng.SetAutoCompactionEnabled(t.Context(), false)
		settingDone <- settingResult{changed: changed, enabled: enabled, err: err}
	}()
	select {
	case result := <-settingDone:
		if result.err != nil || !result.changed || result.enabled {
			t.Fatalf("setting result = %+v, want changed and disabled", result)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("busy Agent Step blocked immediate Auto-compaction setting")
	}
	if eng.AutoCompactionEnabled() {
		t.Fatal("Auto-compaction remained enabled during busy Agent Step")
	}

	close(release)
	if err := <-submitDone; err != nil {
		t.Fatalf("submit while disabling auto-compaction: %v", err)
	}
	if got := len(client.compactionCalls); got != 0 {
		t.Fatalf("expected no compaction call for in-flight run after disabling auto-compaction, got %d", got)
	}
}

func TestSetReviewerEnabledPersistsSessionSetting(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        &fakeClient{},
		},
	})
	changed, mode, err := eng.SetReviewerEnabled(true)
	if err != nil {
		t.Fatalf("enable reviewer: %v", err)
	}
	if !changed || mode != "edits" {
		t.Fatalf("expected changed=true mode=edits, got changed=%v mode=%q", changed, mode)
	}
	if got := eng.ReviewerFrequency(); got != "edits" {
		t.Fatalf("reviewer frequency = %q, want edits", got)
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Supervisor == nil || *meta.ChatSettings.Supervisor != "edits" {
		t.Fatalf("Session Supervisor override = %+v, want edits", meta.ChatSettings)
	}
}

func TestSetReviewerEnabledLazyInitializesReviewerClient(t *testing.T) {
	dir := t.TempDir()
	store := mustCreateTestSessionAt(t, dir)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        nil,
			ClientFactory: func() (llm.Client, error) {
				return &fakeClient{}, nil
			},
		},
	})
	changed, mode, err := eng.SetReviewerEnabled(true)
	if err != nil {
		t.Fatalf("enable reviewer with lazy client init: %v", err)
	}
	if !changed || mode != "edits" {
		t.Fatalf("expected changed=true mode=edits, got changed=%v mode=%q", changed, mode)
	}
}

func TestReadSystemPromptTemplateUsesConfiguredPriorityAndSkipsEmptyFiles(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{filepath.Join(home, agentsGlobalDirName), filepath.Join(workspace, agentsGlobalDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	homeConfigPrompt := filepath.Join(home, "home-config-system.md")
	workspaceConfigPrompt := filepath.Join(workspace, "workspace-config-system.md")
	writeTestFile(t, filepath.Join(home, agentsGlobalDirName, systemPromptFileName), "home SYSTEM")
	writeTestFile(t, homeConfigPrompt, "home config")
	writeTestFile(t, filepath.Join(workspace, agentsGlobalDirName, systemPromptFileName), "workspace SYSTEM")
	writeTestFile(t, workspaceConfigPrompt, "workspace config")

	opts := systemPromptSnapshotOptions{
		WorkspaceRoot: workspace,
		SystemPromptFiles: []config.SystemPromptFile{
			{Path: homeConfigPrompt, Scope: config.SystemPromptFileScopeHomeConfig},
			{Path: workspaceConfigPrompt, Scope: config.SystemPromptFileScopeWorkspaceConfig},
		},
	}
	template, sourcePath, ok, err := readSystemPromptTemplate(opts)
	if err != nil {
		t.Fatalf("read system prompt template: %v", err)
	}
	if !ok || template != "workspace config" || sourcePath != workspaceConfigPrompt {
		t.Fatalf("template=%q sourcePath=%q ok=%t, want workspace config from %q", template, sourcePath, ok, workspaceConfigPrompt)
	}

	writeTestFile(t, workspaceConfigPrompt, " \n\t")
	template, sourcePath, ok, err = readSystemPromptTemplate(opts)
	if err != nil {
		t.Fatalf("read system prompt template after empty workspace config: %v", err)
	}
	if !ok || template != "workspace SYSTEM" {
		t.Fatalf("template=%q sourcePath=%q ok=%t, want workspace SYSTEM", template, sourcePath, ok)
	}

	writeTestFile(t, filepath.Join(workspace, agentsGlobalDirName, systemPromptFileName), "\n")
	template, sourcePath, ok, err = readSystemPromptTemplate(opts)
	if err != nil {
		t.Fatalf("read system prompt template after empty workspace SYSTEM: %v", err)
	}
	if !ok || template != "home config" || sourcePath != homeConfigPrompt {
		t.Fatalf("template=%q sourcePath=%q ok=%t, want home config from %q", template, sourcePath, ok, homeConfigPrompt)
	}

	writeTestFile(t, homeConfigPrompt, " ")
	template, sourcePath, ok, err = readSystemPromptTemplate(opts)
	if err != nil {
		t.Fatalf("read system prompt template after empty home config: %v", err)
	}
	if !ok || template != "home SYSTEM" {
		t.Fatalf("template=%q sourcePath=%q ok=%t, want home SYSTEM", template, sourcePath, ok)
	}

	writeTestFile(t, filepath.Join(home, agentsGlobalDirName, systemPromptFileName), "\n")
	template, sourcePath, ok, err = readSystemPromptTemplate(opts)
	if err != nil {
		t.Fatalf("read system prompt template after all files empty: %v", err)
	}
	if ok || template != "" || sourcePath != "" {
		t.Fatalf("template=%q sourcePath=%q ok=%t, want built-in fallback marker", template, sourcePath, ok)
	}
}

func TestReadSystemPromptTemplateSubagentConfigOverridesWorkspaceConfig(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	subagentPrompt := filepath.Join(home, "subagent-system.md")
	workspaceConfigPrompt := filepath.Join(workspace, "workspace-config-system.md")
	writeTestFile(t, subagentPrompt, "subagent")
	writeTestFile(t, workspaceConfigPrompt, "workspace config")

	template, sourcePath, ok, err := readSystemPromptTemplate(systemPromptSnapshotOptions{
		WorkspaceRoot: workspace,
		SystemPromptFiles: []config.SystemPromptFile{
			{Path: workspaceConfigPrompt, Scope: config.SystemPromptFileScopeWorkspaceConfig},
			{Path: subagentPrompt, Scope: config.SystemPromptFileScopeSubagent},
		},
	})
	if err != nil {
		t.Fatalf("read system prompt template: %v", err)
	}
	if !ok || template != "subagent" || sourcePath != subagentPrompt {
		t.Fatalf("template=%q sourcePath=%q ok=%t, want subagent from %q", template, sourcePath, ok, subagentPrompt)
	}
}
