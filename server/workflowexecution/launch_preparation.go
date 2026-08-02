package workflowexecution

import (
	"errors"

	"core/server/workflow"
	"core/shared/serverapi"
)

type LaunchPreparationKind string

const (
	LaunchPreparationEstablishedRoot          LaunchPreparationKind = "established_root"
	LaunchPreparationRestoreLockedTarget      LaunchPreparationKind = "restore_locked_target"
	LaunchPreparationEstablishUnlockedNone    LaunchPreparationKind = "establish_unlocked_none"
	LaunchPreparationEstablishUnlockedManaged LaunchPreparationKind = "establish_unlocked_managed"
)

type LaunchPreparation struct {
	Kind                LaunchPreparationKind
	SourceWorkspaceID   string
	SourceWorkspaceRoot string
	Selection           workflow.ExecutionTargetSelection
	SetupOperationID    serverapi.WorktreeSetupOperationID
}

func (p LaunchPreparation) Validate() error {
	switch p.Kind {
	case LaunchPreparationEstablishedRoot:
	case LaunchPreparationRestoreLockedTarget:
		if p.SourceWorkspaceID == "" || p.SourceWorkspaceRoot == "" {
			return errors.New("locked target launch preparation requires source workspace snapshot")
		}
		if err := p.SetupOperationID.Validate(); err != nil {
			return err
		}
	case LaunchPreparationEstablishUnlockedNone:
		if p.SourceWorkspaceID == "" || p.SourceWorkspaceRoot == "" {
			return errors.New("unlocked none launch preparation requires source workspace snapshot")
		}
		if p.Selection.Mode != workflow.ExecutionTargetModeNone {
			return errors.New("unlocked none launch preparation requires none selection")
		}
		if err := p.SetupOperationID.Validate(); err != nil {
			return err
		}
	case LaunchPreparationEstablishUnlockedManaged:
		if p.SourceWorkspaceID == "" || p.SourceWorkspaceRoot == "" {
			return errors.New("unlocked managed launch preparation requires source workspace snapshot")
		}
		if err := p.Selection.Validate(); err != nil {
			return err
		}
		if p.Selection.Mode == workflow.ExecutionTargetModeNone {
			return errors.New("unlocked managed launch preparation requires managed selection")
		}
		if err := p.SetupOperationID.Validate(); err != nil {
			return err
		}
	default:
		return errors.New("launch preparation kind is invalid")
	}
	return nil
}
