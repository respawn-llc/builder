package worktree

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestDeleteWorktreeRequiresExplicitForceForDirtyTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-precondition")
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.WorktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	var precondition *serverapi.WorktreeDeletePreconditionError
	if !errors.As(err, &precondition) || precondition.DirtyState.Kind != serverapi.WorktreeDirtyStateDirty {
		t.Fatalf("delete error = %v, want dirty precondition", err)
	}
	if _, err := os.Stat(created.CanonicalRoot); err != nil {
		t.Fatalf("dirty worktree changed after rejected delete: %v", err)
	}
}

func TestDeleteWorktreeCompletesNonCurrentDeletionAndRetainsBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-completed")
	result, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.WorktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Kind != serverapi.WorktreeDeleteResultKindCompleted ||
		result.Completed == nil ||
		result.Completed.Cleanup.Kind != serverapi.WorktreeBranchCleanupOutcomeNotRequested {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree root still exists: %v", err)
	}
	exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, created.BranchName)
	if err != nil || !exists {
		t.Fatalf("retained branch exists=%v err=%v", exists, err)
	}
}

func TestDeleteWorktreeAutoCleanupRetainsBranchWhenLiveProvenanceChanged(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-provenance")
	liveBranch := "feature/delete-provenance-manual"
	runGit(t, created.CanonicalRoot, "switch", "-c", liveBranch)

	result, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.WorktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeAutoIfKentCreated,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Completed == nil || result.Completed.Cleanup.Kind != serverapi.WorktreeBranchCleanupOutcomeRetained {
		t.Fatalf("cleanup = %+v, want retained stale-provenance branch", result.Completed)
	}
	for _, branch := range []string{created.BranchName, liveBranch} {
		exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, branch)
		if err != nil || !exists {
			t.Fatalf("retained branch %q exists=%v err=%v", branch, exists, err)
		}
	}
}

func TestDeleteWorktreeUsesPersistedBranchFactsForMissingWorktree(t *testing.T) {
	for _, policy := range []serverapi.WorktreeBranchCleanupMode{
		serverapi.WorktreeBranchCleanupModeAutoIfKentCreated,
		serverapi.WorktreeBranchCleanupModeDeleteSafe,
	} {
		t.Run(string(policy), func(t *testing.T) {
			env := newServiceTestEnv(t)
			created := mustCreateWorktree(t, env, "feature/delete-missing-"+string(policy))
			if err := env.service.git.Remove(env.ctx, env.workspaceRoot, created.CanonicalRoot, true); err != nil {
				t.Fatalf("Remove worktree: %v", err)
			}
			if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, created.BranchName); err != nil || !exists {
				t.Fatalf("persisted branch before delete exists=%v err=%v", exists, err)
			}

			result, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
				OperationID:         serverapi.NewWorktreeOperationID(),
				SessionID:           env.session.Meta().SessionID,
				Selector:            created.WorktreeID,
				BranchCleanupPolicy: policy,
			})
			if err != nil {
				t.Fatalf("DeleteWorktree: %v", err)
			}
			if result.Completed == nil ||
				result.Completed.Cleanup.Kind != serverapi.WorktreeBranchCleanupOutcomeDeleted ||
				result.Completed.Cleanup.BranchName == nil ||
				*result.Completed.Cleanup.BranchName != created.BranchName {
				t.Fatalf("cleanup = %+v, want deleted persisted branch %q", result.Completed, created.BranchName)
			}
			if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, created.BranchName); err != nil || exists {
				t.Fatalf("persisted branch after delete exists=%v err=%v", exists, err)
			}
			if _, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("GetWorktreeRecordByID after delete error = %v, want sql.ErrNoRows", err)
			}
		})
	}
}

func TestDeleteCurrentWorktreeSchedulesRetargetAndRemoval(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-scheduled")
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	gate := make(chan struct{})
	env.runtime.transitionGate = gate
	operationID := serverapi.NewWorktreeOperationID()
	result, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		OperationID:         operationID,
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.WorktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Kind != serverapi.WorktreeDeleteResultKindScheduled ||
		result.Scheduled == nil ||
		result.Scheduled.OperationID != operationID {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(created.CanonicalRoot); err != nil {
		t.Fatalf("worktree removed before boundary: %v", err)
	}
	close(gate)
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.State != clientui.WorktreeTransitionCompleted || outcome.Transition != clientui.WorktreeTransitionDelete {
		t.Fatalf("outcome = %+v", outcome)
	}
	if target := mustResolveServiceTestTarget(t, env); target.Worktree != nil {
		t.Fatalf("session not retargeted to main: %+v", target)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree root still exists: %v", err)
	}
}

func TestScheduledDeleteRollsBackSessionTargetsWhenWorktreeRemovalFails(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-rollback")
	otherSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, otherSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	env.runtime.mu.Lock()
	env.runtime.activeSessions[otherSession.Meta().SessionID] = true
	env.runtime.mu.Unlock()
	currentBefore := mustResolveServiceTestTarget(t, env)
	otherBefore, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other before: %v", err)
	}
	runGit(t, env.workspaceRoot, "worktree", "lock", created.CanonicalRoot)
	t.Cleanup(func() {
		if _, err := os.Stat(created.CanonicalRoot); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "unlock", created.CanonicalRoot)
		}
	})

	operationID := serverapi.NewWorktreeOperationID()
	result, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
		OperationID:         operationID,
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.WorktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Kind != serverapi.WorktreeDeleteResultKindScheduled {
		t.Fatalf("result = %+v, want scheduled delete", result)
	}
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionFailed {
		t.Fatalf("outcome = %+v, want failed locked-worktree removal", outcome)
	}

	currentAfter := mustResolveServiceTestTarget(t, env)
	otherAfter, err := env.store.ResolveSessionExecutionTarget(env.ctx, otherSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget other after: %v", err)
	}
	for sessionID, targets := range map[string][2]clientui.SessionExecutionTarget{
		env.session.Meta().SessionID:  {currentBefore, currentAfter},
		otherSession.Meta().SessionID: {otherBefore, otherAfter},
	} {
		if sessionTargetWorktreeID(targets[1]) != sessionTargetWorktreeID(targets[0]) ||
			targets[1].EffectiveWorkdir != targets[0].EffectiveWorkdir {
			t.Fatalf("session %s target changed after failed removal: before=%+v after=%+v", sessionID, targets[0], targets[1])
		}
	}
	if _, err := os.Stat(created.CanonicalRoot); err != nil {
		t.Fatalf("locked worktree root changed after failed removal: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID); err != nil {
		t.Fatalf("worktree record changed after failed removal: %v", err)
	}
	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	for _, sessionID := range []string{env.session.Meta().SessionID, otherSession.Meta().SessionID} {
		if !slices.Contains(env.runtime.clearReminderSessions, sessionID) {
			t.Fatalf("session %s reminder was not cleared during rollback: %+v", sessionID, env.runtime.clearReminderSessions)
		}
		restoredRuntime := false
		for _, call := range env.runtime.rebindCalls {
			if call.sessionID == sessionID && call.root == created.CanonicalRoot {
				restoredRuntime = true
				break
			}
		}
		if !restoredRuntime {
			t.Fatalf("session %s runtime target was not restored: %+v", sessionID, env.runtime.rebindCalls)
		}
	}
}

func TestScheduledDeleteRemainsBoundToInitiallyResolvedWorktree(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-bound")
	updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	gate := make(chan struct{})
	env.runtime.transitionGate = gate

	request := serverapi.WorktreeDeleteRequest{
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.BranchName,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	}
	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Kind != serverapi.WorktreeDeleteResultKindScheduled {
		t.Fatalf("result = %+v, want scheduled delete", result)
	}

	runGit(t, created.CanonicalRoot, "switch", "-c", "feature/delete-bound-moved")
	selectorDriftRoot := filepath.Join(t.TempDir(), "selector-drift")
	runGit(t, env.workspaceRoot, "worktree", "add", selectorDriftRoot, created.BranchName)
	t.Cleanup(func() {
		if _, err := os.Stat(selectorDriftRoot); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "remove", "--force", selectorDriftRoot)
		}
	})
	retried, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree retry: %v", err)
	}
	if retried.Kind != serverapi.WorktreeDeleteResultKindScheduled ||
		retried.Scheduled == nil ||
		retried.Scheduled.OperationID != request.OperationID {
		t.Fatalf("retry result = %+v, want original scheduled acknowledgement", retried)
	}

	close(gate)
	outcome := waitForWorktreeTransitionOutcome(t, env.runtime)
	if outcome.State != clientui.WorktreeTransitionCompleted {
		t.Fatalf("outcome = %+v, want completed delete", outcome)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initially resolved worktree still exists: %v", err)
	}
	if _, err := os.Stat(selectorDriftRoot); err != nil {
		t.Fatalf("worktree that later acquired the selector was deleted: %v", err)
	}
}

func TestDeleteBlocksActiveOtherSessionAndRetargetsIdleOtherSession(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-targeting-sessions")
	activeSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	idleSession := createServiceTestSession(t, env.store, env.cfg, env.binding)
	updateServiceTestSessionTarget(t, env, activeSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	updateServiceTestSessionTarget(t, env, idleSession.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
	env.runtime.mu.Lock()
	if env.runtime.runningSessions == nil {
		env.runtime.runningSessions = make(map[string]bool)
	}
	env.runtime.runningSessions[activeSession.Meta().SessionID] = true
	env.runtime.mu.Unlock()
	request := serverapi.WorktreeDeleteRequest{
		OperationID:         serverapi.NewWorktreeOperationID(),
		SessionID:           env.session.Meta().SessionID,
		Selector:            created.WorktreeID,
		BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
	}
	if _, err := env.service.DeleteWorktree(env.ctx, request); !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("active-session delete error = %v", err)
	}
	if env.runtime.runsBlocked(activeSession.Meta().SessionID) || env.runtime.runsBlocked(idleSession.Meta().SessionID) {
		t.Fatal("blocked delete leaked session run exclusion")
	}
	env.runtime.mu.Lock()
	env.runtime.runningSessions[activeSession.Meta().SessionID] = false
	env.runtime.mu.Unlock()
	request.OperationID = serverapi.NewWorktreeOperationID()
	if _, err := env.service.DeleteWorktree(env.ctx, request); err != nil {
		t.Fatalf("idle-session delete: %v", err)
	}
	for _, sessionID := range []string{activeSession.Meta().SessionID, idleSession.Meta().SessionID} {
		target, err := env.store.ResolveSessionExecutionTarget(env.ctx, sessionID)
		if err != nil {
			t.Fatalf("ResolveSessionExecutionTarget(%s): %v", sessionID, err)
		}
		if target.Worktree != nil {
			t.Fatalf("session %s was not retargeted: %+v", sessionID, target)
		}
	}
}
