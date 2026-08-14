package launch

import (
	"errors"
	"slices"
	"testing"

	"core/server/auth"
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

func TestPrepareChatAgentCatalogProjectsChoicesAndOmitsEquivalentAgents(t *testing.T) {
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ThinkingLevel = "medium"
	settings.ProviderCapabilities.ProviderID = "anthropic"
	settings.EnabledTools = map[toolspec.ID]bool{
		toolspec.ToolExecCommand: true,
		toolspec.ToolViewImage:   true,
	}
	settings.Subagents = map[string]config.SubagentRole{
		"equivalent": {
			Settings: config.Settings{Model: "gpt-5", ThinkingLevel: "medium"},
			Sources:  map[string]string{"model": "file", "thinking_level": "file"},
		},
		"worker": {
			Settings: config.Settings{
				Model:            "worker-model",
				ThinkingLevel:    "high",
				SystemPromptFile: "/worker.md",
				SystemPromptFiles: []config.SystemPromptFile{{
					Path: "/worker.md", Scope: config.SystemPromptFileScopeSubagent,
				}},
				EnabledTools: map[toolspec.ID]bool{
					toolspec.ToolExecCommand: true,
					toolspec.ToolViewImage:   true,
				},
				ModelCapabilities: config.ModelCapabilitiesOverride{
					SupportsReasoningEffort: true,
				},
			},
			Sources: map[string]string{
				"model": "file", "thinking_level": "file", "system_prompt_file": "file",
				"model_capabilities.supports_reasoning_effort": "file",
			},
			AgentCallableSet: true,
			AgentCallable:    false,
		},
	}
	catalog, err := PrepareChatAgentCatalog(config.App{Settings: settings}, auth.EmptyState(), true)
	if err != nil {
		t.Fatalf("PrepareChatAgentCatalog: %v", err)
	}
	choices := catalog.Choices()
	if len(choices) != 2 || choices[0].Role != "default" || choices[1].Role != "worker" {
		t.Fatalf("choices = %+v", choices)
	}
	worker := choices[1]
	if worker.Model != "worker-model" || worker.Thinking != "high" ||
		!worker.CustomSystemPrompt || !worker.CustomCapabilities || worker.AgentCallable ||
		!slices.Equal(worker.Tools, []string{"exec_command", "view_image"}) {
		t.Fatalf("worker = %+v", worker)
	}

	settings.Subagents["broken"] = config.SubagentRole{
		Settings: config.Settings{ThinkingLevel: " "},
		Sources:  map[string]string{"thinking_level": "file"},
	}
	_, err = PrepareChatAgentCatalog(config.App{Settings: settings}, auth.EmptyState(), true)
	var typed *serverapi.ChatSettingsAgentPreparationError
	if !errors.As(err, &typed) ||
		typed.Agent != "broken" ||
		typed.Category != serverapi.ChatSettingsAgentInvalidConfiguration {
		t.Fatalf("preparation error = %T %v", err, err)
	}
}
