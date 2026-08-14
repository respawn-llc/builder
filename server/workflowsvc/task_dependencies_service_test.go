package workflowsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestServiceTaskDependencyMutationEventsAreTypedAndIdempotent(t *testing.T) {
	ctx, service, projectID, workflowID, _ := newWorkflowServiceOrdinaryTaskFixture(t)
	createdBlocker, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		Title:      "blocker",
		LabelIDs:   []string{},
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	createdBlocked, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		Title:      "blocked",
		LabelIDs:   []string{},
	})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: projectID})
	if err != nil {
		t.Fatalf("subscribe project: %v", err)
	}
	defer func() { _ = sub.Close() }()

	added, err := service.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: createdBlocker.Task.ID,
		BlockedTaskID: createdBlocked.Task.ID,
	})
	if err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	if added.Outcome != serverapi.WorkflowTaskDependencyOutcomeAdded {
		t.Fatalf("add response = %+v, want added", added)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		event.Action != serverapi.WorkflowProjectEventActionDependenciesChanged ||
		event.PrimaryEntityID != createdBlocker.Task.ID ||
		len(event.RelatedIDs) != 1 || event.RelatedIDs[0] != createdBlocked.Task.ID {
		t.Fatalf("dependency event = %+v", event)
	}
	list, err := service.ListWorkflowTaskDependencies(ctx, serverapi.WorkflowTaskDependencyListRequest{TaskID: createdBlocked.Task.ID})
	if err != nil {
		t.Fatalf("list dependencies: %v", err)
	}
	if err := list.Validate(); err != nil {
		t.Fatalf("list Validate: %v", err)
	}
	if len(list.Directions) != 1 ||
		list.Directions[0].Direction != serverapi.WorkflowTaskDependencyDirectionBlockedBy {
		t.Fatalf("list response = %+v, want one non-empty blocked-by direction", list)
	}

	idempotent, err := service.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: createdBlocker.Task.ID,
		BlockedTaskID: createdBlocked.Task.ID,
	})
	if err != nil {
		t.Fatalf("idempotent add: %v", err)
	}
	if idempotent.Outcome != serverapi.WorkflowTaskDependencyOutcomeAlreadyPresent {
		t.Fatalf("idempotent add response = %+v", idempotent)
	}
	assertNoWorkflowEvent(t, sub)

	removed, err := service.RemoveWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyRemoveRequest{
		BlockerTaskID: createdBlocker.Task.ID,
		BlockedTaskID: createdBlocked.Task.ID,
	})
	if err != nil {
		t.Fatalf("remove dependency: %v", err)
	}
	if removed.Outcome != serverapi.WorkflowTaskDependencyOutcomeRemoved {
		t.Fatalf("remove response = %+v, want removed", removed)
	}
	event = nextWorkflowProjectEvent(t, sub)
	if event.Action != serverapi.WorkflowProjectEventActionDependenciesChanged {
		t.Fatalf("remove event = %+v", event)
	}

	absent, err := service.RemoveWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyRemoveRequest{
		BlockerTaskID: createdBlocker.Task.ID,
		BlockedTaskID: createdBlocked.Task.ID,
	})
	if err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
	if absent.Outcome != serverapi.WorkflowTaskDependencyOutcomeAlreadyAbsent {
		t.Fatalf("idempotent remove response = %+v", absent)
	}
	assertNoWorkflowEvent(t, sub)
}

func TestServiceTaskCreatePublishesOneDependencyEventForEveryAffectedTask(t *testing.T) {
	ctx, service, projectID, workflowID, _ := newWorkflowServiceOrdinaryTaskFixture(t)
	blocked, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: projectID, WorkflowID: &workflowID, Title: "blocked", LabelIDs: []string{},
	})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	blocker, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: projectID, WorkflowID: &workflowID, Title: "blocker", LabelIDs: []string{},
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: projectID})
	if err != nil {
		t.Fatalf("subscribe project: %v", err)
	}
	defer func() { _ = sub.Close() }()

	created, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		Title:      "mixed",
		LabelIDs:   []string{},
		DependencyIntents: []serverapi.WorkflowTaskDependencyCreateIntent{
			{RelatedTaskID: blocked.Task.ID, NewTaskRole: serverapi.WorkflowTaskDependencyRoleBlocker},
			{RelatedTaskID: blocker.Task.ID, NewTaskRole: serverapi.WorkflowTaskDependencyRoleBlocked},
		},
	})
	if err != nil {
		t.Fatalf("create with dependencies: %v", err)
	}
	createdEvent := nextWorkflowProjectEvent(t, sub)
	if createdEvent.Action != serverapi.WorkflowProjectEventActionCreated ||
		createdEvent.PrimaryEntityID != created.Task.ID {
		t.Fatalf("created event = %+v", createdEvent)
	}
	dependencyEvent := nextWorkflowProjectEvent(t, sub)
	if dependencyEvent.Action != serverapi.WorkflowProjectEventActionDependenciesChanged ||
		dependencyEvent.PrimaryEntityID != created.Task.ID {
		t.Fatalf("dependency event = %+v", dependencyEvent)
	}
	gotRelated := map[string]bool{}
	for _, taskID := range dependencyEvent.RelatedIDs {
		gotRelated[taskID] = true
	}
	if len(gotRelated) != 2 || !gotRelated[blocked.Task.ID] || !gotRelated[blocker.Task.ID] {
		t.Fatalf("dependency event related IDs = %+v, want both affected Tasks", dependencyEvent.RelatedIDs)
	}
	assertNoWorkflowEvent(t, sub)
}

func assertNoWorkflowEvent(t *testing.T, sub serverapi.WorkflowProjectSubscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := sub.Next(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next() error = %v, want deadline exceeded", err)
	}
}
