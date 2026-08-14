package launch

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"core/server/auth"
	"core/server/llm"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestSupportedChatThinkingValuesUsesKnownModelContract(t *testing.T) {
	got := supportedChatThinkingValues("gpt-5", "ultra")
	if slices.Contains(got, "ultra") {
		t.Fatalf("supported thinking values = %v, unexpectedly included configured value outside the known model contract", got)
	}
}

func TestSupportedChatThinkingValuesPreservesConfiguredUnknownModelValue(t *testing.T) {
	got := supportedChatThinkingValues("custom-model", "ultra")
	if !slices.Contains(got, "ultra") {
		t.Fatalf("supported thinking values = %v, want configured unknown-model value", got)
	}
}

func TestPrepareChatAgentCatalogOrdersChoicesAndProjectsMetadata(t *testing.T) {
	settings := chatAgentCatalogSettings()
	settings.SystemPromptFiles = []config.SystemPromptFile{
		{Path: "/prompts/default.md", Scope: config.SystemPromptFileScopeWorkspaceConfig},
	}
	settings.EnabledTools[toolspec.ToolViewImage] = true
	settings.Subagents = map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: settings.Subagents[config.BuiltInSubagentRoleFast],
		"zeta": {
			Settings: config.Settings{
				Model:            "zeta-model",
				ThinkingLevel:    "high",
				SystemPromptFile: "/prompts/zeta.md",
				SystemPromptFiles: []config.SystemPromptFile{
					{Path: "/prompts/zeta.md", Scope: config.SystemPromptFileScopeSubagent},
				},
				EnabledTools: map[toolspec.ID]bool{
					toolspec.ToolExecCommand: true,
					toolspec.ToolViewImage:   true,
				},
				ModelCapabilities: config.ModelCapabilitiesOverride{
					SupportsReasoningEffort: true,
				},
			},
			Sources: map[string]string{
				"model":              "file",
				"thinking_level":     "file",
				"system_prompt_file": "file",
				"tools.ask_question": "file",
				"model_capabilities.supports_reasoning_effort": "file",
			},
			AgentCallableSet: true,
			AgentCallable:    false,
		},
		"alpha": {
			Settings: config.Settings{
				Model:         "alpha-model",
				ThinkingLevel: "low",
			},
			Sources: map[string]string{
				"model":          "file",
				"thinking_level": "file",
			},
		},
	}

	catalog, err := PrepareChatAgentCatalog(
		config.App{Settings: settings},
		auth.EmptyState(),
		ChatAgentCatalogOptions{SkipProviderReadinessValidation: true},
	)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	choices := catalog.Choices()
	if got := chatAgentChoiceRoles(choices); !slices.Equal(got, []string{"default", "fast", "alpha", "zeta"}) {
		t.Fatalf("roles = %v", got)
	}
	zeta := choices[3]
	if zeta.Model != "zeta-model" ||
		zeta.Thinking != "high" ||
		!slices.Equal(zeta.Tools, []string{"exec_command", "view_image"}) ||
		!zeta.CustomSystemPrompt ||
		!zeta.CustomCapabilities ||
		zeta.AgentCallable {
		t.Fatalf("zeta choice = %+v", zeta)
	}
	if slices.Contains(zeta.Tools, string(toolspec.ToolCompleteNode)) {
		t.Fatalf("workflow-only complete_node leaked into choice tools: %v", zeta.Tools)
	}
	zeta.Tools[0] = "mutated"
	again, ok := catalog.Lookup("zeta")
	if !ok || again.Choice.Tools[0] == "mutated" {
		t.Fatal("catalog leaked mutable choice tools")
	}
}

func TestPrepareChatAgentCatalogOmitsCompletelyEquivalentRoles(t *testing.T) {
	settings := chatAgentCatalogSettings()
	settings.Subagents = map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: settings.Subagents[config.BuiltInSubagentRoleFast],
		"equivalent": {
			Settings: config.Settings{
				Model:         settings.Model,
				ThinkingLevel: settings.ThinkingLevel,
			},
			Sources: map[string]string{
				"model":          "file",
				"thinking_level": "file",
			},
		},
		"different": {
			Settings: config.Settings{
				Model:         settings.Model,
				ThinkingLevel: settings.ThinkingLevel,
				EnabledTools: map[toolspec.ID]bool{
					toolspec.ToolAskQuestion: false,
				},
			},
			Sources: map[string]string{
				"model":              "file",
				"thinking_level":     "file",
				"tools.ask_question": "file",
			},
		},
	}
	catalog, err := PrepareChatAgentCatalog(
		config.App{Settings: settings},
		auth.EmptyState(),
		ChatAgentCatalogOptions{SkipProviderReadinessValidation: true},
	)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	roles := chatAgentChoiceRoles(catalog.Choices())
	if slices.Contains(roles, "equivalent") {
		t.Fatalf("equivalent role was not omitted: %v", roles)
	}
	if !slices.Contains(roles, "different") {
		t.Fatalf("tool-different role was omitted: %v", roles)
	}
}

func TestPrepareChatAgentCatalogUsesFinalNamedAgentProviderCapabilities(t *testing.T) {
	settings := chatAgentCatalogSettings()
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{}
	settings.Subagents = map[string]config.SubagentRole{
		"anthropic-model": {
			Settings: config.Settings{
				Model:         "claude-sonnet-4-5",
				ThinkingLevel: "high",
			},
			Sources: map[string]string{
				"model":          "file",
				"thinking_level": "file",
			},
		},
	}
	catalog, err := PrepareChatAgentCatalog(
		config.App{Settings: settings},
		auth.EmptyState(),
		ChatAgentCatalogOptions{},
	)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	entry, ok := catalog.Lookup("anthropic-model")
	if !ok {
		t.Fatal("anthropic-model entry is missing")
	}
	if entry.ProviderCapabilities.ProviderID != "anthropic" || entry.Settings.FastAvailable {
		t.Fatalf(
			"provider capabilities = %+v Fast=%t, want anthropic without Fast",
			entry.ProviderCapabilities,
			entry.Settings.FastAvailable,
		)
	}
}

func TestPrepareRunPromptFastRoleUsesCustomOpenAITransportContract(t *testing.T) {
	settings := chatAgentCatalogSettings()
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{}
	settings.OpenAIBaseURL = "http://127.0.0.1:8080/v1"
	role := config.BuiltInSubagentRoleFast

	prepared, err := PrepareRunPromptOverridesWithContext(
		config.App{Settings: settings},
		serverapi.RunPromptOverrides{AgentRole: &role},
		auth.EmptyState(),
		RunPromptPreparationContext{},
	)
	if err != nil {
		t.Fatalf("PrepareRunPromptOverridesWithContext: %v", err)
	}
	if prepared.NamedTarget == nil {
		t.Fatal("fast named target is missing")
	}
	if prepared.ProviderCapabilities.ProviderID != "openai-compatible" ||
		prepared.FastAvailable ||
		prepared.NamedTarget.Settings.PriorityRequestMode {
		t.Fatalf(
			"prepared fast target = provider %+v Fast=%t priority=%t",
			prepared.ProviderCapabilities,
			prepared.FastAvailable,
			prepared.NamedTarget.Settings.PriorityRequestMode,
		)
	}
}

func TestPrepareChatAgentCatalogClassifiesAgentPreparationFailures(t *testing.T) {
	settings := chatAgentCatalogSettings()
	settings.Subagents = map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: settings.Subagents[config.BuiltInSubagentRoleFast],
		"broken": {
			Settings: config.Settings{ThinkingLevel: " "},
			Sources:  map[string]string{"thinking_level": "file"},
		},
	}
	_, err := PrepareChatAgentCatalog(
		config.App{Settings: settings},
		auth.EmptyState(),
		ChatAgentCatalogOptions{SkipProviderReadinessValidation: true},
	)
	var typed *serverapi.ChatSettingsAgentPreparationError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T %v", err, err)
	}
	if typed.Agent != "broken" || typed.Category != serverapi.ChatSettingsAgentInvalidConfiguration {
		t.Fatalf("typed error = %+v", typed)
	}

	providerSelection := &llm.ProviderSelectionError{Model: "custom"}
	if got := classifyChatAgentPreparationError(errors.Join(llm.ErrUnsupportedProvider, providerSelection)); got != serverapi.ChatSettingsAgentProviderUnavailable {
		t.Fatalf("provider category = %q", got)
	}
	if got := classifyChatAgentPreparationError(fmt.Errorf("%w: invalid model", errInvalidPreparedConfiguration)); got != serverapi.ChatSettingsAgentInvalidConfiguration {
		t.Fatalf("configuration category = %q", got)
	}
	if got := classifyChatAgentPreparationError(ErrPatchEditToolsConflict); got != serverapi.ChatSettingsAgentInvalidConfiguration {
		t.Fatalf("tool category = %q", got)
	}
	if got := classifyChatAgentPreparationError(errors.New("unexpected")); got != serverapi.ChatSettingsAgentInternalPreparation {
		t.Fatalf("internal category = %q", got)
	}
}

func chatAgentCatalogSettings() config.Settings {
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ThinkingLevel = "medium"
	settings.Reviewer.Model = settings.Model
	settings.Reviewer.ThinkingLevel = settings.ThinkingLevel
	settings.Reviewer.ModelContextWindow = settings.ModelContextWindow
	settings.ProviderCapabilities = config.ProviderCapabilitiesOverride{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}
	settings.EnabledTools = map[toolspec.ID]bool{
		toolspec.ToolAskQuestion: true,
		toolspec.ToolExecCommand: true,
	}
	settings.Subagents = map[string]config.SubagentRole{
		config.BuiltInSubagentRoleFast: {
			Settings: config.Settings{
				Model:         "gpt-5",
				ThinkingLevel: "low",
			},
			Sources: map[string]string{
				"model":          "file",
				"thinking_level": "file",
			},
		},
	}
	return settings
}

func chatAgentChoiceRoles(choices []serverapi.ChatSettingsAgentChoice) []string {
	roles := make([]string, 0, len(choices))
	for _, choice := range choices {
		roles = append(roles, choice.Role)
	}
	return roles
}
