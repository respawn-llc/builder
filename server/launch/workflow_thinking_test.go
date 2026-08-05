package launch

import (
	"testing"

	"core/server/auth"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestApplyRunPromptOverridesAppliesWorkflowThinkingAfterRoleResolution(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	loaded.Settings.Subagents["reviewer"] = config.SubagentRole{
		Description: "Reviewer",
		Settings:    config.Settings{Model: "workflow-reviewer"},
		Sources:     map[string]string{"model": "test"},
	}
	plan := newLoadedConfigPlan(t, workspace, loaded)
	role := "reviewer"
	thinking, err := workflow.NewThinkingValue("max")
	if err != nil {
		t.Fatalf("NewThinkingValue: %v", err)
	}
	planner, err := testPlannerForPlan(plan)
	if err != nil {
		t.Fatalf("testPlannerForPlan: %v", err)
	}
	store := testStoreForPlanForOverride(plan)
	updated, _, err := planner.ApplyRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{AgentRole: &role},
		auth.EmptyState(),
		RunPromptOverrideOptions{WorkflowThinking: SetWorkflowThinking(thinking)},
	)
	if err != nil {
		t.Fatalf("ApplyRunPromptOverridesWithStore: %v", err)
	}
	if updated.ActiveSettings.Model != "workflow-reviewer" {
		t.Fatalf("model = %q, want workflow-reviewer", updated.ActiveSettings.Model)
	}
	if updated.ActiveSettings.ThinkingLevel != "max" {
		t.Fatalf("thinking level = %q, want max", updated.ActiveSettings.ThinkingLevel)
	}
}

func TestApplyRunPromptOverridesClearsWorkflowThinking(t *testing.T) {
	workspace := t.TempDir()
	loaded := loadLaunchConfig(t, workspace)
	plan := newLoadedConfigPlan(t, workspace, loaded)
	planner, err := testPlannerForPlan(plan)
	if err != nil {
		t.Fatalf("testPlannerForPlan: %v", err)
	}
	store := testStoreForPlanForOverride(plan)
	updated, _, err := planner.ApplyRunPromptOverridesWithStore(
		plan,
		store,
		serverapi.RunPromptOverrides{},
		auth.EmptyState(),
		RunPromptOverrideOptions{WorkflowThinking: ClearWorkflowThinking()},
	)
	if err != nil {
		t.Fatalf("ApplyRunPromptOverridesWithStore: %v", err)
	}
	if updated.ActiveSettings.ThinkingLevel != "" {
		t.Fatalf("thinking level = %q, want cleared", updated.ActiveSettings.ThinkingLevel)
	}
}
