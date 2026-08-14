package sessionlaunch

import (
	"slices"
	"testing"

	"core/server/auth"
	"core/server/launch"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestProjectChatSettingsAuthoritativeReadSemantics(t *testing.T) {
	catalog := testChatSettingsCatalog(t)
	baseline, _ := catalog.Lookup(config.DefaultSubagentRole)

	repaired, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog: catalog,
		Agent:   "removed",
		Settings: session.ChatSettings{
			Supervisor: "all", Thinking: "high", Fast: true, Questions: false,
		},
		WorkflowLocked: true,
	})
	if err != nil {
		t.Fatalf("repair unavailable Agent: %v", err)
	}
	if repaired.SelectedAgent.Role != "default" ||
		repaired.SelectedAgent.Thinking != baseline.Settings.Baseline.Thinking ||
		repaired.AgentEditability != serverapi.ChatSettingsWorkflowLock ||
		!repaired.AutoCompaction.Effective ||
		repaired.AutoCompaction.Policy != serverapi.ChatSettingsAutoCompactionRequired {
		t.Fatalf("repaired projection = %+v", repaired)
	}

	custom, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog: catalog,
		Agent:   "no-questions",
		Settings: session.ChatSettings{
			Supervisor: "edits", Thinking: "provider-depth", Fast: true, Questions: true,
		},
	})
	if err != nil {
		t.Fatalf("project custom settings: %v", err)
	}
	if custom.Thinking == nil ||
		custom.Thinking.Kind != serverapi.ChatSettingsThinkingCustom ||
		custom.Fast == nil || !custom.Fast.Value ||
		custom.Questions.Capable ||
		!custom.Questions.Enabled {
		t.Fatalf("custom projection = %+v", custom)
	}

	locked, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog: catalog,
		Agent:   "historical",
		Settings: session.ChatSettings{
			Supervisor: "off", Thinking: "high", Fast: true, Questions: true,
			AutoCompaction: true,
		},
		Locked: &session.LockedContract{
			Model:        "gpt-5",
			EnabledTools: []string{},
			ProviderContract: session.LockedProviderCapabilities{
				ProviderID: "anthropic",
			},
		},
	})
	if err != nil {
		t.Fatalf("project caching lock: %v", err)
	}
	if locked.SelectedAgent.Role != "historical" ||
		locked.SelectedAgent.Model != "gpt-5" ||
		locked.AgentEditability != serverapi.ChatSettingsCachingLock ||
		!locked.AgentLocked || !locked.CachingLocked ||
		locked.Questions.Capable || !locked.Questions.Enabled ||
		locked.Fast != nil {
		t.Fatalf("locked projection = %+v", locked)
	}

	disabled, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog:        catalog,
		Agent:          "default",
		Settings:       baseline.Settings.Baseline,
		CompactionMode: config.CompactionModeNone,
	})
	if err != nil {
		t.Fatalf("project disabled compaction: %v", err)
	}
	if disabled.AutoCompaction.Policy != serverapi.ChatSettingsAutoCompactionDisabled ||
		disabled.AutoCompaction.Effective ||
		disabled.AutoCompaction.Stored != baseline.Settings.Baseline.AutoCompaction ||
		disabled.AutoCompaction.Editability != serverapi.ChatSettingsPolicyDisabled {
		t.Fatalf("disabled compaction = %+v", disabled.AutoCompaction)
	}
	if got := choiceRoles(disabled.AgentChoices); !slices.Equal(
		got,
		[]string{"default", "fast", "no-questions"},
	) {
		t.Fatalf("choice roles = %v", got)
	}
}

func testChatSettingsCatalog(t *testing.T) launch.PreparedChatAgentCatalog {
	t.Helper()
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ThinkingLevel = "medium"
	settings.ProviderCapabilities.ProviderID = "anthropic"
	settings.EnabledTools = map[toolspec.ID]bool{
		toolspec.ToolAskQuestion: true,
		toolspec.ToolExecCommand: true,
	}
	settings.Subagents = map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: {
			Settings: config.Settings{Model: "gpt-5", ThinkingLevel: "low"},
			Sources:  map[string]string{"model": "file", "thinking_level": "file"},
		},
		"no-questions": {
			Settings: config.Settings{
				EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: false},
			},
			Sources: map[string]string{"tools.ask_question": "file"},
		},
	}
	catalog, err := launch.PrepareChatAgentCatalog(
		config.App{Settings: settings},
		auth.EmptyState(),
		true,
	)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	return catalog
}

func choiceRoles(choices []serverapi.ChatSettingsAgentChoice) []string {
	roles := make([]string, len(choices))
	for i, choice := range choices {
		roles[i] = choice.Role
	}
	return roles
}
