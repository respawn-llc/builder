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
}

type transitionTargetSync func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error

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
	return s.scheduleWorktreeTransition(ctx, request, func(runCtx context.Context, sync transitionTargetSync) error {
		return s.executeEnterWorktree(runCtx, request.sessionID, target, sync)
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
	return s.scheduleWorktreeTransition(ctx, request, func(runCtx context.Context, sync transitionTargetSync) error {
		return s.executeLeaveWorktree(runCtx, request.sessionID, sync)
	})
}

func (s *Service) scheduleWorktreeTransition(
	_ context.Context,
	request worktreeTransitionRequest,
	execute func(context.Context, transitionTargetSync) error,
) (serverapi.WorktreeScheduledAcknowledgement, error) {
	if s == nil || s.runtime == nil {
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
	s.transitions[request.sessionID] = pendingWorktreeTransition{request: request}
	s.transitionWG.Add(1)
	s.transitionMu.Unlock()

	go s.runWorktreeTransition(request, execute)
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: request.operationID}, nil
}

func (s *Service) runWorktreeTransition(request worktreeTransitionRequest, execute func(context.Context, transitionTargetSync) error) {
	defer s.transitionWG.Done()
	err := s.runtime.RunWorktreeTransition(
		s.transitionCtx,
		request.sessionID,
		func(ctx context.Context, sync func(context.Context, clientui.SessionExecutionTarget, *session.WorktreeReminderState) error) error {
			return execute(ctx, func(syncCtx context.Context, target clientui.SessionExecutionTarget, reminder *session.WorktreeReminderState) error {
				return sync(syncCtx, target, reminder)
			})
		},
	)
	if s.transitionCtx.Err() == nil {
		outcome := clientui.WorktreeTransitionOutcome{
			OperationID: request.operationID,
			Transition:  request.kind,
			State:       clientui.WorktreeTransitionCompleted,
		}
		if err != nil {
			outcome.State = clientui.WorktreeTransitionFailed
			outcome.Failure = &clientui.WorktreeTransitionFailure{Diagnostic: err.Error()}
		}
		s.runtime.PublishWorktreeTransitionOutcome(request.sessionID, outcome)
		if err != nil {
			_ = s.runtime.SteerWorktreeTransitionFailure(s.transitionCtx, request.sessionID, outcome)
		}
	}
	s.transitionMu.Lock()
	if pending, ok := s.transitions[request.sessionID]; ok && pending.request == request {
		delete(s.transitions, request.sessionID)
	}
	s.transitionMu.Unlock()
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

func (s *Service) executeEnterWorktree(ctx context.Context, sessionID string, target scheduledWorktreeTarget, sync transitionTargetSync) error {
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
	_, err = s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, next, sync)
	return err
}

func (s *Service) executeLeaveWorktree(ctx context.Context, sessionID string, sync transitionTargetSync) error {
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
	_, err = s.switchSessionTargetWithSync(ctx, workspaceCtx, previous, main, sync)
	return err
}

func (s *Service) enterTransitionWorktree(ctx context.Context, workspaceCtx sessionWorkspaceContext, entry serverapi.WorktreeTopologyEntry) (syncedWorktree, error) {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.Registered.Kent.WorktreeID)
		if err != nil {
			return syncedWorktree{}, err
		}
		return syncedWorktree{record: record, git: gitWorktreeFromFacts(entry.Registered.Git)}, nil
	case serverapi.WorktreeTopologyVariantExternal:
		if entry.External.Git.IsMain {
			return syncedWorktree{
				record: metadata.WorktreeRecord{WorkspaceID: workspaceCtx.workspaceID, CanonicalRoot: workspaceCtx.workspaceRoot},
				git:    gitWorktreeFromFacts(entry.External.Git),
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
	gitEntry := gitWorktreeFromFacts(facts)
	now := time.Now().UTC()
	record := metadata.WorktreeRecord{
		ID:            uuid.NewString(),
		WorkspaceID:   strings.TrimSpace(workspaceID),
		CanonicalRoot: strings.TrimSpace(facts.CanonicalRoot),
		DisplayName:   filepath.Base(strings.TrimSpace(facts.CanonicalRoot)),
		Availability:  pathAvailability(facts.CanonicalRoot),
		IsMain:        facts.IsMain,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	var err error
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
			value := syncedWorktree{record: record, git: gitWorktreeFromFacts(entry.Registered.Git)}
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
				return syncedWorktree{
					record: metadata.WorktreeRecord{
						ID:            entry.Registered.Kent.WorktreeID,
						WorkspaceID:   "",
						CanonicalRoot: entry.Registered.Git.CanonicalRoot,
					},
					git: gitWorktreeFromFacts(entry.Registered.Git),
				}, nil
			}
		case serverapi.WorktreeTopologyVariantExternal:
			if entry.External.Git.IsMain {
				return syncedWorktree{
					record: metadata.WorktreeRecord{CanonicalRoot: strings.TrimSpace(workspaceRoot)},
					git:    gitWorktreeFromFacts(entry.External.Git),
				}, nil
			}
		}
	}
	return syncedWorktree{}, fmt.Errorf("main worktree not found")
}

func gitWorktreeFromFacts(facts serverapi.WorktreeGitFacts) GitWorktree {
	return GitWorktree{
		Root:           strings.TrimSpace(facts.CanonicalRoot),
		HeadOID:        strings.TrimSpace(facts.HeadObject),
		BranchRef:      optionalString(facts.BranchRef),
		BranchName:     optionalString(facts.BranchName),
		Detached:       facts.Detached,
		Bare:           facts.Bare,
		LockedReason:   optionalString(facts.LockedReason),
		PrunableReason: optionalString(facts.PrunableReason),
		IsMain:         facts.IsMain,
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
