package workflowstore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowscript"
)

func TestTaskCreateStartCancelAndComments(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Implement feature", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask default: %v", err)
	}
	if !strings.HasPrefix(task.ShortID, "WOR-1") {
		t.Fatalf("short id = %q, want WOR-1 prefix", task.ShortID)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements after create: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements after create = %+v", placements)
	}

	started := startTask(t, ctx, store, task.ID)
	if started.RunID == "" || started.PlacementID == "" {
		t.Fatalf("start result missing run/placement ids: %+v", started)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].AutomationRequestedAt == nil {
		t.Fatalf("runs after start = %+v", runs)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].TransitionID != "start" {
		t.Fatalf("transitions after start = %+v", transitions)
	}
	transitionEdges, err := store.ListTransitionEdges(ctx, transitions[0].ID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(transitionEdges) != 1 || transitionEdges[0].EdgeKey != "start" || transitionEdges[0].TargetPlacementID != started.PlacementID {
		t.Fatalf("transition edge snapshot after start = %+v", transitionEdges)
	}

	comment, err := store.AddComment(ctx, task.ID, " first note ", "agent", "coder")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := store.AddComment(ctx, task.ID, "system note", "system", ""); !errors.Is(err, ErrCommentAuthorKindInvalid) {
		t.Fatalf("system AddComment error = %v, want author kind validation", err)
	}
	if err := store.ReplaceComment(ctx, comment.ID, "updated"); err != nil {
		t.Fatalf("ReplaceComment: %v", err)
	}
	comments, err := store.ListComments(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "updated" {
		t.Fatalf("comments after replace = %+v", comments)
	}
	if err := store.DeleteComment(ctx, comment.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	comments, err = store.ListComments(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListComments visible: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("deleted comment should be hidden, got %+v", comments)
	}

	if err := store.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := store.CancelTask(ctx, "task-missing", "stop"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("CancelTask missing = %v, want sql.ErrNoRows", err)
	}
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after cancel: %v", err)
	}
	if runs[0].InterruptedAt == nil || runs[0].InterruptionReason == nil || *runs[0].InterruptionReason != "task_canceled" {
		t.Fatalf("run not interrupted by cancel: %+v", runs[0])
	}
	placements, err = store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements after cancel: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	activeDone := false
	activeNonTerminal := false
	for _, placement := range placements {
		if placement.NodeID == workflow.NodeIDOf(done) {
			if placement.State == "active" {
				activeDone = true
			}
			continue
		}
		if placement.State == "active" {
			activeNonTerminal = true
		}
	}
	if !activeDone || activeNonTerminal {
		t.Fatalf("placements after cancel = %+v, want active Done sink and no active non-terminal placement", placements)
	}
}

func TestStartTaskWithExecutionTargetLocksNoneAndCreatesRunAtomically(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	started, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &candidate)
	if err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
	if started.RunID == "" {
		t.Fatalf("started = %+v, want run", started)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot == nil || snapshot.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("locked snapshot = %+v, want none without managed worktree", snapshot)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != started.RunID {
		t.Fatalf("runs = %+v, want one started run", runs)
	}
	input, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.ExecutionRoot == nil ||
		input.ExecutionRoot.Managed != nil ||
		input.ExecutionRoot.SourceWorkspaceID != binding.WorkspaceID ||
		input.ExecutionRoot.EffectiveRoot() != binding.CanonicalRoot {
		t.Fatalf("run execution root = %+v, want source workspace %q at %q", input.ExecutionRoot, binding.WorkspaceID, binding.CanonicalRoot)
	}
}

func TestStartTaskWithExecutionTargetLeavesUnlockedTaskUnchangedWithoutCandidate(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	_, err := store.StartTaskWithExecutionTarget(ctx, task.ID, nil)
	if !errors.Is(err, ErrExecutionTargetRequired) {
		t.Fatalf("StartTaskWithExecutionTarget error = %v, want ErrExecutionTargetRequired", err)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want unlocked task", snapshot)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no run", runs)
	}
}

func TestStartTaskWithExecutionTargetLocksManagedProvisionalCandidate(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "worktree-start-target"
	worktreeRoot := t.TempDir()
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		Availability:  "available",
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET managed_worktree_id = ? WHERE id = ?`, worktreeID, string(task.ID)); err != nil {
		t.Fatalf("attach provisional worktree: %v", err)
	}
	requestedRef := "HEAD"
	commitOID := "0123456789abcdef"
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       worktreeRoot,
			},
		},
	}

	if _, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &candidate); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot == nil || snapshot.CommitOID == nil || *snapshot.CommitOID != commitOID || !row.ManagedWorktreeID.Valid || row.ManagedWorktreeID.String != worktreeID {
		t.Fatalf("locked managed snapshot = %+v", snapshot)
	}
}

func TestStartTaskWithExecutionTargetRejectsManagedCandidateWithoutMatchingProvisionalRelation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "worktree-unattached-target"
	worktreeRoot := t.TempDir()
	requestedRef := "HEAD"
	commitOID := "0123456789abcdef"
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       worktreeRoot,
			},
		},
	}

	_, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &candidate)
	if err == nil {
		t.Fatal("StartTaskWithExecutionTarget accepted unattached managed candidate")
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want unlocked", snapshot)
	}
}

func TestStartTaskWithExecutionTargetReusesLockedSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "worktree-locked-target"
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: t.TempDir(),
		Availability:  "available",
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET
    managed_worktree_id = ?,
    execution_target_mode = ?,
    execution_target_requested_ref = ?,
    execution_target_commit_oid = ?,
    execution_target_provenance = ?
WHERE id = ?`,
		worktreeID,
		string(workflow.ExecutionTargetModeHead),
		"HEAD",
		"0123456789abcdef",
		string(ExecutionTargetProvenanceResolved),
		string(task.ID)); err != nil {
		t.Fatalf("lock target fixture: %v", err)
	}

	if _, err := store.StartTaskWithExecutionTarget(ctx, task.ID, nil); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
}

func TestStartTaskWithExecutionTargetValidatesRelativeScriptInNoneSourceRoot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	scriptName := "source-script"
	if err := os.WriteFile(filepath.Join(binding.CanonicalRoot, scriptName), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	workflowID := createScriptStartWorkflow(t, ctx, store, scriptName)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	if _, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &candidate); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
}

func TestStartTaskWithExecutionTargetDoesNotLockWhenScriptValidationFails(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createScriptStartWorkflow(t, ctx, store, "missing-script")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}

	_, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &candidate)
	var validationErr workflowscript.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("StartTaskWithExecutionTarget error = %v, want script validation", err)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	snapshot, err := executionTargetSnapshotFromTask(row)
	if err != nil {
		t.Fatalf("executionTargetSnapshotFromTask: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want unlocked task", snapshot)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no run", runs)
	}
}

func TestStartTaskWithExecutionTargetConcurrentRequestsCreateOneLockedRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	candidate := ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{Mode: workflow.ExecutionTargetModeNone, Provenance: ExecutionTargetProvenanceResolved},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.StartTaskWithExecutionTarget(ctx, task.ID, &candidate)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	stale := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, sql.ErrNoRows):
			stale++
		default:
			t.Fatalf("StartTaskWithExecutionTarget concurrent error = %v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("concurrent starts successes=%d stale=%d, want 1/1", successes, stale)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want exactly one", runs)
	}
}

func TestTaskAndRunLifecycleAbsencePersistsAsNull(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	var canceledAt, startedAt, completedAt, interruptedAt sql.NullInt64
	var waitingAskID sql.NullString
	readFacts := func() {
		t.Helper()
		if err := store.db.QueryRowContext(ctx, `
SELECT
    t.canceled_at_unix_ms,
    r.started_at_unix_ms,
    r.completed_at_unix_ms,
    r.interrupted_at_unix_ms,
    r.waiting_ask_id
FROM tasks t
JOIN task_node_placements p ON p.task_id = t.id
JOIN task_runs r ON r.placement_id = p.id
WHERE t.id = ? AND r.id = ?
`, string(task.ID), string(started.RunID)).Scan(&canceledAt, &startedAt, &completedAt, &interruptedAt, &waitingAskID); err != nil {
			t.Fatalf("read lifecycle facts: %v", err)
		}
	}

	readFacts()
	if canceledAt.Valid || startedAt.Valid || completedAt.Valid || interruptedAt.Valid || waitingAskID.Valid {
		t.Fatalf("new lifecycle facts = canceled=%+v started=%+v completed=%+v interrupted=%+v waiting_ask=%+v, want NULL absence", canceledAt, startedAt, completedAt, interruptedAt, waitingAskID)
	}

	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-lifecycle-null"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	readFacts()
	if !startedAt.Valid || !waitingAskID.Valid || completedAt.Valid || interruptedAt.Valid {
		t.Fatalf("claimed/question lifecycle facts = started=%+v completed=%+v interrupted=%+v waiting_ask=%+v", startedAt, completedAt, interruptedAt, waitingAskID)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-lifecycle-null"); err != nil {
		t.Fatalf("ClearRunWaitingAsk: %v", err)
	}
	readFacts()
	if waitingAskID.Valid {
		t.Fatalf("cleared waiting ask = %+v, want NULL", waitingAskID)
	}
}

func TestCancelTaskAfterMovingOutOfDoneWritesCurrentTerminalPlacement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)}); err != nil {
		t.Fatalf("ManualMoveTask reset: %v", err)
	}

	if err := store.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	activeTerminalCount := 0
	supersededTerminalCount := 0
	for _, placement := range placements {
		if placement.NodeID != workflow.NodeIDOf(done) {
			continue
		}
		switch placement.State {
		case "active":
			activeTerminalCount++
		case "superseded":
			supersededTerminalCount++
		}
	}
	if activeTerminalCount != 1 || supersededTerminalCount != 1 {
		t.Fatalf("terminal placements after cancel = %+v, want one superseded history row and one active Done sink", placements)
	}
}

func TestListCommentsPageKeysetStaysStableWhenNewerCommentInserted(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Comments"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	first, err := store.AddComment(ctx, task.ID, "first", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	second, err := store.AddComment(ctx, task.ID, "second", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	setCommentCreatedAt(t, ctx, store, first.ID, 1000)
	setCommentCreatedAt(t, ctx, store, second.ID, 2000)

	page1, err := store.ListCommentsPage(ctx, task.ID, CommentPageCursor{}, 1)
	if err != nil {
		t.Fatalf("ListCommentsPage page1: %v", err)
	}
	if len(page1) != 1 || page1[0].ID != second.ID {
		t.Fatalf("page1 = %+v, want newest comment %q", page1, second.ID)
	}

	// A newer comment arriving between page reads must not shift the cursor:
	// an offset would now return the already-seen comment, a keyset must not.
	third, err := store.AddComment(ctx, task.ID, "third", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment third: %v", err)
	}
	setCommentCreatedAt(t, ctx, store, third.ID, 3000)

	cursor := CommentPageCursor{CreatedAtUnixMs: page1[0].CreatedAt, ID: page1[0].ID, HasValue: true}
	page2, err := store.ListCommentsPage(ctx, task.ID, cursor, 1)
	if err != nil {
		t.Fatalf("ListCommentsPage page2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != first.ID {
		t.Fatalf("page2 = %+v, want next-older comment %q with no duplicate/skip", page2, first.ID)
	}
}

func TestCountTaskCommentsCountsVisibleCurrentRows(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Comments"})
	otherTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Other"})

	assertCommentCount := func(taskID workflow.TaskID, want int64) {
		t.Helper()
		got, err := store.CountTaskComments(ctx, taskID)
		if err != nil {
			t.Fatalf("CountTaskComments(%s): %v", taskID, err)
		}
		if got != want {
			t.Fatalf("CountTaskComments(%s) = %d, want %d", taskID, got, want)
		}
	}
	assertCommentCount(task.ID, 0)

	first, err := store.AddComment(ctx, task.ID, "first", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	if _, err := store.AddComment(ctx, task.ID, "second", "agent", "coder"); err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	if _, err := store.AddComment(ctx, otherTask.ID, "other", "user", "nek"); err != nil {
		t.Fatalf("AddComment other: %v", err)
	}
	assertCommentCount(task.ID, 2)

	if err := store.DeleteComment(ctx, first.ID); err != nil {
		t.Fatalf("DeleteComment first: %v", err)
	}
	assertCommentCount(task.ID, 1)
}

func TestTaskCreatePersistsSourceWorkspaceAndOptionalBody(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	source, err := store.metadata.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}

	selected, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: " Selected ", SourceWorkspaceID: source.WorkspaceID})
	if err != nil {
		t.Fatalf("CreateTask selected source workspace: %v", err)
	}
	if selected.Body != "" || selected.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("selected task = %+v, want empty body and source workspace %q", selected, source.WorkspaceID)
	}
	defaulted, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Defaulted"})
	if err != nil {
		t.Fatalf("CreateTask default source workspace: %v", err)
	}
	if defaulted.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("default source workspace = %q, want primary %q", defaulted.SourceWorkspaceID, binding.WorkspaceID)
	}
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace other: %v", err)
	}
	if _, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Foreign", SourceWorkspaceID: other.WorkspaceID}); !errors.Is(err, ErrSourceWorkspaceNotInProject) {
		t.Fatalf("CreateTask foreign source workspace error = %v", err)
	}
}

func TestTaskUpdateEditsTitleAndBodyAfterAutomationStarts(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	source, err := store.metadata.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Before", Body: "before"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	afterTitle := " After "
	afterBody := " after body "
	updated, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &afterTitle, Body: &afterBody, SourceWorkspaceID: source.WorkspaceID})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.Title != "After" || updated.Body != "after body" || updated.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("updated task = %+v", updated)
	}
	renamedTitle := "Renamed"
	renamed, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &renamedTitle})
	if err != nil {
		t.Fatalf("UpdateTask title only: %v", err)
	}
	if renamed.Title != "Renamed" || renamed.Body != "after body" || renamed.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("title-only update = %+v, want previous body and source workspace preserved", renamed)
	}
	bodyOnlyBody := "body only change"
	bodyOnly, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Body: &bodyOnlyBody})
	if err != nil {
		t.Fatalf("UpdateTask body only: %v", err)
	}
	if bodyOnly.Title != "Renamed" || bodyOnly.Body != "body only change" || bodyOnly.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("body-only update = %+v, want previous title and source workspace preserved", bodyOnly)
	}
	startTask(t, ctx, store, task.ID)
	startedTitle := " After start "
	startedBody := " updated after start "
	startedUpdate, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &startedTitle, Body: &startedBody})
	if err != nil {
		t.Fatalf("UpdateTask after start: %v", err)
	}
	if startedUpdate.Title != "After start" || startedUpdate.Body != "updated after start" || startedUpdate.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("after-start update = %+v", startedUpdate)
	}
	moveSourceTitle := "Move source"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &moveSourceTitle, SourceWorkspaceID: binding.WorkspaceID}); !errors.Is(err, ErrSourceWorkspaceAfterAutomation) {
		t.Fatalf("UpdateTask source workspace after start error = %v", err)
	}

	canceled, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Canceled"})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	if err := store.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	canceledTitle := "Canceled renamed"
	canceledBody := "canceled body"
	canceledUpdate, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: canceled.ID, Title: &canceledTitle, Body: &canceledBody})
	if err != nil {
		t.Fatalf("UpdateTask canceled title/body: %v", err)
	}
	if canceledUpdate.Title != "Canceled renamed" || canceledUpdate.Body != "canceled body" {
		t.Fatalf("canceled update = %+v", canceledUpdate)
	}
	canceledSourceTitle := "Canceled source"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: canceled.ID, Title: &canceledSourceTitle, SourceWorkspaceID: source.WorkspaceID}); !errors.Is(err, ErrSourceWorkspaceForCanceledTask) {
		t.Fatalf("UpdateTask canceled source error = %v", err)
	}
}

func TestTaskUpdateRejectsSourceWorkspaceChangeForLockedManagedTargetWithoutBinding(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	otherWorkspace, err := store.metadata.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.db.ExecContext(ctx, `
UPDATE tasks
SET
    managed_worktree_id = NULL,
    execution_target_mode = ?,
    execution_target_requested_ref = ?,
    execution_target_commit_oid = ?,
    execution_target_provenance = ?
WHERE id = ?`,
		string(workflow.ExecutionTargetModeHead),
		"HEAD",
		"0123456789abcdef",
		string(ExecutionTargetProvenanceResolved),
		string(task.ID)); err != nil {
		t.Fatalf("lock managed target without binding: %v", err)
	}

	_, err = store.UpdateTask(ctx, UpdateTaskRequest{
		TaskID:            task.ID,
		SourceWorkspaceID: otherWorkspace.WorkspaceID,
	})
	if !errors.Is(err, ErrSourceWorkspaceAfterAutomation) {
		t.Fatalf("UpdateTask source workspace error = %v, want ErrSourceWorkspaceAfterAutomation", err)
	}
}

func TestDeleteTaskHardDeletesAssociatedRecords(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Delete me", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.AddComment(ctx, task.ID, "note", "user", "nek"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	deleted, err := store.DeleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if deleted.ID != task.ID || deleted.ProjectID != binding.ProjectID {
		t.Fatalf("deleted task identity = %+v, want task %q project %q", deleted, task.ID, binding.ProjectID)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	assertZeroTaskRows(t, store, "task_node_placements", string(task.ID))
	assertZeroTaskRows(t, store, "task_transitions", string(task.ID))
	assertZeroTaskRows(t, store, "task_comments", string(task.ID))
	if _, err := store.queries.GetTaskRun(ctx, string(started.RunID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskRun after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.DeleteTask(ctx, task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteTask missing = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteTaskHardDeletesParallelBatchRecords(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	// Fan-out leaves placements carrying parallel_batch_transition_id and
	// transition rows carrying source_placement_id/source_run_id. These are the
	// ON DELETE SET NULL cross-links whose runtime validation triggers previously
	// aborted a cascading task delete; deletion must remove them cleanly.
	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if len(result.PlacementIDs) != 2 {
		t.Fatalf("fanout result = %+v, want two parallel branch placements", result)
	}

	deleted, err := store.DeleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DeleteTask with parallel batch placements: %v", err)
	}
	if deleted.ID != task.ID {
		t.Fatalf("deleted task id = %q, want %q", deleted.ID, task.ID)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	assertZeroTaskRows(t, store, "task_node_placements", string(task.ID))
	assertZeroTaskRows(t, store, "task_transitions", string(task.ID))
	assertZeroTaskRows(t, store, "task_comments", string(task.ID))
}
