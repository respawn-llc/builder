package app

import (
	"testing"

	"core/shared/serverapi"
)

func TestOnboardingFinalizeProjectsSelectedCommandImportReference(t *testing.T) {
	provider := "codex"
	root := "/source/.codex/prompts"
	state := newOnboardingFinalizeProjectionState(t, nil, serverapi.CapabilityFactsResponse{})
	state.selections.commandImport = onboardingImportSelection{
		Mode: onboardingImportModeSymlinkSource,
		ChoiceRef: serverapi.ImportChoiceRef{
			Mode:             string(onboardingImportModeSymlinkSource),
			ImportProviderID: &provider,
			SourceRootPath:   &root,
		},
	}
	request, err := onboardingFinalizeRequest(state, false)
	if err != nil {
		t.Fatalf("onboardingFinalizeRequest: %v", err)
	}
	if request.CommandsImport == nil || request.CommandsImport.Mode != serverapi.OnboardingImportModeSymlinkSource {
		t.Fatalf("commands import = %+v", request.CommandsImport)
	}
	if request.CommandsImport.ImportProviderID == nil || *request.CommandsImport.ImportProviderID != provider {
		t.Fatalf("provider = %+v", request.CommandsImport.ImportProviderID)
	}
}

func TestOnboardingCommandImportOptionsIncludeServerChoices(t *testing.T) {
	codexRoot := t.TempDir()
	claudeRoot := t.TempDir()
	facts := serverapi.ImportCapabilityFacts{
		Commands: serverapi.ImportItemGroupFact{
			Choices: []serverapi.ImportChoiceFact{
				skillSymlinkChoiceFact(string(onboardingImportProviderCodex), codexRoot, 2),
				skillSymlinkChoiceFact(string(onboardingImportProviderClaudeCode), claudeRoot, 2),
			},
		},
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscoveryFromFacts(facts)

	screen := buildCommandImportScreen(state)
	if len(screen.Options) != 3 {
		t.Fatalf("command import options = %+v, want none plus two providers", screen.Options)
	}
	for _, choice := range state.imports.commandChoices {
		if choice.Mode == onboardingImportModeSymlinkSource && choice.Count > 0 &&
			!containsOnboardingOption(screen.Options, choice.OptionID) {
			t.Fatalf("command import choice omitted from options: %+v", choice)
		}
	}
}
