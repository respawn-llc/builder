package workflowstore

import (
	"context"
	"errors"
	"testing"

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
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-agent", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Start from {{.TaskTitle}}."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-agent", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history."}}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}); err != nil {
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
	if workflow.NodePromptTemplate(agent) != "Do work." || len(workflow.NodeInputFields(agent)) != 1 || workflow.NodeInputFields(agent)[0].Name != "brief" || len(workflow.NodeOutputFields(agent)) != 1 || workflow.NodeOutputFields(agent)[0].Name != "summary" {
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

// Intentional direct timestamp fixture: workflow updates use wall-clock time,
// so pagination ordering needs pinned row times to stay deterministic.

// A filter and a page cursor must compose: the filter applies inside the
// workflow_list CTE while the cursor applies to the outer query, so paging
// through a filtered result set must stay valid and ordered.

func workflowEventID(value string) *string {
	return &value
}

func workflowEventIDEquals(id *string, expected string) bool {
	return id != nil && *id == expected
}

type recordingWorkflowEventPublisher struct {
	records []WorkflowEventRecord
}

func (p *recordingWorkflowEventPublisher) PublishWorkflowEvent(_ context.Context, record WorkflowEventRecord) error {
	p.records = append(p.records, record)
	return nil
}
