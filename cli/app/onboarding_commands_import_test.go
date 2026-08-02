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
