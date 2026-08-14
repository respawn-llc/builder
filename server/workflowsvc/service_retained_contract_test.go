package workflowsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestServiceTaskDependencyMutationEventsAreTypedAndIdempotent(t *testing.T) {
	fixture := corefixture.New(t)
	service := fixture.Core.WorkflowClient()
	createdWorkflow, err := service.CreateAndLinkWorkflowToProject(t.Context(), serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Dependency mutation workflow",
		ProjectID:     fixture.Binding.ProjectID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflowToProject: %v", err)
	}
	createTask := func(title string) serverapi.WorkflowTaskSummary {
		t.Helper()
		response, createErr := service.CreateWorkflowTask(t.Context(), serverapi.WorkflowTaskCreateRequest{
			ProjectID:  fixture.Binding.ProjectID,
			WorkflowID: &createdWorkflow.Workflow.ID,
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
	subscription, err := service.SubscribeWorkflowProject(t.Context(), serverapi.WorkflowProjectSubscribeRequest{
		ProjectID: fixture.Binding.ProjectID,
	})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = subscription.Close() }()

	added, err := service.AddWorkflowTaskDependency(t.Context(), serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	})
	if err != nil {
		t.Fatalf("AddWorkflowTaskDependency: %v", err)
	}
	if added.Outcome != serverapi.WorkflowTaskDependencyOutcomeAdded {
		t.Fatalf("add response = %+v, want added", added)
	}
	assertDependencyChangedEvent(t, subscription, blocker.ID, blocked.ID)

	idempotentAdd, err := service.AddWorkflowTaskDependency(t.Context(), serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	})
	if err != nil {
		t.Fatalf("idempotent AddWorkflowTaskDependency: %v", err)
	}
	if idempotentAdd.Outcome != serverapi.WorkflowTaskDependencyOutcomeAlreadyPresent {
		t.Fatalf("idempotent add response = %+v, want already present", idempotentAdd)
	}
	assertNoWorkflowProjectEvent(t, subscription)

	removed, err := service.RemoveWorkflowTaskDependency(t.Context(), serverapi.WorkflowTaskDependencyRemoveRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	})
	if err != nil {
		t.Fatalf("RemoveWorkflowTaskDependency: %v", err)
	}
	if removed.Outcome != serverapi.WorkflowTaskDependencyOutcomeRemoved {
		t.Fatalf("remove response = %+v, want removed", removed)
	}
	assertDependencyChangedEvent(t, subscription, blocker.ID, blocked.ID)

	idempotentRemove, err := service.RemoveWorkflowTaskDependency(t.Context(), serverapi.WorkflowTaskDependencyRemoveRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	})
	if err != nil {
		t.Fatalf("idempotent RemoveWorkflowTaskDependency: %v", err)
	}
	if idempotentRemove.Outcome != serverapi.WorkflowTaskDependencyOutcomeAlreadyAbsent {
		t.Fatalf("idempotent remove response = %+v, want already absent", idempotentRemove)
	}
	assertNoWorkflowProjectEvent(t, subscription)
}

func TestServiceTaskCreatePublishesOneDependencyEventForEveryAffectedTask(t *testing.T) {
	fixture := corefixture.New(t)
	service := fixture.Core.WorkflowClient()
	createdWorkflow, err := service.CreateAndLinkWorkflowToProject(t.Context(), serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Dependency event workflow",
		ProjectID:     fixture.Binding.ProjectID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflowToProject: %v", err)
	}
	createTask := func(title string) serverapi.WorkflowTaskSummary {
		t.Helper()
		response, createErr := service.CreateWorkflowTask(t.Context(), serverapi.WorkflowTaskCreateRequest{
			ProjectID:  fixture.Binding.ProjectID,
			WorkflowID: &createdWorkflow.Workflow.ID,
			Title:      title,
			LabelIDs:   []string{},
		})
		if createErr != nil {
			t.Fatalf("CreateWorkflowTask %q: %v", title, createErr)
		}
		return response.Task
	}
	blocked := createTask("Blocked")
	blocker := createTask("Blocker")
	subscription, err := service.SubscribeWorkflowProject(t.Context(), serverapi.WorkflowProjectSubscribeRequest{
		ProjectID: fixture.Binding.ProjectID,
	})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = subscription.Close() }()

	created, err := service.CreateWorkflowTask(t.Context(), serverapi.WorkflowTaskCreateRequest{
		ProjectID:  fixture.Binding.ProjectID,
		WorkflowID: &createdWorkflow.Workflow.ID,
		Title:      "Mixed",
		LabelIDs:   []string{},
		DependencyIntents: []serverapi.WorkflowTaskDependencyCreateIntent{
			{RelatedTaskID: blocked.ID, NewTaskRole: serverapi.WorkflowTaskDependencyRoleBlocker},
			{RelatedTaskID: blocker.ID, NewTaskRole: serverapi.WorkflowTaskDependencyRoleBlocked},
		},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask with dependencies: %v", err)
	}
	createdEvent, err := subscription.Next(t.Context())
	if err != nil {
		t.Fatalf("receive created event: %v", err)
	}
	if createdEvent.Action != serverapi.WorkflowProjectEventActionCreated ||
		createdEvent.PrimaryEntityID != created.Task.ID {
		t.Fatalf("created event = %+v", createdEvent)
	}
	dependencyEvent, err := subscription.Next(t.Context())
	if err != nil {
		t.Fatalf("receive dependency event: %v", err)
	}
	if dependencyEvent.Action != serverapi.WorkflowProjectEventActionDependenciesChanged ||
		dependencyEvent.PrimaryEntityID != created.Task.ID {
		t.Fatalf("dependency event = %+v", dependencyEvent)
	}
	related := map[string]bool{}
	for _, taskID := range dependencyEvent.RelatedIDs {
		related[taskID] = true
	}
	if len(related) != 2 || !related[blocked.ID] || !related[blocker.ID] {
		t.Fatalf("dependency event related IDs = %+v", dependencyEvent.RelatedIDs)
	}
	assertNoWorkflowProjectEvent(t, subscription)
}

func assertDependencyChangedEvent(
	t *testing.T,
	subscription serverapi.WorkflowProjectSubscription,
	blockerTaskID string,
	blockedTaskID string,
) {
	t.Helper()
	event, err := subscription.Next(t.Context())
	if err != nil {
		t.Fatalf("receive dependency event: %v", err)
	}
	if event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		event.Action != serverapi.WorkflowProjectEventActionDependenciesChanged ||
		event.PrimaryEntityID != blockerTaskID ||
		len(event.RelatedIDs) != 1 ||
		event.RelatedIDs[0] != blockedTaskID {
		t.Fatalf("dependency event = %+v", event)
	}
}

func assertNoWorkflowProjectEvent(t *testing.T, subscription serverapi.WorkflowProjectSubscription) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := subscription.Next(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected Workflow Project event: %v", err)
	}
}
