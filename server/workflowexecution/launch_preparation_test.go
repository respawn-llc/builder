package workflowexecution

import (
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
)

func TestLaunchPreparationVariantsValidateTheirRequiredFacts(t *testing.T) {
	setupID := serverapi.NewWorktreeSetupOperationID()
	valid := []LaunchPreparation{
		{Kind: LaunchPreparationEstablishedRoot},
		{
			Kind:                LaunchPreparationRestoreLockedTarget,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			SetupOperationID:    setupID,
		},
		{
			Kind:                LaunchPreparationEstablishUnlockedNone,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone},
			SetupOperationID:    setupID,
		},
		{
			Kind:                LaunchPreparationEstablishUnlockedManaged,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead},
			SetupOperationID:    setupID,
		},
	}
	for _, preparation := range valid {
		if err := preparation.Validate(); err != nil {
			t.Fatalf("valid launch preparation %#v rejected: %v", preparation, err)
		}
	}

	invalid := []LaunchPreparation{
		{Kind: LaunchPreparationKind("future")},
		{Kind: LaunchPreparationRestoreLockedTarget},
		{
			Kind:                LaunchPreparationEstablishUnlockedNone,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeHead},
			SetupOperationID:    setupID,
		},
		{
			Kind:                LaunchPreparationEstablishUnlockedManaged,
			SourceWorkspaceID:   "workspace",
			SourceWorkspaceRoot: "/workspace",
			Selection:           workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone},
			SetupOperationID:    setupID,
		},
	}
	for _, preparation := range invalid {
		if err := preparation.Validate(); err == nil {
			t.Fatalf("invalid launch preparation %#v validated", preparation)
		}
	}
}
