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
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"

	"github.com/google/uuid"
)

type worktreeTransitionRequest struct {
	operationID clientui.WorktreeTransitionID
	sessionID   string
	kind        clientui.WorktreeTransitionKind
	selector    string
}

type transitionTargetSync func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error
type transitionAuthority func(func() error) error

func (s *Service) EnterWorktree(ctx context.Context, req *worktreepb.EnterRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	operationID, err := clientui.ParseWorktreeTransitionID(req.OperationId)
	if err != nil {
		return nil, err
	}
	request := worktreeTransitionRequest{
		operationID: operationID,
		sessionID:   strings.TrimSpace(req.SessionId),
		kind:        clientui.WorktreeTransitionEnter,
		selector:    strings.TrimSpace(req.Selector),
	}
	target, err := s.resolveScheduledEnterTarget(ctx, request.sessionID, request.selector)
	if err != nil {
		return nil, err
	}
	return s.scheduleWorktreeTransition(ctx, request, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
		return s.executeEnterWorktree(runCtx, request.sessionID, target, authority, sync)
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
	return s.scheduleWorktreeTransition(ctx, request, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
		return s.executeLeaveWorktree(runCtx, request.sessionID, authority, sync)
	})
}

func (s *Service) scheduleWorktreeTransition(
	ctx context.Context,
	request worktreeTransitionRequest,
	execute func(context.Context, transitionAuthority, transitionTargetSync) error,
) (*worktreepb.ScheduledAcknowledgement, error) {
	if s == nil || s.authority == nil || s.publisher == nil {
		return nil, errors.New("worktree transition runtime is required")
	}
	if execute == nil {
		return nil, errors.New("worktree transition executor is required")
	}
	s.transitionMu.Lock()
	if s.transitionsClosed {
		s.transitionMu.Unlock()
		return nil, context.Canceled
	}
	predecessor := s.transitionTails[request.sessionID]
	completed := make(chan struct{})
	s.transitionTails[request.sessionID] = completed
	s.transitionWG.Add(1)
	s.transitionMu.Unlock()

	go s.runQueuedWorktreeTransition(predecessor, completed, request, execute)
	return &worktreepb.ScheduledAcknowledgement{OperationId: request.operationID.String()}, nil
}

func (s *Service) runQueuedWorktreeTransition(
	predecessor <-chan struct{},
	completed chan struct{},
	request worktreeTransitionRequest,
	execute func(context.Context, transitionAuthority, transitionTargetSync) error,
) {
	defer func() {
		close(completed)
		s.transitionMu.Lock()
		if s.transitionTails[request.sessionID] == completed {
			delete(s.transitionTails, request.sessionID)
		}
		s.transitionWG.Done()
		s.transitionMu.Unlock()
	}()
	if predecessor != nil {
		select {
		case <-predecessor:
		case <-s.transitionCtx.Done():
			return
		}
	}
	_ = s.runWorktreeTransition(s.transitionCtx, request, execute)
}

func (s *Service) runWorktreeTransition(
	ctx context.Context,
	request worktreeTransitionRequest,
	execute func(context.Context, transitionAuthority, transitionTargetSync) error,
) error {
	var terminalOutcome *clientui.WorktreeTransitionOutcome
	err := s.authority.RunWorktreeTransition(
		ctx,
		request.sessionID,
		request.kind,
		func(
			ctx context.Context,
			authority func(func() error) error,
			sync func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
			syncFailure func(clientui.WorktreeTransitionOutcome) error,
		) error {
			executionErr := execute(ctx, transitionAuthority(authority), func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				return sync(syncCtx, target, reminder)
			})
			if executionErr == nil || s.transitionCtx.Err() != nil {
				return executionErr
			}
			outcome := worktreeTransitionOutcome(request, executionErr)
			terminalOutcome = &outcome
			if syncFailure == nil {
				return errors.Join(executionErr, errors.New("worktree transition failure synchronizer is required"))
			}
			return errors.Join(executionErr, syncFailure(outcome))
		},
	)
	if s.transitionCtx.Err() == nil {
		outcome := worktreeTransitionOutcome(request, err)
		if terminalOutcome != nil {
			outcome = *terminalOutcome
		}
		s.publisher.PublishWorktreeTransitionOutcome(request.sessionID, outcome)
	}
	return err
}

func worktreeTransitionOutcome(
	request worktreeTransitionRequest,
	err error,
) clientui.WorktreeTransitionOutcome {
	outcome := clientui.WorktreeTransitionOutcome{
		OperationID: request.operationID,
		Transition:  request.kind,
		State:       clientui.WorktreeTransitionCompleted,
	}
	if err == nil {
		return outcome
	}
	outcome.State = clientui.WorktreeTransitionFailed
	outcome.Failure = &clientui.WorktreeTransitionFailure{Diagnostic: err.Error()}
	return outcome
}

func (s *Service) resolveScheduledEnterTarget(
	ctx context.Context,
	sessionID string,
	selector string,
) (scheduledWorktreeTarget, error) {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return scheduledWorktreeTarget{}, err
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return scheduledWorktreeTarget{}, err
	}
	match, err := resolveTopologySelector(topology, selector)
	if err != nil {
		return scheduledWorktreeTarget{}, err
	}
	if match.entry.GetMissing() != nil {
		return scheduledWorktreeTarget{}, worktreecontract.NewSelectorError(
			worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE,
			selector,
			nil,
		)
	}
	return scheduledWorktreeTargetFromEntry(match.entry)
}

func (s *Service) executeEnterWorktree(ctx context.Context, sessionID string, target scheduledWorktreeTarget, authority transitionAuthority, sync transitionTargetSync) error {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return err
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return err
	}
	entry, err := target.resolve(topology)
	if err != nil {
		return err
	}
	apply := func() error {
		if topologyIsCurrent(entry, workspaceCtx.target) {
			return nil
		}
		previous, err := s.currentTransitionWorktree(ctx, topology, workspaceCtx.target)
		if err != nil {
			return err
		}
		next, err := s.enterTransitionWorktree(ctx, workspaceCtx, entry)
		if err != nil {
			return err
		}
		_, err = s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, next, authority, sync)
		return err
	}
	if authority != nil {
		return authority(apply)
	}
	return apply()
}

func (s *Service) executeLeaveWorktree(ctx context.Context, sessionID string, authority transitionAuthority, sync transitionTargetSync) error {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, sessionID)
	if err != nil {
		return err
	}
	defer release()
	if workspaceCtx.target.Worktree == nil {
		return nil
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return err
	}
	previous, err := s.currentTransitionWorktree(ctx, topology, workspaceCtx.target)
	if err != nil {
		return err
	}
	main, err := mainTransitionWorktree(topology, workspaceCtx.workspaceRoot)
	if err != nil {
		return err
	}
	_, err = s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, main, authority, sync)
	return err
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
