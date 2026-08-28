package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type worktreeTransitionRequest struct {
	operationID serverapi.WorktreeOperationID
	sessionID   string
	kind        clientui.WorktreeTransitionKind
	selector    string
}

type transitionAuthority = sessionruntime.WorktreeTransitionAuthority
type transitionTargetSync = sessionruntime.WorktreeTransitionTargetSync

func (s *Service) EnterWorktree(ctx context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	request := worktreeTransitionRequest{
		operationID: req.OperationID,
		sessionID:   strings.TrimSpace(req.SessionID),
		kind:        clientui.WorktreeTransitionEnter,
		selector:    runtimeinput.NormalizePendingWorkArgument(req.Selector),
	}
	selector := request.selector
	return s.runWorktreeTransition(ctx, request, runtimeinput.PendingWorkWorktreeTransition{
		Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
		Selector:   &selector,
	}, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) runtime.WorktreeApplicationResult {
		return s.executeEnterWorktree(runCtx, request.sessionID, request.selector, authority, sync)
	})
}

func (s *Service) LeaveWorktree(ctx context.Context, req serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	request := worktreeTransitionRequest{
		operationID: req.OperationID,
		sessionID:   strings.TrimSpace(req.SessionID),
		kind:        clientui.WorktreeTransitionLeave,
	}
	return s.runWorktreeTransition(ctx, request, runtimeinput.PendingWorkWorktreeTransition{
		Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
	}, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) runtime.WorktreeApplicationResult {
		return s.executeLeaveWorktree(runCtx, request.sessionID, authority, sync)
	})
}

func (s *Service) runWorktreeTransition(
	ctx context.Context,
	request worktreeTransitionRequest,
	transition runtimeinput.PendingWorkWorktreeTransition,
	execute func(context.Context, transitionAuthority, transitionTargetSync) runtime.WorktreeApplicationResult,
) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if s == nil || s.authority == nil || s.publisher == nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, errors.New("worktree transition runtime is required")
	}
	if execute == nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, errors.New("worktree transition executor is required")
	}
	return s.authority.RunWorktreeTransition(
		ctx,
		request.sessionID,
		request.operationID,
		transition,
		func(
			ctx context.Context,
			authority transitionAuthority,
			sync transitionTargetSync,
			syncFailure func(clientui.WorktreeTransitionOutcome) error,
		) runtime.WorktreeApplicationResult {
			result := execute(ctx, authority, func(
				syncCtx context.Context,
				target clientui.SessionExecutionTarget,
				reminder *session.WorktreeReminderState,
			) runtime.WorktreeApplicationResult {
				syncResult := sync(syncCtx, target, reminder)
				if err := syncResult.Validate(); err != nil {
					return runtime.IndeterminateWorktreeApplication(fmt.Errorf("validate Worktree target sync result: %w", err))
				}
				if syncResult.Certainty != runtime.WorktreeApplicationCommitted {
					return syncResult
				}
				if err := s.publisher.PublishSessionIdentity(request.sessionID); err != nil {
					return runtime.CommittedWorktreeApplication(errors.Join(
						syncResult.Err,
						fmt.Errorf("publish session identity: %w", err),
					))
				}
				return syncResult
			})
			if err := result.Validate(); err != nil {
				return runtime.IndeterminateWorktreeApplication(fmt.Errorf("validate Worktree transition result: %w", err))
			}
			if result.Certainty == runtime.WorktreeApplicationIndeterminate {
				return result
			}
			return s.publishWorktreeTransitionResult(request, result, syncFailure)
		},
	)
}

func (s *Service) publishWorktreeTransitionResult(
	request worktreeTransitionRequest,
	result runtime.WorktreeApplicationResult,
	syncFailure func(clientui.WorktreeTransitionOutcome) error,
) runtime.WorktreeApplicationResult {
	outcome := worktreeTransitionOutcome(request, result)
	if result.Certainty == runtime.WorktreeApplicationUnapplied && syncFailure != nil {
		if syncErr := syncFailure(outcome); syncErr != nil {
			result = result.WithError(errors.Join(result.Err, syncErr))
		}
	}
	s.publisher.PublishWorktreeTransitionOutcome(request.sessionID, outcome)
	return result
}

func worktreeTransitionOutcome(
	request worktreeTransitionRequest,
	result runtime.WorktreeApplicationResult,
) clientui.WorktreeTransitionOutcome {
	outcome := clientui.WorktreeTransitionOutcome{
		OperationID: request.operationID,
		Transition:  request.kind,
		State:       clientui.WorktreeTransitionCompleted,
	}
	if result.Certainty == runtime.WorktreeApplicationCommitted {
		return outcome
	}
	outcome.State = clientui.WorktreeTransitionFailed
	outcome.Failure = &clientui.WorktreeTransitionFailure{Diagnostic: result.Err.Error()}
	var precondition *serverapi.WorktreeDeletePreconditionError
	if errors.As(result.Err, &precondition) {
		dirtyState := precondition.DirtyState
		outcome.Failure.DeletePrecondition = &dirtyState
	}
	return outcome
}

func (s *Service) executeEnterWorktree(
	ctx context.Context,
	sessionID string,
	selector string,
	authority transitionAuthority,
	sync transitionTargetSync,
) runtime.WorktreeApplicationResult {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return runtime.UnappliedWorktreeApplication(err)
	}
	match, err := resolveTopologySelector(topology, selector)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	entry := match.entry
	if entry.Variant == serverapi.WorktreeTopologyVariantMissing {
		return runtime.UnappliedUserCorrectableWorktreeApplication(&serverapi.WorktreeSelectorError{
			Kind:  serverapi.WorktreeSelectorErrorKindUnavailable,
			Input: selector,
		})
	}
	apply := func(applyCtx context.Context) runtime.WorktreeApplicationResult {
		if topologyIsCurrent(entry, workspaceCtx.target) {
			return runtime.CommittedWorktreeApplication(nil)
		}
		previous, err := s.currentTransitionWorktree(applyCtx, topology, workspaceCtx.target)
		if err != nil {
			return worktreeTransitionFailure(err)
		}
		next, err := s.enterTransitionWorktree(applyCtx, workspaceCtx, entry)
		if err != nil {
			return worktreeTransitionFailure(err)
		}
		_, result := s.switchSessionTargetWithSync(applyCtx, workspaceCtx, previous, next, authority, sync)
		return result
	}
	if authority != nil {
		return authority(apply)
	}
	return apply(ctx)
}

func (s *Service) executeLeaveWorktree(
	ctx context.Context,
	sessionID string,
	authority transitionAuthority,
	sync transitionTargetSync,
) runtime.WorktreeApplicationResult {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	defer release()
	if workspaceCtx.target.Worktree == nil {
		return runtime.CommittedWorktreeApplication(nil)
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return runtime.UnappliedWorktreeApplication(err)
	}
	previous, err := s.currentTransitionWorktree(ctx, topology, workspaceCtx.target)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	main, err := mainTransitionWorktree(topology, workspaceCtx.workspaceRoot)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	apply := func(applyCtx context.Context) runtime.WorktreeApplicationResult {
		_, result := s.switchSessionTargetWithSync(applyCtx, workspaceCtx, previous, main, authority, sync)
		return result
	}
	if authority != nil {
		return authority(apply)
	}
	return apply(ctx)
}

func worktreeTransitionFailure(err error) runtime.WorktreeApplicationResult {
	var selector *serverapi.WorktreeSelectorError
	if errors.As(err, &selector) ||
		errors.Is(err, serverapi.ErrWorktreeNotFound) ||
		errors.Is(err, serverapi.ErrWorktreeBlocked) ||
		errors.Is(err, serverapi.ErrWorkspaceNotRegistered) ||
		errors.Is(err, session.ErrSessionNotFound) ||
		errors.Is(err, serverapi.ErrSessionWorktreeDeleting) {
		return runtime.UnappliedUserCorrectableWorktreeApplication(err)
	}
	return runtime.UnappliedWorktreeApplication(err)
}

func (s *Service) enterTransitionWorktree(ctx context.Context, workspaceCtx sessionWorkspaceContext, entry serverapi.WorktreeTopologyEntry) (syncedWorktree, error) {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.Registered.Kent.WorktreeID)
		if err != nil {
			return syncedWorktree{}, err
		}
		gitEntry, err := gitWorktreeFromFacts(entry.Registered.Git)
		if err != nil {
			return syncedWorktree{}, err
		}
		return syncedWorktree{record: record, git: gitEntry}, nil
	case serverapi.WorktreeTopologyVariantExternal:
		gitEntry, err := gitWorktreeFromFacts(entry.External.Git)
		if err != nil {
			return syncedWorktree{}, err
		}
		if entry.External.Git.IsMain {
			return syncedWorktree{
				record: metadata.WorktreeRecord{WorkspaceID: workspaceCtx.workspaceID, CanonicalRoot: workspaceCtx.workspaceRoot},
				git:    gitEntry,
			}, nil
		}
		return s.adoptExternalWorktree(ctx, workspaceCtx.workspaceID, entry.External.Git)
	case serverapi.WorktreeTopologyVariantMissing:
		return syncedWorktree{}, &serverapi.WorktreeSelectorError{
			Kind:  serverapi.WorktreeSelectorErrorKindUnavailable,
			Input: entry.Missing.Kent.WorktreeID,
		}
	default:
		return syncedWorktree{}, errors.New("worktree topology variant is invalid")
	}
}

func (s *Service) adoptExternalWorktree(ctx context.Context, workspaceID string, facts serverapi.WorktreeGitFacts) (syncedWorktree, error) {
	gitEntry, err := gitWorktreeFromFacts(facts)
	if err != nil {
		return syncedWorktree{}, err
	}
	now := time.Now().UTC()
	record := metadata.WorktreeRecord{
		ID:            uuid.NewString(),
		WorkspaceID:   strings.TrimSpace(workspaceID),
		CanonicalRoot: strings.TrimSpace(facts.CanonicalRoot),
		DisplayName:   filepath.Base(strings.TrimSpace(facts.CanonicalRoot)),
		Availability:  string(PathAvailability(facts.CanonicalRoot)),
		IsMain:        facts.IsMain,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	record.GitMetadataJSON, err = marshalGitMetadata(gitEntry)
	if err != nil {
		return syncedWorktree{}, err
	}
	if err := s.metadata.UpsertWorktreeRecord(ctx, record); err != nil {
		return syncedWorktree{}, err
	}
	return syncedWorktree{record: record, git: gitEntry}, nil
}

func (s *Service) currentTransitionWorktree(
	ctx context.Context,
	topology []serverapi.WorktreeTopologyEntry,
	target clientui.SessionExecutionTarget,
) (*syncedWorktree, error) {
	if target.Worktree == nil {
		return nil, nil
	}
	targetID := strings.TrimSpace(target.Worktree.ID)
	for _, entry := range topology {
		id := topologyWorktreeID(entry)
		if id == nil || strings.TrimSpace(*id) != targetID {
			continue
		}
		switch entry.Variant {
		case serverapi.WorktreeTopologyVariantRegistered:
			record, err := s.metadata.GetWorktreeRecordByID(ctx, targetID)
			if err != nil {
				return nil, err
			}
			gitEntry, err := gitWorktreeFromFacts(entry.Registered.Git)
			if err != nil {
				return nil, err
			}
			value := syncedWorktree{record: record, git: gitEntry}
			return &value, nil
		case serverapi.WorktreeTopologyVariantMissing:
			record, err := s.metadata.GetWorktreeRecordByID(ctx, targetID)
			if err != nil {
				return nil, err
			}
			gitEntry, err := worktreeGitMetadataFromRecord(record)
			if err != nil {
				return nil, err
			}
			value := syncedWorktree{record: record, git: gitEntry}
			return &value, nil
		}
	}
	return nil, fmt.Errorf("current worktree %q is absent from projected topology: %w", targetID, serverapi.ErrWorktreeNotFound)
}

func mainTransitionWorktree(topology []serverapi.WorktreeTopologyEntry, workspaceRoot string) (syncedWorktree, error) {
	for _, entry := range topology {
		switch entry.Variant {
		case serverapi.WorktreeTopologyVariantRegistered:
			if entry.Registered.Git.IsMain {
				gitEntry, err := gitWorktreeFromFacts(entry.Registered.Git)
				if err != nil {
					return syncedWorktree{}, err
				}
				return syncedWorktree{
					record: metadata.WorktreeRecord{
						ID:            entry.Registered.Kent.WorktreeID,
						WorkspaceID:   "",
						CanonicalRoot: entry.Registered.Git.CanonicalRoot,
					},
					git: gitEntry,
				}, nil
			}
		case serverapi.WorktreeTopologyVariantExternal:
			if entry.External.Git.IsMain {
				gitEntry, err := gitWorktreeFromFacts(entry.External.Git)
				if err != nil {
					return syncedWorktree{}, err
				}
				return syncedWorktree{
					record: metadata.WorktreeRecord{CanonicalRoot: strings.TrimSpace(workspaceRoot)},
					git:    gitEntry,
				}, nil
			}
		}
	}
	return syncedWorktree{}, fmt.Errorf("main worktree not found: %w", serverapi.ErrWorktreeNotFound)
}

func gitWorktreeFromFacts(facts serverapi.WorktreeGitFacts) (GitWorktree, error) {
	if err := facts.Validate(); err != nil {
		return GitWorktree{}, err
	}
	branch, err := optionalLocalBranch(facts.BranchRef, facts.BranchName)
	if err != nil {
		return GitWorktree{}, err
	}
	entry := GitWorktree{
		Root:           strings.TrimSpace(facts.CanonicalRoot),
		HeadOID:        strings.TrimSpace(facts.HeadObject),
		Branch:         branch,
		Detached:       facts.Detached,
		Bare:           facts.Bare,
		LockedReason:   optionalString(facts.LockedReason),
		PrunableReason: optionalString(facts.PrunableReason),
		IsMain:         facts.IsMain,
	}
	if err := entry.validateHead(); err != nil {
		return GitWorktree{}, err
	}
	return entry, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
