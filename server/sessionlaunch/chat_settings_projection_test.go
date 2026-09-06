package sessionlaunch

import (
	"reflect"
	"slices"
	"testing"

	"core/server/auth"
	"core/server/launch"
	"core/server/session"
	"core/shared/config"
	"core/shared/protoapi"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestNewChatCatalogCarriesCompletePreparedBaselines(t *testing.T) {
	app := testNewChatSettingsApp(t)
	prepared, err := launch.PrepareChatAgentCatalog(app, auth.EmptyState(), false)
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewService(launch.Planner{Config: app}).NewChatSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	catalog := response.NewChat
	if catalog == nil || response.Session != nil {
		t.Fatalf("New Chat response = %+v", response)
	}
	entries := prepared.Entries()
	if len(catalog.Choices) != len(entries) {
		t.Fatalf("choices = %d, want %d", len(catalog.Choices), len(entries))
	}
	for i, choice := range catalog.Choices {
		entry := entries[i]
		if !reflect.DeepEqual(choice.Agent, entry.Choice) ||
			choice.Baseline.AgentRole != entry.Choice.Role ||
			string(choice.Baseline.Supervisor) != entry.Settings.Baseline.Supervisor ||
			choice.Baseline.QuestionsEnabled != entry.Settings.Baseline.Questions ||
			choice.Baseline.AutoCompactionEnabled != entry.Settings.Baseline.AutoCompaction ||
			(choice.Thinking != nil) != (choice.Baseline.Thinking != nil) ||
			(choice.Fast != nil) != (choice.Baseline.Fast != nil) ||
			choice.AutoCompaction.Policy != serverapi.ChatSettingsAutoCompactionDisabled {
			t.Fatalf("choice does not preserve prepared baseline: %+v, entry %+v", choice, entry)
		}
		if choice.Baseline.Thinking != nil && *choice.Baseline.Thinking != entry.Settings.Baseline.Thinking {
			t.Fatalf("Thinking baseline = %s", *choice.Baseline.Thinking)
		}
		if choice.Baseline.Fast != nil && *choice.Baseline.Fast != entry.Settings.Baseline.Fast {
			t.Fatalf("Fast baseline = %v", *choice.Baseline.Fast)
		}
	}
	if !reflect.DeepEqual(catalog.InitialSettings, catalog.Choices[0].Baseline) {
		t.Fatalf("initial selection differs from default baseline")
	}
}

func TestNewChatCatalogRejectsIncompleteAgentSelection(t *testing.T) {
	app := testNewChatSettingsApp(t)
	response, err := NewService(launch.Planner{Config: app}).NewChatSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	target := serverapi.NewChatSettingsTarget("project", "workspace")
	generated, err := protoapi.ChatSettingsReadToProto(response, target)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := protoapi.ChatSettingsReadFromProto(generated, target)
	if err != nil || !reflect.DeepEqual(decoded.NewChat, response.NewChat) {
		t.Fatalf("catalog round trip: %+v, %v", decoded, err)
	}
	for _, choice := range generated.GetNewChat().Choices {
		if choice.Fast != nil {
			choice.Baseline.Fast = nil
			if _, err := protoapi.ChatSettingsReadFromProto(generated, target); err == nil {
				t.Fatal("catalog accepted a capable Agent without its Fast baseline selection")
			}
			return
		}
	}
	t.Fatal("fixture requires a Fast-capable Agent")
}

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
	catalog, err := launch.PrepareChatAgentCatalog(testChatSettingsApp(), auth.EmptyState(), true)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	return catalog
}

func testChatSettingsApp() config.App {
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
	return config.App{Settings: settings}
}

func testNewChatSettingsApp(t *testing.T) config.App {
	t.Helper()
	app := loadSessionLaunchTestConfig(t, t.TempDir(), t.TempDir())
	settings := testChatSettingsApp().Settings
	app.Settings.Model = settings.Model
	app.Settings.ThinkingLevel = settings.ThinkingLevel
	app.Settings.Subagents = settings.Subagents
	app.Settings.EnabledTools = settings.EnabledTools
	app.Settings.CompactionMode = config.CompactionModeNone
	return app
}

func choiceRoles(choices []serverapi.ChatSettingsAgentChoice) []string {
	roles := make([]string, len(choices))
	for i, choice := range choices {
		roles[i] = choice.Role
	}
	return roles
}
