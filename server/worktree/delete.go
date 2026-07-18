package worktree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type worktreeBranchCleanupDecisionKind string

const (
	worktreeBranchCleanupNotApplicable worktreeBranchCleanupDecisionKind = "not_applicable"
	worktreeBranchCleanupRetain        worktreeBranchCleanupDecisionKind = "retain"
	worktreeBranchCleanupDelete        worktreeBranchCleanupDecisionKind = "delete"
)

type worktreeBranchCleanupDecision struct {
	kind    worktreeBranchCleanupDecisionKind
	branch  *localBranch
	outcome serverapi.WorktreeBranchCleanupOutcome
}

type worktreeExitReminderProjection struct {
	branch       *localBranch
	worktreePath string
}

type worktreeDeletionPlan struct {
	cleanup      worktreeBranchCleanupDecision
	exitReminder worktreeExitReminderProjection
	live         *GitWorktree
}

func (s *Service) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	transitionRequest := worktreeTransitionRequest{
		operationID: req.OperationID,
		sessionID:   strings.TrimSpace(req.SessionID),
		kind:        clientui.WorktreeTransitionDelete,
		selector:    strings.TrimSpace(req.Selector),
		force:       req.ForceFolderRemoval,
		cleanup:     req.BranchCleanupPolicy,
	}
	if ack, ok := s.replayPendingWorktreeTransition(transitionRequest); ok {
		return serverapi.WorktreeDeleteResult{
			Kind:      serverapi.WorktreeDeleteResultKindScheduled,
			Scheduled: &ack,
		}, nil
	}
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		release()
		return serverapi.WorktreeDeleteResult{}, err
	}
	match, err := resolveTopologySelector(topology, req.Selector)
	if err != nil {
		release()
		return serverapi.WorktreeDeleteResult{}, err
	}
	if topologyIsMain(match.entry) {
		release()
		return serverapi.WorktreeDeleteResult{}, fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	if topologyIsCurrent(match.entry, workspaceCtx.target) {
		record, err := s.deleteRecord(ctx, match.entry)
		if err != nil {
			release()
			return serverapi.WorktreeDeleteResult{}, err
		}
		plan, err := planWorktreeDeletion(match.entry, record, req.BranchCleanupPolicy)
		if err != nil {
			release()
			return serverapi.WorktreeDeleteResult{}, err
		}
		target, err := deletionTarget(workspaceCtx, match.entry, record, plan)
		if err != nil {
			release()
			return serverapi.WorktreeDeleteResult{}, err
		}
		if err := s.ensureDeleteFolderRemovalAuthorized(ctx, target, req.ForceFolderRemoval); err != nil {
			release()
			return serverapi.WorktreeDeleteResult{}, err
		}
		deleteTarget, err := scheduledKentWorktreeTargetFromEntry(match.entry)
		if err != nil {
			release()
			return serverapi.WorktreeDeleteResult{}, err
		}
		release()
		ack, err := s.scheduleWorktreeTransition(ctx, transitionRequest, func(runCtx context.Context, _ transitionAuthority, sync transitionTargetSync) error {
			_, err := s.executeScheduledDelete(runCtx, req, deleteTarget, sync)
			return err
		}, nil)
		if err != nil {
			return serverapi.WorktreeDeleteResult{}, err
		}
		return serverapi.WorktreeDeleteResult{
			Kind:      serverapi.WorktreeDeleteResultKindScheduled,
			Scheduled: &ack,
		}, nil
	}
	defer release()
	completed, err := s.executeDeleteLocked(ctx, workspaceCtx, match.entry, req, nil)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	return serverapi.WorktreeDeleteResult{
		Kind:      serverapi.WorktreeDeleteResultKindCompleted,
		Completed: &completed,
	}, nil
}

func (s *Service) executeScheduledDelete(
	ctx context.Context,
	req serverapi.WorktreeDeleteRequest,
	deleteTarget scheduledWorktreeTarget,
	sync transitionTargetSync,
) (serverapi.WorktreeDeleteCompletedResult, error) {
	release, workspaceCtx, err := s.beginWorkspaceMutation(ctx, req.SessionID)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	defer release()
	topology, err := s.projectTopology(ctx, workspaceCtx.workspaceID, workspaceCtx.workspaceRoot)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	entry, err := deleteTarget.resolve(topology)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	return s.executeDeleteLocked(ctx, workspaceCtx, entry, req, sync)
}

func (s *Service) executeDeleteLocked(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	entry serverapi.WorktreeTopologyEntry,
	req serverapi.WorktreeDeleteRequest,
	currentSync transitionTargetSync,
) (serverapi.WorktreeDeleteCompletedResult, error) {
	if topologyIsMain(entry) {
		return serverapi.WorktreeDeleteCompletedResult{}, fmt.Errorf("cannot delete main workspace worktree: %w", serverapi.ErrWorktreeBlocked)
	}
	record, err := s.deleteRecord(ctx, entry)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	if record != nil {
		if err := s.ensureNoManagedTaskBlockers(ctx, record.ID); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	releaseRuns, blockers, err := s.freezeDeleteTargetSessions(ctx, workspaceCtx.sessionID, record)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	defer releaseRuns()
	if len(blockers) > 0 {
		return serverapi.WorktreeDeleteCompletedResult{}, activeDeleteBlockerError(blockers)
	}
	plan, err := planWorktreeDeletion(entry, record, req.BranchCleanupPolicy)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	target, err := deletionTarget(workspaceCtx, entry, record, plan)
	if err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	if target != nil {
		if processBlockers := s.backgroundProcessBlockers(target.record.CanonicalRoot); len(processBlockers) > 0 {
			return serverapi.WorktreeDeleteCompletedResult{}, errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree has active background processes: %s", strings.Join(processBlockers, ", ")))
		}
		if err := s.ensureDeleteFolderRemovalAuthorized(ctx, target, req.ForceFolderRemoval); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	retargetCompensation := worktreeSessionRetargetCompensation{}
	if record != nil {
		retargetCompensation, err = s.retargetDeleteSessions(ctx, workspaceCtx, *record, plan.exitReminder, currentSync)
		if err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	leftoverRoot := missingLeftoverRoot(entry)
	if target != nil {
		var err error
		if req.ForceFolderRemoval && entry.Variant == serverapi.WorktreeTopologyVariantRegistered &&
			entry.Registered != nil && entry.Registered.Git.PrunableReason != nil {
			err = s.git.ForceRemovePrunableWorktree(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot)
		} else {
			err = s.git.Remove(ctx, workspaceCtx.workspaceRoot, target.record.CanonicalRoot, req.ForceFolderRemoval)
		}
		if err != nil {
			var recoveryError *PrunableWorktreeRecoveryError
			if errors.As(err, &recoveryError) && recoveryError.Destructive {
				return serverapi.WorktreeDeleteCompletedResult{}, err
			}
			return serverapi.WorktreeDeleteCompletedResult{}, errors.Join(err, retargetCompensation.rollback(ctx))
		}
	}
	if record != nil {
		if err := s.metadata.DeleteWorktreeRecordByID(ctx, record.ID); err != nil {
			return serverapi.WorktreeDeleteCompletedResult{}, err
		}
	}
	cleanup := s.executeWorktreeBranchCleanup(ctx, workspaceCtx.workspaceRoot, plan.cleanup)
	result := serverapi.WorktreeDeleteCompletedResult{Cleanup: cleanup, LeftoverRoot: leftoverRoot}
	if err := result.Validate(); err != nil {
		return serverapi.WorktreeDeleteCompletedResult{}, err
	}
	return result, nil
}

func (s *Service) ensureDeleteFolderRemovalAuthorized(
	ctx context.Context,
	target *syncedWorktree,
	forceFolderRemoval bool,
) error {
	if target == nil {
		return nil
	}
	dirtyState, err := s.git.ProbeDirtyState(ctx, target.record.CanonicalRoot)
	if err != nil {
		return err
	}
	if dirtyState.Kind != serverapi.WorktreeDirtyStateClean && !forceFolderRemoval {
		return &serverapi.WorktreeDeletePreconditionError{DirtyState: dirtyState}
	}
	return nil
}

func (s *Service) deleteRecord(ctx context.Context, entry serverapi.WorktreeTopologyEntry) (*metadata.WorktreeRecord, error) {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.Registered.Kent.WorktreeID)
		if err != nil {
			return nil, err
		}
		return &record, nil
	case serverapi.WorktreeTopologyVariantExternal:
		return nil, nil
	case serverapi.WorktreeTopologyVariantMissing:
		record, err := s.metadata.GetWorktreeRecordByID(ctx, entry.Missing.Kent.WorktreeID)
		if err != nil {
			return nil, err
		}
		return &record, nil
	default:
		return nil, errors.New("worktree topology variant is invalid")
	}
}

func deletionTarget(
	workspaceCtx sessionWorkspaceContext,
	entry serverapi.WorktreeTopologyEntry,
	record *metadata.WorktreeRecord,
	plan worktreeDeletionPlan,
) (*syncedWorktree, error) {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		if record == nil || plan.live == nil {
			return nil, errors.New("registered deletion requires Kent record and live Git facts")
		}
		return &syncedWorktree{record: *record, git: *plan.live}, nil
	case serverapi.WorktreeTopologyVariantExternal:
		if plan.live == nil {
			return nil, errors.New("external deletion requires live Git facts")
		}
		return &syncedWorktree{
			record: metadata.WorktreeRecord{
				WorkspaceID:   workspaceCtx.workspaceID,
				CanonicalRoot: plan.live.Root,
				DisplayName:   filepath.Base(plan.live.Root),
			},
			git: *plan.live,
		}, nil
	case serverapi.WorktreeTopologyVariantMissing:
		return nil, nil
	default:
		return nil, errors.New("worktree topology variant is invalid")
	}
}

func topologyIsMain(entry serverapi.WorktreeTopologyEntry) bool {
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		return entry.Registered.Git.IsMain
	case serverapi.WorktreeTopologyVariantExternal:
		return entry.External.Git.IsMain
	default:
		return false
	}
}

func (s *Service) ensureNoManagedTaskBlockers(ctx context.Context, worktreeID string) error {
	taskBlockers, err := s.metadata.Queries().CountNonTerminalTasksByManagedWorktree(ctx, sql.NullString{String: strings.TrimSpace(worktreeID), Valid: true})
	if err != nil {
		return err
	}
	if taskBlockers > 0 {
		return errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still managed by %d non-terminal workflow task(s)", taskBlockers))
	}
	return nil
}

func (s *Service) freezeDeleteTargetSessions(
	ctx context.Context,
	currentSessionID string,
	record *metadata.WorktreeRecord,
) (func(), []metadata.WorktreeSessionBlocker, error) {
	if record == nil {
		return func() {}, nil, nil
	}
	sessions, err := s.metadata.ListSessionsTargetingWorktree(ctx, record.ID)
	if err != nil {
		return func() {}, nil, err
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, target := range sessions {
		if id := strings.TrimSpace(target.SessionID); id != "" {
			sessionIDs = append(sessionIDs, id)
		}
	}
	release := func() {}
	if s.active != nil && len(sessionIDs) > 0 {
		release = s.active.BlockSessionRuns(sessionIDs)
	}
	activeBlockers := make([]metadata.WorktreeSessionBlocker, 0)
	for _, target := range sessions {
		sessionID := strings.TrimSpace(target.SessionID)
		if sessionID == "" || sessionID == strings.TrimSpace(currentSessionID) {
			continue
		}
		active, err := s.runtime.HasBlockingRuntimeActivity(ctx, sessionID)
		if err != nil {
			release()
			return func() {}, nil, err
		}
		if active {
			activeBlockers = append(activeBlockers, target)
		}
	}
	return release, activeBlockers, nil
}

func activeDeleteBlockerError(blockers []metadata.WorktreeSessionBlocker) error {
	sort.Slice(blockers, func(i int, j int) bool {
		return blockers[i].UpdatedAt.After(blockers[j].UpdatedAt)
	})
	names := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		name := strings.TrimSpace(blocker.SessionName)
		if name == "" {
			name = strings.TrimSpace(blocker.SessionID)
		}
		names = append(names, name)
	}
	return errors.Join(serverapi.ErrWorktreeBlocked, fmt.Errorf("worktree is still targeted by active runs: %s", strings.Join(names, ", ")))
}

func (s *Service) retargetDeleteSessions(
	ctx context.Context,
	workspaceCtx sessionWorkspaceContext,
	record metadata.WorktreeRecord,
	projection worktreeExitReminderProjection,
	currentSync transitionTargetSync,
) (worktreeSessionRetargetCompensation, error) {
	return s.retargetSessionsFromWorktree(
		ctx,
		workspaceCtx.workspaceID,
		workspaceCtx.workspaceRoot,
		record,
		worktreeSessionRetargetOptions{
			reminder: func(_ metadata.WorktreeRecord, nextTarget clientui.SessionExecutionTarget) (session.WorktreeReminderState, error) {
				return projection.reminder(nextTarget), nil
			},
			sync: func(
				syncCtx context.Context,
				sessionID string,
				target clientui.SessionExecutionTarget,
				reminder *session.WorktreeReminderState,
			) error {
				return s.syncDeleteSession(syncCtx, workspaceCtx.sessionID, sessionID, target, reminder, currentSync)
			},
			rollbackOnError: true,
		},
	)
}

func (p worktreeExitReminderProjection) reminder(nextTarget clientui.SessionExecutionTarget) session.WorktreeReminderState {
	branchName := ""
	if p.branch != nil {
		branchName = p.branch.Name()
	}
	return session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeExit,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch(branchName),
			WorktreePath:  strings.TrimSpace(p.worktreePath),
			WorkspaceRoot: strings.TrimSpace(nextTarget.WorkspaceRoot),
			EffectiveCwd:  strings.TrimSpace(nextTarget.EffectiveWorkdir),
		},
	}
}

func (s *Service) syncDeleteSession(
	ctx context.Context,
	currentSessionID string,
	sessionID string,
	target clientui.SessionExecutionTarget,
	reminder *session.WorktreeReminderState,
	currentSync transitionTargetSync,
) error {
	if strings.TrimSpace(sessionID) == strings.TrimSpace(currentSessionID) && currentSync != nil {
		return currentSync(ctx, target, reminder)
	}
	return s.runtime.SyncExecutionTarget(ctx, sessionID, target, reminder)
}

func missingLeftoverRoot(entry serverapi.WorktreeTopologyEntry) *string {
	if entry.Variant != serverapi.WorktreeTopologyVariantMissing {
		return nil
	}
	root := strings.TrimSpace(entry.Missing.Kent.CanonicalRoot)
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	return &root
}

func planWorktreeDeletion(
	entry serverapi.WorktreeTopologyEntry,
	record *metadata.WorktreeRecord,
	policy serverapi.WorktreeBranchCleanupMode,
) (worktreeDeletionPlan, error) {
	if err := entry.Validate(); err != nil {
		return worktreeDeletionPlan{}, err
	}
	if err := policy.Validate(); err != nil {
		return worktreeDeletionPlan{}, err
	}
	if entry.Variant == serverapi.WorktreeTopologyVariantMissing {
		if record == nil {
			return worktreeDeletionPlan{}, errors.New("missing worktree topology requires a Kent record")
		}
		return worktreeDeletionPlan{
			cleanup: worktreeBranchCleanupDecision{
				kind:    worktreeBranchCleanupNotApplicable,
				outcome: serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotApplicable},
			},
			exitReminder: worktreeExitReminderProjection{worktreePath: record.CanonicalRoot},
		}, nil
	}
	var facts serverapi.WorktreeGitFacts
	switch entry.Variant {
	case serverapi.WorktreeTopologyVariantRegistered:
		facts = entry.Registered.Git
	case serverapi.WorktreeTopologyVariantExternal:
		facts = entry.External.Git
	default:
		return worktreeDeletionPlan{}, errors.New("worktree topology variant is invalid")
	}
	live, err := gitWorktreeFromValidatedFacts(facts)
	if err != nil {
		return worktreeDeletionPlan{}, err
	}
	plan := worktreeDeletionPlan{
		live: &live,
		exitReminder: worktreeExitReminderProjection{
			branch:       live.Branch,
			worktreePath: live.Root,
		},
	}
	if live.Branch == nil {
		plan.cleanup = worktreeBranchCleanupDecision{
			kind:    worktreeBranchCleanupNotApplicable,
			outcome: serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotApplicable},
		}
		return plan, nil
	}
	branchName := live.Branch.Name()
	switch policy {
	case serverapi.WorktreeBranchCleanupModeRetain:
		plan.cleanup = worktreeBranchCleanupDecision{
			kind:    worktreeBranchCleanupRetain,
			branch:  live.Branch,
			outcome: serverapi.WorktreeBranchCleanupOutcome{Kind: serverapi.WorktreeBranchCleanupOutcomeNotRequested},
		}
	case serverapi.WorktreeBranchCleanupModeAutoIfKentCreated:
		if record == nil {
			diagnostic := "Kent cannot prove this worktree created the branch"
			plan.cleanup = worktreeBranchCleanupDecision{
				kind:   worktreeBranchCleanupRetain,
				branch: live.Branch,
				outcome: serverapi.WorktreeBranchCleanupOutcome{
					Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
					BranchName: &branchName,
					Diagnostic: &diagnostic,
				},
			}
			return plan, nil
		}
		createdBranch, proven, err := kentCreatedBranchForCleanup(*record, &live)
		if err != nil {
			diagnostic := err.Error()
			plan.cleanup = worktreeBranchCleanupDecision{
				kind:   worktreeBranchCleanupRetain,
				branch: live.Branch,
				outcome: serverapi.WorktreeBranchCleanupOutcome{
					Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
					BranchName: &branchName,
					Diagnostic: &diagnostic,
				},
			}
			return plan, nil
		}
		if !proven {
			diagnostic := "Kent cannot prove this worktree created the branch"
			plan.cleanup = worktreeBranchCleanupDecision{
				kind:   worktreeBranchCleanupRetain,
				branch: live.Branch,
				outcome: serverapi.WorktreeBranchCleanupOutcome{
					Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
					BranchName: &branchName,
					Diagnostic: &diagnostic,
				},
			}
			return plan, nil
		}
		if createdBranch != branchName {
			return worktreeDeletionPlan{}, fmt.Errorf("created branch %q does not match live branch %q", createdBranch, branchName)
		}
		plan.cleanup = worktreeBranchCleanupDecision{kind: worktreeBranchCleanupDelete, branch: live.Branch}
	case serverapi.WorktreeBranchCleanupModeDeleteSafe:
		plan.cleanup = worktreeBranchCleanupDecision{kind: worktreeBranchCleanupDelete, branch: live.Branch}
	}
	return plan, nil
}

func (s *Service) executeWorktreeBranchCleanup(
	ctx context.Context,
	workspaceRoot string,
	decision worktreeBranchCleanupDecision,
) serverapi.WorktreeBranchCleanupOutcome {
	switch decision.kind {
	case worktreeBranchCleanupNotApplicable, worktreeBranchCleanupRetain:
		return decision.outcome
	case worktreeBranchCleanupDelete:
	default:
		panic(fmt.Sprintf("invalid worktree branch cleanup decision %q", decision.kind))
	}
	if decision.branch == nil {
		panic("delete worktree branch cleanup decision has no branch")
	}
	branchName := decision.branch.Name()
	if err := s.git.deleteBranch(ctx, workspaceRoot, branchName, false); err != nil {
		diagnostic := err.Error()
		return serverapi.WorktreeBranchCleanupOutcome{
			Kind:       serverapi.WorktreeBranchCleanupOutcomeRetained,
			BranchName: &branchName,
			Diagnostic: &diagnostic,
		}
	}
	return serverapi.WorktreeBranchCleanupOutcome{
		Kind:       serverapi.WorktreeBranchCleanupOutcomeDeleted,
		BranchName: &branchName,
	}
}
