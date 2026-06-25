package workflowstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestWorkflowCreateUpdateReadAndGraphPersistence(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)

	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Default Pipeline", Description: "desc"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, record, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if record.Version != 1 {
		t.Fatalf("workflow version = %d, want 1", record.Version)
	}
	if !hasNode(def, "backlog", workflow.NodeKindStart) || !hasNode(def, "done", workflow.NodeKindTerminal) {
		t.Fatalf("default nodes missing from %+v", def.Nodes)
	}
	if err := store.UpdateWorkflowInfo(ctx, created.ID, "Renamed", "new desc"); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	_, renamed, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition renamed: %v", err)
	}
	if renamed.Name != "Renamed" || renamed.Version != 2 {
		t.Fatalf("workflow info update = %+v, want name changed with version bump", renamed)
	}
	if err := store.UpdateWorkflowInfo(ctx, created.ID, "   ", "new desc"); !errors.Is(err, ErrWorkflowNameRequired) {
		t.Fatalf("UpdateWorkflowInfo blank name error = %v", err)
	}

	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	revision, err := store.AddNode(ctx, NodeRecord{ID: "node-agent", WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", InputFields: []workflow.InputField{{Name: "brief", Description: "Brief."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}})
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if revision != 3 {
		t.Fatalf("revision after add node = %d, want 3", revision)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-agent", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Start from {{.TaskTitle}}."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-agent", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history."}}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	updated, updatedRecord, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	if updatedRecord.Version != 7 {
		t.Fatalf("workflow version after graph edits = %d, want 7", updatedRecord.Version)
	}
	if len(updated.TransitionGroups) != 2 || len(updated.Edges) != 2 {
		t.Fatalf("graph persistence mismatch: groups=%+v edges=%+v", updated.TransitionGroups, updated.Edges)
	}
	agent := nodeByKey(t, updated, "agent")
	if agent.PromptTemplate != "Do work." || len(agent.InputFields) != 1 || agent.InputFields[0].Name != "brief" || len(agent.OutputFields) != 1 || agent.OutputFields[0].Name != "summary" {
		t.Fatalf("legacy node contract fields = %+v, want prompt/input/output metadata round-tripped", agent)
	}
	startEdge := edgeByKey(t, updated, "start")
	if startEdge.PromptTemplate != "Start from {{.TaskTitle}}." {
		t.Fatalf("start edge prompt = %q, want transition prompt round-tripped", startEdge.PromptTemplate)
	}
	doneEdge := edgeByKey(t, updated, "done")
	if len(doneEdge.Parameters) != 1 || doneEdge.Parameters[0].Key != "summary" || doneEdge.Parameters[0].Description != "Summary for terminal history." {
		t.Fatalf("done edge parameters = %+v, want transition parameters round-tripped", doneEdge.Parameters)
	}
	workflows, err := store.ListWorkflows(ctx, ListWorkflowsRequest{})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(workflows.Workflows) != 1 || workflows.Workflows[0].ID != created.ID {
		t.Fatalf("ListWorkflows = %+v", workflows)
	}
}

func TestWorkflowListPaginatesWithMostRecentOrderAndFilters(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created := map[string]workflow.WorkflowID{}
	for index, name := range []string{"Gamma", "Alpha", "Beta", "Beta Searchable"} {
		record, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: name, Description: "desc " + name})
		if err != nil {
			t.Fatalf("CreateWorkflow %q: %v", name, err)
		}
		created[name] = record.ID
		// Intentional direct timestamp fixture: workflow updates use wall-clock time,
		// so pagination ordering needs pinned row times to stay deterministic.
		if _, err := store.db.ExecContext(ctx, `UPDATE workflows SET updated_at_unix_ms = ? WHERE id = ?`, int64(index+1), string(record.ID)); err != nil {
			t.Fatalf("force workflow timestamp: %v", err)
		}
	}

	page1, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListWorkflows page1: %v", err)
	}
	if len(page1.Workflows) != 2 || page1.NextPageToken == "" {
		t.Fatalf("page1 = %+v, want two workflows and next token", page1)
	}
	if page1.Workflows[0].ID != created["Beta Searchable"] || page1.Workflows[1].ID != created["Beta"] {
		t.Fatalf("page1 order = %+v", page1.Workflows)
	}
	page2, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 2, PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("ListWorkflows page2: %v", err)
	}
	if len(page2.Workflows) != 2 || page2.NextPageToken != "" {
		t.Fatalf("page2 = %+v, want final two workflows", page2)
	}
	if page2.Workflows[0].ID != created["Alpha"] || page2.Workflows[1].ID != created["Gamma"] {
		t.Fatalf("page2 order = %+v", page2.Workflows)
	}
	filtered, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, Query: "search"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered: %v", err)
	}
	if len(filtered.Workflows) != 1 || filtered.Workflows[0].ID != created["Beta Searchable"] {
		t.Fatalf("filtered = %+v", filtered.Workflows)
	}
	exact, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, ExactName: "Beta"})
	if err != nil {
		t.Fatalf("ListWorkflows exact: %v", err)
	}
	if len(exact.Workflows) != 1 || exact.Workflows[0].ID != created["Beta"] {
		t.Fatalf("exact = %+v", exact.Workflows)
	}

	// A filter and a page cursor must compose: the filter applies inside the
	// workflow_list CTE while the cursor applies to the outer query, so paging
	// through a filtered result set must stay valid and ordered.
	filteredPage1, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 1, Query: "Beta"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered page1: %v", err)
	}
	if len(filteredPage1.Workflows) != 1 || filteredPage1.NextPageToken == "" {
		t.Fatalf("filtered page1 = %+v, want one workflow and next token", filteredPage1)
	}
	if filteredPage1.Workflows[0].ID != created["Beta Searchable"] {
		t.Fatalf("filtered page1 order = %+v", filteredPage1.Workflows)
	}
	filteredPage2, err := store.ListWorkflows(ctx, ListWorkflowsRequest{
		PageSize:  1,
		Query:     "Beta",
		PageToken: filteredPage1.NextPageToken,
	})
	if err != nil {
		t.Fatalf("ListWorkflows filtered page2: %v", err)
	}
	if len(filteredPage2.Workflows) != 1 || filteredPage2.NextPageToken != "" {
		t.Fatalf("filtered page2 = %+v, want final filtered workflow", filteredPage2)
	}
	if filteredPage2.Workflows[0].ID != created["Beta"] {
		t.Fatalf("filtered page2 order = %+v", filteredPage2.Workflows)
	}
}

func TestProjectWorkflowLinkFirstDefaultAndDuplicateIdempotency(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowA, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow A"})
	if err != nil {
		t.Fatalf("CreateWorkflow A: %v", err)
	}
	workflowB, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow B"})
	if err != nil {
		t.Fatalf("CreateWorkflow B: %v", err)
	}

	first, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowA.ID, WorkflowLinkDefaultIfProjectHasNone)
	if err != nil {
		t.Fatalf("LinkWorkflowWithDefaultPolicy first: %v", err)
	}
	if !first.IsDefault {
		t.Fatalf("first link = %+v, want default", first)
	}
	duplicate, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowA.ID, WorkflowLinkDefaultIfProjectHasNone)
	if err != nil {
		t.Fatalf("duplicate LinkWorkflowWithDefaultPolicy: %v", err)
	}
	if duplicate.ID != first.ID || !duplicate.IsDefault {
		t.Fatalf("duplicate link = %+v, want existing default link %+v", duplicate, first)
	}
	second, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowB.ID, WorkflowLinkDefaultIfProjectHasNone)
	if err != nil {
		t.Fatalf("LinkWorkflowWithDefaultPolicy second: %v", err)
	}
	if second.IsDefault {
		t.Fatalf("second link = %+v, want non-default", second)
	}
}

func TestCreateAndLinkWorkflowIsAtomicAndAppliesFirstDefaultPolicy(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, link, err := store.CreateAndLinkWorkflow(ctx, CreateAndLinkWorkflowRequest{
		Name:          "Created from Project",
		ProjectID:     binding.ProjectID,
		DefaultPolicy: WorkflowLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflow: %v", err)
	}
	if created.ID == "" || link.WorkflowID != created.ID || !link.IsDefault {
		t.Fatalf("created=%+v link=%+v, want linked first default", created, link)
	}
	if _, _, err := store.CreateAndLinkWorkflow(ctx, CreateAndLinkWorkflowRequest{
		Name:          "Broken",
		ProjectID:     "missing-project",
		DefaultPolicy: WorkflowLinkDefaultIfProjectHasNone,
	}); err == nil {
		t.Fatalf("expected invalid project create-and-link to fail")
	}
	listed, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, Query: "Broken"})
	if err != nil {
		t.Fatalf("ListWorkflows after failed create-and-link: %v", err)
	}
	if len(listed.Workflows) != 0 {
		t.Fatalf("failed create-and-link left workflows: %+v", listed.Workflows)
	}
}

func TestAddNodeRejectsNodeGroupFromDifferentWorkflow(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowA, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow A"})
	if err != nil {
		t.Fatalf("CreateWorkflow A: %v", err)
	}
	workflowB, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow B"})
	if err != nil {
		t.Fatalf("CreateWorkflow B: %v", err)
	}
	group, _, err := store.AddNodeGroup(ctx, NodeGroupRecord{ID: "group-a", WorkflowID: workflowA.ID, Key: "impl", DisplayName: "Implementation"})
	if err != nil {
		t.Fatalf("AddNodeGroup: %v", err)
	}

	_, err = store.AddNode(ctx, NodeRecord{ID: "node-cross-group", WorkflowID: workflowB.ID, GroupID: group.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent"})
	if !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("AddNode cross-workflow group error = %v", err)
	}
}

func TestWorkflowEventPublisherNormalizesAndDispatchesEvents(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	store.now = func() time.Time { return time.UnixMilli(1234).UTC() }
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Action: "created"}); !errors.Is(err, ErrEventResourceRequired) {
		t.Fatalf("missing resource error = %v", err)
	}
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Resource: "task"}); !errors.Is(err, ErrEventActionRequired) {
		t.Fatalf("missing action error = %v", err)
	}
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Resource: "task", Action: "created"}); err != nil {
		t.Fatalf("PublishWorkflowEvent with default no-op sink: %v", err)
	}

	sink := &recordingWorkflowEventPublisher{}
	store.SetWorkflowEventPublisher(sink)
	changedIDs := []string{"task-1"}
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{
		ProjectID:  " project-1 ",
		WorkflowID: " workflow-1 ",
		Resource:   " task ",
		Action:     " updated ",
		ChangedIDs: changedIDs,
	}); err != nil {
		t.Fatalf("PublishWorkflowEvent: %v", err)
	}
	changedIDs[0] = "mutated"
	if len(sink.records) != 1 {
		t.Fatalf("published records = %+v, want one", sink.records)
	}
	record := sink.records[0]
	if record.ProjectID != "project-1" || record.WorkflowID != "workflow-1" || record.Resource != "task" || record.Action != "updated" || record.OccurredAtUnixMs != 1234 {
		t.Fatalf("published record = %+v, want normalized fields and default timestamp", record)
	}
	if len(record.ChangedIDs) != 1 || record.ChangedIDs[0] != "task-1" {
		t.Fatalf("changed ids = %+v, want defensive copy", record.ChangedIDs)
	}
	store.SetWorkflowEventPublisher(nil)
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Resource: "task", Action: "deleted"}); err != nil {
		t.Fatalf("PublishWorkflowEvent after nil publisher reset: %v", err)
	}
}

type recordingWorkflowEventPublisher struct {
	records []WorkflowEventRecord
}

func (p *recordingWorkflowEventPublisher) PublishWorkflowEvent(_ context.Context, record WorkflowEventRecord) error {
	p.records = append(p.records, record)
	return nil
}

func TestWorkflowGraphUpdatesRejectCrossWorkflowReferences(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	firstWorkflowID := createValidWorkflow(t, ctx, store)
	secondWorkflowID := createValidWorkflow(t, ctx, store)
	firstDef, _, err := store.GetDefinition(ctx, firstWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition first: %v", err)
	}
	secondDef, _, err := store.GetDefinition(ctx, secondWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition second: %v", err)
	}
	firstAgent := nodeByKey(t, firstDef, "agent")
	secondAgent := nodeByKey(t, secondDef, "agent")
	secondDone := nodeByKind(t, secondDef, workflow.NodeKindTerminal)

	if _, err := store.UpdateTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(firstWorkflowID)), WorkflowID: firstWorkflowID, SourceNodeID: secondAgent.ID, TransitionID: "done", DisplayName: "Done"}); !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("UpdateTransitionGroup cross-workflow error = %v, want workflow mismatch", err)
	}
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(firstWorkflowID)), WorkflowID: firstWorkflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(secondWorkflowID)), Key: "done", TargetNodeID: firstAgent.ID, ContextMode: workflow.ContextModeNewSession}); !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("UpdateEdge cross-workflow group error = %v, want workflow mismatch", err)
	}
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(firstWorkflowID)), WorkflowID: firstWorkflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(firstWorkflowID)), Key: "done", TargetNodeID: secondDone.ID, ContextMode: workflow.ContextModeNewSession}); !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("UpdateEdge cross-workflow target error = %v, want workflow mismatch", err)
	}
}
