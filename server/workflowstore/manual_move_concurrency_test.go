package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
)

func TestManualMoveToNonExecutableWaitsForConcurrentWriterBeforeRevalidation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKind(t, definition, workflow.NodeKindStart)
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}

	moveStore, writerStore := openConcurrentManualMoveStores(t, cfg)
	writer := acquireUnrelatedManualMoveWriter(t, ctx, writerStore, binding.ProjectID)
	moveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	moved := make(chan manualMoveApplyResult, 1)
	go func() {
		result, err := moveStore.ApplyManualMove(moveCtx, prepared, nil)
		moved <- manualMoveApplyResult{result: result, err: err}
	}()
	assertManualMoveWaitsForWriter(t, moved)
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit competing writer: %v", err)
	}
	applied := <-moved
	if applied.err != nil {
		t.Fatalf("ApplyManualMove after competing commit: %v", applied.err)
	}
	if applied.result.Outcome != ManualMoveResultOutcomeApplied ||
		len(applied.result.Mutation.Created) != 1 ||
		applied.result.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("manual move result = %+v, want applied non-executable target", applied.result)
	}
}

func TestManualMoveToExecutableWaitsForConcurrentWriterBeforeRevalidationAndLocksTarget(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	candidate := noneManualMoveExecutionTargetCandidate(binding)

	moveStore, writerStore := openConcurrentManualMoveStores(t, cfg)
	writer := acquireUnrelatedManualMoveWriter(t, ctx, writerStore, binding.ProjectID)
	moveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	moved := make(chan manualMoveApplyResult, 1)
	go func() {
		result, err := moveStore.ApplyManualMove(moveCtx, prepared, candidate)
		moved <- manualMoveApplyResult{result: result, err: err}
	}()
	assertManualMoveWaitsForWriter(t, moved)
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit competing writer: %v", err)
	}
	applied := <-moved
	if applied.err != nil {
		t.Fatalf("ApplyManualMove after competing commit: %v", applied.err)
	}
	if applied.result.Outcome != ManualMoveResultOutcomeApplied ||
		len(applied.result.Mutation.Created) != 1 ||
		applied.result.Mutation.Created[0].Reference.NodeID != workflow.NodeIDOf(target) {
		t.Fatalf("manual move result = %+v, want applied executable target", applied.result)
	}
	targetContext, err := moveStore.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("execution target after manual move = %+v, want locked none target", targetContext.Task.ExecutionTarget)
	}
}

func TestManualMoveExecutableRejectsBranchKindDriftAfterTargetValidation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "impl_a")
	driftingBranch := nodeByKey(t, definition, "impl_b")
	transition := workflow.TransitionID("split")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:        task.ID,
		TargetNodeID:  workflow.NodeIDOf(target),
		TransitionKey: &transition,
		Values:        map[workflow.ModelKey]map[string]string{"plan": {"summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}

	moveStore, writerStore := openConcurrentManualMoveStores(t, cfg)
	writer := acquireUnrelatedManualMoveWriter(t, ctx, writerStore, binding.ProjectID)
	moveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	moved := make(chan manualMoveApplyResult, 1)
	go func() {
		result, err := moveStore.ApplyManualMove(moveCtx, prepared, noneManualMoveExecutionTargetCandidate(binding))
		moved <- manualMoveApplyResult{result: result, err: err}
	}()
	assertManualMoveWaitsForWriter(t, moved)
	if _, err := writer.ExecContext(
		ctx,
		`UPDATE workflow_nodes
		 SET kind = 'script',
		     subagent_role = '',
		     completion_mode = '',
		     script_path = 'scripts/branch'
		 WHERE id = ?`,
		workflow.NodeIDOf(driftingBranch),
	); err != nil {
		t.Fatalf("change competing branch kind: %v", err)
	}
	if _, err := writer.ExecContext(
		ctx,
		`UPDATE workflow_edges
		 SET prompt_template = ''
		 WHERE target_node_id = ?`,
		workflow.NodeIDOf(driftingBranch),
	); err != nil {
		t.Fatalf("change competing branch prompt: %v", err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit competing workflow change: %v", err)
	}
	assertManualMoveTargetShapeDriftRejected(t, ctx, moveStore, task.ID, source.Reference, <-moved)
}

func TestManualMoveExecutableRejectsScriptPathDriftAfterTargetValidation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	const validatedScriptPath = "scripts/validated"
	absoluteScriptPath := filepath.Join(binding.CanonicalRoot, validatedScriptPath)
	if err := os.MkdirAll(filepath.Dir(absoluteScriptPath), 0o755); err != nil {
		t.Fatalf("create script directory: %v", err)
	}
	if err := os.WriteFile(absoluteScriptPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("create validated script: %v", err)
	}
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		target := nodeByKey(t, def, "implement")
		node := workflowGraphSaveNodeRecord(t, req.Nodes, workflow.NodeIDOf(target))
		node.Kind = workflow.NodeKindScript
		node.SubagentRole = ""
		node.CompletionMode = ""
		node.ScriptPath = validatedScriptPath
		edge := edgeByKey(t, def, "next")
		workflowGraphSaveEdgeRecord(t, req.Edges, edge.ID).PromptTemplate = ""
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	target := nodeByKey(t, definition, "implement")
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(target),
		Values:       map[workflow.ModelKey]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}

	moveStore, writerStore := openConcurrentManualMoveStores(t, cfg)
	writer := acquireUnrelatedManualMoveWriter(t, ctx, writerStore, binding.ProjectID)
	moveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	moved := make(chan manualMoveApplyResult, 1)
	go func() {
		result, err := moveStore.ApplyManualMove(moveCtx, prepared, noneManualMoveExecutionTargetCandidate(binding))
		moved <- manualMoveApplyResult{result: result, err: err}
	}()
	assertManualMoveWaitsForWriter(t, moved)
	if _, err := writer.ExecContext(
		ctx,
		`UPDATE workflow_nodes SET script_path = 'scripts/missing' WHERE id = ?`,
		workflow.NodeIDOf(target),
	); err != nil {
		t.Fatalf("change competing script path: %v", err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit competing workflow change: %v", err)
	}
	assertManualMoveTargetShapeDriftRejected(t, ctx, moveStore, task.ID, source.Reference, <-moved)
}

func assertManualMoveTargetShapeDriftRejected(
	t *testing.T,
	ctx context.Context,
	store *Store,
	taskID workflow.TaskID,
	source workflow.CurrentNodeReference,
	applied manualMoveApplyResult,
) {
	t.Helper()
	if !errors.Is(applied.err, errManualMoveTargetShapeChanged) {
		t.Fatalf("ApplyManualMove error = %T %v, want target-shape drift", applied.err, applied.err)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(source) {
		t.Fatalf("current nodes after rejected drift = %+v, want unchanged source", currentNodes)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target after rejected drift = %+v, want unlocked", targetContext.Task.ExecutionTarget)
	}
}

type manualMoveApplyResult struct {
	result ManualMoveResult
	err    error
}

func noneManualMoveExecutionTargetCandidate(binding metadata.Binding) *ExecutionTargetCandidate {
	return &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}
}

func openConcurrentManualMoveStores(t *testing.T, cfg config.App) (*Store, *Store) {
	t.Helper()
	moveStore, writerStore := openConcurrentWorkflowStores(t, cfg)
	moveStore.roleResolver = testsetup.QuestionsEnabled("coder", "reviewer")
	return moveStore, writerStore
}

func acquireUnrelatedManualMoveWriter(t *testing.T, ctx context.Context, store *Store, projectID string) *sql.Tx {
	t.Helper()
	writer, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin competing writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Rollback() })
	if _, err := writer.ExecContext(
		ctx,
		`UPDATE projects SET updated_at_unix_ms = updated_at_unix_ms WHERE id = ?`,
		projectID,
	); err != nil {
		t.Fatalf("acquire competing write transaction: %v", err)
	}
	return writer
}

func assertManualMoveWaitsForWriter(t *testing.T, moved <-chan manualMoveApplyResult) {
	t.Helper()
	select {
	case result := <-moved:
		t.Fatalf("ApplyManualMove returned before the competing writer committed: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
}
