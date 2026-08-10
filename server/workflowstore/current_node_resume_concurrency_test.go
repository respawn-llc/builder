package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

func TestPrepareTaskResumeWaitsForConcurrentWriterBeforeReadingInterruption(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	reference := started.Mutation.Created[0].Reference
	if err := store.InterruptCurrentNode(
		ctx,
		reference,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.CurrentNodeInterruptionDetail{Code: string(workflow.CurrentNodeInterruptionReasonUserInterrupt)},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	resumeStore, writerStore := openConcurrentWorkflowStores(t, cfg)
	writer, err := writerStore.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin competing writer: %v", err)
	}
	defer func() { _ = writer.Rollback() }()
	if _, err := writer.ExecContext(
		ctx,
		`UPDATE projects SET updated_at_unix_ms = updated_at_unix_ms WHERE id = ?`,
		binding.ProjectID,
	); err != nil {
		t.Fatalf("acquire competing write transaction: %v", err)
	}

	resumeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resumed := make(chan error, 1)
	go func() {
		prepared, err := resumeStore.PrepareTaskResume(resumeCtx, task.ID)
		if err == nil {
			err = prepared.Commit()
		}
		resumed <- err
	}()
	select {
	case err := <-resumed:
		t.Fatalf("PrepareTaskResume returned before the competing writer committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit competing writer: %v", err)
	}
	if err := <-resumed; err != nil {
		t.Fatalf("PrepareTaskResume after competing commit: %v", err)
	}
	currentNodes, err := resumeStore.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(reference) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("Current Nodes after Resume = %+v, want ready %v", currentNodes, reference)
	}
}

func TestConcurrentTaskStartCommitsExactlyOnePreparedMutation(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	first, second := openConcurrentWorkflowStores(
		t,
		cfg,
		WithRoleResolver(testsetup.QuestionsEnabled("coder", "reviewer")),
	)

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, candidate := range []*Store{first, second} {
		go func(candidate *Store) {
			ready.Done()
			<-start
			_, err := candidate.StartTask(ctx, task.ID)
			results <- err
		}(candidate)
	}
	ready.Wait()
	close(start)

	var committed, unchanged int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			committed++
		case errors.Is(err, sql.ErrNoRows):
			unchanged++
		default:
			t.Fatalf("concurrent Start error = %v, want committed or unchanged conflict", err)
		}
	}
	if committed != 1 || unchanged != 1 {
		t.Fatalf("concurrent Start outcomes: committed=%d unchanged=%d, want 1/1", committed, unchanged)
	}
	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("Current Nodes after concurrent Start = %+v, want one ready node", currentNodes)
	}
}
