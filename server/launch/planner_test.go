package launch

import (
	"context"
	"core/server/auth"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingUpdateMetadataExecutionTargetStore struct {
	base             *metadata.Store
	updateErr        error
	updatedSessionID string
}

func (s *failingUpdateMetadataExecutionTargetStore) ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	return s.base.ResolveSessionExecutionTarget(ctx, sessionID)
}

func (s *failingUpdateMetadataExecutionTargetStore) UpdateSessionExecutionTarget(_ context.Context, update metadata.SessionExecutionTargetUpdate) error {
	s.updatedSessionID = update.SessionID
	return s.updateErr
}

func (s *failingUpdateMetadataExecutionTargetStore) DeleteSessionRecordByID(ctx context.Context, sessionID string) error {
	return s.base.DeleteSessionRecordByID(ctx, sessionID)
}

func (s *failingUpdateMetadataExecutionTargetStore) Close() error {
	return nil
}

func TestPlannerHeadlessCreatesNewSessionAndAppliesContinuationContext(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
		Settings: config.Settings{
			OpenAIBaseURL: "http://headless.local/v1",
		},
	}, containerDir, persistence.Options()...)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeHeadless, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	meta := testStoreForPlannerPlan(t, planner, plan).Meta()
	if meta.SessionID == "" {
		t.Fatal("expected session id")
	}
	if !strings.HasSuffix(meta.Name, " "+SubagentSessionSuffix) {
		t.Fatalf("expected subagent session name, got %q", meta.Name)
	}
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL == nil || *meta.Continuation.OpenAIBaseURL != "http://headless.local/v1" {
		t.Fatalf("expected continuation base url applied, got %+v", meta.Continuation)
	}
	if plan.SessionName == nil || *plan.SessionName != meta.Name {
		t.Fatalf("expected plan session name %q, got %v", meta.Name, plan.SessionName)
	}
	if plan.WorkspaceRoot != "/tmp/workspace-a" {
		t.Fatalf("expected workspace root passthrough, got %q", plan.WorkspaceRoot)
	}
}

func TestPlannerInteractiveRequiresExplicitOpenOrCreateIntent(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a")
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
		Settings:        config.Settings{},
	}, containerDir)

	_, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive})
	if err == nil || !errors.Is(err, errSessionLaunchIntentRequired) {
		t.Fatalf("PlanSession error = %v, want explicit intent required", err)
	}
}

func TestPlannerReappliesPersistedSubagentRoleSettingsOnResume(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("smart_reviewer")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	loaded := loadLaunchConfig(t, workspace)
	settings := loaded.Settings
	settings.ThinkingLevel = "medium"
	settings.EnabledTools = map[toolspec.ID]bool{
		toolspec.ToolExecCommand: true,
		toolspec.ToolPatch:       true,
	}
	roleSettings := settings
	roleSettings.ThinkingLevel = "xhigh"
	roleSettings.EnabledTools = map[toolspec.ID]bool{
		toolspec.ToolExecCommand: true,
		toolspec.ToolPatch:       false,
	}
	settings.Subagents = map[string]config.SubagentRole{
		"smart_reviewer": {
			Settings: roleSettings,
			Sources:  map[string]string{"thinking_level": "file", "tools.patch": "file"},
		},
	}
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        settings,
		Source:          loaded.Source,
	}, containerDir, persistence.Options()...)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, store.Meta().SessionID))})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.ThinkingLevel != "xhigh" {
		t.Fatalf("thinking level = %q, want persisted subagent role xhigh", plan.ActiveSettings.ThinkingLevel)
	}
	if plan.ActiveSettings.EnabledTools[toolspec.ToolPatch] {
		t.Fatalf("patch tool should be disabled by persisted role: %+v", plan.ActiveSettings.EnabledTools)
	}
	if plan.Source.Sources["thinking_level"] != "subagent" || plan.Source.Sources["tools.patch"] != "subagent" {
		t.Fatalf("source report did not mark role overrides as subagent: %+v", plan.Source.Sources)
	}
	if got := plan.Continuation; got == nil || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("smart_reviewer")) {
		t.Fatalf("continuation = %+v, want smart_reviewer preserved", got)
	}
}

func TestResumedSessionUsesActiveProviderIdentifierWithoutPersistingIt(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	persistence := sessiontest.NewPersistence()
	containerDir := filepath.Join(t.TempDir(), "projects", testProjectID, "sessions")
	store := createTestSessionInContainer(t, containerDir, testWorkspaceContainer, workspace, persistence.Options()...)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:           loaded.Settings.Model,
		EnabledTools:    []string{string(toolspec.ToolExecCommand)},
		HasEnabledTools: true,
		WebSearchMode:   loaded.Settings.WebSearch,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}

	reopened, err := session.Open(store.Dir(), persistence.Options()...)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	loaded.Settings.ProviderIdentifier = "restarted-agent"
	plan, err := ResolvePromptFacingSnapshotPlan(loaded, reopened, false)
	if err != nil {
		t.Fatalf("ResolvePromptFacingSnapshotPlan: %v", err)
	}
	if plan.ActiveSettings.ProviderIdentifier != "restarted-agent" {
		t.Fatalf("provider identifier = %q, want restarted-agent", plan.ActiveSettings.ProviderIdentifier)
	}

	encoded, err := json.Marshal(reopened.Meta().Locked)
	if err != nil {
		t.Fatalf("marshal locked contract: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("decode locked contract: %v", err)
	}
	if _, exists := persisted["provider_identifier"]; exists {
		t.Fatalf("locked contract persisted provider_identifier: %+v", persisted)
	}
}

func TestResolvePromptFacingSnapshotPlanIncludesEffectiveSessionChatSettings(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	persistence := sessiontest.NewPersistence()
	containerDir := filepath.Join(t.TempDir(), "projects", testProjectID, "sessions")
	store := createTestSessionInContainer(t, containerDir, testWorkspaceContainer, workspace, persistence.Options()...)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Supervisor:     textutil.Value("all"),
		Thinking:       textutil.Value("high"),
		Fast:           textutil.Value(true),
		Questions:      textutil.Value(true),
		AutoCompaction: textutil.Value(true),
	}); err != nil {
		t.Fatalf("MutateChatSettings: %v", err)
	}

	plan, err := ResolvePromptFacingSnapshotPlan(loaded, store, false)
	if err != nil {
		t.Fatalf("ResolvePromptFacingSnapshotPlan: %v", err)
	}
	if plan.ActiveSettings.Reviewer.Frequency != "all" ||
		plan.ActiveSettings.ThinkingLevel != "high" ||
		!plan.ActiveSettings.PriorityRequestMode ||
		!plan.QuestionsEnabled ||
		!plan.AutoCompactionEnabled {
		t.Fatalf("snapshot effective Chat settings = %+v questions=%t auto_compaction=%t", plan.ActiveSettings, plan.QuestionsEnabled, plan.AutoCompactionEnabled)
	}
}

func TestPlannerIgnoresMissingPersistedSubagentRoleOnResume(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("deleted_role")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings: config.Settings{
			Model:         "gpt-5.6-sol",
			ThinkingLevel: "medium",
		},
	}, containerDir, persistence.Options()...)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, store.Meta().SessionID))})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.ThinkingLevel != "medium" {
		t.Fatalf("thinking level = %q, want base config when role is missing", plan.ActiveSettings.ThinkingLevel)
	}
	if got := plan.Continuation; got == nil || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("deleted_role")) {
		t.Fatalf("continuation = %+v, want missing role preserved", got)
	}
}

func TestPlannerKeepsRoleBaseURLOutOfBaseSettingsOnResume(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := store.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: textutil.Value("https://worker.example/v1"),
		AgentRole:     sessiontest.AgentRole("worker"),
	}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	settings := loaded.Settings
	settings.OpenAIBaseURL = "https://base.example/v1"
	workerSettings := cloneSettings(settings)
	workerSettings.OpenAIBaseURL = "https://worker.example/v1"
	researchSettings := cloneSettings(settings)
	researchSettings.ThinkingLevel = "high"
	settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: workerSettings,
			Sources:  map[string]string{"openai_base_url": "file"},
		},
		"research": {
			Settings: researchSettings,
			Sources:  map[string]string{"thinking_level": "file"},
		},
	}
	source := loaded.Source
	source.Sources = cloneMapOrEmpty(loaded.Source.Sources)
	source.Sources["openai_base_url"] = "file"
	source.Sources["thinking_level"] = "file"
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        settings,
		Source:          source,
	}, containerDir, persistence.Options()...)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, store.Meta().SessionID))})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.OpenAIBaseURL != "https://worker.example/v1" {
		t.Fatalf("active base url = %q, want worker", plan.ActiveSettings.OpenAIBaseURL)
	}
	if plan.BaseSettings.OpenAIBaseURL != "https://base.example/v1" {
		t.Fatalf("base settings url = %q, want base", plan.BaseSettings.OpenAIBaseURL)
	}

	cleared, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(config.DefaultSubagentRole)}, auth.EmptyState())
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides clear: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if cleared.ActiveSettings.OpenAIBaseURL != "https://base.example/v1" {
		t.Fatalf("cleared base url = %q, want base", cleared.ActiveSettings.OpenAIBaseURL)
	}
	if got := cleared.Continuation; got != nil && got.AgentRole != nil {
		t.Fatalf("continuation after clear = %+v, want no role", got)
	}

	if err := testStoreForPlan(t, plan).SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: textutil.Value("https://worker.example/v1"), AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("reset continuation: %v", err)
	}
	switched, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("research")}, auth.EmptyState())
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides switch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected switch warnings: %+v", warnings)
	}
	if switched.ActiveSettings.OpenAIBaseURL != "https://base.example/v1" {
		t.Fatalf("switched base url = %q, want base", switched.ActiveSettings.OpenAIBaseURL)
	}
}

func TestApplyRunPromptOverridesDefaultCannotClearLockedRoleAfterSkippingPersistedRoleLookup(t *testing.T) {
	workspace := t.TempDir()
	settings := config.Settings{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: "medium",
		EnabledTools:  map[toolspec.ID]bool{toolspec.ToolExecCommand: true},
	}
	plan := newLockedRoleOverridePlan(t, workspace, settings, config.SourceReport{}, sessiontest.AgentRole("worker"), session.LockedContract{
		Model:        settings.Model,
		EnabledTools: []string{"shell"},
	})
	plan.SkipContinuationAgentRoleValidation = true

	_, _, err := ApplyRunPromptOverrides(
		plan,
		serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(config.DefaultSubagentRole)},
		auth.EmptyState(),
	)
	if !errors.Is(err, ErrLockedAgentRoleChange) {
		t.Fatalf("default locked-role clear error = %v, want %v", err, ErrLockedAgentRoleChange)
	}
	if got := plan.Continuation; got == nil || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("worker")) {
		t.Fatalf("locked continuation changed after rejected clear: %+v", got)
	}
}

func TestApplyRunPromptOverridesRejectsDifferentAgentRoleForLockedSession(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"[subagents.old_role]",
		"model = \"gpt-5.6-sol\"",
		"",
		"[subagents.worker]",
		"model = \"gpt-5.4-mini\"",
	)

	tests := []struct {
		name      string
		persisted *string
		override  string
	}{
		{
			name:      "different role",
			persisted: sessiontest.AgentRole("old_role"),
			override:  "worker",
		},
		{
			name:      "default clears role",
			persisted: sessiontest.AgentRole("old_role"),
			override:  config.DefaultSubagentRole,
		},
		{
			name:     "base session gains role",
			override: "worker",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := newLockedRoleOverridePlan(t, workspace, loaded.Settings, loaded.Source, tt.persisted, session.LockedContract{
				Model:        "locked-model",
				EnabledTools: []string{"shell"},
			})
			_, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(tt.override)}, auth.EmptyState())
			if !errors.Is(err, ErrLockedAgentRoleChange) {
				t.Fatalf("ApplyRunPromptOverrides error = %v, want locked role change", err)
			}
		})
	}
}

func TestApplyRunPromptOverridesAllowsSameAgentRoleForLockedSession(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"[subagents.worker]",
		"model = \"gpt-5.4-mini\"",
	)
	loaded.Settings.ProviderOverride = "openai"
	plan := newLockedRoleOverridePlan(t, workspace, loaded.Settings, loaded.Source, sessiontest.AgentRole("worker"), session.LockedContract{
		Model:        "locked-model",
		EnabledTools: []string{"shell"},
	})
	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("worker")}, auth.EmptyState())
	if got := updated.Continuation; got == nil || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("worker")) {
		t.Fatalf("continuation = %+v, want worker", got)
	}
	if updated.ActiveSettings.Model != "locked-model" {
		t.Fatalf("model = %q, want locked-model", updated.ActiveSettings.Model)
	}
}

func TestApplyRunPromptOverridesOptionAllowsAgentRoleChangeForLockedSession(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"[subagents.worker]",
		"model = \"gpt-5.4-mini\"",
	)
	loaded.Settings.ProviderOverride = "openai"
	workerRole := loaded.Settings.Subagents["worker"]
	workerRole.Settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolEdit: true}
	workerRole.Sources = map[string]string{
		"model":       "file",
		"tools.shell": "file",
		"tools.patch": "file",
		"tools.edit":  "file",
	}
	loaded.Settings.Subagents["worker"] = workerRole
	plan := newLockedRoleOverridePlan(t, workspace, loaded.Settings, loaded.Source, sessiontest.AgentRole("old_role"), session.LockedContract{
		Model:           "locked-model",
		EnabledTools:    []string{"shell"},
		HasEnabledTools: true,
	})
	updated, _, err := ApplyRunPromptOverridesWithOptions(plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("worker")}, auth.EmptyState(), RunPromptOverrideOptions{
		AllowLockedAgentRoleChange: true,
	})
	if err != nil {
		t.Fatalf("ApplyRunPromptOverridesWithOptions: %v", err)
	}
	if got := updated.Continuation; got == nil || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("worker")) {
		t.Fatalf("continuation = %+v, want worker", got)
	}
	if updated.ActiveSettings.Model != "locked-model" {
		t.Fatalf("model = %q, want locked-model", updated.ActiveSettings.Model)
	}
	if containsTool(updated.EnabledTools, toolspec.ToolExecCommand) || !containsTool(updated.EnabledTools, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want recomputed role tools without old locked shell", updated.EnabledTools)
	}
}

func TestApplyRunPromptOverridesLockedModelDoesNotMarkModelSourceAsSubagent(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	baseSettings := loaded.Settings
	baseSettings.Model = "locked-model"
	baseSettings.ProviderOverride = "openai"
	workerSettings := cloneSettings(baseSettings)
	workerSettings.Model = "gpt-5.4-mini"
	workerSettings.ThinkingLevel = "high"
	baseSettings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: workerSettings,
			Sources:  map[string]string{"model": "file", "thinking_level": "file"},
		},
	}
	baseSource := loaded.Source
	baseSource.Sources = cloneMapOrEmpty(loaded.Source.Sources)
	baseSource.Sources["model"] = "file"
	baseSource.Sources["thinking_level"] = "file"
	plan := newLockedRoleOverridePlan(t, workspace, baseSettings, baseSource, sessiontest.AgentRole("worker"), session.LockedContract{
		Model:        "locked-model",
		EnabledTools: []string{"shell"},
	})
	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("worker")}, auth.EmptyState())
	if updated.ActiveSettings.Model != "locked-model" {
		t.Fatalf("model = %q, want locked-model", updated.ActiveSettings.Model)
	}
	if updated.Source.Sources["model"] != "file" {
		t.Fatalf("model source = %q, want original file source under lock", updated.Source.Sources["model"])
	}
	if updated.Source.Sources["thinking_level"] != "subagent" {
		t.Fatalf("thinking source = %q, want subagent", updated.Source.Sources["thinking_level"])
	}
}

func newLockedRoleOverridePlan(t *testing.T, workspace string, settings config.Settings, source config.SourceReport, persistedRole *string, locked session.LockedContract) SessionPlan {
	t.Helper()
	store := createTestSession(t, workspace)
	if persistedRole != nil {
		if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: persistedRole}); err != nil {
			t.Fatalf("SetContinuationContext: %v", err)
		}
	}
	if err := store.MarkModelDispatchLocked(locked); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	effective := EffectiveSettings(settings, store.Meta().Locked)
	return sessionPlanWithSnapshot(SessionPlan{
		ActiveSettings:      effective,
		BaseSettings:        effective,
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: locked.Model,
		WorkspaceRoot:       workspace,
		Source:              source,
		BaseSource:          source,
		ModelContractLocked: true,
	}, store, filepath.Dir(store.Dir()))
}

func TestPlannerNewChildSessionPreservesParentWorktreeContext(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	cfg := loadLaunchConfig(t, workspace)
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	siblingWorkspace := t.TempDir()
	canonicalSiblingWorkspace, err := config.CanonicalWorkspaceRoot(siblingWorkspace)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot sibling: %v", err)
	}
	if _, err := metadataStore.AttachWorkspaceToProject(ctx, binding.ProjectID, canonicalSiblingWorkspace); err != nil {
		t.Fatalf("AttachWorkspaceToProject sibling: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	parent := createTestSessionInContainer(t, containerDir, filepath.Base(containerDir), cfg.WorkspaceRoot, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: textutil.Value("http://parent.local/v1")}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	if err := parent.MarkModelDispatchLocked(session.LockedContract{
		Model:             "locked-parent-model",
		EnabledTools:      []string{"shell"},
		SystemPrompt:      "parent interactive system prompt",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "parent interactive reviewer prompt",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	worktreeRoot := filepath.Join(cfg.WorkspaceRoot, "wt-review")
	if err := os.MkdirAll(filepath.Join(worktreeRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              "worktree-review",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   canonicalWorktreeRoot,
		DisplayName:     filepath.Base(canonicalWorktreeRoot),
		Availability:    "available",
		Managed:         true,
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{SessionID: parent.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "worktree-review"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/review"),
			WorktreePath:  canonicalWorktreeRoot,
			WorkspaceRoot: cfg.WorkspaceRoot,
			EffectiveCwd:  filepath.Join(canonicalWorktreeRoot, "pkg"),
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	planner := Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		StoreOptions:             metadataStore.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        metadataStore,
		ProjectWorkspaceBoundary: metadataStore,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeInteractive,
		Intent: createNewTypedIntentWithPreviousSession(t, parent.Meta().SessionID),
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	childMeta := testStoreForPlannerPlan(t, planner, plan).Meta()
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if childMeta.PreviousSessionID == nil || *childMeta.PreviousSessionID != parentID {
		t.Fatalf("child previous session id = %v, want %q", childMeta.PreviousSessionID, parent.Meta().SessionID)
	}
	if childMeta.Locked == nil || childMeta.Locked.Model != "locked-parent-model" {
		t.Fatalf("child locked contract = %+v, want parent model lock", childMeta.Locked)
	}
	if childMeta.Locked.SystemPrompt != "parent interactive system prompt" || !childMeta.Locked.HasSystemPrompt {
		t.Fatalf("child system prompt lock = %+v, want parent interactive prompt", childMeta.Locked)
	}
	if childMeta.Locked.ReviewerPrompt != "parent interactive reviewer prompt" || !childMeta.Locked.HasReviewerPrompt {
		t.Fatalf("child reviewer prompt lock = %+v, want parent interactive reviewer prompt", childMeta.Locked)
	}
	if childMeta.Continuation == nil || childMeta.Continuation.OpenAIBaseURL == nil || *childMeta.Continuation.OpenAIBaseURL != "http://parent.local/v1" {
		t.Fatalf("child continuation = %+v, want parent continuation", childMeta.Continuation)
	}
	if plan.ActiveSettings.OpenAIBaseURL != "http://parent.local/v1" {
		t.Fatalf("plan openai base url = %q, want parent continuation", plan.ActiveSettings.OpenAIBaseURL)
	}
	if plan.ActiveSettings.Model != "locked-parent-model" {
		t.Fatalf("plan model = %q, want locked-parent-model", plan.ActiveSettings.Model)
	}
	if childMeta.WorktreeReminder == nil {
		t.Fatal("expected child worktree reminder")
	}
	if childMeta.WorktreeReminder.Branch == nil ||
		*childMeta.WorktreeReminder.Branch != "feature/review" ||
		childMeta.WorktreeReminder.WorktreePath != canonicalWorktreeRoot {
		t.Fatalf("child worktree reminder = %+v", childMeta.WorktreeReminder)
	}
	target, err := metadataStore.ResolveSessionExecutionTarget(ctx, childMeta.SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget child: %v", err)
	}
	if target.Worktree == nil || target.Worktree.ID != "worktree-review" {
		t.Fatalf("child worktree = %+v, want worktree-review", target.Worktree)
	}
	if target.CwdRelpath != "pkg" {
		t.Fatalf("child cwd relpath = %q, want pkg", target.CwdRelpath)
	}
	if target.EffectiveWorkdir != filepath.Join(canonicalWorktreeRoot, "pkg") {
		t.Fatalf("child effective workdir = %q, want %q", target.EffectiveWorkdir, filepath.Join(canonicalWorktreeRoot, "pkg"))
	}
	if !clientui.SessionExecutionTargetsEqual(plan.ExecutionTarget, target) {
		t.Fatalf("new child plan execution target = %+v, want %+v", plan.ExecutionTarget, target)
	}
	foundSibling := false
	for _, workspace := range plan.ProjectWorkspaceBoundary.Workspaces {
		if workspace.CanonicalRoot == canonicalSiblingWorkspace {
			foundSibling = true
			break
		}
	}
	if !foundSibling {
		t.Fatalf("interactive plan boundary = %+v, want sibling Workspace %q", plan.ProjectWorkspaceBoundary, canonicalSiblingWorkspace)
	}
	reopenedPlan, err := planner.PlanSession(ctx, SessionRequest{
		Mode:   ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, childMeta.SessionID)),
	})
	if err != nil {
		t.Fatalf("PlanSession existing child: %v", err)
	}
	if !clientui.SessionExecutionTargetsEqual(reopenedPlan.ExecutionTarget, target) {
		t.Fatalf("existing child plan execution target = %+v, want %+v", reopenedPlan.ExecutionTarget, target)
	}
}

func TestPlannerHeadlessChildWithRoleUsesFreshSystemPromptSnapshot(t *testing.T) {
	workspace := t.TempDir()
	cfg := loadLaunchConfig(t, workspace)
	rolePrompt := filepath.Join(workspace, "code-review-system.md")
	if err := os.WriteFile(rolePrompt, []byte("code review system prompt"), 0o644); err != nil {
		t.Fatalf("write role prompt: %v", err)
	}
	cfg.Settings.Subagents = map[string]config.SubagentRole{
		"code_review": {
			Settings: config.Settings{
				Model:             "gpt-5.4-mini",
				SystemPromptFile:  rolePrompt,
				SystemPromptFiles: []config.SystemPromptFile{{Path: rolePrompt, Scope: config.SystemPromptFileScopeSubagent}},
				EnabledTools: map[toolspec.ID]bool{
					toolspec.ToolExecCommand: true,
					toolspec.ToolPatch:       false,
					toolspec.ToolEdit:        true,
				},
			},
			Sources: map[string]string{
				"model":              "file",
				"system_prompt_file": "file",
				"tools.patch":        "file",
				"tools.edit":         "file",
			},
		},
	}
	containerDir := filepath.Join(cfg.PersistenceRoot, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	parent := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := parent.MarkModelDispatchLocked(session.LockedContract{
		Model:           "locked-parent-model",
		EnabledTools:    []string{"shell"},
		SystemPrompt:    "parent generic system prompt",
		HasSystemPrompt: true,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: textutil.Value("https://parent.example/v1"),
		AgentRole:     sessiontest.AgentRole("old_parent_role"),
	}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	planner := newPersistenceBackedTestPlanner(cfg, containerDir, persistence)
	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustTypedIntentSessionID(t, parent.Meta().SessionID))),
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}

	childStore := testStoreForPlannerPlan(t, planner, plan)
	updated, warnings, err := planner.ApplyRunPromptOverridesWithStore(
		plan,
		childStore,
		serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("code_review")},
		auth.EmptyState(),
		RunPromptOverrideOptions{},
	)
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if childLocked := updated.Locked; childLocked != nil {
		t.Fatalf("child lock = %+v, want headless child to use its own role contract", childLocked)
	}
	if updated.ActiveSettings.Model != "gpt-5.4-mini" {
		t.Fatalf("active model = %q, want role model", updated.ActiveSettings.Model)
	}
	if updated.ActiveSettings.OpenAIBaseURL == "https://parent.example/v1" {
		t.Fatalf("active base url inherited parent continuation, want headless child role/base config")
	}
	if containsTool(updated.EnabledTools, toolspec.ToolPatch) || !containsTool(updated.EnabledTools, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want role tools", updated.EnabledTools)
	}
	if len(updated.ActiveSettings.SystemPromptFiles) != 1 || updated.ActiveSettings.SystemPromptFiles[0].Path != rolePrompt {
		t.Fatalf("active system prompt files = %+v, want role prompt %q", updated.ActiveSettings.SystemPromptFiles, rolePrompt)
	}
	if got := updated.Continuation; got == nil || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("code_review")) {
		t.Fatalf("child continuation = %+v, want only selected role persisted", got)
	}
}

func TestPlannerNewChildSessionFallsBackWhenParentExecutionTargetIsNotMetadataBacked(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	parent := createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a", persistence.Options()...)
	if err := parent.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeEnter,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch("feature/file-backed"),
			WorktreePath:  "/tmp/worktree-a",
			WorkspaceRoot: "/tmp/workspace-a",
			EffectiveCwd:  "/tmp/worktree-a/pkg",
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	planner := newPersistenceBackedTestPlanner(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
	}, containerDir, persistence)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeInteractive,
		Intent: createNewTypedIntentWithPreviousSession(t, parent.Meta().SessionID),
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	childMeta := testStoreForPlannerPlan(t, planner, plan).Meta()
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if childMeta.PreviousSessionID == nil || *childMeta.PreviousSessionID != parentID {
		t.Fatalf("previous session id = %v, want %q", childMeta.PreviousSessionID, parent.Meta().SessionID)
	}
	if childMeta.WorktreeReminder == nil ||
		childMeta.WorktreeReminder.Branch == nil ||
		*childMeta.WorktreeReminder.Branch != "feature/file-backed" {
		t.Fatalf("worktree reminder = %+v, want parent reminder copied", childMeta.WorktreeReminder)
	}
}

func TestPlannerNewChildSessionResolvesPreviousSessionAcrossProjectContainers(t *testing.T) {
	root := t.TempDir()
	containerA := filepath.Join(root, "projects", "project-a", "sessions")
	containerB := filepath.Join(root, "projects", "project-b", "sessions")
	persistence := sessiontest.NewPersistence()
	parent := createTestSessionInContainer(t, containerB, "workspace-b", "/tmp/workspace-b", persistence.Options()...)
	if err := parent.MarkModelDispatchLocked(session.LockedContract{Model: "foreign-parent-model"}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: textutil.Value("http://foreign.local/v1")}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	planner := newPersistenceBackedTestPlanner(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
	}, containerA, persistence)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeInteractive,
		Intent: createNewTypedIntentWithPreviousSession(t, parent.Meta().SessionID),
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	childMeta := testStoreForPlannerPlan(t, planner, plan).Meta()
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if childMeta.PreviousSessionID == nil || *childMeta.PreviousSessionID != parentID {
		t.Fatalf("previous session id = %v, want %q", childMeta.PreviousSessionID, parent.Meta().SessionID)
	}
	if childMeta.WorkspaceRoot != "/tmp/workspace-b" || childMeta.WorkspaceContainer != "workspace-b" {
		t.Fatalf("child workspace context = root %q container %q, want source session", childMeta.WorkspaceRoot, childMeta.WorkspaceContainer)
	}
	if childMeta.Locked == nil || childMeta.Locked.Model != "foreign-parent-model" {
		t.Fatalf("locked contract = %+v, want source session lock copied", childMeta.Locked)
	}
	if childMeta.Continuation == nil || childMeta.Continuation.OpenAIBaseURL == nil || *childMeta.Continuation.OpenAIBaseURL != "http://foreign.local/v1" {
		t.Fatalf("continuation = %+v, want source session continuation copied", childMeta.Continuation)
	}
}

func TestPlannerNewChildSessionRollsBackDurableChildWhenExecutionTargetCopyFails(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	cfg := loadLaunchConfig(t, workspace)
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	parent := createTestSessionInContainer(t, containerDir, filepath.Base(containerDir), cfg.WorkspaceRoot, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	worktreeRoot := filepath.Join(cfg.WorkspaceRoot, "wt-review")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:              "worktree-review",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   canonicalWorktreeRoot,
		DisplayName:     filepath.Base(canonicalWorktreeRoot),
		Availability:    "available",
		Managed:         true,
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{SessionID: parent.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "worktree-review"}, CwdRelpath: "."}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget parent: %v", err)
	}
	beforeEntries, err := os.ReadDir(containerDir)
	if err != nil {
		t.Fatalf("read container before plan: %v", err)
	}
	failingStore := &failingUpdateMetadataExecutionTargetStore{base: metadataStore, updateErr: session.ErrSessionNotFound}
	planner := Planner{
		Config:              cfg,
		ContainerDir:        containerDir,
		StoreOptions:        metadataStore.AuthoritativeSessionStoreOptions(),
		PersistedSessions:   metadataStore,
		MetadataStoreOpener: func(string) (MetadataExecutionTargetStore, error) { return failingStore, nil },
	}

	_, err = planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeInteractive,
		Intent: createNewTypedIntentWithPreviousSession(t, parent.Meta().SessionID),
	})
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("PlanSession error = %v, want session not found from metadata target update", err)
	}
	if strings.TrimSpace(failingStore.updatedSessionID) == "" {
		t.Fatal("expected child execution target update to be attempted")
	}
	if _, err := metadataStore.ResolveSessionExecutionTarget(ctx, failingStore.updatedSessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ResolveSessionExecutionTarget child after rollback error = %v, want sql.ErrNoRows", err)
	}
	if _, err := os.Stat(filepath.Join(containerDir, failingStore.updatedSessionID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child session dir stat after rollback error = %v, want not exist", err)
	}
	afterEntries, err := os.ReadDir(containerDir)
	if err != nil {
		t.Fatalf("read container after plan: %v", err)
	}
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("session dirs after failed plan = %d, want %d", len(afterEntries), len(beforeEntries))
	}
	if len(afterEntries) != 1 || afterEntries[0].Name() != parent.Meta().SessionID {
		t.Fatalf("unexpected remaining session dirs after rollback: %+v", afterEntries)
	}
}

func TestPlannerNewSessionHonorsCanceledContextBeforeDurableCreation(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
		},
		ContainerDir: containerDir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planner.PlanSession(ctx, SessionRequest{
		Mode:   ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PlanSession error = %v, want context canceled", err)
	}
	if _, err := os.Stat(containerDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("container stat error = %v, want not exist", err)
	}
}

func TestPlannerNewChildSessionHonorsCanceledContextBeforeParentCopy(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	parent := createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a")
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
		},
		ContainerDir:      containerDir,
		PersistedSessions: sessiontest.NewPersistence(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planner.PlanSession(ctx, SessionRequest{
		Mode:   ModeInteractive,
		Intent: createNewTypedIntentWithPreviousSession(t, parent.Meta().SessionID),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PlanSession error = %v, want context canceled", err)
	}
}

func TestApplyRunPromptOverridesOverridesHeadlessSettingsWithoutMutatingBasePlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	settings := loaded.Settings
	settings.Model = "base-model"
	settings.ThinkingLevel = "low"
	settings.Theme = "dark"
	settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	settings.Timeouts = config.Timeouts{ModelRequestSeconds: 100}
	loaded.Settings = settings
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{
		Model:               "gpt-5-mini",
		ThinkingLevel:       "medium",
		Theme:               "light",
		ModelTimeoutSeconds: 12,
		Tools:               "shell,patch",
		OpenAIBaseURL:       "http://override.local/v1",
	}, auth.EmptyState())
	if updated.ActiveSettings.Model != "gpt-5-mini" {
		t.Fatalf("model = %q, want gpt-5-mini", updated.ActiveSettings.Model)
	}
	if updated.ConfiguredModelName != "gpt-5-mini" {
		t.Fatalf("configured model = %q, want gpt-5-mini", updated.ConfiguredModelName)
	}
	if updated.ActiveSettings.ThinkingLevel != "medium" {
		t.Fatalf("thinking level = %q, want medium", updated.ActiveSettings.ThinkingLevel)
	}
	if updated.ActiveSettings.Theme != "light" {
		t.Fatalf("theme = %q, want light", updated.ActiveSettings.Theme)
	}
	if updated.ActiveSettings.Timeouts.ModelRequestSeconds != 12 {
		t.Fatalf("timeouts = %+v, want model_request_seconds=12", updated.ActiveSettings.Timeouts)
	}
	if len(updated.EnabledTools) != 2 || updated.EnabledTools[0] != toolspec.ToolExecCommand || updated.EnabledTools[1] != toolspec.ToolPatch {
		t.Fatalf("enabled tools = %+v, want patch+shell", updated.EnabledTools)
	}
	if updated.ActiveSettings.OpenAIBaseURL != "http://override.local/v1" {
		t.Fatalf("openai base url = %q, want http://override.local/v1", updated.ActiveSettings.OpenAIBaseURL)
	}
	if got := updated.Continuation; got == nil || got.OpenAIBaseURL == nil || *got.OpenAIBaseURL != "http://override.local/v1" {
		t.Fatalf("continuation = %+v, want override url", got)
	}
	if plan.ActiveSettings.Model != "base-model" {
		t.Fatalf("base plan mutated: %+v", plan.ActiveSettings)
	}
}

func TestApplyRunPromptOverridesPreservesExplicitThinkingOverSessionSetting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	store := testStoreForPlan(t, plan)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Thinking: textutil.Value("low"),
	}); err != nil {
		t.Fatalf("persist Session Thinking: %v", err)
	}

	updated, warnings, err := (Planner{ContainerDir: filepath.Dir(store.Dir())}).ApplyRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{ThinkingLevel: "high"},
		auth.EmptyState(),
		RunPromptOverrideOptions{},
	)
	if err != nil {
		t.Fatalf("ApplyRunPromptOverridesWithStore: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if updated.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want explicit override high", updated.ActiveSettings.ThinkingLevel)
	}
	if !updated.ThinkingOverrideExplicit {
		t.Fatal("explicit Thinking override marker = false, want true")
	}
}

func TestApplyRunPromptOverridesRejectsPersistedThinkingUnsupportedByModelOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	loaded.Settings.Model = "gpt-5.6-sol"
	loaded.Settings.ThinkingLevel = "high"
	plan := newLoadedConfigPlan(t, workspace, loaded)
	store := testStoreForPlan(t, plan)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Thinking: textutil.Value("ultra"),
	}); err != nil {
		t.Fatalf("persist Session Thinking: %v", err)
	}

	_, _, err := (Planner{ContainerDir: filepath.Dir(store.Dir())}).ApplyRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{Model: "gpt-5"},
		auth.EmptyState(),
		RunPromptOverrideOptions{},
	)
	if err == nil {
		t.Fatal("ApplyRunPromptOverridesWithStore accepted persisted ultra Thinking for gpt-5")
	}
}

func TestApplyRunPromptOverridesValidatesExplicitThinkingInsteadOfPersistedThinking(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	loaded.Settings.Model = "gpt-5.6-sol"
	loaded.Settings.ThinkingLevel = "high"
	plan := newLoadedConfigPlan(t, workspace, loaded)
	store := testStoreForPlan(t, plan)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Thinking: textutil.Value("ultra"),
	}); err != nil {
		t.Fatalf("persist Session Thinking: %v", err)
	}

	updated, _, err := (Planner{ContainerDir: filepath.Dir(store.Dir())}).ApplyRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{Model: "gpt-5", ThinkingLevel: "high"},
		auth.EmptyState(),
		RunPromptOverrideOptions{},
	)
	if err != nil {
		t.Fatalf("ApplyRunPromptOverridesWithStore: %v", err)
	}
	if updated.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want explicit high", updated.ActiveSettings.ThinkingLevel)
	}
}

func TestApplyRunPromptOverridesRejectsPersistedFastUnsupportedByProviderOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	store := testStoreForPlan(t, plan)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Fast: textutil.Value(true),
	}); err != nil {
		t.Fatalf("persist Session Fast: %v", err)
	}

	_, _, err := (Planner{ContainerDir: filepath.Dir(store.Dir())}).ApplyRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{
			ProviderOverride: "openai",
			OpenAIBaseURL:    "https://example.test/v1",
		},
		auth.EmptyState(),
		RunPromptOverrideOptions{},
	)
	if err == nil {
		t.Fatal("ApplyRunPromptOverridesWithStore accepted persisted Fast for third-party provider")
	}
}

func TestApplyPreparedRunPromptOverridesRejectsPersistedFastUnsupportedByActiveProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.6-sol\"",
		"provider_override = \"openai\"",
		"openai_base_url = \"https://example.test/v1\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	store := testStoreForPlan(t, plan)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Fast: textutil.Value(true),
	}); err != nil {
		t.Fatalf("persist Session Fast: %v", err)
	}
	prepared, err := PrepareRunPromptOverrides(loaded, serverapi.RunPromptOverrides{}, auth.EmptyState())
	if err != nil {
		t.Fatalf("PrepareRunPromptOverrides: %v", err)
	}

	_, _, err = (Planner{ContainerDir: filepath.Dir(store.Dir())}).ApplyPreparedRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{},
		prepared,
		RunPromptOverrideOptions{},
	)
	if err == nil {
		t.Fatal("ApplyPreparedRunPromptOverridesWithStore accepted persisted Fast for third-party provider")
	}
}

func TestApplyPreparedRunPromptOverridesRejectsPersistedThinkingUnsupportedAfterConfigModelChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	loaded.Settings.Model = "gpt-5.6-sol"
	loaded.Settings.ThinkingLevel = "high"
	plan := newLoadedConfigPlan(t, workspace, loaded)
	store := testStoreForPlan(t, plan)
	if _, err := store.MutateChatSettings(session.ChatSettingsMutation{
		Thinking: textutil.Value("ultra"),
	}); err != nil {
		t.Fatalf("persist Session Thinking: %v", err)
	}

	reloaded := loaded
	reloaded.Settings.Model = "gpt-5"
	prepared, err := PrepareRunPromptOverrides(reloaded, serverapi.RunPromptOverrides{}, auth.EmptyState())
	if err != nil {
		t.Fatalf("PrepareRunPromptOverrides: %v", err)
	}
	plan.ActiveSettings = reloaded.Settings
	_, _, err = (Planner{ContainerDir: filepath.Dir(store.Dir())}).ApplyPreparedRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{},
		prepared,
		RunPromptOverrideOptions{},
	)
	if err == nil {
		t.Fatal("ApplyPreparedRunPromptOverridesWithStore accepted persisted ultra Thinking after config changed to gpt-5")
	}
}

func TestApplyPreparedRunPromptOverridesWithoutRolePreservesConfiguredModelAndContinuation(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	overrides := serverapi.RunPromptOverrides{
		Model:         "gpt-5-mini",
		OpenAIBaseURL: "http://override.local/v1",
	}

	prepared, err := PrepareRunPromptOverrides(loaded, overrides, auth.EmptyState())
	if err != nil {
		t.Fatalf("PrepareRunPromptOverrides: %v", err)
	}
	prepared.BaseTarget = nil
	updated, _, err := ApplyPreparedRunPromptOverrides(plan, overrides, prepared)
	if err != nil {
		t.Fatalf("ApplyPreparedRunPromptOverrides: %v", err)
	}
	if updated.ActiveSettings.Model != "gpt-5-mini" {
		t.Fatalf("model = %q, want gpt-5-mini", updated.ActiveSettings.Model)
	}
	if updated.ConfiguredModelName != "gpt-5-mini" {
		t.Fatalf("configured model = %q, want gpt-5-mini", updated.ConfiguredModelName)
	}
	if updated.ActiveSettings.OpenAIBaseURL != "http://override.local/v1" {
		t.Fatalf("openai base url = %q, want override url", updated.ActiveSettings.OpenAIBaseURL)
	}
	if got := updated.Continuation; got == nil || got.OpenAIBaseURL == nil || *got.OpenAIBaseURL != "http://override.local/v1" {
		t.Fatalf("continuation = %+v, want override url", got)
	}
}

func TestApplyRunPromptOverridesRejectsInvalidAgentRole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	for _, role := range []string{"fast!", "none", "self"} {
		t.Run(role, func(t *testing.T) {
			plan := newSettingsPlan(t, workspace, config.Settings{Model: "gpt-5.4"})
			_, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(role)}, auth.EmptyState())
			if err == nil {
				t.Fatal("expected invalid agent role to fail")
			}
			if !errors.Is(err, errInvalidAgentRole) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestApplyPreparedRunPromptOverridesDefaultWithoutBaseTargetReturnsError(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	role := config.DefaultSubagentRole
	_, _, err := ApplyPreparedRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: &role}, PreparedRunPromptOverrides{
		AgentRole: serverapi.RunPromptAgentRoleOverride{Present: true, Default: true},
	})
	if err == nil {
		t.Fatal("ApplyPreparedRunPromptOverrides succeeded without a prepared base target")
	}
}

func TestApplyRunPromptOverridesKeepsExplicitToolSourcesWhenOnlyModelOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	settings := loaded.Settings
	settings.Model = "gpt-5.4"
	settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	source := loaded.Source
	source.Sources = cloneMapOrEmpty(loaded.Source.Sources)
	source.Sources["tools.shell"] = "cli"
	source.Sources["tools.patch"] = "file"
	source.Sources["tools.edit"] = "file"
	plan := newSettingsPlanWithSource(t, workspace, settings, source)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{Model: "gpt-5.3-codex"}, auth.EmptyState())
	if updated.ActiveSettings.Model != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want gpt-5.3-codex", updated.ActiveSettings.Model)
	}
	if len(updated.EnabledTools) != 1 || updated.EnabledTools[0] != toolspec.ToolExecCommand {
		t.Fatalf("enabled tools = %+v, want shell only", updated.EnabledTools)
	}
	if updated.Source.Sources["tools.shell"] != "cli" {
		t.Fatalf("tool source = %q, want cli", updated.Source.Sources["tools.shell"])
	}
}

func TestApplyRunPromptOverridesFastRoleWarnsWhenHeuristicDoesNothing(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"openai_base_url = \"https://example.test/v1\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(config.BuiltInSubagentRoleFast)}, auth.EmptyState())
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides: %v", err)
	}
	if updated.ActiveSettings.Model != loaded.Settings.Model {
		t.Fatalf("model = %q, want %q", updated.ActiveSettings.Model, loaded.Settings.Model)
	}
	if len(warnings) != 1 || warnings[0] != fastRoleSameAsMainWarning {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

func TestApplyRunPromptOverridesFastRoleAppliesBuiltInHeuristics(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(config.BuiltInSubagentRoleFast)}, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if updated.ActiveSettings.Model != "gpt-5.6-terra" {
		t.Fatalf("model = %q, want gpt-5.6-terra", updated.ActiveSettings.Model)
	}
	if !updated.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected priority request mode enabled for fast role")
	}
	if updated.ActiveSettings.Reviewer.Model != "gpt-5.6-terra" {
		t.Fatalf("reviewer model = %q, want gpt-5.6-terra", updated.ActiveSettings.Reviewer.Model)
	}
	if updated.ActiveSettings.ModelContextWindow != 372_000 {
		t.Fatalf("context window = %d, want 372000", updated.ActiveSettings.ModelContextWindow)
	}
	if updated.ConfiguredModelName != "gpt-5.6-terra" {
		t.Fatalf("configured model = %q, want gpt-5.6-terra", updated.ConfiguredModelName)
	}
}

func TestApplyRunPromptOverridesSubagentProviderOverrideCanInheritBaseModel(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"my-team-alias\"",
		"",
		"[subagents.worker]",
		"provider_override = \"openai\"",
		"openai_base_url = \"https://api.openai.com/v1\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("worker")}, auth.EmptyState())
	if updated.ActiveSettings.Model != "my-team-alias" {
		t.Fatalf("model = %q, want my-team-alias", updated.ActiveSettings.Model)
	}
	if updated.ActiveSettings.ProviderOverride != "openai" {
		t.Fatalf("provider override = %q, want openai", updated.ActiveSettings.ProviderOverride)
	}
	if updated.ActiveSettings.OpenAIBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("openai base url = %q, want https://api.openai.com/v1", updated.ActiveSettings.OpenAIBaseURL)
	}
}

func TestApplyRunPromptOverridesDerivesRoleContextBudgets(t *testing.T) {
	tests := []struct {
		name          string
		configLines   []string
		overrides     serverapi.RunPromptOverrides
		wantModel     string
		wantWindow    int
		wantThreshold int
		wantLead      int
	}{
		{
			name: "role model derives budget while preserving explicit base lead",
			configLines: []string{
				"model = \"gpt-5.4\"",
				"pre_submit_compaction_lead_tokens = 35000",
				"[subagents.fast]",
				"model = \"gpt-5.3-codex-spark\"",
			},
			overrides:     serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(config.BuiltInSubagentRoleFast)},
			wantModel:     "gpt-5.3-codex-spark",
			wantWindow:    128_000,
			wantThreshold: 121_600,
			wantLead:      35_000,
		},
		{
			name: "CLI model repairs role-derived window and preserves explicit role threshold",
			configLines: []string{
				"model = \"gpt-5.4\"",
				"[subagents.worker]",
				"model = \"gpt-5.3-codex-spark\"",
				"context_compaction_threshold_tokens = 201000",
				"pre_submit_compaction_lead_tokens = 1000",
			},
			overrides: serverapi.RunPromptOverrides{
				AgentRole: launchTestStringPtr("worker"),
				Model:     "gpt-5.3-codex",
			},
			wantModel:     "gpt-5.3-codex",
			wantWindow:    400_000,
			wantThreshold: 201_000,
			wantLead:      1_000,
		},
		{
			name: "explicit role window derives only the omitted threshold",
			configLines: []string{
				"model = \"gpt-5.4\"",
				"pre_submit_compaction_lead_tokens = 35000",
				"[subagents.fast]",
				"model = \"gpt-5.3-codex-spark\"",
				"model_context_window = 100000",
			},
			overrides:     serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr(config.BuiltInSubagentRoleFast)},
			wantModel:     "gpt-5.3-codex-spark",
			wantWindow:    100_000,
			wantThreshold: 95_000,
			wantLead:      35_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			updated := applyRunPromptOverridesNoWarnings(
				t,
				newLoadedConfigPlan(t, workspace, loadLaunchConfig(t, workspace, tt.configLines...)),
				tt.overrides,
				auth.EmptyState(),
			)
			if got := updated.ActiveSettings.Model; got != tt.wantModel {
				t.Fatalf("model = %q, want %q", got, tt.wantModel)
			}
			if got := updated.ActiveSettings.ModelContextWindow; got != tt.wantWindow {
				t.Fatalf("context window = %d, want %d", got, tt.wantWindow)
			}
			if got := updated.ActiveSettings.ContextCompactionThresholdTokens; got != tt.wantThreshold {
				t.Fatalf("compaction threshold = %d, want %d", got, tt.wantThreshold)
			}
			if got := updated.ActiveSettings.PreSubmitCompactionLeadTokens; got != tt.wantLead {
				t.Fatalf("pre-submit lead = %d, want %d", got, tt.wantLead)
			}
		})
	}
}

func TestApplyRunPromptOverridesFastRoleUsesCLIProviderOverrideForHeuristic(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"openai_base_url = \"https://example.test/v1\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{
		AgentRole:        launchTestStringPtr(config.BuiltInSubagentRoleFast),
		ProviderOverride: "openai",
		OpenAIBaseURL:    "https://api.openai.com/v1",
	}, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if updated.ActiveSettings.Model != "gpt-5.6-terra" {
		t.Fatalf("model = %q, want gpt-5.6-terra", updated.ActiveSettings.Model)
	}
	if !updated.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected priority request mode enabled")
	}
}

func TestPrepareRunPromptOverridesLockedSessionFastRoleUsesLockedModelForProviderHeuristic(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"claude-opus-4-6\"",
	)
	locked := &session.LockedContract{Model: "gpt-5.6-sol"}

	prepared, err := PrepareRunPromptOverridesForLockedSession(loaded, serverapi.RunPromptOverrides{
		AgentRole: launchTestStringPtr(config.BuiltInSubagentRoleFast),
	}, auth.EmptyState(), locked)
	if err != nil {
		t.Fatalf("PrepareRunPromptOverridesForLockedSession: %v", err)
	}
	if prepared.NamedTarget == nil {
		t.Fatal("expected named fast target")
	}
	if prepared.NamedTarget.Settings.Model != locked.Model {
		t.Fatalf("model = %q, want locked model %q", prepared.NamedTarget.Settings.Model, locked.Model)
	}
	if !prepared.NamedTarget.Settings.PriorityRequestMode {
		t.Fatal("expected fast heuristic priority mode")
	}
}

func TestPlannerResumeFastRoleUsesProviderOverrideForHeuristic(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"my-team-alias\"",
		"provider_override = \"openai\"",
	)
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole(config.BuiltInSubagentRoleFast)}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        loaded.Settings,
		Source:          loaded.Source,
	}, containerDir, persistence.Options()...)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, store.Meta().SessionID))})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "gpt-5.6-terra" {
		t.Fatalf("model = %q, want fast heuristic model", plan.ActiveSettings.Model)
	}
	if !plan.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected fast heuristic priority mode")
	}
}

func TestPlannerResumeLockedDefaultModelTreatsSessionModelAsExplicitForRoleProvider(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"[subagents.worker]",
		"model = \"local-worker\"",
		"provider_override = \"openai\"",
		"openai_base_url = \"https://local.example/v1\"",
	)
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-session-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        loaded.Settings,
		Source:          loaded.Source,
	}, containerDir, persistence.Options()...)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, store.Meta().SessionID))})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "locked-session-model" {
		t.Fatalf("model = %q, want locked-session-model", plan.ActiveSettings.Model)
	}
	if plan.ActiveSettings.ProviderOverride != "openai" {
		t.Fatalf("provider override = %q, want openai", plan.ActiveSettings.ProviderOverride)
	}
	if plan.Source.Sources["model"] != "session" {
		t.Fatalf("model source = %q, want session", plan.Source.Sources["model"])
	}
}

func TestPlannerResumePersistedRoleRejectsContextWindowBelowMinimum(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	roleSettings := loaded.Settings
	roleSettings.ModelContextWindow = 39_999
	roleSettings.ContextCompactionThresholdTokens = 38_000
	loaded.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"model_context_window": "file", "context_compaction_threshold_tokens": "file"},
		},
	}
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace, persistence.Options()...)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := newTestPlanner(config.App{
		WorkspaceRoot:   workspace,
		PersistenceRoot: root,
		Settings:        loaded.Settings,
		Source:          loaded.Source,
	}, containerDir, persistence.Options()...)

	if _, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, Intent: serverapi.OpenExistingSessionLaunchIntent(mustTypedIntentSessionID(t, store.Meta().SessionID))}); err == nil {
		t.Fatal("expected persisted subagent role context window below minimum to fail")
	} else if !config.IsModelContextWindowBelowMinimum(err) {
		t.Fatalf("error = %v, want context window minimum failure", err)
	}
}

func TestApplyRunPromptOverridesFailedConfigOverrideDoesNotPersistContinuation(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"openai_base_url = \"https://base.example/v1\"",
		"",
		"[subagents.worker]",
		"provider_override = \"openai\"",
		"openai_base_url = \"https://worker.example/v1\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	if err := testStoreForPlan(t, plan).SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: textutil.Value(loaded.Settings.OpenAIBaseURL)}); err != nil {
		t.Fatalf("seed continuation: %v", err)
	}

	_, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{
		AgentRole: launchTestStringPtr("worker"),
		Tools:     "not-a-tool",
	}, auth.EmptyState())
	if err == nil {
		t.Fatal("expected invalid tools override to fail")
	}
	got := testStoreForPlan(t, plan).Meta().Continuation
	if got == nil || got.OpenAIBaseURL == nil || *got.OpenAIBaseURL != "https://base.example/v1" {
		t.Fatalf("continuation = %+v, want unchanged base url", got)
	}
}

func TestApplyRunPromptOverridesRoleOnlyOverridePersistsContinuation(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"openai_base_url = \"https://base.example/v1\"",
		"",
		"[subagents.worker]",
		"provider_override = \"openai\"",
		"openai_base_url = \"https://worker.example/v1\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	if err := testStoreForPlan(t, plan).SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: textutil.Value(loaded.Settings.OpenAIBaseURL)}); err != nil {
		t.Fatalf("seed continuation: %v", err)
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: launchTestStringPtr("worker")}, auth.EmptyState())
	if updated.ActiveSettings.OpenAIBaseURL != "https://worker.example/v1" {
		t.Fatalf("openai base url = %q, want worker override", updated.ActiveSettings.OpenAIBaseURL)
	}
	got := updated.Continuation
	if got == nil || got.OpenAIBaseURL == nil || *got.OpenAIBaseURL != "https://worker.example/v1" || !textutil.EqualOptional(got.AgentRole, sessiontest.AgentRole("worker")) {
		t.Fatalf("continuation = %+v, want worker base url and agent role", got)
	}
}

func TestApplyRunPromptOverridesCLIModelOverrideRecomputesBudgetAfterFastRole(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{
		AgentRole: launchTestStringPtr(config.BuiltInSubagentRoleFast),
		Model:     "gpt-5.3-codex-spark",
	}, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if updated.ActiveSettings.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q, want gpt-5.3-codex-spark", updated.ActiveSettings.Model)
	}
	if updated.ConfiguredModelName != "gpt-5.3-codex-spark" {
		t.Fatalf("configured model = %q, want gpt-5.3-codex-spark", updated.ConfiguredModelName)
	}
	if updated.ActiveSettings.ModelContextWindow != 128_000 {
		t.Fatalf("context window = %d, want 128000", updated.ActiveSettings.ModelContextWindow)
	}
	if updated.ActiveSettings.ContextCompactionThresholdTokens != 121_600 {
		t.Fatalf("compaction threshold = %d, want 121600", updated.ActiveSettings.ContextCompactionThresholdTokens)
	}
	if updated.ActiveSettings.PreSubmitCompactionLeadTokens != 35_000 {
		t.Fatalf("pre-submit lead = %d, want 35000", updated.ActiveSettings.PreSubmitCompactionLeadTokens)
	}
	if !updated.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected fast-role priority mode to stay enabled")
	}
}
