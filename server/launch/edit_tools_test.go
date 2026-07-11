package launch

import (
	"errors"
	"testing"

	"core/server/auth"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestActiveToolIDsDynamicDefaultChoosesPatchForGPTModels(t *testing.T) {
	settings := validLaunchSettings("gpt-5.6-sol")
	source := defaultToolSources()

	ids, err := ActiveToolIDsForPlan(settings, source, nil)
	if err != nil {
		t.Fatalf("ActiveToolIDsForPlan: %v", err)
	}
	if !containsTool(ids, toolspec.ToolPatch) || containsTool(ids, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want patch without edit", ids)
	}
}

func TestActiveToolIDsDynamicDefaultChoosesEditForNonGPTModels(t *testing.T) {
	settings := validLaunchSettings("claude-sonnet-4.5")
	source := defaultToolSources()

	ids, err := ActiveToolIDsForPlan(settings, source, nil)
	if err != nil {
		t.Fatalf("ActiveToolIDsForPlan: %v", err)
	}
	if containsTool(ids, toolspec.ToolPatch) || !containsTool(ids, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want edit without patch", ids)
	}
}

func TestActiveToolIDsRejectsEffectivePatchAndEdit(t *testing.T) {
	settings := validLaunchSettings("claude-sonnet-4.5")
	settings.EnabledTools[toolspec.ToolEdit] = true
	source := defaultToolSources()
	source.Sources["tools.edit"] = "file"

	_, err := ActiveToolIDsForPlan(settings, source, nil)
	if err == nil || !errors.Is(err, ErrPatchEditToolsConflict) {
		t.Fatalf("error = %v, want mutual exclusion failure", err)
	}
}

func TestActiveToolIDsLockedSessionPreservesPatchAndEdit(t *testing.T) {
	settings := validLaunchSettings("claude-sonnet-4.5")
	locked := &session.LockedContract{EnabledTools: []string{"patch", "edit"}}

	ids, err := ActiveToolIDsForPlan(settings, defaultToolSources(), locked)
	if err != nil {
		t.Fatalf("ActiveToolIDsForPlan: %v", err)
	}
	if !containsTool(ids, toolspec.ToolPatch) || !containsTool(ids, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want locked patch+edit preserved", ids)
	}
}

func TestActiveToolIDsLockedSessionPreservesExplicitZeroTools(t *testing.T) {
	settings := validLaunchSettings("gpt-5.6-sol")
	locked := &session.LockedContract{HasEnabledTools: true}

	ids, err := ActiveToolIDsForPlan(settings, defaultToolSources(), locked)
	if err != nil {
		t.Fatalf("ActiveToolIDsForPlan: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("enabled tools = %+v, want explicit zero-tool lock", ids)
	}
}

func TestActiveToolIDsLegacyMissingLockUsesEffectiveConfig(t *testing.T) {
	settings := validLaunchSettings("gpt-5.6-sol")
	locked := &session.LockedContract{}

	ids, err := ActiveToolIDsForPlan(settings, defaultToolSources(), locked)
	if err != nil {
		t.Fatalf("ActiveToolIDsForPlan: %v", err)
	}
	if !containsTool(ids, toolspec.ToolPatch) {
		t.Fatalf("enabled tools = %+v, want effective config fallback for legacy missing lock", ids)
	}
}

func TestApplyRunPromptOverridesSubagentExplicitEditToolWins(t *testing.T) {
	store := createTestSession(t, t.TempDir())
	settings := validLaunchSettings("gpt-5.6-sol")
	settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: config.Settings{
				Model: "gpt-5.6-sol",
				EnabledTools: map[toolspec.ID]bool{
					toolspec.ToolPatch: false,
					toolspec.ToolEdit:  true,
				},
			},
			Sources: map[string]string{
				"tools.patch": "file",
				"tools.edit":  "file",
			},
		},
	}
	plan := SessionPlan{
		Store:          store,
		ActiveSettings: settings,
		EnabledTools:   []toolspec.ID{toolspec.ToolPatch},
		Source:         defaultToolSources(),
	}

	updated, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: "worker"}, auth.EmptyState())
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides: %v", err)
	}
	if containsTool(updated.EnabledTools, toolspec.ToolPatch) || !containsTool(updated.EnabledTools, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want explicit subagent edit without patch", updated.EnabledTools)
	}
}

func TestApplyRunPromptOverridesSubagentToolSourceSurvivesModelOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := createTestSession(t, t.TempDir())
	settings := validLaunchSettings("gpt-5.6-sol")
	settings.Subagents = map[string]config.SubagentRole{
		"worker": {
			Settings: config.Settings{
				Model: "gpt-5.6-sol",
				EnabledTools: map[toolspec.ID]bool{
					toolspec.ToolPatch: false,
					toolspec.ToolEdit:  true,
				},
			},
			Sources: map[string]string{
				"tools.patch": "file",
				"tools.edit":  "file",
			},
		},
	}
	plan := SessionPlan{
		Store:          store,
		ActiveSettings: settings,
		EnabledTools:   []toolspec.ID{toolspec.ToolPatch},
		WorkspaceRoot:  t.TempDir(),
		Source:         defaultToolSources(),
	}

	updated, _, err := ApplyRunPromptOverrides(plan, serverapi.RunPromptOverrides{AgentRole: "worker", Model: "gpt-5.6-sol"}, auth.EmptyState())
	if err != nil {
		t.Fatalf("ApplyRunPromptOverrides: %v", err)
	}
	if containsTool(updated.EnabledTools, toolspec.ToolPatch) || !containsTool(updated.EnabledTools, toolspec.ToolEdit) {
		t.Fatalf("enabled tools = %+v, want explicit subagent edit preserved across model override", updated.EnabledTools)
	}
	if updated.Source.Sources["tools.edit"] != "subagent" || updated.Source.Sources["tools.patch"] != "subagent" {
		t.Fatalf("tool sources = %+v, want subagent markers preserved", updated.Source.Sources)
	}
}

func defaultToolSources() config.SourceReport {
	sources := map[string]string{}
	for _, id := range toolspec.CatalogIDs() {
		sources["tools."+toolspec.ConfigName(id)] = "default"
	}
	return config.SourceReport{Sources: sources}
}

func validLaunchSettings(model string) config.Settings {
	return config.Settings{
		Model:                            model,
		ThinkingLevel:                    "medium",
		NotificationMethod:               "auto",
		Theme:                            "auto",
		WebSearch:                        "native",
		ServerHost:                       "127.0.0.1",
		ServerPort:                       53082,
		Reviewer:                         config.ReviewerSettings{Frequency: "edits", Model: model, ThinkingLevel: "medium", ModelContextWindow: 272000, TimeoutSeconds: 60},
		Timeouts:                         config.Timeouts{ModelRequestSeconds: 400},
		ShellOutputMaxChars:              16000,
		MinimumExecToBgSeconds:           15,
		CompactionMode:                   "local",
		BGShellsOutput:                   "default",
		Shell:                            config.ShellSettings{PostprocessingMode: "builtin"},
		CacheWarningMode:                 "default",
		ModelContextWindow:               272000,
		ContextCompactionThresholdTokens: 258400,
		PreSubmitCompactionLeadTokens:    35000,
		PreventSleep:                     config.SleepPreventionModeNever,
		EnabledTools: map[toolspec.ID]bool{
			toolspec.ToolPatch: true,
			toolspec.ToolEdit:  false,
		},
	}
}

func containsTool(ids []toolspec.ID, target toolspec.ID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
