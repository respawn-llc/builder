package workflowrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/workflowtest"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestEnsureCurrentNodeAssignmentForScriptIsCommittedNoOp(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	scriptPath := filepath.Join(f.workspace, "unused-script")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write Script executable: %v", err)
	}
	workflowID := createCurrentNodeScriptWorkflow(t, f.store, scriptPath)
	task := f.createTask(t, workflowID)
	lockCurrentNodeAssignmentExecutionTarget(t, f, task.ID)
	currentNode := f.publishTaskStart(t, task.ID).Mutation.Created[0]

	ensure, err := f.starter.EnsureCurrentNodeAssignment(
		context.Background(),
		currentNode.Reference,
		workflowruntime.TaskPromptDeliveryAssignment,
	)
	if err != nil {
		t.Fatalf("EnsureCurrentNodeAssignment: %v", err)
	}
	receipt, err := ensure.Wait(t.Context())
	if err != nil || !receipt.Committed {
		t.Fatalf("Script assignment ensure = %+v, error = %v; want committed no-op", receipt, err)
	}
	if count, err := f.store.CountTaskSessions(context.Background(), task.ID); err != nil || count != 0 {
		t.Fatalf("Script Task Sessions = %d, error = %v; want none", count, err)
	}
}

func TestEnsureCurrentNodeAssignmentReusesMatchingAgentAssignmentAfterStartupRecovery(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	lockCurrentNodeAssignmentExecutionTarget(t, f, task.ID)
	currentNode := f.publishTaskStart(t, task.ID).Mutation.Created[0]

	first, err := f.starter.EnsureCurrentNodeAssignment(
		context.Background(),
		currentNode.Reference,
		workflowruntime.TaskPromptDeliveryAssignment,
	)
	if err != nil {
		t.Fatalf("first EnsureCurrentNodeAssignment: %v", err)
	}
	if receipt, waitErr := first.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("first assignment ensure = %+v, error = %v", receipt, waitErr)
	}
	before := currentNodeWorkflowAssignmentCount(t, f, currentNode.Reference)
	if err := f.store.InterruptCurrentNode(
		context.Background(),
		currentNode.Reference,
		"workflow_startup_recovery",
		workflow.NewCurrentNodeInterruptionDetail("workflow_startup_recovery", nil),
	); err != nil {
		t.Fatalf("startup recovery interruption: %v", err)
	}
	publication, err := workflowstore.NewLifecyclePublication(f.store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	t.Cleanup(func() {
		if err := publication.Close(); err != nil {
			t.Errorf("close lifecycle publication: %v", err)
		}
	})
	delta, err := workflowstore.NewQueuedTaskLifecycleDelta(
		task.ID,
		[]workflow.CurrentNodeReference{currentNode.Reference},
	)
	if err != nil {
		t.Fatalf("NewQueuedTaskLifecycleDelta: %v", err)
	}
	if _, err := publication.PublishResume(context.Background(), delta); err != nil {
		t.Fatalf("PublishResume after startup recovery: %v", err)
	}

	second, err := f.starter.EnsureCurrentNodeAssignment(
		context.Background(),
		currentNode.Reference,
		workflowruntime.TaskPromptDeliveryResume,
	)
	if err != nil {
		t.Fatalf("second EnsureCurrentNodeAssignment: %v", err)
	}
	if receipt, waitErr := second.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("second assignment ensure = %+v, error = %v", receipt, waitErr)
	}
	if after := currentNodeWorkflowAssignmentCount(t, f, currentNode.Reference); after != before {
		t.Fatalf("matching assignment ensure appended assignment: before=%d after=%d", before, after)
	}
}

func TestResumeRepairsMissingRetainedSuccessorProvenance(t *testing.T) {
	f := newCurrentNodeRunnerFixture(t)
	workflowID := createCurrentNodeChainedWorkflow(t, f.store, workflow.ContextModeContinueSession)
	task := f.createTask(t, workflowID)
	lockCurrentNodeAssignmentExecutionTarget(t, f, task.ID)
	source := f.publishTaskStart(t, task.ID).Mutation.Created[0]

	sourceEnsure, err := f.starter.EnsureCurrentNodeAssignment(
		context.Background(),
		source.Reference,
		workflowruntime.TaskPromptDeliveryAssignment,
	)
	if err != nil {
		t.Fatalf("ensure source assignment: %v", err)
	}
	if receipt, waitErr := sourceEnsure.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("source assignment ensure = %+v, error = %v", receipt, waitErr)
	}
	sourceContext, err := f.store.ResolveCurrentNodeStartContext(context.Background(), source.Reference)
	if err != nil {
		t.Fatalf("resolve source context: %v", err)
	}
	if sourceContext.CurrentNode.SessionID == nil {
		t.Fatal("source Current Node has no retained Session")
	}
	sessionID := *sourceContext.CurrentNode.SessionID

	completed, err := workflowtest.CompleteCurrentNode(
		f.store,
		context.Background(),
		workflowstore.CurrentNodeCompletionRequest{
			Source:       source.Reference,
			TransitionID: "next",
		},
	)
	if err != nil {
		t.Fatalf("complete source: %v", err)
	}
	if len(completed.Mutation.Created) != 1 {
		t.Fatalf("completion created = %+v, want one successor", completed.Mutation.Created)
	}
	successor := completed.Mutation.Created[0]
	if successor.SessionID == nil || *successor.SessionID != sessionID {
		t.Fatalf("successor Session = %v, want retained %s", successor.SessionID, sessionID)
	}
	if _, err := f.metadata.DB().ExecContext(
		context.Background(),
		`DELETE FROM session_workflow_node_associations
WHERE session_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
		sessionID.String(),
		string(successor.Reference.NodeID),
	); err != nil {
		t.Fatalf("remove successor Session provenance after publication: %v", err)
	}
	if count := currentNodeSessionAssociationCount(t, f, sessionID, successor.Reference); count != 0 {
		t.Fatalf("successor Session associations after simulated crash = %d, want none", count)
	}

	if err := f.store.InterruptCurrentNode(
		context.Background(),
		successor.Reference,
		"workflow_startup_recovery",
		workflow.NewCurrentNodeInterruptionDetail("workflow_startup_recovery", nil),
	); err != nil {
		t.Fatalf("interrupt successor after simulated crash: %v", err)
	}
	publication, err := workflowstore.NewLifecyclePublication(f.store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	delta, err := workflowstore.NewQueuedTaskLifecycleDelta(
		task.ID,
		[]workflow.CurrentNodeReference{successor.Reference},
	)
	if err != nil {
		t.Fatalf("NewQueuedTaskLifecycleDelta: %v", err)
	}
	if _, err := publication.PublishResume(context.Background(), delta); err != nil {
		t.Fatalf("PublishResume: %v", err)
	}
	if count := currentNodeSessionAssociationCount(t, f, sessionID, successor.Reference); count != 1 {
		t.Fatalf("successor Session associations after first Resume = %d, want one", count)
	}

	before := currentNodeWorkflowAssignmentCount(t, f, successor.Reference)
	first, err := f.starter.EnsureCurrentNodeAssignment(
		context.Background(),
		successor.Reference,
		workflowruntime.TaskPromptDeliveryResume,
	)
	if err != nil {
		t.Fatalf("first successor Resume ensure: %v", err)
	}
	if receipt, waitErr := first.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("first successor Resume ensure = %+v, error = %v", receipt, waitErr)
	}
	afterFirst := currentNodeWorkflowAssignmentCount(t, f, successor.Reference)
	if afterFirst != before+1 {
		t.Fatalf("successor assignment count after first Resume = %d, want %d", afterFirst, before+1)
	}
	if err := publication.Close(); err != nil {
		t.Fatalf("close first lifecycle publication: %v", err)
	}
	if err := f.store.InterruptCurrentNode(
		context.Background(),
		successor.Reference,
		"workflow_startup_recovery",
		workflow.NewCurrentNodeInterruptionDetail("workflow_startup_recovery", nil),
	); err != nil {
		t.Fatalf("interrupt successor before repeated Resume: %v", err)
	}
	repeatedPublication, err := workflowstore.NewLifecyclePublication(f.store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication for repeated Resume: %v", err)
	}
	t.Cleanup(func() { _ = repeatedPublication.Close() })
	repeatedDelta, err := workflowstore.NewQueuedTaskLifecycleDelta(
		task.ID,
		[]workflow.CurrentNodeReference{successor.Reference},
	)
	if err != nil {
		t.Fatalf("NewQueuedTaskLifecycleDelta for repeated Resume: %v", err)
	}
	if _, err := repeatedPublication.PublishResume(context.Background(), repeatedDelta); err != nil {
		t.Fatalf("repeated PublishResume: %v", err)
	}
	if count := currentNodeSessionAssociationCount(t, f, sessionID, successor.Reference); count != 1 {
		t.Fatalf("successor Session associations after repeated Resume = %d, want one", count)
	}
	second, err := f.starter.EnsureCurrentNodeAssignment(
		context.Background(),
		successor.Reference,
		workflowruntime.TaskPromptDeliveryResume,
	)
	if err != nil {
		t.Fatalf("second successor Resume ensure: %v", err)
	}
	if receipt, waitErr := second.Wait(t.Context()); waitErr != nil || !receipt.Committed {
		t.Fatalf("second successor Resume ensure = %+v, error = %v", receipt, waitErr)
	}
	if afterSecond := currentNodeWorkflowAssignmentCount(t, f, successor.Reference); afterSecond != afterFirst {
		t.Fatalf("second Resume duplicated assignment: first=%d second=%d", afterFirst, afterSecond)
	}
	if err := f.store.ValidateCurrentNodeSessionBinding(
		context.Background(),
		sessionID,
		successor.Reference,
	); err != nil {
		t.Fatalf("validate successor Session binding after repeated Resume: %v", err)
	}
}

func currentNodeSessionAssociationCount(
	t *testing.T,
	f *currentNodeRunnerFixture,
	sessionID runtimeids.SessionID,
	reference workflow.CurrentNodeReference,
) int {
	t.Helper()
	var count int
	branchKey, branchScoped := reference.TransitionBranchKey()
	var err error
	if branchScoped {
		err = f.metadata.DB().QueryRowContext(
			context.Background(),
			`SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = ?
  AND node_id = ?
  AND transition_branch_key = ?`,
			sessionID.String(),
			string(reference.NodeID),
			string(branchKey),
		).Scan(&count)
	} else {
		err = f.metadata.DB().QueryRowContext(
			context.Background(),
			`SELECT COUNT(*)
FROM session_workflow_node_associations
WHERE session_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`,
			sessionID.String(),
			string(reference.NodeID),
		).Scan(&count)
	}
	if err != nil {
		t.Fatalf("count Current Node Session associations: %v", err)
	}
	return count
}

func lockCurrentNodeAssignmentExecutionTarget(
	t *testing.T,
	f *currentNodeRunnerFixture,
	taskID workflow.TaskID,
) {
	t.Helper()
	if err := f.store.LockTaskExecutionTarget(context.Background(), taskID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   f.workspaceID,
			SourceWorkspaceRoot: f.workspace,
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
}

func currentNodeWorkflowAssignmentCount(
	t *testing.T,
	f *currentNodeRunnerFixture,
	reference workflow.CurrentNodeReference,
) int {
	t.Helper()
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	if err != nil {
		t.Fatalf("ResolveCurrentNodeStartContext: %v", err)
	}
	if input.CurrentNode.SessionID == nil {
		t.Fatal("Agent Current Node has no bound Session")
	}
	descriptor, err := session.NewOpenSessionDescriptor(*input.CurrentNode.SessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	count := 0
	admission, err := f.authority.WithDormantSessionStore(context.Background(), descriptor, func(_ context.Context, store *session.Store) error {
		eventLog, materializeErr := store.MaterializeEventLog()
		if materializeErr != nil {
			return materializeErr
		}
		window, readErr := eventLog.ReadRecentRecords(64)
		if readErr != nil {
			return readErr
		}
		for _, record := range window.Records {
			payload, payloadErr := record.Payload()
			if payloadErr != nil {
				return payloadErr
			}
			message, ok := payload.(session.MessageRecord)
			if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeWorkflowMode {
				count++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read dormant workflow Session: %v", err)
	}
	if admission.RuntimeAvailable {
		t.Fatal("assignment ensure unexpectedly left an active Runtime")
	}
	return count
}
