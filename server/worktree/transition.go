package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/runtimecommand"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type worktreeTransitionRequest struct {
	operationID serverapi.WorktreeOperationID
	sessionID   string
	kind        clientui.WorktreeTransitionKind
	selector    string
	force       bool
	cleanup     serverapi.WorktreeBranchCleanupMode
}

type pendingWorktreeTransition struct {
	request worktreeTransitionRequest
	claim   *sessionruntime.WorktreeBoundaryClaim
}

type transitionTargetSync func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error
type transitionAuthority func(func() error) error

func (s *Service) replayPendingWorktreeTransition(request worktreeTransitionRequest) (serverapi.WorktreeScheduledAcknowledgement, bool) {
	if s == nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, false
	}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	pending, ok := s.transitions[request.sessionID]
	if !ok || pending.request != request {
		return serverapi.WorktreeScheduledAcknowledgement{}, false
	}
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: pending.request.operationID}, true
}

func (s *Service) EnterWorktree(ctx context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	request := worktreeTransitionRequest{
		operationID: req.OperationID,
		sessionID:   strings.TrimSpace(req.SessionID),
		kind:        clientui.WorktreeTransitionEnter,
		selector:    strings.TrimSpace(req.Selector),
	}
	if ack, ok := s.replayPendingWorktreeTransition(request); ok {
		return ack, nil
	}
	target, err := s.resolveScheduledEnterTarget(ctx, request.sessionID, request.selector)
	if err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	return s.scheduleWorktreeTransition(ctx, request, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
		return s.executeEnterWorktree(runCtx, request.sessionID, target, authority, sync)
	}, req.WorktreeTransitionHeader.Origin)
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
	return s.scheduleWorktreeTransition(ctx, request, func(runCtx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
		return s.executeLeaveWorktree(runCtx, request.sessionID, authority, sync)
	}, req.WorktreeTransitionHeader.Origin)
}

func (s *Service) scheduleWorktreeTransition(
	ctx context.Context,
	request worktreeTransitionRequest,
	execute func(context.Context, transitionAuthority, transitionTargetSync) error,
	origin *serverapi.RuntimeStepOrigin,
) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if s == nil || s.authority == nil || s.publisher == nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, errors.New("worktree transition runtime is required")
	}
	if execute == nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, errors.New("worktree transition executor is required")
	}
	s.transitionMu.Lock()
	if s.transitionsClosed {
		s.transitionMu.Unlock()
		return serverapi.WorktreeScheduledAcknowledgement{}, context.Canceled
	}
	if pending, ok := s.transitions[request.sessionID]; ok {
		s.transitionMu.Unlock()
		if pending.request == request {
			return serverapi.WorktreeScheduledAcknowledgement{OperationID: pending.request.operationID}, nil
		}
		return serverapi.WorktreeScheduledAcknowledgement{}, &serverapi.WorktreeTransitionPendingError{
			SessionID:          request.sessionID,
			PendingOperationID: pending.request.operationID,
		}
	}
	pending := pendingWorktreeTransition{request: request}
	s.transitions[request.sessionID] = pending
	if origin == nil {
		claim, claimErr := s.authority.ClaimCurrentWorktreeBoundary(
			request.sessionID,
			request.operationID,
		)
		if claimErr != nil {
			delete(s.transitions, request.sessionID)
			s.transitionMu.Unlock()
			return serverapi.WorktreeScheduledAcknowledgement{}, claimErr
		}
		pending.claim = claim
		s.transitions[request.sessionID] = pending
	}
	s.transitionWG.Add(1)
	s.transitionMu.Unlock()

	if origin != nil {
		if err := s.runWorktreeTransition(ctx, pending, execute, origin); err != nil {
			return serverapi.WorktreeScheduledAcknowledgement{}, err
		}
	} else {
		go func() {
			_ = s.runWorktreeTransition(s.transitionCtx, pending, execute, nil)
		}()
	}
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: request.operationID}, nil
}

func (s *Service) runWorktreeTransition(
	ctx context.Context,
	pending pendingWorktreeTransition,
	execute func(context.Context, transitionAuthority, transitionTargetSync) error,
	origin *serverapi.RuntimeStepOrigin,
) error {
	defer s.transitionWG.Done()
	request := pending.request
	run := func(
		runCtx context.Context,
		authority func(func() error) error,
		sync func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
	) error {
		return execute(runCtx, transitionAuthority(authority), func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
			return sync(syncCtx, target, reminder)
		})
	}
	var operationErr error
	if pending.claim != nil {
		if grantErr := pending.claim.AwaitGrant(ctx); grantErr != nil {
			operationErr = grantErr
		} else {
			operationErr = s.authority.RunClaimedWorktreeTransition(ctx, pending.claim, run)
		}
	} else {
		operationErr = s.authority.RunWorktreeTransition(
			ctx,
			request.sessionID,
			origin,
			run,
		)
	}
	outcome := worktreeTransitionOutcome(request, operationErr)
	var outcomeErr error
	if s.transitionCtx.Err() == nil {
		if pending.claim != nil {
			outcomeErr = s.submitClaimedWorktreeTransitionOutcome(
				pending.claim,
				request.sessionID,
				outcome,
			)
		} else {
			outcomeErr = s.publisher.PublishWorktreeTransitionOutcome(request.sessionID, outcome)
			if operationErr != nil {
				outcomeErr = errors.Join(
					outcomeErr,
					s.authority.SteerWorktreeTransitionFailure(
						s.transitionCtx,
						request.sessionID,
						outcome,
					),
				)
			}
		}
	} else if pending.claim != nil {
		outcomeErr = pending.claim.Cancel(context.Cause(s.transitionCtx))
	}
	s.transitionMu.Lock()
	if pending, ok := s.transitions[request.sessionID]; ok && pending.request == request {
		delete(s.transitions, request.sessionID)
	}
	s.transitionMu.Unlock()
	return errors.Join(operationErr, outcomeErr)
}

func worktreeTransitionOutcome(
	request worktreeTransitionRequest,
	operationErr error,
) clientui.WorktreeTransitionOutcome {
	outcome := clientui.WorktreeTransitionOutcome{
		OperationID: request.operationID,
		Transition:  request.kind,
		State:       clientui.WorktreeTransitionCompleted,
	}
	if operationErr == nil {
		return outcome
	}
	outcome.State = clientui.WorktreeTransitionFailed
	outcome.Failure = &clientui.WorktreeTransitionFailure{Diagnostic: operationErr.Error()}
	var precondition *serverapi.WorktreeDeletePreconditionError
	if errors.As(operationErr, &precondition) {
		dirtyState := precondition.DirtyState
		outcome.Failure.DeletePrecondition = &dirtyState
	}
	return outcome
}

func (s *Service) submitClaimedWorktreeTransitionOutcome(
	claim *sessionruntime.WorktreeBoundaryClaim,
	sessionID string,
	outcome clientui.WorktreeTransitionOutcome,
) error {
	err := s.authority.WithRuntimeEvents(
		s.transitionCtx,
		claim.Resource(),
		func(callbackCtx context.Context, target sessionruntime.RuntimeEventTarget) error {
			deferred, err := runtimecommand.Submit(
				callbackCtx,
				target.Events,
				outcome,
				func(
					command runtimecommand.Admission,
					event clientui.WorktreeTransitionOutcome,
					complete func(struct{}, error),
				) error {
					admission, admissionErr := target.Engine.AdmitWorktreeOutcome(command)
					publicationErr := s.publisher.PublishWorktreeTransitionOutcome(
						sessionID,
						event,
					)
					var noticeErr error
					if event.State == clientui.WorktreeTransitionFailed {
						noticeErr = admission.ApplyFailure(event)
					}
					grant, releaseErr := claim.Release()
					reductionErr := admission.ReduceAfterRelease(grant)
					complete(struct{}{}, errors.Join(
						admissionErr,
						publicationErr,
						noticeErr,
						releaseErr,
						reductionErr,
					))
					return nil
				},
			)
			if err != nil {
				return err
			}
			_, err = deferred.Await(s.transitionCtx)
			return err
		},
	)
	if err == nil {
		return nil
	}
	return errors.Join(err, claim.Cancel(err))
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
	if match.entry.Variant == serverapi.WorktreeTopologyVariantMissing {
		return scheduledWorktreeTarget{}, &serverapi.WorktreeSelectorError{
			Kind:  serverapi.WorktreeSelectorErrorKindUnavailable,
			Input: selector,
		}
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
	return syncedWorktree{}, fmt.Errorf("main worktree not found")
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
