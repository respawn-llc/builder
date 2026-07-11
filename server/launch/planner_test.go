package launch

import (
	"context"
	"core/internal/testharness/testoption"
	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"database/sql"
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
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
			Settings: config.Settings{
				OpenAIBaseURL: "http://headless.local/v1",
			},
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeHeadless})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	meta := plan.Store.Meta()
	if meta.SessionID == "" {
		t.Fatal("expected session id")
	}
	if !strings.HasSuffix(meta.Name, " "+SubagentSessionSuffix) {
		t.Fatalf("expected subagent session name, got %q", meta.Name)
	}
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL != "http://headless.local/v1" {
		t.Fatalf("expected continuation base url applied, got %+v", meta.Continuation)
	}
	if plan.SessionName != meta.Name {
		t.Fatalf("expected plan session name %q, got %q", meta.Name, plan.SessionName)
	}
	if plan.WorkspaceRoot != "/tmp/workspace-a" {
		t.Fatalf("expected workspace root passthrough, got %q", plan.WorkspaceRoot)
	}
}

func TestPlannerHeadlessUsesDefaultGPT55ModelAndOpenAIProviderInference(t *testing.T) {
	workspace := t.TempDir()
	cfg := loadLaunchConfig(t, workspace)
	planner := Planner{
		Config:       cfg,
		ContainerDir: filepath.Join(cfg.PersistenceRoot, "projects", "project-a", "sessions"),
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeHeadless})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if plan.ActiveSettings.Model != "gpt-5.5" {
		t.Fatalf("active model = %q, want gpt-5.5", plan.ActiveSettings.Model)
	}
	if plan.ConfiguredModelName != "gpt-5.5" {
		t.Fatalf("configured model = %q, want gpt-5.5", plan.ConfiguredModelName)
	}
	provider, err := llm.InferProviderFromModel(plan.ActiveSettings.Model)
	if err != nil {
		t.Fatalf("InferProviderFromModel: %v", err)
	}
	if provider != llm.ProviderOpenAI {
		t.Fatalf("provider = %q, want %q", provider, llm.ProviderOpenAI)
	}
}

func TestPlannerInteractiveRequiresExplicitOpenOrCreateIntent(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a")
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
			Settings:        config.Settings{},
		},
		ContainerDir: containerDir,
	}

	_, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive})
	if err == nil || !errors.Is(err, errSessionSelectionRequired) {
		t.Fatalf("PlanSession error = %v, want explicit intent required", err)
	}
}

func TestPlannerInteractiveReopensSelectedSessionID(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	first := createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a")
	if err := first.SetName("first"); err != nil {
		t.Fatalf("persist first session meta: %v", err)
	}
	second := createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a")
	if err := second.SetName("second"); err != nil {
		t.Fatalf("persist second session meta: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
			Settings:        config.Settings{},
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: second.Meta().SessionID})
	if err != nil {
		t.Fatalf("plan session: %v", err)
	}
	if plan.Store.Meta().SessionID != second.Meta().SessionID {
		t.Fatalf("expected selected session %q, got %q", second.Meta().SessionID, plan.Store.Meta().SessionID)
	}
	if plan.Store.Meta().SessionID == first.Meta().SessionID {
		t.Fatalf("did not expect first session %q", first.Meta().SessionID)
	}
}

func TestPlannerReappliesPersistedSubagentRoleSettingsOnResume(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("smart_reviewer")}); err != nil {
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
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
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
	if got := plan.Store.Meta().Continuation; got == nil || !testoption.EqualString(got.AgentRole, testoption.String("smart_reviewer")) {
		t.Fatalf("continuation = %+v, want smart_reviewer preserved", got)
	}
}

func TestPlannerIgnoresMissingPersistedSubagentRoleOnResume(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("deleted_role")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings: config.Settings{
				Model:         "gpt-5.5",
				ThinkingLevel: "medium",
			},
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.ThinkingLevel != "medium" {
		t.Fatalf("thinking level = %q, want base config when role is missing", plan.ActiveSettings.ThinkingLevel)
	}
	if got := plan.Store.Meta().Continuation; got == nil || !testoption.EqualString(got.AgentRole, testoption.String("deleted_role")) {
		t.Fatalf("continuation = %+v, want missing role preserved", got)
	}
}

func TestPlannerKeepsRoleBaseURLOutOfBaseSettingsOnResume(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: "https://worker.example/v1",
		AgentRole:     testoption.String("worker"),
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
	source.Sources = cloneStringMap(loaded.Source.Sources)
	source.Sources["openai_base_url"] = "file"
	source.Sources["thinking_level"] = "file"
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        settings,
			Source:          source,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.OpenAIBaseURL != "https://worker.example/v1" {
		t.Fatalf("active base url = %q, want worker", plan.ActiveSettings.OpenAIBaseURL)
	}
	if plan.BaseSettings.OpenAIBaseURL != "https://base.example/v1" {
		t.Fatalf("base settings url = %q, want base", plan.BaseSettings.OpenAIBaseURL)
	}

	cleared, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: config.DefaultSubagentRole}, auth.EmptyState())
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides clear: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if cleared.ActiveSettings.OpenAIBaseURL != "https://base.example/v1" {
		t.Fatalf("cleared base url = %q, want base", cleared.ActiveSettings.OpenAIBaseURL)
	}
	if got := plan.Store.Meta().Continuation; got != nil && got.AgentRole != nil {
		t.Fatalf("continuation after clear = %+v, want no role", got)
	}

	if err := plan.Store.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: "https://worker.example/v1", AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("reset continuation: %v", err)
	}
	switched, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: "research"}, auth.EmptyState())
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

func TestApplyRunPromptOverridesExplicitRoleUsesBaseSettingsAfterPersistedRoleResume(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	store := createTestSession(t, workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("old_role")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	baseSettings := loaded.Settings
	baseSettings.EnabledTools = cloneEnabledToolSet(baseSettings.EnabledTools)
	baseSettings.EnabledTools[toolspec.ToolExecCommand] = true
	baseSettings.EnabledTools[toolspec.ToolPatch] = true
	workerSettings := cloneSettings(baseSettings)
	workerSettings.ThinkingLevel = "high"
	baseSettings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: workerSettings,
			Sources:  map[string]string{"thinking_level": "file"},
		},
	}
	resumedSettings := cloneSettings(baseSettings)
	resumedSettings.ThinkingLevel = "xhigh"
	resumedSettings.EnabledTools[toolspec.ToolPatch] = false
	baseSource := loaded.Source
	baseSource.Sources = cloneStringMap(loaded.Source.Sources)
	baseSource.Sources["thinking_level"] = "file"
	baseSource.Sources["tools.shell"] = "file"
	baseSource.Sources["tools.patch"] = "file"
	baseSource.Sources["tools.edit"] = "file"
	resumedSource := baseSource
	resumedSource.Sources = cloneStringMap(baseSource.Sources)
	resumedSource.Sources["thinking_level"] = "subagent"
	resumedSource.Sources["tools.patch"] = "subagent"
	plan := SessionPlan{
		Store:               store,
		ActiveSettings:      resumedSettings,
		BaseSettings:        baseSettings,
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: "gpt-5.5",
		WorkspaceRoot:       workspace,
		Source:              resumedSource,
		BaseSource:          baseSource,
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
	if updated.ActiveSettings.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want new role", updated.ActiveSettings.ThinkingLevel)
	}
	if !updated.ActiveSettings.EnabledTools[toolspec.ToolPatch] {
		t.Fatalf("patch tool should come from base settings, got %+v", updated.ActiveSettings.EnabledTools)
	}
	if updated.Source.Sources["tools.patch"] != "file" {
		t.Fatalf("tools.patch source = %q, want base source", updated.Source.Sources["tools.patch"])
	}
	if got := store.Meta().Continuation; got == nil || !testoption.EqualString(got.AgentRole, testoption.String("worker")) {
		t.Fatalf("continuation = %+v, want worker", got)
	}
}

func TestApplyRunPromptOverridesExplicitDefaultClearsPersistedRole(t *testing.T) {
	workspace := t.TempDir()
	store := createTestSession(t, workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("old_role")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	baseSettings := config.Settings{
		Model:         "gpt-5.5",
		ThinkingLevel: "medium",
		EnabledTools:  map[toolspec.ID]bool{toolspec.ToolExecCommand: true},
	}
	resumedSettings := cloneSettings(baseSettings)
	resumedSettings.ThinkingLevel = "xhigh"
	plan := SessionPlan{
		Store:               store,
		ActiveSettings:      resumedSettings,
		BaseSettings:        baseSettings,
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: "gpt-5.5",
		WorkspaceRoot:       workspace,
		Source:              config.SourceReport{Sources: map[string]string{"thinking_level": "subagent"}},
		BaseSource:          config.SourceReport{Sources: map[string]string{"thinking_level": "file"}},
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: config.DefaultSubagentRole}, auth.EmptyState())
	if updated.ActiveSettings.ThinkingLevel != "medium" {
		t.Fatalf("thinking level = %q, want base config", updated.ActiveSettings.ThinkingLevel)
	}
	if updated.Source.Sources["thinking_level"] != "file" {
		t.Fatalf("thinking source = %q, want base source", updated.Source.Sources["thinking_level"])
	}
	if got := store.Meta().Continuation; got != nil && got.AgentRole != nil {
		t.Fatalf("continuation = %+v, want cleared role", got)
	}
}

func TestApplyRunPromptOverridesResumedRoleMatrix(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	baseSettings := loaded.Settings
	baseSettings.EnabledTools = cloneEnabledToolSet(baseSettings.EnabledTools)
	baseSettings.Model = "gpt-5.5"
	baseSettings.ThinkingLevel = "medium"
	baseSettings.EnabledTools[toolspec.ToolExecCommand] = true
	baseSettings.EnabledTools[toolspec.ToolPatch] = true
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
	baseSource.Sources = cloneStringMap(loaded.Source.Sources)
	baseSource.Sources["model"] = "file"
	baseSource.Sources["thinking_level"] = "file"
	baseSource.Sources["tools.shell"] = "file"
	baseSource.Sources["tools.patch"] = "file"
	baseSource.Sources["tools.edit"] = "file"
	resumedSettings := cloneSettings(baseSettings)
	resumedSettings.ThinkingLevel = "xhigh"
	resumedSettings.EnabledTools[toolspec.ToolPatch] = false
	resumedSource := baseSource
	resumedSource.Sources = cloneStringMap(baseSource.Sources)
	resumedSource.Sources["thinking_level"] = "subagent"
	resumedSource.Sources["tools.patch"] = "subagent"

	tests := []struct {
		name             string
		locked           bool
		overrides        serverapi.RunPromptOverrides
		wantModel        string
		wantThinking     string
		wantPatchSetting bool
		wantAgentRole    *string
	}{
		{
			name:             "no override keeps resumed role",
			overrides:        serverapi.RunPromptOverrides{},
			wantModel:        "gpt-5.5",
			wantThinking:     "xhigh",
			wantPatchSetting: false,
			wantAgentRole:    testoption.String("old_role"),
		},
		{
			name:             "new role starts from base settings",
			overrides:        serverapi.RunPromptOverrides{AgentRole: "worker"},
			wantModel:        "gpt-5.4-mini",
			wantThinking:     "high",
			wantPatchSetting: true,
			wantAgentRole:    testoption.String("worker"),
		},
		{
			name:             "default clears resumed role",
			overrides:        serverapi.RunPromptOverrides{AgentRole: config.DefaultSubagentRole},
			wantModel:        "gpt-5.5",
			wantThinking:     "medium",
			wantPatchSetting: true,
			wantAgentRole:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := createTestSession(t, workspace)
			if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("old_role")}); err != nil {
				t.Fatalf("SetContinuationContext: %v", err)
			}
			planBaseSettings := cloneSettings(baseSettings)
			planResumedSettings := cloneSettings(resumedSettings)
			if tt.locked {
				if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
					t.Fatalf("MarkModelDispatchLocked: %v", err)
				}
				planBaseSettings.Model = "locked-model"
				planResumedSettings.Model = "locked-model"
			}
			plan := SessionPlan{
				Store:               store,
				ActiveSettings:      planResumedSettings,
				BaseSettings:        planBaseSettings,
				EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
				ConfiguredModelName: "gpt-5.5",
				WorkspaceRoot:       workspace,
				Source:              resumedSource,
				BaseSource:          baseSource,
				ModelContractLocked: tt.locked,
			}

			updated := applyRunPromptOverridesNoWarnings(t, plan, tt.overrides, auth.EmptyState())
			if updated.ActiveSettings.Model != tt.wantModel {
				t.Fatalf("model = %q, want %q", updated.ActiveSettings.Model, tt.wantModel)
			}
			if updated.ActiveSettings.ThinkingLevel != tt.wantThinking {
				t.Fatalf("thinking = %q, want %q", updated.ActiveSettings.ThinkingLevel, tt.wantThinking)
			}
			if updated.ActiveSettings.EnabledTools[toolspec.ToolPatch] != tt.wantPatchSetting {
				t.Fatalf("patch setting = %t, want %t", updated.ActiveSettings.EnabledTools[toolspec.ToolPatch], tt.wantPatchSetting)
			}
			var gotRole *string
			if continuation := store.Meta().Continuation; continuation != nil {
				gotRole = continuation.AgentRole
			}
			if !testoption.EqualString(gotRole, tt.wantAgentRole) {
				t.Fatalf("continuation role = %v, want %v", gotRole, tt.wantAgentRole)
			}
		})
	}
}

func TestApplyRunPromptOverridesRejectsDifferentAgentRoleForLockedSession(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"[subagents.old_role]",
		"model = \"gpt-5.5\"",
		"",
		"[subagents.worker]",
		"model = \"gpt-5.4-mini\"",
	)

	tests := []struct {
		name      string
		persisted string
		override  string
	}{
		{
			name:      "different role",
			persisted: "old_role",
			override:  "worker",
		},
		{
			name:      "default clears role",
			persisted: "old_role",
			override:  config.DefaultSubagentRole,
		},
		{
			name:     "base session gains role",
			override: "worker",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := createTestSession(t, workspace)
			if tt.persisted != "" {
				if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String(tt.persisted)}); err != nil {
					t.Fatalf("SetContinuationContext: %v", err)
				}
			}
			if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
				t.Fatalf("MarkModelDispatchLocked: %v", err)
			}
			plan := SessionPlan{
				Store:               store,
				ActiveSettings:      EffectiveSettings(loaded.Settings, store.Meta().Locked),
				BaseSettings:        EffectiveSettings(loaded.Settings, store.Meta().Locked),
				EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
				ConfiguredModelName: "locked-model",
				WorkspaceRoot:       workspace,
				Source:              loaded.Source,
				BaseSource:          loaded.Source,
				ModelContractLocked: true,
			}

			_, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: tt.override}, auth.EmptyState())
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
	store := createTestSession(t, workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	plan := SessionPlan{
		Store:               store,
		ActiveSettings:      EffectiveSettings(loaded.Settings, store.Meta().Locked),
		BaseSettings:        EffectiveSettings(loaded.Settings, store.Meta().Locked),
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: "locked-model",
		WorkspaceRoot:       workspace,
		Source:              loaded.Source,
		BaseSource:          loaded.Source,
		ModelContractLocked: true,
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
	if got := updated.Store.Meta().Continuation; got == nil || !testoption.EqualString(got.AgentRole, testoption.String("worker")) {
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
	store := createTestSession(t, workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("old_role")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}, HasEnabledTools: true}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	plan := SessionPlan{
		Store:               store,
		ActiveSettings:      EffectiveSettings(loaded.Settings, store.Meta().Locked),
		BaseSettings:        EffectiveSettings(loaded.Settings, store.Meta().Locked),
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: "locked-model",
		WorkspaceRoot:       workspace,
		Source:              loaded.Source,
		BaseSource:          loaded.Source,
		ModelContractLocked: true,
	}

	updated, _, err := ApplyRunPromptOverridesWithOptions(plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState(), RunPromptOverrideOptions{
		AllowLockedAgentRoleChange: true,
	})
	if err != nil {
		t.Fatalf("ApplyRunPromptOverridesWithOptions: %v", err)
	}
	if got := updated.Store.Meta().Continuation; got == nil || !testoption.EqualString(got.AgentRole, testoption.String("worker")) {
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
	baseSource.Sources = cloneStringMap(loaded.Source.Sources)
	baseSource.Sources["model"] = "file"
	baseSource.Sources["thinking_level"] = "file"
	store := createTestSession(t, workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	plan := SessionPlan{
		Store:               store,
		ActiveSettings:      baseSettings,
		BaseSettings:        baseSettings,
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: "gpt-5.5",
		WorkspaceRoot:       workspace,
		Source:              baseSource,
		BaseSource:          baseSource,
		ModelContractLocked: true,
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
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
	containerDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	parent := createTestSessionInContainer(t, containerDir, filepath.Base(containerDir), cfg.WorkspaceRoot, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: "http://parent.local/v1"}); err != nil {
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
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{SessionID: parent.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "worktree-review"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode:                  session.WorktreeReminderModeEnter,
		Branch:                "feature/review",
		WorktreePath:          canonicalWorktreeRoot,
		WorkspaceRoot:         cfg.WorkspaceRoot,
		EffectiveCwd:          filepath.Join(canonicalWorktreeRoot, "pkg"),
		HasIssuedInGeneration: true,
		IssuedCompactionCount: 3,
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	planner := Planner{
		Config:       cfg,
		ContainerDir: containerDir,
		StoreOptions: metadataStore.AuthoritativeSessionStoreOptions(),
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:            ModeInteractive,
		ForceNewSession: true,
		ParentSessionID: parent.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	childMeta := plan.Store.Meta()
	if childMeta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("child parent session id = %q, want %q", childMeta.ParentSessionID, parent.Meta().SessionID)
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
	if childMeta.Continuation == nil || childMeta.Continuation.OpenAIBaseURL != "http://parent.local/v1" {
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
	if childMeta.WorktreeReminder.Branch != "feature/review" || childMeta.WorktreeReminder.WorktreePath != canonicalWorktreeRoot {
		t.Fatalf("child worktree reminder = %+v", childMeta.WorktreeReminder)
	}
	if childMeta.WorktreeReminder.HasIssuedInGeneration || childMeta.WorktreeReminder.IssuedCompactionCount != 0 {
		t.Fatalf("child worktree reminder generation flags = %+v, want reset", childMeta.WorktreeReminder)
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
	parent := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := parent.MarkModelDispatchLocked(session.LockedContract{
		Model:           "locked-parent-model",
		EnabledTools:    []string{"shell"},
		SystemPrompt:    "parent generic system prompt",
		HasSystemPrompt: true,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{
		OpenAIBaseURL: "https://parent.example/v1",
		AgentRole:     testoption.String("old_parent_role"),
	}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	planner := Planner{
		Config:       cfg,
		ContainerDir: containerDir,
	}
	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:            ModeHeadless,
		ParentSessionID: parent.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "code_review"}, auth.EmptyState())
	if childLocked := updated.Store.Meta().Locked; childLocked != nil {
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
	if got := updated.Store.Meta().Continuation; got == nil || !testoption.EqualString(got.AgentRole, testoption.String("code_review")) {
		t.Fatalf("child continuation = %+v, want only selected role persisted", got)
	}
}

func TestPlannerNewChildSessionFallsBackWhenParentExecutionTargetIsNotMetadataBacked(t *testing.T) {
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	parent := createTestSessionInContainer(t, containerDir, "workspace-a", "/tmp/workspace-a")
	if err := parent.SetWorktreeReminderState(&session.WorktreeReminderState{
		Mode:                  session.WorktreeReminderModeEnter,
		Branch:                "feature/file-backed",
		WorktreePath:          "/tmp/worktree-a",
		WorkspaceRoot:         "/tmp/workspace-a",
		EffectiveCwd:          "/tmp/worktree-a/pkg",
		HasIssuedInGeneration: true,
		IssuedCompactionCount: 4,
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:            ModeInteractive,
		ForceNewSession: true,
		ParentSessionID: parent.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	childMeta := plan.Store.Meta()
	if childMeta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("parent session id = %q, want %q", childMeta.ParentSessionID, parent.Meta().SessionID)
	}
	if childMeta.WorktreeReminder == nil || childMeta.WorktreeReminder.Branch != "feature/file-backed" {
		t.Fatalf("worktree reminder = %+v, want parent reminder copied", childMeta.WorktreeReminder)
	}
	if childMeta.WorktreeReminder.HasIssuedInGeneration || childMeta.WorktreeReminder.IssuedCompactionCount != 0 {
		t.Fatalf("worktree reminder generation flags = %+v, want reset", childMeta.WorktreeReminder)
	}
}

func TestPlannerNewChildSessionIgnoresParentOutsideActiveContainer(t *testing.T) {
	root := t.TempDir()
	containerA := filepath.Join(root, "projects", "project-a", "sessions")
	containerB := filepath.Join(root, "projects", "project-b", "sessions")
	parent := createTestSessionInContainer(t, containerB, "workspace-b", "/tmp/workspace-b")
	if err := parent.MarkModelDispatchLocked(session.LockedContract{Model: "foreign-parent-model"}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	if err := parent.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: "http://foreign.local/v1"}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
		},
		ContainerDir: containerA,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:            ModeInteractive,
		ForceNewSession: true,
		ParentSessionID: parent.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("PlanSession child: %v", err)
	}
	childMeta := plan.Store.Meta()
	if childMeta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("parent session id = %q, want %q", childMeta.ParentSessionID, parent.Meta().SessionID)
	}
	if childMeta.WorkspaceRoot != "/tmp/workspace-a" || childMeta.WorkspaceContainer != "sessions" {
		t.Fatalf("child workspace context = root %q container %q, want active project session root", childMeta.WorkspaceRoot, childMeta.WorkspaceContainer)
	}
	if childMeta.Locked != nil {
		t.Fatalf("locked contract = %+v, want no foreign parent lock copied", childMeta.Locked)
	}
	if childMeta.Continuation != nil {
		t.Fatalf("continuation = %+v, want no foreign parent continuation copied", childMeta.Continuation)
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
	parent := createTestSessionInContainer(t, containerDir, filepath.Base(containerDir), cfg.WorkspaceRoot, metadataStore.SessionStoreOptions()...)
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
		StoreOptions:        metadataStore.SessionStoreOptions(),
		MetadataStoreOpener: func(string) (MetadataExecutionTargetStore, error) { return failingStore, nil },
	}

	_, err = planner.PlanSession(context.Background(), SessionRequest{
		Mode:            ModeInteractive,
		ForceNewSession: true,
		ParentSessionID: parent.Meta().SessionID,
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
		Mode:            ModeInteractive,
		ForceNewSession: true,
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
		ContainerDir: containerDir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planner.PlanSession(ctx, SessionRequest{
		Mode:            ModeInteractive,
		ForceNewSession: true,
		ParentSessionID: parent.Meta().SessionID,
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
	if got := updated.Store.Meta().Continuation; got == nil || got.OpenAIBaseURL != "http://override.local/v1" {
		t.Fatalf("continuation = %+v, want override url", got)
	}
	if plan.ActiveSettings.Model != "base-model" {
		t.Fatalf("base plan mutated: %+v", plan.ActiveSettings)
	}
}

func TestSubagentRoleMetadataSurvivesCloneAndSourceReport(t *testing.T) {
	settings := config.Settings{
		Subagents: map[string]config.SubagentRole{
			"worker": {
				Settings:         config.Settings{Model: "gpt-5.4-mini"},
				Sources:          map[string]string{"model": "file"},
				Description:      "Worker role",
				AgentCallable:    false,
				AgentCallableSet: true,
			},
		},
	}

	cloned := cloneSettings(settings)
	copiedRole := cloned.Subagents["worker"]
	copiedRole.Description = "changed"
	copiedRole.Sources["model"] = "changed"
	cloned.Subagents["worker"] = copiedRole
	if settings.Subagents["worker"].Description != "Worker role" {
		t.Fatalf("clone mutated source description: %+v", settings.Subagents["worker"])
	}
	if settings.Subagents["worker"].Sources["model"] != "file" {
		t.Fatalf("clone mutated source map: %+v", settings.Subagents["worker"].Sources)
	}
	if !cloned.Subagents["worker"].AgentCallableSet || cloned.Subagents["worker"].AgentCallable {
		t.Fatalf("metadata did not survive clone: %+v", cloned.Subagents["worker"])
	}

	report := sourceReportWithSubagentRoleSources(config.SourceReport{Sources: map[string]string{"model": "file"}}, settings, "worker", true)
	if report.Sources["model"] != "subagent" {
		t.Fatalf("source report model source = %q, want subagent", report.Sources["model"])
	}
}

func TestResolveSubagentSettingsPreservesSubagentCatalogMetadata(t *testing.T) {
	cfg := loadLaunchConfig(t, t.TempDir())
	base := cfg.Settings
	workerSettings := base
	workerSettings.ThinkingLevel = "high"
	blockedSettings := base
	blockedSettings.Model = "gpt-5.4-mini"
	base.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings:    workerSettings,
			Sources:     map[string]string{"thinking_level": "file"},
			Description: "Worker role",
		},
		"blocked": {
			Settings:         blockedSettings,
			Sources:          map[string]string{"model": "file"},
			AgentCallable:    false,
			AgentCallableSet: true,
		},
	}

	resolved, _, err := resolveSubagentSettingsWithValidation(base, base, cfg.Source.Sources, "worker", auth.EmptyState(), true, true)
	if err != nil {
		t.Fatalf("resolveSubagentSettings: %v", err)
	}
	if resolved.Subagents["worker"].Description != "Worker role" {
		t.Fatalf("worker metadata lost after resolve: %+v", resolved.Subagents["worker"])
	}
	if !resolved.Subagents["blocked"].AgentCallableSet || resolved.Subagents["blocked"].AgentCallable {
		t.Fatalf("blocked metadata lost after resolve: %+v", resolved.Subagents["blocked"])
	}
}

func TestResolveSubagentSettingsRejectsRoleContextWindowBelowMinimum(t *testing.T) {
	cfg := loadLaunchConfig(t, t.TempDir())
	base := cfg.Settings
	roleSettings := base
	roleSettings.ModelContextWindow = 39_999
	roleSettings.ContextCompactionThresholdTokens = 38_000
	base.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"model_context_window": "file", "context_compaction_threshold_tokens": "file"},
		},
	}

	_, _, err := resolveSubagentSettingsWithValidation(base, base, cfg.Source.Sources, "worker", auth.EmptyState(), true, true)
	if err == nil {
		t.Fatal("expected role context window below minimum to fail")
	}
	if !config.IsModelContextWindowBelowMinimum(err) {
		t.Fatalf("error = %v, want context window minimum failure", err)
	}
}

func TestResolveSubagentSettingsRejectsRoleReviewerContextWindowBelowMinimum(t *testing.T) {
	cfg := loadLaunchConfig(t, t.TempDir())
	base := cfg.Settings
	roleSettings := base
	roleSettings.Reviewer.ModelContextWindow = 39_999
	base.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"reviewer.model_context_window": "file"},
		},
	}

	_, _, err := resolveSubagentSettingsWithValidation(base, base, cfg.Source.Sources, "worker", auth.EmptyState(), true, true)
	if err == nil {
		t.Fatal("expected role reviewer context window below minimum to fail")
	}
	if !config.IsModelContextWindowBelowMinimum(err) {
		t.Fatalf("error = %v, want context window minimum failure", err)
	}
}

func TestApplyRunPromptOverridesRejectsInvalidAgentRole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	for _, role := range []string{"fast!", "none", "self"} {
		t.Run(role, func(t *testing.T) {
			plan := newSettingsPlan(t, workspace, config.Settings{Model: "gpt-5.4"})
			_, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: role}, auth.EmptyState())
			if err == nil {
				t.Fatal("expected invalid agent role to fail")
			}
			if !errors.Is(err, errInvalidAgentRole) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestApplyRunPromptOverridesRecomputesEnabledToolsForModelOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	settings := loaded.Settings
	settings.Model = "gpt-5.4"
	settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	source := loaded.Source
	source.Sources = cloneStringMap(loaded.Source.Sources)
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
}

func TestApplyRunPromptOverridesKeepsExplicitToolSourcesWhenOnlyModelOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	settings := loaded.Settings
	settings.Model = "gpt-5.4"
	settings.EnabledTools = map[toolspec.ID]bool{toolspec.ToolExecCommand: true}
	source := loaded.Source
	source.Sources = cloneStringMap(loaded.Source.Sources)
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

	updated, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: config.BuiltInSubagentRoleFast}, auth.EmptyState())
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

func TestApplySubagentRoleOverridesAppendsRoleSystemPromptFile(t *testing.T) {
	mainPrompt := filepath.Join(t.TempDir(), "main-system.md")
	rolePrompt := filepath.Join(t.TempDir(), "worker-system.md")
	settings := config.Settings{
		SystemPromptFiles: []config.SystemPromptFile{{Path: mainPrompt, Scope: config.SystemPromptFileScopeHomeConfig}},
	}
	role := config.SubagentRole{
		Settings: config.Settings{
			SystemPromptFile:  "worker-system.md",
			SystemPromptFiles: []config.SystemPromptFile{{Path: rolePrompt, Scope: config.SystemPromptFileScopeSubagent}},
		},
		Sources: map[string]string{"system_prompt_file": "file"},
	}
	applySubagentRoleOverrides(&settings, role, true)

	if settings.SystemPromptFile != "worker-system.md" {
		t.Fatalf("system_prompt_file = %q, want worker-system.md", settings.SystemPromptFile)
	}
	files := settings.SystemPromptFiles
	if len(files) != 2 {
		t.Fatalf("system prompt files = %+v, want base and role entries", files)
	}
	if got := files[0]; got.Path != mainPrompt || got.Scope != config.SystemPromptFileScopeHomeConfig {
		t.Fatalf("base system prompt file = %+v, want %q %s", got, mainPrompt, config.SystemPromptFileScopeHomeConfig)
	}
	if got := files[1]; got.Path != rolePrompt || got.Scope != config.SystemPromptFileScopeSubagent {
		t.Fatalf("role system prompt file = %+v, want %q %s", got, rolePrompt, config.SystemPromptFileScopeSubagent)
	}
}

func TestApplyRunPromptOverridesFastRoleAppliesBuiltInHeuristics(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: config.BuiltInSubagentRoleFast}, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if updated.ActiveSettings.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want gpt-5.4-mini", updated.ActiveSettings.Model)
	}
	if !updated.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected priority request mode enabled for fast role")
	}
	if updated.ActiveSettings.Reviewer.Model != "gpt-5.4-mini" {
		t.Fatalf("reviewer model = %q, want gpt-5.4-mini", updated.ActiveSettings.Reviewer.Model)
	}
	if updated.ActiveSettings.ModelContextWindow != 272_000 {
		t.Fatalf("context window = %d, want 272000", updated.ActiveSettings.ModelContextWindow)
	}
	if updated.ConfiguredModelName != "gpt-5.4-mini" {
		t.Fatalf("configured model = %q, want gpt-5.4-mini", updated.ConfiguredModelName)
	}
}

func TestApplyRunPromptOverridesLockedDefaultModelTreatsSessionModelAsExplicitForRoleProvider(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"[subagents.worker]",
		"model = \"local-worker\"",
		"provider_override = \"openai\"",
		"openai_base_url = \"https://local.example/v1\"",
	)
	baseSettings := EffectiveSettings(loaded.Settings, &session.LockedContract{Model: "locked-session-model"})
	store := createTestSession(t, workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-session-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	plan := SessionPlan{
		Store:               store,
		ActiveSettings:      baseSettings,
		BaseSettings:        baseSettings,
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ConfiguredModelName: loaded.Settings.Model,
		WorkspaceRoot:       workspace,
		Source:              loaded.Source,
		BaseSource:          loaded.Source,
		ModelContractLocked: true,
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
	if updated.ActiveSettings.Model != "locked-session-model" {
		t.Fatalf("model = %q, want locked-session-model", updated.ActiveSettings.Model)
	}
	if updated.ActiveSettings.ProviderOverride != "openai" {
		t.Fatalf("provider override = %q, want openai", updated.ActiveSettings.ProviderOverride)
	}
	if updated.Source.Sources["model"] != "session" {
		t.Fatalf("model source = %q, want session", updated.Source.Sources["model"])
	}
}

func TestApplyRunPromptOverridesFastRoleWarnsWhenExplicitRoleMatchesBase(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"thinking_level = \"medium\"",
		"",
		"[subagents.fast]",
		"model = \"gpt-5.4\"",
		"thinking_level = \"medium\"",
		"priority_request_mode = false",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated, warnings, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: config.BuiltInSubagentRoleFast}, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides: %v", err)
	}
	if updated.ActiveSettings.Model != loaded.Settings.Model || updated.ActiveSettings.ThinkingLevel != loaded.Settings.ThinkingLevel {
		t.Fatalf("expected explicit fast role to match base settings, got %+v vs %+v", updated.ActiveSettings, loaded.Settings)
	}
	if len(warnings) != 1 || warnings[0] != fastRoleSameAsMainWarning {
		t.Fatalf("unexpected warnings: %+v", warnings)
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

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
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

func TestApplyRunPromptOverridesSubagentReviewerSystemPromptFile(t *testing.T) {
	workspace := t.TempDir()
	loaded, home := loadLaunchConfigWithHome(t, workspace,
		"model = \"gpt-5.5\"",
		"",
		"[reviewer]",
		"system_prompt_file = \"base-reviewer.md\"",
		"",
		"[subagents.worker.reviewer]",
		"system_prompt_file = \"worker-reviewer.md\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
	if want := filepath.Join(home, config.ConfigDirName, "worker-reviewer.md"); updated.ActiveSettings.Reviewer.SystemPromptFile != want {
		t.Fatalf("reviewer system prompt file = %q, want %q", updated.ActiveSettings.Reviewer.SystemPromptFile, want)
	}
}

func TestApplyRunPromptOverridesRoleModelOverrideRecomputesContextBudget(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"model_context_window = 272000",
		"context_compaction_threshold_tokens = 258400",
		"pre_submit_compaction_lead_tokens = 35000",
		"",
		"[subagents.fast]",
		"model = \"gpt-5.3-codex-spark\"",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: config.BuiltInSubagentRoleFast}, auth.EmptyState())
	if updated.ActiveSettings.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model = %q, want gpt-5.3-codex-spark", updated.ActiveSettings.Model)
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
}

func TestApplyRunPromptOverridesModelOverrideCanRepairRoleDerivedBudget(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"",
		"[subagents.worker]",
		"model = \"gpt-5.3-codex-spark\"",
		"context_compaction_threshold_tokens = 201000",
		"pre_submit_compaction_lead_tokens = 1000",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{
		AgentRole: "worker",
		Model:     "gpt-5.3-codex",
	}, auth.EmptyState())
	if updated.ActiveSettings.Model != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want gpt-5.3-codex", updated.ActiveSettings.Model)
	}
	if updated.ActiveSettings.ModelContextWindow != 400_000 {
		t.Fatalf("context window = %d, want 400000", updated.ActiveSettings.ModelContextWindow)
	}
	if updated.ActiveSettings.ContextCompactionThresholdTokens != 201_000 {
		t.Fatalf("compaction threshold = %d, want 201000", updated.ActiveSettings.ContextCompactionThresholdTokens)
	}
}

func TestApplyRunPromptOverridesRoleModelOverrideKeepsExplicitContextWindow(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"gpt-5.4\"",
		"model_context_window = 272000",
		"context_compaction_threshold_tokens = 258400",
		"pre_submit_compaction_lead_tokens = 35000",
		"",
		"[subagents.fast]",
		"model = \"gpt-5.3-codex-spark\"",
		"model_context_window = 100000",
	)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: config.BuiltInSubagentRoleFast}, auth.EmptyState())
	if updated.ActiveSettings.ModelContextWindow != 100_000 {
		t.Fatalf("context window = %d, want 100000", updated.ActiveSettings.ModelContextWindow)
	}
	if updated.ActiveSettings.ContextCompactionThresholdTokens != 95_000 {
		t.Fatalf("compaction threshold = %d, want 95000", updated.ActiveSettings.ContextCompactionThresholdTokens)
	}
	if updated.ActiveSettings.PreSubmitCompactionLeadTokens != 35_000 {
		t.Fatalf("pre-submit lead = %d, want 35000", updated.ActiveSettings.PreSubmitCompactionLeadTokens)
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
		AgentRole:        config.BuiltInSubagentRoleFast,
		ProviderOverride: "openai",
		OpenAIBaseURL:    "https://api.openai.com/v1",
	}, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if updated.ActiveSettings.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want gpt-5.4-mini", updated.ActiveSettings.Model)
	}
	if !updated.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected priority request mode enabled")
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
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String(config.BuiltInSubagentRoleFast)}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        loaded.Settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "gpt-5.4-mini" {
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
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: "locked-session-model", EnabledTools: []string{"shell"}}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        loaded.Settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
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

func TestPlannerResumeFastRoleUsesOpenAIBaseURLForHeuristic(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace,
		"model = \"my-team-alias\"",
		"openai_base_url = \"https://api.openai.com/v1\"",
	)
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String(config.BuiltInSubagentRoleFast)}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        loaded.Settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want fast heuristic model", plan.ActiveSettings.Model)
	}
	if !plan.ActiveSettings.PriorityRequestMode {
		t.Fatal("expected fast heuristic priority mode")
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
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        loaded.Settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	if _, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID}); err == nil {
		t.Fatal("expected persisted subagent role context window below minimum to fail")
	} else if !config.IsModelContextWindowBelowMinimum(err) {
		t.Fatalf("error = %v, want context window minimum failure", err)
	}
}

func TestPlannerResumePersistedRoleRejectsReviewerContextWindowBelowMinimum(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	roleSettings := loaded.Settings
	roleSettings.Reviewer.ModelContextWindow = 39_999
	loaded.Settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: roleSettings,
			Sources:  map[string]string{"reviewer.model_context_window": "file"},
		},
	}
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("worker")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        loaded.Settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	if _, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID}); err == nil {
		t.Fatal("expected persisted subagent role reviewer context window below minimum to fail")
	} else if !config.IsModelContextWindowBelowMinimum(err) {
		t.Fatalf("error = %v, want context window minimum failure", err)
	}
}

func TestPlannerResumeRemovedPersistedRoleKeepsBaseSettings(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	store := createTestSessionInContainer(t, containerDir, "workspace-a", workspace)
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: testoption.String("removed")}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	planner := Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: root,
			Settings:        loaded.Settings,
			Source:          loaded.Source,
		},
		ContainerDir: containerDir,
	}

	plan, err := planner.PlanSession(context.Background(), SessionRequest{Mode: ModeInteractive, SelectedSessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	if plan.ActiveSettings.Model != loaded.Settings.Model {
		t.Fatalf("model = %q, want base model %q", plan.ActiveSettings.Model, loaded.Settings.Model)
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
	if err := plan.Store.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: loaded.Settings.OpenAIBaseURL}); err != nil {
		t.Fatalf("seed continuation: %v", err)
	}

	_, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{
		AgentRole: "worker",
		Tools:     "not-a-tool",
	}, auth.EmptyState())
	if err == nil {
		t.Fatal("expected invalid tools override to fail")
	}
	got := plan.Store.Meta().Continuation
	if got == nil || got.OpenAIBaseURL != "https://base.example/v1" {
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
	if err := plan.Store.SetContinuationContext(session.ContinuationContext{OpenAIBaseURL: loaded.Settings.OpenAIBaseURL}); err != nil {
		t.Fatalf("seed continuation: %v", err)
	}

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
	if updated.ActiveSettings.OpenAIBaseURL != "https://worker.example/v1" {
		t.Fatalf("openai base url = %q, want worker override", updated.ActiveSettings.OpenAIBaseURL)
	}
	got := plan.Store.Meta().Continuation
	if got == nil || got.OpenAIBaseURL != "https://worker.example/v1" || !testoption.EqualString(got.AgentRole, testoption.String("worker")) {
		t.Fatalf("continuation = %+v, want worker base url and agent role", got)
	}
}

func TestApplyRunPromptOverridesCLIModelOverrideRecomputesBudgetAfterFastRole(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)

	updated := applyRunPromptOverridesNoWarnings(t, plan, serverapi.RunPromptOverrides{
		AgentRole: config.BuiltInSubagentRoleFast,
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
