package sessionlaunch

import (
	"reflect"
	"testing"

	"core/server/auth"
	"core/server/launch"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestProjectChatSettingsRepairsUnlockedAgentsAndInvalidControls(t *testing.T) {
	catalog := projectionCatalog(t)
	tests := []struct {
		name     string
		input    ChatSettingsProjectionInput
		wantRole string
		want     session.ChatSettings
	}{
		{
			name: "available Agent retains valid controls",
			input: ChatSettingsProjectionInput{
				Catalog: catalog,
				Agent:   "worker",
				Settings: session.ChatSettings{
					Supervisor:     "all",
					Thinking:       "high",
					Fast:           false,
					Questions:      true,
					AutoCompaction: false,
				},
				PersistedQuestionsPolicy: true,
				CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
			},
			wantRole: "worker",
			want: session.ChatSettings{
				Supervisor:     "all",
				Thinking:       "high",
				Fast:           false,
				Questions:      true,
				AutoCompaction: false,
			},
		},
		{
			name: "unavailable Agent repairs to default complete baseline",
			input: ChatSettingsProjectionInput{
				Catalog:                  catalog,
				Agent:                    "removed",
				Settings:                 session.ChatSettings{Supervisor: "all", Thinking: "high", Fast: true},
				PersistedQuestionsPolicy: false,
				CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
			},
			wantRole: "default",
			want:     catalogBaseline(t, catalog, "default"),
		},
		{
			name: "available Agent preserves custom Thinking and repairs Fast",
			input: ChatSettingsProjectionInput{
				Catalog: catalog,
				Agent:   "worker",
				Settings: session.ChatSettings{
					Supervisor:     "off",
					Thinking:       "invalid",
					Fast:           true,
					Questions:      false,
					AutoCompaction: true,
				},
				PersistedQuestionsPolicy: false,
				CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
			},
			wantRole: "worker",
			want: session.ChatSettings{
				Supervisor:     "off",
				Thinking:       "invalid",
				Fast:           false,
				Questions:      false,
				AutoCompaction: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ProjectChatSettings(test.input)
			if err != nil {
				t.Fatalf("ProjectChatSettings: %v", err)
			}
			if got.SelectedAgent.Role != test.wantRole {
				t.Fatalf("selected role = %q, want %q", got.SelectedAgent.Role, test.wantRole)
			}
			if got.Supervisor.Value != serverapi.ChatSettingsSupervisorValue(test.want.Supervisor) ||
				got.Thinking.Value != test.want.Thinking ||
				got.Questions.Enabled != test.want.Questions ||
				got.AutoCompaction.Stored != test.want.AutoCompaction {
				t.Fatalf("projected settings = %+v, want %+v", got, test.want)
			}
			if got.Fast != nil && got.Fast.Value != test.want.Fast {
				t.Fatalf("Fast = %+v, want %t", got.Fast, test.want.Fast)
			}
		})
	}
}

func TestProjectChatSettingsSeparatesQuestionsCapabilityAndPolicy(t *testing.T) {
	catalog := projectionCatalog(t)
	input := ChatSettingsProjectionInput{
		Catalog: catalog,
		Agent:   "no-questions",
		Settings: session.ChatSettings{
			Supervisor:     "edits",
			Thinking:       "medium",
			Questions:      false,
			AutoCompaction: true,
		},
		PersistedQuestionsPolicy: true,
		CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
	}
	got, err := ProjectChatSettings(input)
	if err != nil {
		t.Fatalf("ProjectChatSettings: %v", err)
	}
	if got.Questions.Capable || !got.Questions.Enabled ||
		got.Questions.Editability != serverapi.ChatSettingsEditable {
		t.Fatalf("Questions = %+v", got.Questions)
	}
}

func TestProjectChatSettingsTrimsCustomThinkingAndReturnsCustomMode(t *testing.T) {
	catalog := projectionCatalog(t)
	got, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog:                  catalog,
		Agent:                    "worker",
		Settings:                 session.ChatSettings{Supervisor: "edits", Thinking: "  provider-depth  "},
		PersistedQuestionsPolicy: false,
		CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
	})
	if err != nil {
		t.Fatalf("ProjectChatSettings: %v", err)
	}
	if got.SelectedAgent.Thinking != "provider-depth" ||
		got.Thinking == nil ||
		got.Thinking.Kind != serverapi.ChatSettingsThinkingCustom ||
		got.Thinking.Value != "provider-depth" ||
		got.Thinking.BaselineValue != "high" {
		t.Fatalf("Thinking = %+v, selected = %+v", got.Thinking, got.SelectedAgent)
	}
}

func TestProjectChatSettingsAppliesBlockerAndCompactionMatrix(t *testing.T) {
	catalog := projectionCatalog(t)
	tests := []struct {
		name           string
		workflowLocked bool
		cachingLocked  bool
		policy         serverapi.ChatSettingsAutoCompactionPolicy
		stored         bool
		wantAgent      serverapi.ChatSettingsEditability
		wantCompaction serverapi.ChatSettingsEditability
		wantEffective  bool
	}{
		{
			name:           "editable optional",
			policy:         serverapi.ChatSettingsAutoCompactionOptional,
			stored:         false,
			wantAgent:      serverapi.ChatSettingsEditable,
			wantCompaction: serverapi.ChatSettingsEditable,
		},
		{
			name:           "workflow required",
			workflowLocked: true,
			policy:         serverapi.ChatSettingsAutoCompactionRequired,
			stored:         false,
			wantAgent:      serverapi.ChatSettingsWorkflowLock,
			wantCompaction: serverapi.ChatSettingsWorkflowLock,
			wantEffective:  true,
		},
		{
			name:           "caching does not block controls",
			cachingLocked:  true,
			policy:         serverapi.ChatSettingsAutoCompactionOptional,
			stored:         true,
			wantAgent:      serverapi.ChatSettingsCachingLock,
			wantCompaction: serverapi.ChatSettingsEditable,
			wantEffective:  true,
		},
		{
			name:           "disabled wins workflow",
			workflowLocked: true,
			policy:         serverapi.ChatSettingsAutoCompactionDisabled,
			stored:         true,
			wantAgent:      serverapi.ChatSettingsWorkflowLock,
			wantCompaction: serverapi.ChatSettingsPolicyDisabled,
			wantEffective:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := catalogBaseline(t, catalog, "default")
			settings.AutoCompaction = test.stored
			var locked *session.LockedContract
			if test.cachingLocked {
				locked = &session.LockedContract{
					Model: "gpt-5",
					ProviderContract: session.LockedProviderCapabilities{
						ProviderID: "anthropic",
					},
				}
			}
			got, err := ProjectChatSettings(ChatSettingsProjectionInput{
				Catalog:                  catalog,
				Agent:                    "default",
				Settings:                 settings,
				WorkflowLocked:           test.workflowLocked,
				CachingLocked:            test.cachingLocked,
				CompactionPolicy:         test.policy,
				PersistedQuestionsPolicy: true,
				Locked:                   locked,
			})
			if err != nil {
				t.Fatalf("ProjectChatSettings: %v", err)
			}
			if got.AgentEditability != test.wantAgent ||
				got.AutoCompaction.Editability != test.wantCompaction ||
				got.AutoCompaction.Stored != test.stored ||
				got.AutoCompaction.Effective != test.wantEffective {
				t.Fatalf("projection = %+v", got)
			}
			if got.Supervisor.Editability != serverapi.ChatSettingsEditable ||
				got.Thinking.Editability != serverapi.ChatSettingsEditable ||
				got.Questions.Editability != serverapi.ChatSettingsEditable {
				t.Fatal("independent control was locked")
			}
		})
	}
}

func TestProjectChatSettingsDoesNotMutateInputs(t *testing.T) {
	catalog := projectionCatalog(t)
	before := catalog.Entries()
	input := ChatSettingsProjectionInput{
		Catalog:                  catalog,
		Agent:                    "worker",
		Settings:                 catalogBaseline(t, catalog, "worker"),
		PersistedQuestionsPolicy: true,
		CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
	}
	if _, err := ProjectChatSettings(input); err != nil {
		t.Fatalf("ProjectChatSettings: %v", err)
	}
	if !reflect.DeepEqual(before, catalog.Entries()) {
		t.Fatal("projector mutated prepared catalog")
	}
}

func TestProjectChatSettingsUsesLockedCapabilityAuthorities(t *testing.T) {
	catalog := projectionCatalog(t)
	current := catalogBaseline(t, catalog, "default")
	current.Thinking = "high"
	current.Fast = true
	current.Questions = true
	locked := &session.LockedContract{
		Model: "gpt-5",
		EnabledTools: []string{
			string(toolspec.ToolAskQuestion),
		},
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID:           "openai",
			SupportsResponsesAPI: true,
			IsOpenAIFirstParty:   true,
		},
		ModelCapabilities: session.LockedModelCapabilities{
			SupportsReasoningEffort: true,
		},
	}
	got, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog:                  catalog,
		Agent:                    "default",
		Settings:                 current,
		PersistedQuestionsPolicy: false,
		CachingLocked:            true,
		CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
		Locked:                   locked,
	})
	if err != nil {
		t.Fatalf("ProjectChatSettings: %v", err)
	}
	if got.SelectedAgent.Model != "gpt-5" ||
		got.Thinking == nil ||
		got.Fast == nil ||
		!got.Fast.Value ||
		!got.Questions.Capable ||
		got.Questions.Enabled {
		t.Fatalf(
			"locked projection model=%q thinking=%+v fast=%+v questions=%+v",
			got.SelectedAgent.Model,
			got.Thinking,
			got.Fast,
			got.Questions,
		)
	}
}

func TestProjectChatSettingsTreatsEmptyLockedToolsAsAuthoritative(t *testing.T) {
	catalog := projectionCatalog(t)
	locked := &session.LockedContract{
		Model:           "gpt-5",
		HasEnabledTools: false,
		EnabledTools:    nil,
		ProviderContract: session.LockedProviderCapabilities{
			ProviderID: "anthropic",
		},
	}
	got, err := ProjectChatSettings(ChatSettingsProjectionInput{
		Catalog:                  catalog,
		Agent:                    "historical",
		Settings:                 catalogBaseline(t, catalog, "default"),
		PersistedQuestionsPolicy: true,
		CachingLocked:            true,
		CompactionPolicy:         serverapi.ChatSettingsAutoCompactionOptional,
		Locked:                   locked,
	})
	if err != nil {
		t.Fatalf("ProjectChatSettings: %v", err)
	}
	if got.SelectedAgent.Role != "historical" ||
		got.Questions.Capable ||
		!got.Questions.Enabled {
		t.Fatalf("locked zero-tool projection = %+v", got)
	}
}

func projectionCatalog(t *testing.T) launch.PreparedChatAgentCatalog {
	t.Helper()
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ThinkingLevel = "medium"
	settings.Reviewer.Model = settings.Model
	settings.Reviewer.ThinkingLevel = settings.ThinkingLevel
	settings.Reviewer.ModelContextWindow = settings.ModelContextWindow
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID: "anthropic",
	}
	settings.EnabledTools = map[toolspec.ID]bool{
		toolspec.ToolAskQuestion: true,
		toolspec.ToolExecCommand: true,
	}
	settings.Subagents = map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: {
			Settings: config.Settings{Model: "gpt-5", ThinkingLevel: "low"},
			Sources: map[string]string{
				"model":          "file",
				"thinking_level": "file",
			},
		},
		"worker": {
			Settings: config.Settings{
				Model:         "gpt-5",
				ThinkingLevel: "high",
				Reviewer:      config.ReviewerSettings{Frequency: "all"},
			},
			Sources: map[string]string{
				"thinking_level":     "file",
				"reviewer.frequency": "file",
			},
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
		launch.ChatAgentCatalogOptions{},
	)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	return catalog
}

func catalogBaseline(
	t *testing.T,
	catalog launch.PreparedChatAgentCatalog,
	agent string,
) session.ChatSettings {
	t.Helper()
	entry, ok := catalog.Lookup(agent)
	if !ok {
		t.Fatalf("catalog entry %q is missing", agent)
	}
	return entry.Settings.Baseline
}
