package workflowexecution

import (
	"context"
	"errors"
	"sync"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

type LaunchSourceWorkspaceSnapshot struct {
	ID   string
	Root string
}

type RestoreLockedTargetLaunchPreparation struct {
	SourceWorkspace  LaunchSourceWorkspaceSnapshot
	SetupOperationID serverapi.WorktreeSetupOperationID
}

type EstablishUnlockedNoneLaunchPreparation struct {
	SourceWorkspace  LaunchSourceWorkspaceSnapshot
	SetupOperationID serverapi.WorktreeSetupOperationID
}

type EstablishUnlockedManagedLaunchPreparation struct {
	SourceWorkspace  LaunchSourceWorkspaceSnapshot
	Selection        workflow.ExecutionTargetSelection
	SelectionSource  ExecutionTargetSelectionSource
	SetupOperationID serverapi.WorktreeSetupOperationID
}

type launchPreparationVariant interface {
	launchPreparationVariant()
}

type establishedRootLaunchPreparation struct{}

func (establishedRootLaunchPreparation) launchPreparationVariant() {}

func (RestoreLockedTargetLaunchPreparation) launchPreparationVariant() {}

func (EstablishUnlockedNoneLaunchPreparation) launchPreparationVariant() {}

func (EstablishUnlockedManagedLaunchPreparation) launchPreparationVariant() {}

// LaunchPreparation is a closed volatile union. Its constructors make
// cross-variant field combinations unrepresentable.
type LaunchPreparation struct {
	variant     launchPreparationVariant
	coordinator *LaunchPreparationCoordinator
}

func EstablishedRootLaunchPreparation() LaunchPreparation {
	return LaunchPreparation{variant: establishedRootLaunchPreparation{}}
}

func NewRestoreLockedTargetLaunchPreparation(
	sourceWorkspace LaunchSourceWorkspaceSnapshot,
	setupOperationID serverapi.WorktreeSetupOperationID,
	coordinator *LaunchPreparationCoordinator,
) LaunchPreparation {
	return LaunchPreparation{
		variant: RestoreLockedTargetLaunchPreparation{
			SourceWorkspace:  sourceWorkspace,
			SetupOperationID: setupOperationID,
		},
		coordinator: coordinator,
	}
}

func NewEstablishUnlockedNoneLaunchPreparation(
	sourceWorkspace LaunchSourceWorkspaceSnapshot,
	setupOperationID serverapi.WorktreeSetupOperationID,
	coordinator *LaunchPreparationCoordinator,
) LaunchPreparation {
	return LaunchPreparation{
		variant: EstablishUnlockedNoneLaunchPreparation{
			SourceWorkspace:  sourceWorkspace,
			SetupOperationID: setupOperationID,
		},
		coordinator: coordinator,
	}
}

func NewEstablishUnlockedManagedLaunchPreparation(
	sourceWorkspace LaunchSourceWorkspaceSnapshot,
	selection workflow.ExecutionTargetSelection,
	selectionSource ExecutionTargetSelectionSource,
	setupOperationID serverapi.WorktreeSetupOperationID,
	coordinator *LaunchPreparationCoordinator,
) LaunchPreparation {
	return LaunchPreparation{
		variant: EstablishUnlockedManagedLaunchPreparation{
			SourceWorkspace:  sourceWorkspace,
			Selection:        selection,
			SelectionSource:  selectionSource,
			SetupOperationID: setupOperationID,
		},
		coordinator: coordinator,
	}
}

func (p LaunchPreparation) IsEstablishedRoot() bool {
	_, ok := p.variant.(establishedRootLaunchPreparation)
	return ok
}

func (p LaunchPreparation) RestoreLockedTarget() (RestoreLockedTargetLaunchPreparation, bool) {
	preparation, ok := p.variant.(RestoreLockedTargetLaunchPreparation)
	return preparation, ok
}

func (p LaunchPreparation) EstablishUnlockedNone() (EstablishUnlockedNoneLaunchPreparation, bool) {
	preparation, ok := p.variant.(EstablishUnlockedNoneLaunchPreparation)
	return preparation, ok
}

func (p LaunchPreparation) EstablishUnlockedManaged() (EstablishUnlockedManagedLaunchPreparation, bool) {
	preparation, ok := p.variant.(EstablishUnlockedManagedLaunchPreparation)
	return preparation, ok
}

func (p LaunchPreparation) Coordinator() *LaunchPreparationCoordinator {
	return p.coordinator
}

func (p LaunchPreparation) Validate() error {
	switch preparation := p.variant.(type) {
	case establishedRootLaunchPreparation:
		return nil
	case RestoreLockedTargetLaunchPreparation:
		if err := preparation.SourceWorkspace.validate(); err != nil {
			return err
		}
		return preparation.SetupOperationID.Validate()
	case EstablishUnlockedNoneLaunchPreparation:
		if err := preparation.SourceWorkspace.validate(); err != nil {
			return err
		}
		return preparation.SetupOperationID.Validate()
	case EstablishUnlockedManagedLaunchPreparation:
		if err := preparation.SourceWorkspace.validate(); err != nil {
			return err
		}
		if err := preparation.Selection.Validate(); err != nil {
			return err
		}
		if preparation.Selection.Mode == workflow.ExecutionTargetModeNone {
			return errors.New("unlocked managed launch preparation requires managed selection")
		}
		if err := preparation.SelectionSource.Validate(); err != nil {
			return err
		}
		return preparation.SetupOperationID.Validate()
	default:
		return errors.New("launch preparation variant is required")
	}
}

func (s LaunchSourceWorkspaceSnapshot) validate() error {
	if s.ID == "" || s.Root == "" {
		return errors.New("launch preparation requires source workspace snapshot")
	}
	return nil
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
