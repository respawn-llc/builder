package workflowexecution

import (
	"context"
	"errors"
	"sync"

	"core/server/workflow"
	"core/server/workflowstore"
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
	Coordinator         *LaunchPreparationCoordinator
}

type LaunchTargetPreparer interface {
	PrepareExecutionTarget(context.Context, workflow.CurrentNodeReference, LaunchPreparation) (workflowstore.ExecutionRoot, error)
}

type LaunchPreparationCoordinator struct {
	once sync.Once
	root workflowstore.ExecutionRoot
	err  error
}

func NewLaunchPreparationCoordinator() *LaunchPreparationCoordinator {
	return &LaunchPreparationCoordinator{}
}

func (c *LaunchPreparationCoordinator) Prepare(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	preparation LaunchPreparation,
	preparer LaunchTargetPreparer,
) (workflowstore.ExecutionRoot, error) {
	if preparer == nil {
		return workflowstore.ExecutionRoot{}, errors.New("execution target preparer is required")
	}
	c.once.Do(func() {
		c.root, c.err = preparer.PrepareExecutionTarget(ctx, reference, preparation)
	})
	return c.root, c.err
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
