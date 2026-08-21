package app

import (
	"testing"

	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
)

func TestOnboardingFinalizeProjectsSelectedCommandImportReference(t *testing.T) {
	provider := "codex"
	root := "/source/.codex/prompts"
	state := newOnboardingFinalizeProjectionState(t, nil, emptyOnboardingCapabilityFacts())
	state.selections.commandImport = onboardingImportSelection{
		Mode: onboardingImportModeSymlinkSource,
		ChoiceRef: &capabilitypb.ImportChoiceRef{
			Mode:             capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_SYMLINK_SOURCE,
			ImportProviderId: &provider,
			SourceRootPath:   &root,
		},
	}
	request, err := onboardingFinalizeRequest(state, false)
	if err != nil {
		t.Fatalf("onboardingFinalizeRequest: %v", err)
	}
	if request.CommandsImport == nil || request.CommandsImport.Mode != onboardingpb.ImportMode_IMPORT_MODE_SYMLINK_SOURCE {
		t.Fatalf("commands import = %+v", request.CommandsImport)
	}
	if request.CommandsImport.ImportProviderId == nil || *request.CommandsImport.ImportProviderId != provider {
		t.Fatalf("provider = %+v", request.CommandsImport.ImportProviderId)
	}
}

func TestOnboardingCommandImportOptionsIncludeServerChoices(t *testing.T) {
	codexRoot := t.TempDir()
	claudeRoot := t.TempDir()
	facts := emptyOnboardingImportFacts()
	facts.Commands.Choices = []*capabilitypb.ImportChoiceFact{
		skillSymlinkChoiceFact(string(onboardingImportProviderCodex), codexRoot, 2),
		skillSymlinkChoiceFact(string(onboardingImportProviderClaudeCode), claudeRoot, 2),
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
