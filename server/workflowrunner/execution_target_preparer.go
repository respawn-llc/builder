package workflowrunner

import (
	"context"
	"errors"
	"fmt"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/server/worktree"
)

type executionTargetPreparer struct {
	service *worktree.Service
	git     *worktree.GitInspector
	store   executionTargetStore
	permit  *workflowexecution.MutationPermit
}

type executionTargetStore interface {
	LockTaskExecutionTarget(context.Context, workflow.TaskID, *workflowstore.ExecutionTargetCandidate) (workflowstore.ExecutionRoot, error)
	GetTaskExecutionTargetContext(context.Context, workflow.TaskID) (workflowstore.TaskExecutionTargetContext, error)
}

func newExecutionTargetPreparer(
	store executionTargetStore,
	permit *workflowexecution.MutationPermit,
	service *worktree.Service,
	git *worktree.GitInspector,
) workflowexecution.LaunchTargetPreparer {
	return &executionTargetPreparer{
		service: service,
		git:     git,
		store:   store,
		permit:  permit,
	}
}

func (p *executionTargetPreparer) PrepareExecutionTarget(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	preparation workflowexecution.LaunchPreparation,
) (workflowstore.ExecutionRoot, error) {
	if err := preparation.Validate(); err != nil {
		return workflowstore.ExecutionRoot{}, err
	}
	switch preparation.Kind {
	case workflowexecution.LaunchPreparationEstablishUnlockedNone:
		return workflowexecution.RunMutation(ctx, p.permit, func(ctx context.Context) (workflowstore.ExecutionRoot, error) {
			return p.store.LockTaskExecutionTarget(ctx, reference.TaskID, &workflowstore.ExecutionTargetCandidate{
				Snapshot: workflowstore.ExecutionTargetSnapshot{
					Mode:       workflow.ExecutionTargetModeNone,
					Provenance: workflowstore.ExecutionTargetProvenanceResolved,
				},
				Root: workflowstore.ExecutionRoot{
					SourceWorkspaceID:   preparation.SourceWorkspaceID,
					SourceWorkspaceRoot: preparation.SourceWorkspaceRoot,
				},
			})
		})
	case workflowexecution.LaunchPreparationEstablishUnlockedManaged:
		if p.service == nil {
			return workflowstore.ExecutionRoot{}, errors.New("worktree service is required")
		}
		snapshot, err := p.resolveExecutionTarget(ctx, preparation.SourceWorkspaceRoot, preparation.Selection)
		if err != nil {
			return workflowstore.ExecutionRoot{}, err
		}
		materialized, err := p.service.PrepareInitialTaskWorktree(ctx, worktree.InitialTaskWorktreeMaterializationRequest{
			TaskID:           reference.TaskID,
			SetupOperationID: preparation.SetupOperationID,
			ResolvedTarget: worktree.GitRevision{
				RequestedRef: *snapshot.RequestedRef,
				CommitOID:    *snapshot.CommitOID,
				CanonicalRef: snapshot.ResolvedRef,
			},
			SourceWorkspace: &worktree.SourceWorkspaceSnapshot{
				ID:   preparation.SourceWorkspaceID,
				Root: preparation.SourceWorkspaceRoot,
			},
		})
		if err != nil {
			return workflowstore.ExecutionRoot{}, err
		}
		if materialized.Worktree.Registered == nil {
			cleanupErr := materialized.Cleanup(ctx)
			return workflowstore.ExecutionRoot{}, errors.Join(
				errors.New("prepared managed worktree is not registered"),
				cleanupErr,
			)
		}
		worktreeID := materialized.Worktree.Registered.Kent.WorktreeID
		worktreeRoot := materialized.Worktree.Registered.Git.CanonicalRoot
		root := workflowstore.ExecutionRoot{
			SourceWorkspaceID:   preparation.SourceWorkspaceID,
			SourceWorkspaceRoot: preparation.SourceWorkspaceRoot,
			Managed:             &workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot},
		}
		candidate := &workflowstore.ExecutionTargetCandidate{Snapshot: snapshot, Root: root}
		lockedRoot, lockErr := workflowexecution.RunMutation(ctx, p.permit, func(ctx context.Context) (workflowstore.ExecutionRoot, error) {
			return p.store.LockTaskExecutionTarget(ctx, reference.TaskID, candidate)
		})
		if lockErr != nil {
			cleanupErr := materialized.Cleanup(ctx)
			return workflowstore.ExecutionRoot{}, errors.Join(lockErr, cleanupErr)
		}
		materialized.Commit()
		if setupErr := materialized.RunSetup(ctx); setupErr != nil {
			return workflowstore.ExecutionRoot{}, setupErr
		}
		return lockedRoot, nil
	case workflowexecution.LaunchPreparationRestoreLockedTarget:
		if p.service == nil {
			return workflowstore.ExecutionRoot{}, errors.New("worktree service is required")
		}
		materialized, err := p.service.RestoreLockedTaskWorktree(ctx, worktree.LockedTaskWorktreeRestoreRequest{
			TaskID:           reference.TaskID,
			SetupOperationID: preparation.SetupOperationID,
			SourceWorkspace: &worktree.SourceWorkspaceSnapshot{
				ID:   preparation.SourceWorkspaceID,
				Root: preparation.SourceWorkspaceRoot,
			},
		})
		if err != nil {
			return workflowstore.ExecutionRoot{}, err
		}
		targetContext, err := p.store.GetTaskExecutionTargetContext(ctx, reference.TaskID)
		if err != nil {
			return workflowstore.ExecutionRoot{}, err
		}
		root := workflowstore.ExecutionRoot{
			SourceWorkspaceID:   preparation.SourceWorkspaceID,
			SourceWorkspaceRoot: preparation.SourceWorkspaceRoot,
		}
		if targetContext.Task.ExecutionTarget != nil && targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
			if materialized.Worktree.Registered == nil {
				return workflowstore.ExecutionRoot{}, errors.New("restored managed worktree is not registered")
			}
			root.Managed = &workflowstore.ManagedExecutionRoot{
				WorktreeID: materialized.Worktree.Registered.Kent.WorktreeID,
				Root:       materialized.Worktree.Registered.Git.CanonicalRoot,
			}
		}
		return root, nil
	default:
		return workflowstore.ExecutionRoot{}, errors.New("unsupported execution target preparation")
	}
}

func (p *executionTargetPreparer) resolveExecutionTarget(
	ctx context.Context,
	sourceWorkspaceRoot string,
	selection workflow.ExecutionTargetSelection,
) (workflowstore.ExecutionTargetSnapshot, error) {
	if p.git == nil {
		return workflowstore.ExecutionTargetSnapshot{}, errors.New("git inspector is required")
	}
	if err := selection.Validate(); err != nil {
		return workflowstore.ExecutionTargetSnapshot{}, err
	}
	var revision worktree.GitRevision
	var err error
	switch selection.Mode {
	case workflow.ExecutionTargetModeHead:
		revision, err = p.git.ResolveHEAD(ctx, sourceWorkspaceRoot)
	case workflow.ExecutionTargetModeDefaultBranch:
		var defaultBranch worktree.GitDefaultBranch
		defaultBranch, err = p.git.ResolveDefaultBranch(ctx, sourceWorkspaceRoot)
		if err == nil {
			revision, err = p.git.ResolveRevision(ctx, sourceWorkspaceRoot, defaultBranch.Ref)
		}
	case workflow.ExecutionTargetModeCustomRef:
		if selection.CustomRef == nil {
			return workflowstore.ExecutionTargetSnapshot{}, errors.New("custom execution target ref is required")
		}
		revision, err = p.git.ResolveRevision(ctx, sourceWorkspaceRoot, *selection.CustomRef)
	default:
		return workflowstore.ExecutionTargetSnapshot{}, fmt.Errorf("execution target mode %q is not managed", selection.Mode)
	}
	if err != nil {
		return workflowstore.ExecutionTargetSnapshot{}, err
	}
	requestedRef := revision.RequestedRef
	commitOID := revision.CommitOID
	return workflowstore.ExecutionTargetSnapshot{
		Mode:         selection.Mode,
		RequestedRef: &requestedRef,
		ResolvedRef:  revision.CanonicalRef,
		CommitOID:    &commitOID,
		Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
	}, nil
}
