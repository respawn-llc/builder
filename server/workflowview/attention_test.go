package workflowview

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestAttentionReadsGlobalAndTaskCandidatesThroughFocusedInterface(t *testing.T) {
	ctx, metadataStore, workflowStore, firstProject := newWorkflowViewTestContextStore(t)
	secondProject, err := metadataStore.CreateProjectForWorkspace(t.Context(), t.TempDir(), "Second attention project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}

	approvalWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, firstProject.ProjectID, approvalWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow approval: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, metadataStore, approvalWorkflowID)
	approvalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: firstProject.ProjectID, Title: "Approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask approval: %v", err)
	}
	approvalStarted, err := workflowStore.StartTask(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	pendingApproval, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"})
	if err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}

	interruptedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, secondProject.ProjectID, interruptedWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow interrupted: %v", err)
	}
	interruptedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: secondProject.ProjectID, Title: "Interrupted", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask interrupted: %v", err)
	}
	interruptedStarted, err := workflowStore.StartTask(ctx, interruptedTask.ID)
	if err != nil {
		t.Fatalf("StartTask interrupted: %v", err)
	}
	interruptedClaimed, err := workflowStore.ClaimRun(ctx, interruptedStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, interruptedStarted.RunID, interruptedClaimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	cleanWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, secondProject.ProjectID, cleanWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow clean: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(ctx, `UPDATE workflows SET updated_at_unix_ms = ? WHERE id = ?`, int64(2_000_000_000_000), string(cleanWorkflowID)); err != nil {
		t.Fatalf("force clean workflow timestamp: %v", err)
	}

	fanout := createWorkflowViewFanoutStatusFixture(t, ctx, workflowStore, firstProject)
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	attention, err := NewAttention(metadataStore, definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}

	var globalItems []serverapi.WorkflowAttentionItem
	pageToken := ""
	for {
		page, err := attention.List(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1, PageToken: pageToken}, testsetup.QuestionsEnabled("coder"))
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(page.Items) == 0 {
			if page.NextPageToken != "" {
				t.Fatal("empty global attention page retained a continuation token")
			}
			break
		}
		globalItems = append(globalItems, page.Items...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	byKindAndProject := map[string]serverapi.WorkflowAttentionItem{}
	for index, item := range globalItems {
		if item.Kind == "validation_blocker" {
			t.Fatalf("clean validation candidate was not dropped: %+v", item)
		}
		if index > 0 {
			previous := globalItems[index-1]
			if previous.OccurredAtUnixMs < item.OccurredAtUnixMs ||
				(previous.OccurredAtUnixMs == item.OccurredAtUnixMs && previous.ID < item.ID) {
				t.Fatalf("global attention is not newest-first across projects: %+v", globalItems)
			}
		}
		byKindAndProject[item.Kind+":"+item.ProjectID] = item
	}
	approval := byKindAndProject["approval:"+firstProject.ProjectID]
	if approval.TaskID != string(approvalTask.ID) || approval.TaskTransitionID != string(pendingApproval.TransitionID) {
		t.Fatalf("approval projection = %+v", approval)
	}
	interrupted := byKindAndProject["interrupted_run:"+secondProject.ProjectID]
	if interrupted.TaskID != string(interruptedTask.ID) || interrupted.RunID != string(interruptedStarted.RunID) {
		t.Fatalf("interrupted projection = %+v", interrupted)
	}

	taskAttention, err := attention.ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(fanout.task.ID)}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTask: %v", err)
	}
	if len(taskAttention.Items) != 2 {
		t.Fatalf("task attention items = %+v, want question and interruption", taskAttention.Items)
	}
	for index := 1; index < len(taskAttention.Items); index++ {
		previous := taskAttention.Items[index-1]
		current := taskAttention.Items[index]
		if previous.OccurredAtUnixMs < current.OccurredAtUnixMs ||
			(previous.OccurredAtUnixMs == current.OccurredAtUnixMs && previous.ID < current.ID) {
			t.Fatalf("task attention is not newest-first: %+v", taskAttention.Items)
		}
	}
	kinds := map[string]bool{}
	for _, item := range taskAttention.Items {
		kinds[item.Kind] = true
	}
	if !kinds["question"] || !kinds["interrupted_run"] {
		t.Fatalf("task attention kinds = %+v", taskAttention.Items)
	}
}
