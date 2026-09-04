package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata"
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
	}, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
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
	}, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
		return s.executeLeaveWorktree(runCtx, request.sessionID, authority, sync)
	})
}

func (s *Service) runWorktreeTransition(
	ctx context.Context,
	request worktreeTransitionRequest,
	transition runtimeinput.PendingWorkWorktreeTransition,
	execute func(context.Context, transitionAuthority, transitionTargetSync) error,
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
		) error {
			runErr := execute(ctx, authority, func(
				syncCtx context.Context,
				target clientui.SessionExecutionTarget,
				reminder *session.WorktreeReminderState,
			) error {
				if err := sync(syncCtx, target, reminder); err != nil {
					return err
				}
				if err := s.publisher.PublishSessionIdentity(request.sessionID); err != nil {
					return worktreeApplied(fmt.Errorf("publish session identity: %w", err))
				}
				return nil
			})
			if isWorktreeIndeterminate(runErr) {
				return runErr
			}
			return s.publishWorktreeTransitionResult(request, runErr, syncFailure)
		},
	)
}

func (s *Service) publishWorktreeTransitionResult(
	request worktreeTransitionRequest,
	runErr error,
	syncFailure func(clientui.WorktreeTransitionOutcome) error,
) error {
	outcome := clientui.WorktreeTransitionOutcome{
		OperationID: request.operationID,
		Transition:  request.kind,
		State:       clientui.WorktreeTransitionCompleted,
	}
	if runErr != nil && !isWorktreeApplied(runErr) {
		outcome.State = clientui.WorktreeTransitionFailed
		outcome.Failure = projectWorktreeTransitionFailure(runErr)
	}
	if isWorktreeUnapplied(runErr) && syncFailure != nil {
		if syncErr := syncFailure(outcome); syncErr != nil {
			runErr = errors.Join(runErr, syncErr)
		}
	}
	s.publisher.PublishWorktreeTransitionOutcome(request.sessionID, outcome)
	return runErr
}

func projectWorktreeTransitionFailure(err error) *clientui.WorktreeTransitionFailure {
	failure := &clientui.WorktreeTransitionFailure{Diagnostic: err.Error()}
	var selector *worktreecontract.SelectorError
	if errors.As(err, &selector) && selector.Details != nil {
		failure.SelectorError = selector.Details
	}
	return failure
}

func (s *Service) executeEnterWorktree(ctx context.Context, sessionID string, selector string, authority transitionAuthority, sync transitionTargetSync) error {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return worktreeUnappliedTechnical(err)
	}
	match, err := resolveTopologySelector(topology, selector)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	entry := match.entry
	if entry.GetMissing() != nil {
		return worktreeUnappliedUserCorrectable(worktreecontract.NewSelectorError(
			worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE,
			selector,
			nil,
		))
	}
	apply := func(applyCtx context.Context) error {
		if topologyIsCurrent(entry, workspaceCtx.target) {
			return nil
		}
		previous, err := s.currentTransitionWorktree(applyCtx, topology, workspaceCtx.target)
		if err != nil {
			return worktreeTransitionFailure(err)
		}
		next, err := s.enterTransitionWorktree(applyCtx, workspaceCtx, entry)
		if err != nil {
			return worktreeTransitionFailure(err)
		}
		_, err = s.switchSessionTargetWithSync(applyCtx, workspaceCtx, previous, next, authority, sync)
		return err
	}
	return applyWorktreeTransition(ctx, authority, apply)
}

func (s *Service) executeLeaveWorktree(ctx context.Context, sessionID string, authority transitionAuthority, sync transitionTargetSync) error {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	defer release()
	if workspaceCtx.target.Worktree == nil {
		return nil
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return worktreeUnappliedTechnical(err)
	}
	previous, err := s.currentTransitionWorktree(ctx, topology, workspaceCtx.target)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	main, err := mainTransitionWorktree(topology, workspaceCtx.workspaceRoot)
	if err != nil {
		return worktreeTransitionFailure(err)
	}
	apply := func(applyCtx context.Context) error {
		_, err := s.switchSessionTargetWithSync(applyCtx, workspaceCtx, previous, main, authority, sync)
		return err
	}
	return applyWorktreeTransition(ctx, authority, apply)
}

func applyWorktreeTransition(ctx context.Context, authority transitionAuthority, apply func(context.Context) error) error {
	if authority == nil {
		return apply(ctx)
	}
	return worktreeUnappliedTechnicalUnlessClassified(authority(apply))
}

func worktreeTransitionFailure(err error) error {
	var selector *worktreecontract.SelectorError
	if errors.As(err, &selector) ||
		errors.Is(err, worktreecontract.ErrWorktreeNotFound) ||
		errors.Is(err, worktreecontract.ErrWorktreeBlocked) ||
		errors.Is(err, session.ErrSessionNotFound) {
		return worktreeUnappliedUserCorrectable(err)
	}
	return worktreeUnappliedTechnical(err)
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
		return s.adoptExternalWorktree(ctx, workspaceCtx.workspaceID, entry.GetExternal().GetGit())
	case entry.GetMainWorkspace() != nil:
		gitEntry, err := gitWorktreeFromFacts(entry.GetMainWorkspace().GetGit())
		if err != nil {
			return syncedWorktree{}, err
		}
		return syncedWorktree{
			record: metadata.WorktreeRecord{WorkspaceID: workspaceCtx.workspaceID, CanonicalRoot: workspaceCtx.workspaceRoot},
			git:    gitEntry,
		}, nil
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
		case entry.GetMainWorkspace() != nil:
			gitEntry, err := gitWorktreeFromFacts(entry.GetMainWorkspace().GetGit())
			if err != nil {
				return syncedWorktree{}, err
			}
			return syncedWorktree{
				record: metadata.WorktreeRecord{CanonicalRoot: strings.TrimSpace(workspaceRoot)},
				git:    gitEntry,
			}, nil
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
		IsMainWorktree: facts.IsMainWorktree,
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
