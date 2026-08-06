package workflowstore

import (
	"database/sql"
	"errors"
	"testing"
)

func TestLifecyclePublicationDeletesTaskWithRuntimeRootRemoval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}

	deleted, err := publication.PublishTaskDeletion(ctx, task.ID)
	if err != nil {
		t.Fatalf("PublishTaskDeletion: %v", err)
	}
	if deleted.ID != task.ID {
		t.Fatalf("deleted Task ID = %q, want %q", deleted.ID, task.ID)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("metadata GetTask after deletion error = %v, want sql.ErrNoRows", err)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture after Task deletion: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(task.ID); len(queued) != 0 {
		t.Fatalf("deleted Task queued root = %+v, want none", queued)
	}
	if exact := capture.ExactExecutions(task.ID); len(exact) != 0 {
		t.Fatalf("deleted Task Exact root = %+v, want none", exact)
	}
}

func TestLifecyclePublicationDeletesWorkflowAndAffectedTaskRoots(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	impact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}

	deleted, err := publication.PublishWorkflowDeletion(ctx, WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      impact.Version,
		ExpectedProjectCount: impact.ProjectCount,
		ExpectedLinkCount:    impact.LinkCount,
		ExpectedTaskCount:    impact.TaskCount,
	})
	if err != nil {
		t.Fatalf("PublishWorkflowDeletion: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("Workflow deletion = %+v, want deleted", deleted)
	}
	if _, _, err := store.GetDefinition(ctx, workflowID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetDefinition after deletion error = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("metadata GetTask after Workflow deletion error = %v, want sql.ErrNoRows", err)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture after Workflow deletion: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(task.ID); len(queued) != 0 {
		t.Fatalf("deleted Workflow Task queued root = %+v, want none", queued)
	}
}

func TestLifecyclePublicationDeletesProjectAndAffectedTaskRoots(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}

	blockers, err := publication.PublishProjectDeletion(ctx, ProjectDeleteRequest{
		ProjectID: binding.ProjectID,
		Artifacts: projectDeleteArtifactsNoop{},
	})
	if err != nil {
		t.Fatalf("PublishProjectDeletion: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("Project deletion blockers = %+v, want none", blockers)
	}
	assertProjectAbsent(t, ctx, store, binding.ProjectID)
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after Project deletion error = %v, want sql.ErrNoRows", err)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture after Project deletion: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(task.ID); len(queued) != 0 {
		t.Fatalf("deleted Project Task queued root = %+v, want none", queued)
	}
}
