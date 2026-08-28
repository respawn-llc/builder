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
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeinput"
	"core/shared/worktreecontract"

	"github.com/google/uuid"
)

type worktreeTransitionRequest struct {
	operationID clientui.WorktreeTransitionID
	sessionID   string
	kind        clientui.WorktreeTransitionKind
	selector    string
}

type transitionAuthority = sessionruntime.WorktreeTransitionAuthority
type transitionTargetSync = sessionruntime.WorktreeTransitionTargetSync

func (s *Service) EnterWorktree(ctx context.Context, req *worktreepb.EnterRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	operationID, err := clientui.ParseWorktreeTransitionID(req.OperationId)
	if err != nil {
		return nil, err
	}
	request := worktreeTransitionRequest{
		operationID: operationID,
		sessionID:   strings.TrimSpace(req.SessionId),
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

func (s *Service) LeaveWorktree(ctx context.Context, req *worktreepb.LeaveRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	operationID, err := clientui.ParseWorktreeTransitionID(req.OperationId)
	if err != nil {
		return nil, err
	}
	request := worktreeTransitionRequest{
		operationID: operationID,
		sessionID:   strings.TrimSpace(req.SessionId),
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
) (*worktreepb.ScheduledAcknowledgement, error) {
	if s == nil || s.authority == nil || s.publisher == nil {
		return nil, errors.New("worktree transition runtime is required")
	}
	if execute == nil {
		return nil, errors.New("worktree transition executor is required")
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

func worktreeTransitionOutcome(request worktreeTransitionRequest, result runtime.WorktreeApplicationResult) clientui.WorktreeTransitionOutcome {
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
	return outcome
}

func (s *Service) executeEnterWorktree(ctx context.Context, sessionID string, selector string, authority transitionAuthority, sync transitionTargetSync) runtime.WorktreeApplicationResult {
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
	if entry.GetMissing() != nil {
		return runtime.UnappliedUserCorrectableWorktreeApplication(worktreecontract.NewSelectorError(
			worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE,
			selector,
			nil,
		))
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

func (s *Service) executeLeaveWorktree(ctx context.Context, sessionID string, authority transitionAuthority, sync transitionTargetSync) runtime.WorktreeApplicationResult {
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
	var selector *worktreecontract.SelectorError
	if errors.As(err, &selector) ||
		errors.Is(err, worktreecontract.ErrWorktreeNotFound) ||
		errors.Is(err, worktreecontract.ErrWorktreeBlocked) ||
		errors.Is(err, session.ErrSessionNotFound) {
		return runtime.UnappliedUserCorrectableWorktreeApplication(err)
	}
	return runtime.UnappliedWorktreeApplication(err)
}

func (s *Service) enterTransitionWorktree(ctx context.Context, workspaceCtx sessionWorkspaceContext, entry *worktreepb.TopologyEntry) (syncedWorktree, error) {
	switch {
	case entry.GetRegistered() != nil:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.GetRegistered().GetKent().GetWorktreeId())
		if err != nil {
			return syncedWorktree{}, err
		}
		gitEntry, err := gitWorktreeFromFacts(entry.GetRegistered().GetGit())
		if err != nil {
			return syncedWorktree{}, err
		}
		return syncedWorktree{record: record, git: gitEntry}, nil
	case entry.GetExternal() != nil:
		gitEntry, err := gitWorktreeFromFacts(entry.GetExternal().GetGit())
		if err != nil {
			return syncedWorktree{}, err
		}
		if entry.GetExternal().GetGit().GetIsMain() {
			return syncedWorktree{
				record: metadata.WorktreeRecord{WorkspaceID: workspaceCtx.workspaceID, CanonicalRoot: workspaceCtx.workspaceRoot},
				git:    gitEntry,
			}, nil
		}
		return s.adoptExternalWorktree(ctx, workspaceCtx.workspaceID, entry.GetExternal().GetGit())
	case entry.GetMissing() != nil:
		return syncedWorktree{}, worktreecontract.NewSelectorError(
			worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE,
			entry.GetMissing().GetKent().GetWorktreeId(),
			nil,
		)
	default:
		return syncedWorktree{}, errors.New("worktree topology variant is invalid")
	}
}

func (s *Service) adoptExternalWorktree(ctx context.Context, workspaceID string, facts *worktreepb.GitFacts) (syncedWorktree, error) {
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
	topology []*worktreepb.TopologyEntry,
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
		switch {
		case entry.GetRegistered() != nil:
			record, err := s.metadata.GetWorktreeRecordByID(ctx, targetID)
			if err != nil {
				return nil, err
			}
			gitEntry, err := gitWorktreeFromFacts(entry.GetRegistered().GetGit())
			if err != nil {
				return nil, err
			}
			value := syncedWorktree{record: record, git: gitEntry}
			return &value, nil
		case entry.GetMissing() != nil:
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
	return nil, fmt.Errorf("current worktree %q is absent from projected topology: %w", targetID, worktreecontract.ErrWorktreeNotFound)
}

func mainTransitionWorktree(topology []*worktreepb.TopologyEntry, workspaceRoot string) (syncedWorktree, error) {
	for _, entry := range topology {
		switch {
		case entry.GetRegistered() != nil:
			if entry.GetRegistered().GetGit().GetIsMain() {
				gitEntry, err := gitWorktreeFromFacts(entry.GetRegistered().GetGit())
				if err != nil {
					return syncedWorktree{}, err
				}
				return syncedWorktree{
					record: metadata.WorktreeRecord{
						ID:            entry.GetRegistered().GetKent().GetWorktreeId(),
						WorkspaceID:   "",
						CanonicalRoot: entry.GetRegistered().GetGit().GetCanonicalRoot(),
					},
					git: gitEntry,
				}, nil
			}
		case entry.GetExternal() != nil:
			if entry.GetExternal().GetGit().GetIsMain() {
				gitEntry, err := gitWorktreeFromFacts(entry.GetExternal().GetGit())
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
	return syncedWorktree{}, fmt.Errorf("main worktree not found")
}

func gitWorktreeFromFacts(facts *worktreepb.GitFacts) (GitWorktree, error) {
	if facts == nil {
		return GitWorktree{}, errors.New("worktree Git facts are required")
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
