package workflowstore

import (
	"context"
	"testing"
	"time"

	"core/server/workflow"
)

func TestResumeCurrentNodeWaitsForConcurrentWriterBeforeReadingInterruption(t *testing.T) {
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
		_, _, err := resumeStore.ResumeCurrentNode(resumeCtx, reference)
		resumed <- err
	}()
	select {
	case err := <-resumed:
		t.Fatalf("ResumeCurrentNode returned before the competing writer committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit competing writer: %v", err)
	}
	if err := <-resumed; err != nil {
		t.Fatalf("ResumeCurrentNode after competing commit: %v", err)
	}
}
