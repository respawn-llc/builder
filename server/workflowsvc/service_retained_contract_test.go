package workflowsvc_test

import (
	"testing"

	"core/internal/testharness/corefixture"
	"core/shared/serverapi"
)

func TestServiceRetainsWorkflowTaskAndCommentBoundaries(t *testing.T) {
	fixture := corefixture.New(t)
	service := fixture.Core.WorkflowClient()

	created, err := service.CreateAndLinkWorkflowToProject(t.Context(), serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Retained workflow",
		ProjectID:     fixture.Binding.ProjectID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflowToProject: %v", err)
	}
	definition, err := service.GetWorkflow(t.Context(), serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if definition.Definition.Workflow.ID != created.Workflow.ID {
		t.Fatalf("Workflow definition ID = %q, want %q", definition.Definition.Workflow.ID, created.Workflow.ID)
	}
	validation, err := service.ValidateWorkflow(t.Context(), serverapi.WorkflowValidateRequest{
		WorkflowID: created.Workflow.ID,
		Mode:       serverapi.WorkflowValidationModeDraft,
	})
	if err != nil || !validation.Valid {
		t.Fatalf("ValidateWorkflow = %+v, %v", validation, err)
	}
	task, err := service.CreateWorkflowTask(t.Context(), serverapi.WorkflowTaskCreateRequest{
		ProjectID:  fixture.Binding.ProjectID,
		WorkflowID: &created.Workflow.ID,
		Title:      "Retained task",
		Body:       "Initial body",
		LabelIDs:   []string{},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	list, err := service.ListWorkflowTasks(t.Context(), serverapi.WorkflowTaskListRequest{
		ProjectID: &fixture.Binding.ProjectID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("ListWorkflowTasks: %v", err)
	}
	if len(list.Tasks) != 1 || list.Tasks[0].TaskID != task.Task.ID {
		t.Fatalf("task list = %+v, want task %q", list.Tasks, task.Task.ID)
	}

	updatedTitle := "Updated retained task"
	updated, err := service.UpdateWorkflowTask(t.Context(), serverapi.WorkflowTaskUpdateRequest{
		TaskID: task.Task.ID,
		Title:  &updatedTitle,
	})
	if err != nil || updated.Task.Title != updatedTitle {
		t.Fatalf("UpdateWorkflowTask = %+v, %v", updated, err)
	}
	comment, err := service.AddWorkflowTaskComment(t.Context(), serverapi.WorkflowTaskCommentAddRequest{
		TaskID:   task.Task.ID,
		Body:     "Retained comment",
		Author:   "user",
		AuthorID: "user",
	})
	if err != nil {
		t.Fatalf("AddWorkflowTaskComment: %v", err)
	}
	replacement := "Updated retained comment"
	if err := service.ReplaceWorkflowTaskComment(t.Context(), serverapi.WorkflowTaskCommentReplaceRequest{
		CommentID: comment.Comment.ID,
		Body:      replacement,
	}); err != nil {
		t.Fatalf("ReplaceWorkflowTaskComment: %v", err)
	}
	comments, err := service.ListWorkflowTaskComments(t.Context(), serverapi.WorkflowTaskOffsetPageRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments: %v", err)
	}
	if len(comments.Items) != 1 || comments.Items[0].Body != replacement {
		t.Fatalf("comments = %+v, want replacement", comments.Items)
	}
	if err := service.DeleteWorkflowTaskComment(t.Context(), serverapi.WorkflowTaskCommentDeleteRequest{
		CommentID: comment.Comment.ID,
	}); err != nil {
		t.Fatalf("DeleteWorkflowTaskComment: %v", err)
	}
	comments, err = service.ListWorkflowTaskComments(t.Context(), serverapi.WorkflowTaskOffsetPageRequest{TaskID: task.Task.ID})
	if err != nil || len(comments.Items) != 0 {
		t.Fatalf("comments after delete = %+v, %v", comments.Items, err)
	}
}

func TestServiceRetainsWorkflowLabelsAndDependencies(t *testing.T) {
	fixture := corefixture.New(t)
	service := fixture.Core.WorkflowClient()
	created, err := service.CreateAndLinkWorkflowToProject(t.Context(), serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Retained dependency workflow",
		ProjectID:     fixture.Binding.ProjectID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	label, err := service.CreateWorkflowProjectLabel(t.Context(), serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: fixture.Binding.ProjectID,
		Name:      "Retained",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	createTask := func(title string) serverapi.WorkflowTaskSummary {
		t.Helper()
		response, createErr := service.CreateWorkflowTask(t.Context(), serverapi.WorkflowTaskCreateRequest{
			ProjectID:  fixture.Binding.ProjectID,
			WorkflowID: &created.Workflow.ID,
			Title:      title,
			LabelIDs:   []string{},
		})
		if createErr != nil {
			t.Fatalf("CreateWorkflowTask %q: %v", title, createErr)
		}
		return response.Task
	}
	blocker := createTask("Blocker")
	blocked := createTask("Blocked")
	if _, err := service.UpdateWorkflowTaskLabels(t.Context(), serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      blocked.ID,
		AddLabelIDs: []string{label.Label.ID},
	}); err != nil {
		t.Fatalf("UpdateWorkflowTaskLabels: %v", err)
	}
	labels, err := service.GetWorkflowTaskLabels(t.Context(), serverapi.WorkflowTaskLabelsGetRequest{TaskID: blocked.ID})
	if err != nil || len(labels.Assignment.LabelIDs) != 1 || labels.Assignment.LabelIDs[0] != label.Label.ID {
		t.Fatalf("GetWorkflowTaskLabels = %+v, %v", labels, err)
	}
	if _, err := service.AddWorkflowTaskDependency(t.Context(), serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	}); err != nil {
		t.Fatalf("AddWorkflowTaskDependency: %v", err)
	}
	dependencies, err := service.ListWorkflowTaskDependencies(t.Context(), serverapi.WorkflowTaskDependencyListRequest{TaskID: blocked.ID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskDependencies = %+v, %v", dependencies, err)
	}
	var blockedBy []serverapi.WorkflowTaskDependencyItem
	for _, direction := range dependencies.Directions {
		if direction.Direction == serverapi.WorkflowTaskDependencyDirectionBlockedBy {
			blockedBy = direction.Items
		}
	}
	if len(blockedBy) != 1 || blockedBy[0].TaskID != blocker.ID {
		t.Fatalf("blocked-by dependencies = %+v, want blocker %q", blockedBy, blocker.ID)
	}
	if _, err := service.RemoveWorkflowTaskDependency(t.Context(), serverapi.WorkflowTaskDependencyRemoveRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	}); err != nil {
		t.Fatalf("RemoveWorkflowTaskDependency: %v", err)
	}
}
