package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/workflow"
	"core/shared/config"
)

// completionHasCode reports whether err is a CompletionValidationError carrying
// an issue with the given structured code, letting tests assert which validation
// rule fired rather than its message wording.
func completionHasCode(err error, code CompletionCode) bool {
	var cve CompletionValidationError
	return errors.As(err, &cve) && cve.HasCode(code)
}

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

func TestTaskCreateStartCancelAndComments(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Implement feature", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask default: %v", err)
	}
	if !strings.HasPrefix(task.ShortID, "WOR-1") {
		t.Fatalf("short id = %q, want WOR-1 prefix", task.ShortID)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements after create: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements after create = %+v", placements)
	}

	started := startTask(t, ctx, store, task.ID)
	if started.RunID == "" || started.PlacementID == "" {
		t.Fatalf("start result missing run/placement ids: %+v", started)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].AutomationRequestedAt == 0 {
		t.Fatalf("runs after start = %+v", runs)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].TransitionID != "start" {
		t.Fatalf("transitions after start = %+v", transitions)
	}
	transitionEdges, err := store.ListTransitionEdges(ctx, transitions[0].ID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(transitionEdges) != 1 || transitionEdges[0].EdgeKey != "start" || transitionEdges[0].TargetPlacementID != started.PlacementID {
		t.Fatalf("transition edge snapshot after start = %+v", transitionEdges)
	}

	comment, err := store.AddComment(ctx, task.ID, " first note ", "agent", "coder")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := store.AddComment(ctx, task.ID, "system note", "system", ""); !errors.Is(err, ErrCommentAuthorKindInvalid) {
		t.Fatalf("system AddComment error = %v, want author kind validation", err)
	}
	if err := store.ReplaceComment(ctx, comment.ID, "updated"); err != nil {
		t.Fatalf("ReplaceComment: %v", err)
	}
	comments, err := store.ListComments(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "updated" {
		t.Fatalf("comments after replace = %+v", comments)
	}
	if err := store.DeleteComment(ctx, comment.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	comments, err = store.ListComments(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListComments visible: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("deleted comment should be hidden, got %+v", comments)
	}

	if err := store.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := store.CancelTask(ctx, "task-missing", "stop"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("CancelTask missing = %v, want sql.ErrNoRows", err)
	}
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after cancel: %v", err)
	}
	if runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != "task_canceled" {
		t.Fatalf("run not interrupted by cancel: %+v", runs[0])
	}
	placements, err = store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements after cancel: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	activeDone := false
	activeNonTerminal := false
	for _, placement := range placements {
		if placement.State != "active" {
			continue
		}
		if placement.NodeID == done.ID {
			activeDone = true
			continue
		}
		activeNonTerminal = true
	}
	if !activeDone || activeNonTerminal {
		t.Fatalf("placements after cancel = %+v, want only active Done placement", placements)
	}
}

func TestListCommentsPageKeysetStaysStableWhenNewerCommentInserted(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Comments"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	first, err := store.AddComment(ctx, task.ID, "first", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	second, err := store.AddComment(ctx, task.ID, "second", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	setCommentCreatedAt(t, ctx, store, first.ID, 1000)
	setCommentCreatedAt(t, ctx, store, second.ID, 2000)

	page1, err := store.ListCommentsPage(ctx, task.ID, CommentPageCursor{}, 1)
	if err != nil {
		t.Fatalf("ListCommentsPage page1: %v", err)
	}
	if len(page1) != 1 || page1[0].ID != second.ID {
		t.Fatalf("page1 = %+v, want newest comment %q", page1, second.ID)
	}

	// A newer comment arriving between page reads must not shift the cursor:
	// an offset would now return the already-seen comment, a keyset must not.
	third, err := store.AddComment(ctx, task.ID, "third", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment third: %v", err)
	}
	setCommentCreatedAt(t, ctx, store, third.ID, 3000)

	cursor := CommentPageCursor{CreatedAtUnixMs: page1[0].CreatedAt, ID: page1[0].ID, HasValue: true}
	page2, err := store.ListCommentsPage(ctx, task.ID, cursor, 1)
	if err != nil {
		t.Fatalf("ListCommentsPage page2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != first.ID {
		t.Fatalf("page2 = %+v, want next-older comment %q with no duplicate/skip", page2, first.ID)
	}
}

func TestCountTaskCommentsCountsVisibleCurrentRows(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Comments"})
	otherTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Other"})

	assertCommentCount := func(taskID workflow.TaskID, want int64) {
		t.Helper()
		got, err := store.CountTaskComments(ctx, taskID)
		if err != nil {
			t.Fatalf("CountTaskComments(%s): %v", taskID, err)
		}
		if got != want {
			t.Fatalf("CountTaskComments(%s) = %d, want %d", taskID, got, want)
		}
	}
	assertCommentCount(task.ID, 0)

	first, err := store.AddComment(ctx, task.ID, "first", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment first: %v", err)
	}
	if _, err := store.AddComment(ctx, task.ID, "second", "agent", "coder"); err != nil {
		t.Fatalf("AddComment second: %v", err)
	}
	if _, err := store.AddComment(ctx, otherTask.ID, "other", "user", "nek"); err != nil {
		t.Fatalf("AddComment other: %v", err)
	}
	assertCommentCount(task.ID, 2)

	if err := store.DeleteComment(ctx, first.ID); err != nil {
		t.Fatalf("DeleteComment first: %v", err)
	}
	assertCommentCount(task.ID, 1)
}

func setCommentCreatedAt(t *testing.T, ctx context.Context, store *Store, commentID string, createdAtUnixMs int64) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `UPDATE task_comments SET created_at_unix_ms = ? WHERE id = ?`, createdAtUnixMs, commentID); err != nil {
		t.Fatalf("force comment timestamp: %v", err)
	}
}

func TestTaskCreatePersistsSourceWorkspaceAndOptionalBody(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	source, err := store.metadata.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}

	selected, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: " Selected ", SourceWorkspaceID: source.WorkspaceID})
	if err != nil {
		t.Fatalf("CreateTask selected source workspace: %v", err)
	}
	if selected.Body != "" || selected.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("selected task = %+v, want empty body and source workspace %q", selected, source.WorkspaceID)
	}
	defaulted, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Defaulted"})
	if err != nil {
		t.Fatalf("CreateTask default source workspace: %v", err)
	}
	if defaulted.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("default source workspace = %q, want primary %q", defaulted.SourceWorkspaceID, binding.WorkspaceID)
	}
	other, err := store.metadata.CreateProjectForWorkspace(ctx, t.TempDir(), "Other")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace other: %v", err)
	}
	if _, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Foreign", SourceWorkspaceID: other.WorkspaceID}); !errors.Is(err, ErrSourceWorkspaceNotInProject) {
		t.Fatalf("CreateTask foreign source workspace error = %v", err)
	}
}

func TestTaskUpdateEditsTitleAndBodyAfterAutomationStarts(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	source, err := store.metadata.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Before", Body: "before"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	afterTitle := " After "
	afterBody := " after body "
	updated, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &afterTitle, Body: &afterBody, SourceWorkspaceID: source.WorkspaceID})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.Title != "After" || updated.Body != "after body" || updated.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("updated task = %+v", updated)
	}
	renamedTitle := "Renamed"
	renamed, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &renamedTitle})
	if err != nil {
		t.Fatalf("UpdateTask title only: %v", err)
	}
	if renamed.Title != "Renamed" || renamed.Body != "after body" || renamed.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("title-only update = %+v, want previous body and source workspace preserved", renamed)
	}
	bodyOnlyBody := "body only change"
	bodyOnly, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Body: &bodyOnlyBody})
	if err != nil {
		t.Fatalf("UpdateTask body only: %v", err)
	}
	if bodyOnly.Title != "Renamed" || bodyOnly.Body != "body only change" || bodyOnly.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("body-only update = %+v, want previous title and source workspace preserved", bodyOnly)
	}
	startTask(t, ctx, store, task.ID)
	startedTitle := " After start "
	startedBody := " updated after start "
	startedUpdate, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &startedTitle, Body: &startedBody})
	if err != nil {
		t.Fatalf("UpdateTask after start: %v", err)
	}
	if startedUpdate.Title != "After start" || startedUpdate.Body != "updated after start" || startedUpdate.SourceWorkspaceID != source.WorkspaceID {
		t.Fatalf("after-start update = %+v", startedUpdate)
	}
	moveSourceTitle := "Move source"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &moveSourceTitle, SourceWorkspaceID: binding.WorkspaceID}); !errors.Is(err, ErrSourceWorkspaceAfterAutomation) {
		t.Fatalf("UpdateTask source workspace after start error = %v", err)
	}

	canceled, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Canceled"})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	if err := store.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	canceledTitle := "Canceled renamed"
	canceledBody := "canceled body"
	canceledUpdate, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: canceled.ID, Title: &canceledTitle, Body: &canceledBody})
	if err != nil {
		t.Fatalf("UpdateTask canceled title/body: %v", err)
	}
	if canceledUpdate.Title != "Canceled renamed" || canceledUpdate.Body != "canceled body" {
		t.Fatalf("canceled update = %+v", canceledUpdate)
	}
	canceledSourceTitle := "Canceled source"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: canceled.ID, Title: &canceledSourceTitle, SourceWorkspaceID: source.WorkspaceID}); !errors.Is(err, ErrSourceWorkspaceForCanceledTask) {
		t.Fatalf("UpdateTask canceled source error = %v", err)
	}
}

func TestDeleteTaskHardDeletesAssociatedRecords(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Delete me", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.AddComment(ctx, task.ID, "note", "user", "nek"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	deleted, err := store.DeleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if deleted.ID != task.ID || deleted.ProjectID != binding.ProjectID {
		t.Fatalf("deleted task identity = %+v, want task %q project %q", deleted, task.ID, binding.ProjectID)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	assertZeroTaskRows(t, store, "task_node_placements", string(task.ID))
	assertZeroTaskRows(t, store, "task_transitions", string(task.ID))
	assertZeroTaskRows(t, store, "task_comments", string(task.ID))
	if _, err := store.queries.GetTaskRun(ctx, string(started.RunID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskRun after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.DeleteTask(ctx, task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteTask missing = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteTaskHardDeletesParallelBatchRecords(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	// Fan-out leaves placements carrying parallel_batch_transition_id and
	// transition rows carrying source_placement_id/source_run_id. These are the
	// ON DELETE SET NULL cross-links whose runtime validation triggers previously
	// aborted a cascading task delete; deletion must remove them cleanly.
	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if len(result.PlacementIDs) != 2 {
		t.Fatalf("fanout result = %+v, want two parallel branch placements", result)
	}

	deleted, err := store.DeleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DeleteTask with parallel batch placements: %v", err)
	}
	if deleted.ID != task.ID {
		t.Fatalf("deleted task id = %q, want %q", deleted.ID, task.ID)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after DeleteTask = %v, want sql.ErrNoRows", err)
	}
	assertZeroTaskRows(t, store, "task_node_placements", string(task.ID))
	assertZeroTaskRows(t, store, "task_transitions", string(task.ID))
	assertZeroTaskRows(t, store, "task_comments", string(task.ID))
}

func TestCompleteRunUsesRunStartSnapshotAfterGraphChanges(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	beforeEdit := currentWorkflowRevision(t, ctx, store, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID,
		[]NodeRecord{{ID: "node-extra-terminal", WorkflowID: workflowID, Key: "archived", Kind: workflow.NodeKindTerminal, DisplayName: "Archived"}},
		[]TransitionGroupRecord{{ID: "group-archive", WorkflowID: workflowID, SourceNodeID: agent.ID, TransitionID: "archive", DisplayName: "Archive"}},
		[]EdgeRecord{{ID: "edge-archive", WorkflowID: workflowID, TransitionGroupID: "group-archive", Key: "archive", TargetNodeID: "node-extra-terminal", ContextMode: workflow.ContextModeNewSession}},
	)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "archive", OutputValues: map[string]string{"summary": "done"}}); !completionHasCode(err, CompletionCodeInvalidTransitionID) {
		t.Fatalf("expected completion to reject transition added after run start, got %v", err)
	}
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", Commentary: "finished"})
	if completed.State != "applied" || len(completed.PlacementIDs) != 1 || len(completed.RunIDs) != 0 {
		t.Fatalf("completion result = %+v", completed)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].TransitionID != "done" || transitions[1].State != "applied" {
		t.Fatalf("transitions after completion = %+v", transitions)
	}
	edges, err := store.ListTransitionEdges(ctx, transitions[1].ID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].EdgeKey != "done" || edges[0].WorkflowRevisionSeen != beforeEdit || edges[0].TargetPlacementID != completed.PlacementIDs[0] {
		t.Fatalf("completion edge snapshot = %+v, want one done edge at revision %d", edges, beforeEdit)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	terminalActive := false
	sourceCompleted := false
	for _, placement := range placements {
		if placement.ID == started.PlacementID && placement.State == "completed" {
			sourceCompleted = true
		}
		if placement.ID == completed.PlacementIDs[0] && placement.State == "active" {
			terminalActive = true
		}
	}
	if !sourceCompleted || !terminalActive {
		t.Fatalf("placements after completion = %+v, want completed source and active terminal", placements)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt == 0 {
		t.Fatalf("runs after completion = %+v", runs)
	}
}

func TestCompleteRunBuildsChildSnapshotFromParentRevision(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	reviewerID := workflow.NodeID("node-reviewer-" + string(workflowID))
	if _, err := store.AddNode(ctx, NodeRecord{ID: reviewerID, WorkflowID: workflowID, Key: "reviewer", Kind: workflow.NodeKindAgent, DisplayName: "Reviewer", SubagentRole: "coder", PromptTemplate: "Review work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode reviewer: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-review-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: agent.ID, TransitionID: "review", DisplayName: "Review"}); err != nil {
		t.Fatalf("AddTransitionGroup review: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-review-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-review-" + string(workflowID)), Key: "review", TargetNodeID: reviewerID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review work."}); err != nil {
		t.Fatalf("AddEdge review: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-review-done-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: reviewerID, TransitionID: "review_done", DisplayName: "Review Done"}); err != nil {
		t.Fatalf("AddTransitionGroup review done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-review-done-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-review-done-" + string(workflowID)), Key: "review_done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}); err != nil {
		t.Fatalf("AddEdge review done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	archiveID := workflow.NodeID("node-archive-" + string(workflowID))
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID,
		[]NodeRecord{{ID: archiveID, WorkflowID: workflowID, Key: "archive", Kind: workflow.NodeKindTerminal, DisplayName: "Archive"}},
		[]TransitionGroupRecord{{ID: workflow.TransitionGroupID("group-review-archive-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: reviewerID, TransitionID: "archive", DisplayName: "Archive"}},
		[]EdgeRecord{{ID: workflow.EdgeID("edge-review-archive-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-review-archive-" + string(workflowID)), Key: "archive", TargetNodeID: archiveID, ContextMode: workflow.ContextModeNewSession}},
	)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "review"})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("completion child runs = %+v, want one", completed.RunIDs)
	}
	runContext, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if len(runContext.TransitionIDs) != 1 || runContext.TransitionIDs[0] != "review_done" {
		t.Fatalf("child transition ids = %+v, want only review_done from parent snapshot", runContext.TransitionIDs)
	}
}

func TestStartTaskRejectsCanceledAndAlreadyStartedTasks(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	canceled, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Canceled", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	if err := store.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if _, err := store.StartTask(ctx, canceled.ID); !errors.Is(err, ErrTaskCanceled) {
		t.Fatalf("StartTask canceled error = %v", err)
	}

	startedTask, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Started", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask started: %v", err)
	}
	if _, err := store.StartTask(ctx, startedTask.ID); err != nil {
		t.Fatalf("StartTask first: %v", err)
	}
	if _, err := store.StartTask(ctx, startedTask.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("StartTask second = %v, want sql.ErrNoRows", err)
	}
	runs, err := store.ListRuns(ctx, startedTask.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after duplicate start = %+v, want exactly one", runs)
	}
}

func TestRunStartContextExposesAcceptedStartTransitionPath(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	input, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.AcceptedTransitionPath.SourceNodeDisplayName != "Backlog" || input.AcceptedTransitionPath.TargetNodeDisplayName != "Agent" {
		t.Fatalf("accepted transition path = %+v, want Backlog -> Agent", input.AcceptedTransitionPath)
	}
}

func TestWorkflowTransitionsRefreshTaskUpdatedAt(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 1 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force stale task timestamp: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	afterStart, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after start: %v", err)
	}
	if afterStart.UpdatedAtUnixMs <= 1 {
		t.Fatalf("task updated_at after start = %d, want refreshed", afterStart.UpdatedAtUnixMs)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 2 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force stale task timestamp after start: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	afterComplete, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after complete: %v", err)
	}
	if afterComplete.UpdatedAtUnixMs <= 2 {
		t.Fatalf("task updated_at after complete = %d, want refreshed", afterComplete.UpdatedAtUnixMs)
	}
}

func TestStartTaskConcurrentCallsCreateOneRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.StartTask(ctx, task.ID)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	noRows := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, sql.ErrNoRows):
			noRows++
		default:
			t.Fatalf("StartTask concurrent unexpected error: %v", err)
		}
	}
	if successes != 1 || noRows != 1 {
		t.Fatalf("concurrent starts successes=%d noRows=%d, want 1/1", successes, noRows)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after concurrent start = %+v, want exactly one", runs)
	}
}

func TestCompleteRunCreatesTargetRunForContinueSessionContextMode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "plan done"}})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("target run ids = %+v, want one continuation target", completed.RunIDs)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].CompletedAt == 0 || runs[1].CompletedAt != 0 || runs[1].InterruptedAt != 0 {
		t.Fatalf("runs after continuation completion = %+v, want completed source and active target", runs)
	}
	edges, err := store.ListTransitionEdges(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	var persistedContextMode string
	if err := store.db.QueryRowContext(ctx, `SELECT context_mode FROM task_transition_edges WHERE id = ?`, edges[0].ID).Scan(&persistedContextMode); err != nil {
		t.Fatalf("query transition edge context mode: %v", err)
	}
	if len(edges) != 1 || persistedContextMode != string(workflow.ContextModeContinueSession) || edges[0].TargetPlacementID != runs[1].PlacementID {
		t.Fatalf("transition edge snapshot = %+v, want continue_session target edge", edges)
	}
	input, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.Node.Key != "implement" || input.InputValues["prior_summary"] != "plan done" {
		t.Fatalf("target run context = %+v, want implement node with bound prior output", input)
	}
	var runMetadataJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT metadata_json FROM task_runs WHERE id = ?`, string(completed.RunIDs[0])).Scan(&runMetadataJSON); err != nil {
		t.Fatalf("query target run metadata: %v", err)
	}
	runMetadata := struct {
		ContextMode     string `json:"context_mode"`
		SourceRunID     string `json:"source_run_id"`
		SourceSessionID string `json:"source_session_id"`
	}{}
	if err := workflow.UnmarshalString(runMetadataJSON, &runMetadata); err != nil {
		t.Fatalf("unmarshal target run metadata: %v", err)
	}
	if runMetadata.ContextMode != string(workflow.ContextModeContinueSession) || runMetadata.SourceRunID != string(started.RunID) {
		t.Fatalf("target run metadata = %+v, want context mode and source run", runMetadata)
	}
}

func TestRunStartContextResolvesPriorTransitionParameters(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createPromptNodeReferenceWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"summary": "plan summary"}})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("target run ids = %+v, want one", completed.RunIDs)
	}
	audit := completeRun(t, ctx, store, CompleteRunRequest{RunID: completed.RunIDs[0], TransitionID: "audit"})
	if len(audit.RunIDs) != 1 {
		t.Fatalf("audit target run ids = %+v, want one", audit.RunIDs)
	}
	input, err := store.GetRunStartContext(ctx, audit.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.PriorParameterValues["next"]["summary"] != "plan summary" {
		t.Fatalf("prior parameter values = %+v, want next.summary", input.PriorParameterValues)
	}
	mutatedOutputs, err := workflow.MarshalString(map[string]string{"summary": "mutated later"})
	if err != nil {
		t.Fatalf("marshal mutated outputs: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transitions SET output_values_json = ? WHERE source_run_id = ?`, mutatedOutputs, string(started.RunID)); err != nil {
		t.Fatalf("mutate source output: %v", err)
	}
	frozenInput, err := store.GetRunStartContext(ctx, audit.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext frozen: %v", err)
	}
	if frozenInput.PriorParameterValues["next"]["summary"] != "plan summary" {
		t.Fatalf("frozen prior parameter values = %+v, want original plan summary", frozenInput.PriorParameterValues)
	}
}

func TestTransitionParameterDerivesOutputRequirement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createPromptNodeReferenceWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	sourceContext, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext source: %v", err)
	}
	if len(sourceContext.Node.OutputFields) != 1 || sourceContext.Node.OutputFields[0].Name != "summary" || sourceContext.Node.OutputFields[0].Description != "Plan summary." {
		t.Fatalf("source output fields = %+v, want prompt-derived summary description", sourceContext.Node.OutputFields)
	}
	if len(sourceContext.TransitionOptions) != 1 || sourceContext.TransitionOptions[0].ID != "next" || sourceContext.TransitionOptions[0].Description != "Continue after planning is complete." || len(sourceContext.TransitionOptions[0].Parameters) != 1 || sourceContext.TransitionOptions[0].Parameters[0].Key != "summary" || sourceContext.TransitionOptions[0].Parameters[0].Description != "Plan summary." {
		t.Fatalf("source transition options = %+v, want next description and summary parameter", sourceContext.TransitionOptions)
	}

	_, err = store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next"})
	if !completionHasCode(err, CompletionCodeRequiredOutputMissing) {
		t.Fatalf("CompleteRun error = %v, want required output", err)
	}
}

func TestTransitionOptionsFromSnapshotUnionFanoutBranchParameters(t *testing.T) {
	options := transitionOptionsFromSnapshot(runStartSnapshot{
		Node: nodeContractSnapshot{ID: "node-plan"},
		TransitionGroups: []transitionContractSnapshot{
			{
				SourceNodeID: "node-plan",
				TransitionID: "split",
				DisplayName:  "Split",
				Edges: []edgeContractSnapshot{
					{ID: "edge-a", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
					{ID: "edge-b", Parameters: []workflow.Parameter{{Key: "risk", Description: "Known risk."}}},
				},
			},
		},
	})

	if len(options) != 1 || options[0].ID != "split" || options[0].DisplayName != "Split" {
		t.Fatalf("transition options = %+v, want split", options)
	}
	if len(options[0].Parameters) != 2 || options[0].Parameters[0].Key != "summary" || options[0].Parameters[1].Key != "risk" {
		t.Fatalf("transition option parameters = %+v, want branch parameter union", options[0].Parameters)
	}
}

func TestPriorTransitionParameterApprovalFreezesOutputValue(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createPromptNodeReferenceWorkflow(t, ctx, store)
	if _, err := store.UpdateEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-audit-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: workflow.TransitionGroupID("group-audit-" + string(workflowID)),
		Key:               "audit",
		TargetNodeID:      workflow.NodeID("node-audit-" + string(workflowID)),
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Audit {{.Params.next.summary}}.",
		RequiresApproval:  true,
	}); err != nil {
		t.Fatalf("UpdateEdge approval: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	review := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"summary": "approval summary"}})
	if len(review.RunIDs) != 1 {
		t.Fatalf("review target run ids = %+v, want one", review.RunIDs)
	}
	pending, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: review.RunIDs[0], TransitionID: "audit"})
	if err != nil {
		t.Fatalf("CompleteRun audit: %v", err)
	}
	if !pending.RequiresApproval {
		t.Fatalf("completion result = %+v, want pending approval", pending)
	}
	mutatedOutputs, err := workflow.MarshalString(map[string]string{"summary": "mutated before approval"})
	if err != nil {
		t.Fatalf("marshal mutated outputs: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transitions SET output_values_json = ? WHERE source_run_id = ?`, mutatedOutputs, string(started.RunID)); err != nil {
		t.Fatalf("mutate source output: %v", err)
	}

	approved, err := store.ApproveTransition(ctx, pending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approved run ids = %+v, want one", approved.RunIDs)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.PriorParameterValues["next"]["summary"] != "approval summary" {
		t.Fatalf("approved prior parameter values = %+v, want frozen approval summary", input.PriorParameterValues)
	}
}

func TestRunStartContextUsesSelectedPriorNodeSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after plan: %v", err)
	}
	var implementationRun RunRecord
	for _, run := range runs {
		if run.NodeID == implementationNode.ID {
			implementationRun = run
		}
	}
	if implementationRun.ID == "" {
		t.Fatalf("implementation run not found: %+v", runs)
	}
	claimedImplementation, err := store.ClaimRun(ctx, implementationRun.ID, implementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun implementation: %v", err)
	}
	implementationSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, implementationRun.ID, claimedImplementation.Generation, implementationSessionID); err != nil {
		t.Fatalf("AttachRunSession implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after implementation: %v", err)
	}
	var acceptanceRun RunRecord
	for _, run := range runs {
		if run.NodeID == acceptanceNode.ID {
			acceptanceRun = run
		}
	}
	if acceptanceRun.ID == "" {
		t.Fatalf("acceptance run not found: %+v", runs)
	}
	claimedAcceptance, err := store.ClaimRun(ctx, acceptanceRun.ID, acceptanceRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun acceptance: %v", err)
	}
	acceptanceSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, acceptanceRun.ID, claimedAcceptance.Generation, acceptanceSessionID); err != nil {
		t.Fatalf("AttachRunSession acceptance: %v", err)
	}
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	if len(completed.RunIDs) != 1 {
		t.Fatalf("acceptance completion = %+v, want open_pr run", completed)
	}
	input, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext open_pr: %v", err)
	}
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("open_pr context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
	if input.AcceptedTransitionPath.SourceNodeDisplayName != "Acceptance" || input.AcceptedTransitionPath.TargetNodeDisplayName != "Open PR" {
		t.Fatalf("open_pr accepted transition path = %+v, want Acceptance -> Open PR", input.AcceptedTransitionPath)
	}
	if input.InputValues["acceptance_decision"] != "approved" {
		t.Fatalf("open_pr input values = %+v, want immediate acceptance output", input.InputValues)
	}
}

func TestSelectedContextSourceUsesLatestCompletedPriorNodeRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	reworkGroup := workflow.TransitionGroupID("group-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: acceptanceNode.ID, TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-rework-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reworkGroup, Key: "rework", TargetNodeID: implementationNode.ID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Rework summary."}}}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	firstImplementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	firstClaim, err := store.ClaimRun(ctx, firstImplementationRun.ID, firstImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun first implementation: %v", err)
	}
	firstSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, firstImplementationRun.ID, firstClaim.Generation, firstSessionID); err != nil {
		t.Fatalf("AttachRunSession first implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	firstAcceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	secondImplementationRun := latestRunForNode(t, ctx, store, task.ID, implementationNode.ID)
	if secondImplementationRun.ID == firstImplementationRun.ID {
		t.Fatalf("second implementation run = first run %q", secondImplementationRun.ID)
	}
	secondClaim, err := store.ClaimRun(ctx, secondImplementationRun.ID, secondImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun second implementation: %v", err)
	}
	secondSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, secondImplementationRun.ID, secondClaim.Generation, secondSessionID); err != nil {
		t.Fatalf("AttachRunSession second implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: secondImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "second implementation"}})
	secondAcceptanceRun := latestRunForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: secondAcceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	input, err := store.GetRunStartContext(ctx, completed.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext open_pr: %v", err)
	}
	if input.SourceRunID != secondImplementationRun.ID || input.SourceSessionID != secondSessionID {
		t.Fatalf("open_pr source run/session = %q/%q, want latest implementation %q/%q; first session was %q", input.SourceRunID, input.SourceSessionID, secondImplementationRun.ID, secondSessionID, firstSessionID)
	}
}

func TestPreviousTargetContextSourceUsesLatestCompletedTargetRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	addOutputFieldToNode(t, ctx, store, workflowID, acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, acceptanceNode.ID, implementationNode.ID, false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	firstImplementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	firstClaim, err := store.ClaimRun(ctx, firstImplementationRun.ID, firstImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun first implementation: %v", err)
	}
	firstSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, firstImplementationRun.ID, firstClaim.Generation, firstSessionID); err != nil {
		t.Fatalf("AttachRunSession first implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: firstImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "first implementation"}})
	firstAcceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	firstRework := completeRun(t, ctx, store, CompleteRunRequest{RunID: firstAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	if len(firstRework.RunIDs) != 1 {
		t.Fatalf("first rework result = %+v, want one implementation run", firstRework)
	}
	firstReworkInput, err := store.GetRunStartContext(ctx, firstRework.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext first rework: %v", err)
	}
	if firstReworkInput.SourceRunID != firstImplementationRun.ID || firstReworkInput.SourceSessionID != firstSessionID || firstReworkInput.SourceNode.Key != "implementation" {
		t.Fatalf("first rework source = run %q session %q node %q, want first implementation %q/%q", firstReworkInput.SourceRunID, firstReworkInput.SourceSessionID, firstReworkInput.SourceNode.Key, firstImplementationRun.ID, firstSessionID)
	}
	secondImplementationRun := latestRunForNode(t, ctx, store, task.ID, implementationNode.ID)
	secondClaim, err := store.ClaimRun(ctx, secondImplementationRun.ID, secondImplementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun second implementation: %v", err)
	}
	secondSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, secondImplementationRun.ID, secondClaim.Generation, secondSessionID); err != nil {
		t.Fatalf("AttachRunSession second implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: secondImplementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "second implementation"}})
	secondAcceptanceRun := latestRunForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	secondRework := completeRun(t, ctx, store, CompleteRunRequest{RunID: secondAcceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "still needs changes"}})
	if len(secondRework.RunIDs) != 1 {
		t.Fatalf("second rework result = %+v, want one implementation run", secondRework)
	}
	secondReworkInput, err := store.GetRunStartContext(ctx, secondRework.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext second rework: %v", err)
	}
	if secondReworkInput.SourceRunID != secondImplementationRun.ID || secondReworkInput.SourceSessionID != secondSessionID {
		t.Fatalf("second rework source run/session = %q/%q, want latest implementation %q/%q; first session was %q", secondReworkInput.SourceRunID, secondReworkInput.SourceSessionID, secondImplementationRun.ID, secondSessionID, firstSessionID)
	}
}

func TestPendingApprovalResolvesPreviousTargetContextSourceOnCompletion(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	addOutputFieldToNode(t, ctx, store, workflowID, acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, acceptanceNode.ID, implementationNode.ID, true)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	claimedImplementation, err := store.ClaimRun(ctx, implementationRun.ID, implementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun implementation: %v", err)
	}
	implementationSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, implementationRun.ID, claimedImplementation.Generation, implementationSessionID); err != nil {
		t.Fatalf("AttachRunSession implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	pending := completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})
	if pending.State != "pending_approval" {
		t.Fatalf("rework completion = %+v, want pending approval", pending)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	insertCompletedRunForNodeAfterTransition(t, ctx, store, task.ID, implementationNode.ID, implementationRun.ID, competingSessionID, pending.TransitionID)
	approved, err := store.ApproveTransition(ctx, pending.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approval result = %+v, want one implementation run", approved)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext approved rework: %v", err)
	}
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("approved rework context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
	if input.SourceSessionID == competingSessionID {
		t.Fatalf("approved rework used competing implementation session %q completed after approval wait started", competingSessionID)
	}
}

func TestPreviousTargetContextSourceStaysInParallelBatch(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	fixedNow := time.UnixMilli(1_000_000)
	store.now = func() time.Time { return fixedNow }
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, _ := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	currentRun := runForNode(t, ctx, store, task.ID, implA.ID)
	mutateRunStartSnapshot(t, ctx, store, currentRun.ID, func(t *testing.T, snapshot *runStartSnapshot) {
		target := nodeSnapshotByID(t, *snapshot, implA.ID)
		target.OutputFields = append(target.OutputFields, workflow.OutputField{Name: "summary", Description: "Summary."})
		snapshot.Node = target
		for index := range snapshot.Nodes {
			if snapshot.Nodes[index].ID == implA.ID {
				snapshot.Nodes[index] = target
			}
		}
		snapshot.TransitionGroups = append(snapshot.TransitionGroups, transitionContractSnapshot{
			ID:           workflow.TransitionGroupID("snapshot-group-redo-a"),
			SourceNodeID: implA.ID,
			TransitionID: "redo",
			DisplayName:  "Redo A",
			Edges: []edgeContractSnapshot{{
				Key:                "redo",
				TargetNode:         target,
				ContextMode:        workflow.ContextModeContinueSession,
				ContextSource:      workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
				InputBindings:      []workflow.InputBinding{{Name: "summary", Source: workflow.BindingSourceTransitionOutput, Field: "summary"}},
				OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}},
			}},
		})
	})
	claimedCurrent, err := store.ClaimRun(ctx, currentRun.ID, currentRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun current branch: %v", err)
	}
	currentSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, currentRun.ID, claimedCurrent.Generation, currentSessionID); err != nil {
		t.Fatalf("AttachRunSession current branch: %v", err)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	currentBatchID, _ := placementParallelIDs(t, ctx, store, currentRun.PlacementID)
	competingBatchID := taskTransitionIDOtherThan(t, ctx, store, task.ID, currentBatchID)
	competingRunID := insertCompletedRunForNodeInBatch(t, ctx, store, task.ID, implA.ID, currentRun.ID, competingSessionID, string(competingBatchID), fixedNow.UnixMilli())

	redo := completeRun(t, ctx, store, CompleteRunRequest{RunID: currentRun.ID, TransitionID: "redo", OutputValues: map[string]string{"summary": "redo current branch"}})
	if len(redo.RunIDs) != 1 {
		t.Fatalf("redo result = %+v, want one branch rerun", redo)
	}
	input, err := store.GetRunStartContext(ctx, redo.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext redo: %v", err)
	}
	if input.SourceRunID != currentRun.ID || input.SourceSessionID != currentSessionID {
		t.Fatalf("redo context source = run %q session %q, want current batch run %q session %q; competing run was %q session %q", input.SourceRunID, input.SourceSessionID, currentRun.ID, currentSessionID, competingRunID, competingSessionID)
	}
}

func TestPendingApprovalResolvesSelectedContextSourceOnApproval(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	openPREdge := edgeByKey(t, def, "open_pr")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: openPREdge.ID, WorkflowID: workflowID, TransitionGroupID: openPREdge.TransitionGroupID, Key: openPREdge.Key, TargetNodeID: openPREdge.TargetNodeID, ContextMode: openPREdge.ContextMode, ContextSource: openPREdge.ContextSource, PromptTemplate: openPREdge.PromptTemplate, Parameters: openPREdge.Parameters, RequiresApproval: true}); err != nil {
		t.Fatalf("UpdateEdge open_pr approval: %v", err)
	}
	def, _, err = store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after plan: %v", err)
	}
	var implementationRun RunRecord
	for _, run := range runs {
		if run.NodeID == implementationNode.ID {
			implementationRun = run
		}
	}
	claimedImplementation, err := store.ClaimRun(ctx, implementationRun.ID, implementationRun.Generation)
	if err != nil {
		t.Fatalf("ClaimRun implementation: %v", err)
	}
	implementationSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, implementationRun.ID, claimedImplementation.Generation, implementationSessionID); err != nil {
		t.Fatalf("AttachRunSession implementation: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after implementation: %v", err)
	}
	var acceptanceRun RunRecord
	for _, run := range runs {
		if run.NodeID == acceptanceNode.ID {
			acceptanceRun = run
		}
	}
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	if completed.State != "pending_approval" {
		t.Fatalf("acceptance completion = %+v, want pending approval", completed)
	}
	competingSessionID := createTestSession(t, ctx, store, binding, cfg)
	insertCompletedRunForNodeAfterTransition(t, ctx, store, task.ID, implementationNode.ID, implementationRun.ID, competingSessionID, completed.TransitionID)
	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approval result = %+v, want open_pr run", approved)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext open_pr: %v", err)
	}
	if input.SourceRunID != implementationRun.ID || input.SourceSessionID != implementationSessionID || input.SourceNode.Key != "implementation" {
		t.Fatalf("approved open_pr context source = run %q session %q node %q, want implementation run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, implementationRun.ID, implementationSessionID)
	}
	if input.SourceSessionID == competingSessionID {
		t.Fatalf("approved open_pr used competing implementation session %q completed after approval wait started", competingSessionID)
	}
}

func TestSelectedContextSourceMissingPriorRunFailsClearly(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	mutateRunStartSnapshot(t, ctx, store, started.RunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "implement", func(group *transitionContractSnapshot) {
			group.Edges[0].ContextMode = workflow.ContextModeContinueSession
			group.Edges[0].ContextSource = workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}
		})
	})
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	var selectedErr ContextSourceNoCompletedRunError
	if !errors.As(err, &selectedErr) || selectedErr.Kind != ContextSourceKindSelected || selectedErr.NodeKey != "implementation" {
		t.Fatalf("CompleteRun selected source error = %v, want missing completed run", err)
	}
}

func TestPreviousTargetContextSourceMissingPriorRunFailsClearly(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	mutateRunStartSnapshot(t, ctx, store, started.RunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "implement", func(group *transitionContractSnapshot) {
			group.Edges[0].ContextMode = workflow.ContextModeContinueSession
			group.Edges[0].ContextSource = workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget}
		})
	})
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	var previousTargetErr ContextSourceNoCompletedRunError
	if !errors.As(err, &previousTargetErr) || previousTargetErr.Kind != ContextSourceKindPreviousTarget || previousTargetErr.NodeKey != "implementation" {
		t.Fatalf("CompleteRun previous target source error = %v, want missing completed run", err)
	}
}

func TestCompleteRunCreatesPendingApprovalTransition(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if result.State != "pending_approval" || !result.RequiresApproval || len(result.PlacementIDs) != 0 || len(result.RunIDs) != 0 {
		t.Fatalf("completion result = %+v, want pending approval without target placement/run", result)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "pending_approval" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("transitions after approval completion = %+v", transitions)
	}
	edges, err := store.ListTransitionEdges(ctx, result.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].State != "pending" || edges[0].TargetPlacementID != "" {
		t.Fatalf("approval edge snapshots = %+v, want pending edge without placement", edges)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt == 0 {
		t.Fatalf("runs after pending approval = %+v, want source run completed", runs)
	}
}

func TestApprovePendingTransitionStartsStoredTargetEdgeSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if approved.State != "approved" || len(approved.PlacementIDs) != 1 || len(approved.RunIDs) != 0 {
		t.Fatalf("approved result = %+v, want approved terminal placement without run", approved)
	}
	again, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition duplicate: %v", err)
	}
	if again.State != "approved" || len(again.PlacementIDs) != 1 || again.PlacementIDs[0] != approved.PlacementIDs[0] {
		t.Fatalf("duplicate approval = %+v, want idempotent same placement", again)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].State != "approved" {
		t.Fatalf("transitions after approval = %+v", transitions)
	}
	edges, err := store.ListTransitionEdges(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].State != "applied" || edges[0].TargetPlacementID == "" {
		t.Fatalf("approval edges = %+v, want applied edge with target placement", edges)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 3 || placements[2].ID != approved.PlacementIDs[0] || placements[2].State != "active" {
		t.Fatalf("placements after approval = %+v", placements)
	}
}

func TestApprovePendingAgentTransitionRetryPreservesRunIDs(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	again, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition duplicate: %v", err)
	}
	if len(approved.RunIDs) != 1 || len(again.RunIDs) != 1 || approved.RunIDs[0] != again.RunIDs[0] {
		t.Fatalf("duplicate approval run ids approved=%+v again=%+v", approved.RunIDs, again.RunIDs)
	}
	if len(approved.PlacementIDs) != 1 || len(again.PlacementIDs) != 1 || approved.PlacementIDs[0] != again.PlacementIDs[0] {
		t.Fatalf("duplicate approval placements approved=%+v again=%+v", approved.PlacementIDs, again.PlacementIDs)
	}
}

func TestApprovePendingAgentTransitionUsesFrozenTargetSnapshotAfterSourceSnapshotMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "next")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "done"}})
	legacySnapshot := runStartSnapshot{WorkflowID: workflowID, WorkflowRevisionSeen: currentWorkflowRevision(t, ctx, store, workflowID), Node: nodeSnapshot(plan)}
	updateRunStartSnapshot(t, ctx, store, started.RunID, legacySnapshot)

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	if len(approved.RunIDs) != 1 {
		t.Fatalf("approved run ids = %+v, want one", approved.RunIDs)
	}
	input, err := store.GetRunStartContext(ctx, approved.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext approved target: %v", err)
	}
	if input.Node.Key != "implement" || len(input.TransitionIDs) != 1 || input.TransitionIDs[0] != "done" {
		t.Fatalf("approved target context = node %s transitions %+v, want frozen implement snapshot", input.Node.Key, input.TransitionIDs)
	}
}

func TestApprovePendingTransitionIsConcurrentIdempotent(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	var wg sync.WaitGroup
	results := make([]CompleteRunResult, 2)
	errs := make([]error, 2)
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = store.ApproveTransition(context.Background(), completed.TransitionID)
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("ApproveTransition[%d]: %v", index, err)
		}
	}
	if results[0].State != "approved" || results[1].State != "approved" || len(results[0].PlacementIDs) != 1 || len(results[1].PlacementIDs) != 1 || results[0].PlacementIDs[0] != results[1].PlacementIDs[0] {
		t.Fatalf("concurrent approval results = %+v", results)
	}
}

func TestApprovePendingJoinEdgesProgressesJoin(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	if first.State != "pending_approval" {
		t.Fatalf("first branch result = %+v, want pending approval", first)
	}
	firstApproved, err := store.ApproveTransition(ctx, first.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch a: %v", err)
	}
	if len(firstApproved.PlacementIDs) != 0 || len(firstApproved.RunIDs) != 0 {
		t.Fatalf("first approval result = %+v, want join still waiting", firstApproved)
	}
	second, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[implB.ID], TransitionID: "join"})
	if err != nil {
		t.Fatalf("CompleteRun branch b: %v", err)
	}
	secondApproved, err := store.ApproveTransition(ctx, second.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch b: %v", err)
	}
	if len(secondApproved.PlacementIDs) != 1 || len(secondApproved.RunIDs) != 1 {
		t.Fatalf("second approval result = %+v, want joined downstream run", secondApproved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if transitions[len(transitions)-1].TransitionID != "done" || transitions[len(transitions)-1].OutputValues["joined"] != "branch a" {
		t.Fatalf("transitions after approved join = %+v", transitions)
	}
}

func TestApprovePendingJoinWaitsForAllBranchApprovals(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	second, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRuns[implB.ID], TransitionID: "join"})
	if err != nil {
		t.Fatalf("CompleteRun branch b: %v", err)
	}
	firstApproved, err := store.ApproveTransition(ctx, first.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch a: %v", err)
	}
	if len(firstApproved.PlacementIDs) != 0 || len(firstApproved.RunIDs) != 0 {
		t.Fatalf("first approval result = %+v, want join waiting for second approval", firstApproved)
	}
	secondApproved, err := store.ApproveTransition(ctx, second.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition branch b: %v", err)
	}
	if len(secondApproved.PlacementIDs) != 1 || len(secondApproved.RunIDs) != 1 {
		t.Fatalf("second approval result = %+v, want joined downstream run", secondApproved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	doneTransitions := 0
	for _, transition := range transitions {
		if transition.TransitionID == "done" {
			doneTransitions++
		}
	}
	if doneTransitions != 1 {
		t.Fatalf("done transition count = %d, transitions=%+v", doneTransitions, transitions)
	}
}

func TestApprovalTransitionGroupWaitsAsWholeWhenAnyEdgeRequiresApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	mutateRunStartSnapshot(t, ctx, store, started.RunID, func(t *testing.T, snapshot *runStartSnapshot) {
		mutateSnapshotTransition(t, snapshot, "done", func(group *transitionContractSnapshot) {
			second := group.Edges[0]
			second.Key = "second"
			second.RequiresApproval = false
			group.Edges = append(group.Edges, second)
		})
	})

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if result.State != "pending_approval" || len(result.PlacementIDs) != 0 {
		t.Fatalf("completion result = %+v, want whole group pending approval", result)
	}
	edges, err := store.ListTransitionEdges(ctx, result.TransitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 2 || edges[0].State != "pending" || edges[1].State != "pending" {
		t.Fatalf("transition edges = %+v, want both edges pending", edges)
	}
}

func TestCompleteRunFanoutCreatesParallelBranchPlacements(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "worktree-fanout-" + string(workflowID)
	worktreeRoot := filepath.Join(t.TempDir(), "fanout-worktree")
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET source_workspace_id = ?, managed_worktree_id = ? WHERE id = ?`, binding.WorkspaceID, worktreeID, string(task.ID)); err != nil {
		t.Fatalf("attach managed worktree to task: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if len(result.PlacementIDs) != 2 || len(result.RunIDs) != 2 {
		t.Fatalf("fanout result = %+v, want two branch placements and runs", result)
	}
	for _, runID := range result.RunIDs {
		input, err := store.GetRunStartContext(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunStartContext branch %s: %v", runID, err)
		}
		if !input.IsFanoutBranch {
			t.Fatalf("branch run context %s IsFanoutBranch=false, want true", runID)
		}
		if input.WorktreeID != worktreeID || input.WorktreeRoot != worktreeRoot {
			t.Fatalf("branch run context %s worktree id/root = %q/%q, want %q/%q", runID, input.WorktreeID, input.WorktreeRoot, worktreeID, worktreeRoot)
		}
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT id, parallel_batch_transition_id, parallel_branch_edge_id
FROM task_node_placements
WHERE id IN (?, ?)
ORDER BY parallel_branch_edge_id ASC`, string(result.PlacementIDs[0]), string(result.PlacementIDs[1]))
	if err != nil {
		t.Fatalf("query branch placements: %v", err)
	}
	defer func() { _ = rows.Close() }()
	branches := map[string]string{}
	for rows.Next() {
		var placementID string
		var batchID sql.NullString
		var branchEdgeID sql.NullString
		if err := rows.Scan(&placementID, &batchID, &branchEdgeID); err != nil {
			t.Fatalf("scan branch placement: %v", err)
		}
		if batchID.String != string(result.TransitionID) || !branchEdgeID.Valid || branchEdgeID.String == "" {
			t.Fatalf("branch placement %s batch=%+v branch=%+v, want batch transition and branch edge", placementID, batchID, branchEdgeID)
		}
		branches[branchEdgeID.String] = placementID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("branch rows: %v", err)
	}
	if len(branches) != 2 || branches["edge-split-a-"+string(workflowID)] == "" || branches["edge-split-b-"+string(workflowID)] == "" {
		t.Fatalf("branch identities = %+v, want split edge ids", branches)
	}
}

func TestSerialCompletionDoesNotCreateParallelBranchPlacement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if len(completed.PlacementIDs) != 1 {
		t.Fatalf("completion result = %+v, want one target placement", completed)
	}
	batchID, branchID := placementParallelIDs(t, ctx, store, completed.PlacementIDs[0])
	if batchID != "" || branchID != "" {
		t.Fatalf("serial placement parallel ids batch=%q branch=%q, want empty", batchID, branchID)
	}
}

func TestJoinWaitsForAllBranchesAndRoutesSelectedProvider(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	split, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if err != nil {
		t.Fatalf("CompleteRun split: %v", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	branchRunsByNode := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRunsByNode[run.NodeID] = run.ID
		}
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")

	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[implB.ID], TransitionID: "join"})
	if err != nil {
		t.Fatalf("CompleteRun branch b: %v", err)
	}
	if len(first.PlacementIDs) != 0 || len(first.RunIDs) != 0 {
		t.Fatalf("first branch result = %+v, want join waiting for missing branch", first)
	}
	selectedProviderValue := "  branch a\n"
	second, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": selectedProviderValue}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	if len(second.PlacementIDs) != 1 || len(second.RunIDs) != 1 {
		t.Fatalf("second branch result = %+v, want joined provider-routed agent run", second)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	joinTransition := transitions[len(transitions)-1]
	if joinTransition.TransitionID != "done" || joinTransition.OutputValues["joined"] != selectedProviderValue {
		t.Fatalf("join transition = %+v, want selected provider output", joinTransition)
	}
	input, err := store.GetRunStartContext(ctx, second.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext joined run: %v", err)
	}
	if input.InputValues["joined"] != selectedProviderValue {
		t.Fatalf("joined input = %+v, want selected provider value", input.InputValues)
	}
	if split.TransitionID == "" {
		t.Fatalf("split transition id missing")
	}
}

func TestJoinArrivalsKeepMultipleJoinEdgesForOneBranchPlacement(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRunsByNode := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	join := nodeByKey(t, def, "join")
	implA := nodeByKey(t, def, "impl_a")
	alternateProviderEdgeID := workflow.EdgeID("edge-join-a-alt-" + string(workflowID))
	// Bypass graph-edit policy here to model a historical transition snapshot
	// containing more than one applied join edge for the same branch placement.
	if err := store.queries.InsertWorkflowEdge(ctx, sqlitegen.InsertWorkflowEdgeParams{
		ID:                     string(alternateProviderEdgeID),
		TransitionGroupID:      "group-join-a-" + string(workflowID),
		EdgeKey:                "join_a_alt",
		TargetNodeID:           string(join.ID),
		ContextMode:            string(workflow.ContextModeNewSession),
		ContextSourceKind:      string(workflow.ContextSourceImmediateSource),
		PromptTemplate:         "",
		ParametersJson:         "[]",
		InputBindingsJson:      "[]",
		OutputRequirementsJson: "[]",
		SortOrder:              999,
	}); err != nil {
		t.Fatalf("insert alternate workflow edge: %v", err)
	}
	first, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "from alternate edge"}})
	if err != nil {
		t.Fatalf("CompleteRun branch a: %v", err)
	}
	if len(first.PlacementIDs) != 0 || len(first.RunIDs) != 0 {
		t.Fatalf("first branch result = %+v, want join waiting for missing branch", first)
	}
	if err := insertTransitionEdgeSnapshotWithMetadata(ctx, store.queries, string(first.TransitionID), edgeContractSnapshot{
		ID:         alternateProviderEdgeID,
		Key:        "join_a_alt",
		TargetNode: nodeSnapshot(join),
	}, "", "applied", workflowRunMetadata{}); err != nil {
		t.Fatalf("insert alternate transition edge snapshot: %v", err)
	}
	var batchID string
	if err := store.db.QueryRowContext(ctx, `
SELECT p.parallel_batch_transition_id
FROM task_runs r
JOIN task_node_placements p ON p.id = r.placement_id
WHERE r.id = ?`, string(branchRunsByNode[implA.ID])).Scan(&batchID); err != nil {
		t.Fatalf("query branch batch id: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	arrivals, err := joinArrivals(ctx, store.queries.WithTx(tx), batchID, join.ID)
	if err != nil {
		t.Fatalf("joinArrivals: %v", err)
	}
	joinSnapshot := nodeSnapshot(join)
	joinSnapshot.JoinInputProviders = []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: alternateProviderEdgeID}}
	values, ready, err := selectedJoinOutputValues(joinSnapshot, edgeContractSnapshot{OutputRequirements: []workflow.OutputRequirement{{FieldName: "joined"}}}, arrivals)
	if err != nil {
		t.Fatalf("selectedJoinOutputValues: %v", err)
	}
	if !ready || values["joined"] != "from alternate edge" {
		t.Fatalf("selected join values ready=%t values=%+v arrivals=%+v task=%s", ready, values, arrivals, task.ID)
	}
}

func TestJoinDownstreamCanUseSelectedPriorContextSource(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	planNode := nodeByKey(t, def, "plan")
	synthNode := nodeByKey(t, def, "synth")
	if _, err := store.UpdateEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-join-synth-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: workflow.TransitionGroupID("group-join-synth-" + string(workflowID)),
		Key:               "synth",
		TargetNodeID:      synthNode.ID,
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "plan"},
		PromptTemplate:    "Synthesize {{.Params.joined}}.",
	}); err != nil {
		t.Fatalf("UpdateEdge join synth: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimedPlan, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun plan: %v", err)
	}
	planSessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, started.RunID, claimedPlan.Generation, planSessionID); err != nil {
		t.Fatalf("AttachRunSession plan: %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns branches: %v", err)
	}
	branchRuns := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.NodeID != planNode.ID {
			branchRuns[run.NodeID] = run.ID
		}
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "a"}})
	joined := completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRuns[implB.ID], TransitionID: "join"})
	if len(joined.RunIDs) != 1 {
		t.Fatalf("joined result = %+v, want synth run", joined)
	}
	input, err := store.GetRunStartContext(ctx, joined.RunIDs[0])
	if err != nil {
		t.Fatalf("GetRunStartContext synth: %v", err)
	}
	if input.SourceRunID != started.RunID || input.SourceSessionID != planSessionID || input.SourceNode.Key != "plan" {
		t.Fatalf("synth context source = run %q session %q node %q, want plan run %q session %q", input.SourceRunID, input.SourceSessionID, input.SourceNode.Key, started.RunID, planSessionID)
	}
	if input.InputValues["joined"] != "a" {
		t.Fatalf("synth input values = %+v, want selected provider value", input.InputValues)
	}
}

func TestDuplicateBranchArrivalIsRejectedAndDoesNotDuplicateJoin(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	branchRunsByNode := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRunsByNode[run.NodeID] = run.ID
		}
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRunsByNode[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a"}})
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: branchRunsByNode[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "branch a again"}}); !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("duplicate branch completion error = %v, want run already completed", err)
	}
	joined := completeRun(t, ctx, store, CompleteRunRequest{RunID: branchRunsByNode[implB.ID], TransitionID: "join"})
	if len(joined.PlacementIDs) != 1 || len(joined.RunIDs) != 1 {
		t.Fatalf("join result = %+v, want one downstream placement/run", joined)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	joinTransitions := 0
	for _, transition := range transitions {
		if transition.TransitionID == "done" && transition.State == "applied" {
			joinTransitions++
		}
	}
	if joinTransitions != 1 {
		t.Fatalf("join transition count = %d, transitions=%+v", joinTransitions, transitions)
	}
}

func TestUnrelatedFanoutBatchDoesNotSatisfyWaitingJoin(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	waitingTask, waitingRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	otherTask, otherRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implA := nodeByKey(t, def, "impl_a")
	implB := nodeByKey(t, def, "impl_b")
	waitingFirst := completeRun(t, ctx, store, CompleteRunRequest{RunID: waitingRuns[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "waiting a"}})
	if len(waitingFirst.PlacementIDs) != 0 || len(waitingFirst.RunIDs) != 0 {
		t.Fatalf("waiting branch result = %+v, want no join yet", waitingFirst)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: otherRuns[implA.ID], TransitionID: "join", OutputValues: map[string]string{"joined": "other a"}})
	completeRun(t, ctx, store, CompleteRunRequest{RunID: otherRuns[implB.ID], TransitionID: "join"})
	transitions, err := store.ListTransitions(ctx, waitingTask.ID)
	if err != nil {
		t.Fatalf("ListTransitions waiting task: %v", err)
	}
	for _, transition := range transitions {
		if transition.TransitionID == "done" {
			t.Fatalf("waiting task transitions = %+v, unrelated batch satisfied join", transitions)
		}
	}
	joined := completeRun(t, ctx, store, CompleteRunRequest{RunID: waitingRuns[implB.ID], TransitionID: "join"})
	if len(joined.PlacementIDs) != 1 || len(joined.RunIDs) != 1 {
		t.Fatalf("waiting final join = %+v, want downstream run after own missing branch", joined)
	}
	_ = otherTask
}

func TestApprovalUsesStoredEdgeSnapshotAfterGraphEdit(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	originalDone := nodeByKind(t, def, workflow.NodeKindTerminal)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completed := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	archiveID := workflow.NodeID("node-archive-" + string(workflowID))
	forceWorkflowGraphRowsForSnapshotTest(t, ctx, store, workflowID, []NodeRecord{{ID: archiveID, WorkflowID: workflowID, Key: "archive", Kind: workflow.NodeKindTerminal, DisplayName: "Archive"}}, nil, nil)
	if _, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET target_node_id = ?
WHERE edge_key = 'done'
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups tg
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE tg.id = workflow_edges.transition_group_id
        AND source.workflow_id = ?
  )`, string(archiveID), string(workflowID)); err != nil {
		t.Fatalf("edit workflow edge target: %v", err)
	}

	approved, err := store.ApproveTransition(ctx, completed.TransitionID)
	if err != nil {
		t.Fatalf("ApproveTransition: %v", err)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 3 || placements[2].ID != approved.PlacementIDs[0] || placements[2].NodeID != originalDone.ID {
		t.Fatalf("placements after graph edit approval = %+v, want snapshotted original target %s", placements, originalDone.ID)
	}
}

func TestManualMoveToTerminalArchivesWithoutOutputValues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: done.ID})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 0 {
		t.Fatalf("manual move result = %+v, want applied terminal placement", moved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].ID == "" || transitions[1].TransitionID != "manual_done" || len(transitions[1].OutputValues) != 0 {
		t.Fatalf("manual move transition = %+v", transitions)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 3 || placements[1].State != "completed" || placements[2].NodeID != done.ID || placements[2].State != "active" {
		t.Fatalf("manual terminal placements = %+v", placements)
	}
}

func TestManualMoveFromTerminalToStartResetsTaskToBacklog(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: start.ID})
	if err != nil {
		t.Fatalf("ManualMoveTask reset: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 0 {
		t.Fatalf("reset move = %+v, want applied start placement without automation", moved)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 4 || placements[2].State != "completed" || placements[3].NodeID != start.ID || placements[3].State != "active" {
		t.Fatalf("reset placements = %+v, want active start placement after completed terminal", placements)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after reset = %+v, want no new automation", runs)
	}
	restarted, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask after reset: %v", err)
	}
	if restarted.RunID == "" || restarted.RunID == started.RunID {
		t.Fatalf("restart result = %+v, want second run", restarted)
	}
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after restart: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs after restart = %+v, want second automation run", runs)
	}
}

func TestManualMoveBackwardReusesStoredOutputValues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "reused"}})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: plan.ID})
	if err != nil {
		t.Fatalf("ManualMoveTask backward: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("backward move = %+v, want pending approval before executable target automation", moved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 3 || transitions[2].OutputValues["prior_summary"] != "reused" {
		t.Fatalf("backward transition outputs = %+v, want reused prior_summary", transitions)
	}
}

func TestManualMoveFromPendingApprovalToBacklogDiscardsApproval(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "carry"}})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	plan := nodeByKey(t, def, "plan")
	start := nodeByKind(t, def, workflow.NodeKindStart)

	// A backward move onto an agent node parks the task in pending approval
	// with no active placement: its source placement is already completed.
	approval, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: plan.ID})
	if err != nil {
		t.Fatalf("ManualMoveTask to approval: %v", err)
	}
	if approval.State != "pending_approval" {
		t.Fatalf("setup move state = %q, want pending_approval", approval.State)
	}

	// Moving the awaiting-approval task back to Backlog must succeed, discard
	// the pending approval, and land a single active placement at the start node.
	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: start.ID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("ManualMoveTask from approval to backlog: %v", err)
	}
	if moved.State != "applied" || len(moved.PlacementIDs) != 1 {
		t.Fatalf("approval-to-backlog move = %+v, want applied with one placement", moved)
	}
	if len(moved.ResolvedApprovalTransitionIDs) != 1 || moved.ResolvedApprovalTransitionIDs[0] != approval.TransitionID {
		t.Fatalf("resolved approval ids = %+v, want %s", moved.ResolvedApprovalTransitionIDs, approval.TransitionID)
	}

	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	rejected := 0
	for _, transition := range transitions {
		if transition.ID == approval.TransitionID {
			if transition.State != "rejected" {
				t.Fatalf("approval transition state = %q, want rejected", transition.State)
			}
			rejected++
		}
	}
	if rejected != 1 {
		t.Fatalf("expected the pending approval to be rejected exactly once, transitions = %+v", transitions)
	}

	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	active := 0
	for _, placement := range placements {
		if placement.State == "active" {
			active++
			if placement.NodeID != start.ID {
				t.Fatalf("active placement node = %q, want start node %q", placement.NodeID, start.ID)
			}
		}
	}
	if active != 1 {
		t.Fatalf("expected exactly one active placement at the start node, placements = %+v", placements)
	}
}

func TestManualMoveMissingEdgeOverrideDoesNotValidateLegacyTargetInputFields(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: impl.ID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("ManualMoveTask missing edge override: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("missing-edge move = %+v, want pending approval before agent automation", moved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || len(transitions[0].OutputValues) != 0 {
		t.Fatalf("missing-edge transition outputs = %+v, want no synthesized legacy inputs", transitions)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "completed" {
		t.Fatalf("placements after missing-edge move = %+v, want completed backlog while approval is pending", placements)
	}
}

func TestManualMoveMissingEdgeOverrideKeepsContractlessSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: impl.ID, AllowMissingEdge: true, OutputValues: map[string]string{"prior_summary": "replacement"}})
	if err != nil {
		t.Fatalf("ManualMoveTask missing edge override: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("missing-edge move = %+v, want pending approval before agent automation", moved)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].OutputValues["prior_summary"] != "replacement" {
		t.Fatalf("missing-edge transition outputs = %+v, want replacement prior_summary", transitions)
	}
	var inputBindingsJSON string
	var outputRequirementsJSON string
	if err := store.db.QueryRowContext(ctx, `
SELECT input_bindings_json, output_requirements_json
FROM task_transition_edges
WHERE task_transition_id = ?
LIMIT 1`, string(transitions[0].ID)).Scan(&inputBindingsJSON, &outputRequirementsJSON); err != nil {
		t.Fatalf("select transition edge snapshot: %v", err)
	}
	inputBindings := []workflow.InputBinding{}
	if err := workflow.UnmarshalString(inputBindingsJSON, &inputBindings); err != nil {
		t.Fatalf("unmarshal input bindings: %v", err)
	}
	outputRequirements := []workflow.OutputRequirement{}
	if err := workflow.UnmarshalString(outputRequirementsJSON, &outputRequirements); err != nil {
		t.Fatalf("unmarshal output requirements: %v", err)
	}
	if len(inputBindings) != 0 {
		t.Fatalf("input bindings = %+v, want no synthesized legacy target input bindings", inputBindings)
	}
	if len(outputRequirements) != 0 {
		t.Fatalf("output requirements = %+v, want no synthesized legacy output requirements", outputRequirements)
	}
}

func TestMissingEdgeManualMoveContractDoesNotUseLegacyTargetInputs(t *testing.T) {
	source := workflow.Node{ID: "node-source", Key: "source", Kind: workflow.NodeKindAgent}
	target := workflow.Node{
		ID:   "node-target",
		Key:  "target",
		Kind: workflow.NodeKindAgent,
		InputFields: []workflow.InputField{
			{Name: "prior", Description: "Prior."},
			{Name: "details", Description: "Details."},
		},
	}

	_, edge, ok := missingEdgeManualMoveContract(source, target)

	if !ok {
		t.Fatalf("missing edge manual move contract not returned")
	}
	if len(edge.InputBindings) != 0 || len(edge.OutputRequirements) != 0 {
		t.Fatalf("manual missing-edge contract = %+v, want no legacy target input bindings", edge)
	}
}

func TestManualMoveContinueSessionRequiresSourceSession(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: impl.ID, OutputValues: map[string]string{"prior_summary": "done"}})
	if !errors.Is(err, ErrManualMoveContinueSessionNeedsSource) {
		t.Fatalf("ManualMoveTask continue_session error = %v, want source session requirement", err)
	}
}

func TestManualMoveRejectsSelectedContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after plan: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	openPRNode := nodeByKey(t, def, "open_pr")
	var implementationRun RunRecord
	for _, run := range runs {
		if run.NodeID == implementationNode.ID {
			implementationRun = run
		}
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: openPRNode.ID, OutputValues: map[string]string{"acceptance_decision": "approved"}})
	if !errors.Is(err, ErrManualMoveSelectedContextSource) {
		t.Fatalf("ManualMoveTask selected context source error = %v, want unsupported selected context source", err)
	}
}

func TestBackwardManualMoveRejectsHistoricalSelectedContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: acceptanceNode.ID, OutputValues: map[string]string{"summary": "needs recheck"}})
	if !errors.Is(err, ErrManualMoveSelectedContextSource) {
		t.Fatalf("backward ManualMoveTask selected context source error = %v, want unsupported selected context source", err)
	}
}

func TestManualMoveRejectsPreviousTargetContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	addOutputFieldToNode(t, ctx, store, workflowID, acceptanceNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, acceptanceNode.ID, implementationNode.ID, false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: implementationNode.ID, OutputValues: map[string]string{"summary": "needs changes"}})
	if !errors.Is(err, ErrManualMovePreviousTargetContext) {
		t.Fatalf("ManualMoveTask previous target context source error = %v, want unsupported previous target context source", err)
	}
}

func TestBackwardManualMoveRejectsHistoricalPreviousTargetContextSourceV1(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createSelectedContextSourceWorkflow(t, ctx, store, workflow.ContextModeContinueSession)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationNode := nodeByKey(t, def, "implementation")
	acceptanceNode := nodeByKey(t, def, "acceptance")
	openPRNode := nodeByKey(t, def, "open_pr")
	addOutputFieldToNode(t, ctx, store, workflowID, openPRNode, workflow.OutputField{Name: "summary", Description: "Rework summary."})
	addPreviousTargetReworkEdge(t, ctx, store, workflowID, openPRNode.ID, implementationNode.ID, false)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "implement", OutputValues: map[string]string{"summary": "plan done"}})
	implementationRun := runForNode(t, ctx, store, task.ID, implementationNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: implementationRun.ID, TransitionID: "accept", OutputValues: map[string]string{"summary": "implemented"}})
	acceptanceRun := runForNode(t, ctx, store, task.ID, acceptanceNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: acceptanceRun.ID, TransitionID: "open_pr", OutputValues: map[string]string{"acceptance_decision": "approved"}})
	openPRRun := runForNode(t, ctx, store, task.ID, openPRNode.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: openPRRun.ID, TransitionID: "rework", OutputValues: map[string]string{"summary": "needs changes"}})

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: openPRNode.ID, OutputValues: map[string]string{"summary": "needs recheck"}})
	if !errors.Is(err, ErrManualMovePreviousTargetContext) {
		t.Fatalf("backward ManualMoveTask previous target context source error = %v, want unsupported previous target context source", err)
	}
}

func TestManualMovePendingApprovalRequiresSourceRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")

	_, err = store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: agent.ID})
	if !errors.Is(err, ErrManualMoveApprovalNeedsSourceRun) {
		t.Fatalf("ManualMoveTask missing source run error = %v, want source run requirement", err)
	}
	placements, err := store.ListPlacements(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements after rejected manual move = %+v, want original active placement", placements)
	}
}

func TestManualMoveExecutableTargetRequiresApprovalBeforeAutomation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	startTask(t, ctx, store, task.ID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	impl := nodeByKey(t, def, "implement")

	moved, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: impl.ID, OutputValues: map[string]string{"prior_summary": "done"}})
	if err != nil {
		t.Fatalf("ManualMoveTask executable: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 {
		t.Fatalf("manual executable move = %+v, want pending approval without automation", moved)
	}
}

func TestManualMoveRejectsActiveParallelBatch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	join := nodeByKey(t, def, "join")
	if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{TaskID: task.ID, TargetNodeID: join.ID, OutputValues: map[string]string{"summary": "manual"}}); !errors.Is(err, ErrManualMoveDuringParallelBatch) {
		t.Fatalf("ManualMoveTask active parallel error = %v, want active parallel rejection", err)
	}
	if len(branchRuns) != 2 {
		t.Fatalf("branch runs = %+v, want two active branches", branchRuns)
	}
}

func TestStartTaskAllowsCrossRoleContinueSessionContextMode(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeContinueSession, "reviewer")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)

	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask invalid workflow backlog: %v", err)
	}
	if _, err := store.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
}

func mutateSnapshotTransition(t *testing.T, snapshot *runStartSnapshot, transitionID string, mutate func(*transitionContractSnapshot)) {
	t.Helper()
	for index := range snapshot.TransitionGroups {
		if snapshot.TransitionGroups[index].TransitionID == transitionID {
			mutate(&snapshot.TransitionGroups[index])
			return
		}
	}
	t.Fatalf("snapshot transition %q missing from %+v", transitionID, snapshot.TransitionGroups)
}

func mutateRunStartSnapshot(t *testing.T, ctx context.Context, store *Store, runID workflow.RunID, mutate func(*testing.T, *runStartSnapshot)) {
	t.Helper()
	row, err := store.queries.GetTaskRun(ctx, string(runID))
	if err != nil {
		t.Fatalf("GetTaskRun: %v", err)
	}
	snapshot := runStartSnapshot{}
	if err := workflow.UnmarshalString(row.RunStartSnapshotJson, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	mutate(t, &snapshot)
	updateRunStartSnapshot(t, ctx, store, runID, snapshot)
}

func nodeSnapshotByID(t *testing.T, snapshot runStartSnapshot, nodeID workflow.NodeID) nodeContractSnapshot {
	t.Helper()
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	t.Fatalf("snapshot node %q missing from %+v", nodeID, snapshot.Nodes)
	return nodeContractSnapshot{}
}

func updateRunStartSnapshot(t *testing.T, ctx context.Context, store *Store, runID workflow.RunID, snapshot runStartSnapshot) {
	t.Helper()
	snapshotJSON, err := workflow.MarshalString(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_runs SET run_start_snapshot_json = ? WHERE id = ?`, snapshotJSON, string(runID)); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}
}

func TestCompleteRunValidatesOutputRequirements(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "  "}}); !completionHasCode(err, CompletionCodeRequiredOutputMissing) {
		t.Fatalf("expected missing required output error, got %v", err)
	}
	transitions, err := store.ListTransitions(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTransitions after rejected completion: %v", err)
	}
	if len(transitions) != 1 || transitions[0].TransitionID != "start" {
		t.Fatalf("rejected completion left partial transition rows: %+v", transitions)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns after rejected completion: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt != 0 || runs[0].InterruptedAt != 0 {
		t.Fatalf("rejected completion mutated run outcome: %+v", runs)
	}
}

func TestCompleteRunInfersSingleTransitionID(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID})
}

func TestCompleteRunRejectsMissingTransitionIDWhenAmbiguous(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-blocked-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: agent.ID, TransitionID: "blocked", DisplayName: "Blocked"}); err != nil {
		t.Fatalf("AddTransitionGroup blocked: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-blocked-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-blocked-" + string(workflowID)), Key: "blocked", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge blocked: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID}); !completionHasCode(err, CompletionCodeTransitionIDRequired) {
		t.Fatalf("expected missing transition id error, got %v", err)
	}
}

func TestCompleteRunRejectsUnknownOutputField(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", OutputValues: map[string]string{"extra": "nope"}}); !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("expected unknown output error, got %v", err)
	}
}

func TestCompleteRunRejectsParameterDeclaredOnlyByAnotherTransition(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	blockedGroup := workflow.TransitionGroupID("group-blocked-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: blockedGroup, WorkflowID: workflowID, SourceNodeID: agent.ID, TransitionID: "blocked", DisplayName: "Blocked"}); err != nil {
		t.Fatalf("AddTransitionGroup blocked: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-blocked-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: blockedGroup,
		Key:               "blocked",
		TargetNodeID:      done.ID,
		ContextMode:       workflow.ContextModeNewSession,
		Parameters:        []workflow.Parameter{{Key: "blocked_reason", Description: "Why the task is blocked."}},
	}); err != nil {
		t.Fatalf("AddEdge blocked: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", OutputValues: map[string]string{"blocked_reason": "blocked"}}); !completionHasCode(err, CompletionCodeUnknownOutputField) {
		t.Fatalf("expected selected-transition unknown output error, got %v", err)
	}
}

func TestCompleteRunReturnsStructuredValidationIssues(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"extra": "nope"}})
	var validation CompletionValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want CompletionValidationError", err, err)
	}
	if !validation.HasCode(CompletionCodeUnknownOutputField) || !validation.HasCode(CompletionCodeRequiredOutputMissing) {
		t.Fatalf("validation issues = %+v, want unknown_output_field and required_output_missing", validation.Issues)
	}
}

func TestCompleteRunRejectsOversizedCompletionFields(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	_, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", OutputValues: map[string]string{"prior_summary": strings.Repeat("a", workflow.MaxOutputValueBytes+1)}})
	if !completionHasCode(err, CompletionCodeOutputTooLarge) {
		t.Fatalf("expected oversized output error, got %v", err)
	}
	_, err = store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "next", Commentary: strings.Repeat("a", workflow.MaxCommentaryBytes+1), OutputValues: map[string]string{"prior_summary": "done"}})
	if !completionHasCode(err, CompletionCodeCommentaryTooLarge) {
		t.Fatalf("expected oversized commentary error, got %v", err)
	}
}

func TestRecordProtocolViolationInterruptsAtCap(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	first, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{RunID: started.RunID, Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"first"}`})
	if err != nil {
		t.Fatalf("RecordProtocolViolation first: %v", err)
	}
	if first.Count != 1 || first.Interrupted {
		t.Fatalf("first violation = %+v, want count 1 active", first)
	}
	second, err := store.RecordProtocolViolation(ctx, RecordProtocolViolationRequest{RunID: started.RunID, Kind: ProtocolViolationInvalidCompletion, MaxCount: 2, Detail: `{"detail":"second"}`})
	if err != nil {
		t.Fatalf("RecordProtocolViolation second: %v", err)
	}
	if second.Count != 2 || !second.Interrupted {
		t.Fatalf("second violation = %+v, want count 2 interrupted", second)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InvalidCompletions != 2 || runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != "workflow_protocol_violation_limit" {
		t.Fatalf("run after cap = %+v", runs)
	}
}

func TestSetRunEffectiveCompletionModePersistsAndRefusesDrift(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if claimed.EffectiveCompletionMode != "" {
		t.Fatalf("claimed effective mode = %q, want empty before resolution", claimed.EffectiveCompletionMode)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "invalid"); !errors.Is(err, ErrInvalidEffectiveCompletionMode) {
		t.Fatalf("SetRunEffectiveCompletionMode invalid error = %v, want ErrInvalidEffectiveCompletionMode", err)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "shell_command"); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode: %v", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].EffectiveCompletionMode != "shell_command" {
		t.Fatalf("run effective mode = %+v, want shell_command", runs)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "shell_command"); err != nil {
		t.Fatalf("SetRunEffectiveCompletionMode same mode: %v", err)
	}
	if err := store.SetRunEffectiveCompletionMode(ctx, started.RunID, claimed.Generation, "tool"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetRunEffectiveCompletionMode drift error = %v, want sql.ErrNoRows", err)
	}
}

func TestWorkflowNodeCompletionModePersistsThroughGraphAndRunSnapshot(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: agent.ID, WorkflowID: workflowID, Key: agent.Key, Kind: agent.Kind, DisplayName: agent.DisplayName, SubagentRole: agent.SubagentRole, PromptTemplate: agent.PromptTemplate, CompletionMode: "tool", OutputFields: agent.OutputFields}); err != nil {
		t.Fatalf("UpdateNode completion mode: %v", err)
	}
	updated, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	updatedAgent := nodeByKey(t, updated, "agent")
	if updatedAgent.CompletionMode != "tool" || updatedRecord.Version != record.Version+1 {
		t.Fatalf("updated agent mode=%q version=%d, want tool version %d", updatedAgent.CompletionMode, updatedRecord.Version, record.Version+1)
	}
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: "node-terminal-invalid", WorkflowID: workflowID, Key: "invalid", Kind: workflow.NodeKindTerminal, DisplayName: "Invalid", CompletionMode: "tool"}); err == nil {
		t.Fatal("UpdateNode accepted completion mode override on terminal node")
	}
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	input, err := store.GetRunStartContext(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if input.Node.CompletionMode != "tool" {
		t.Fatalf("run start node completion mode = %q, want tool", input.Node.CompletionMode)
	}
}

func TestCompleteRunRejectsStaleGeneration(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if _, err := store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", ExpectedGeneration: 0, RequireGeneration: true}); !errors.Is(err, ErrStaleRunGeneration) {
		t.Fatalf("expected stale generation error, got %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done", ExpectedGeneration: claimed.Generation, RequireGeneration: true})
}

func TestRunStartContextHandlesMissingInputEdgeAndRejectsNonArrayInputJSON(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM task_transition_edges WHERE target_placement_id = ?`, string(started.PlacementID)); err != nil {
		t.Fatalf("delete transition edge snapshot: %v", err)
	}
	input, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext without input edge: %v", err)
	}
	if len(input.InputValues) != 0 {
		t.Fatalf("input values without input edge = %+v, want empty", input.InputValues)
	}
	taskWithInvalidInputs, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task 2", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask malformed inputs: %v", err)
	}
	startedInvalidInputs, err := store.StartTask(ctx, taskWithInvalidInputs.ID)
	if err != nil {
		t.Fatalf("StartTask malformed inputs: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transition_edges SET input_bindings_json = '{}' WHERE target_placement_id = ?`, string(startedInvalidInputs.PlacementID)); err == nil {
		t.Fatalf("expected non-array transition edge input bindings to be rejected")
	}
	taskWithJoinInputs, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task 3", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask join inputs: %v", err)
	}
	startedJoinInputs, err := store.StartTask(ctx, taskWithJoinInputs.ID)
	if err != nil {
		t.Fatalf("StartTask join inputs: %v", err)
	}
	joinInputsJSON, err := workflow.MarshalString([]workflow.InputBinding{{Name: "joined", Source: workflow.BindingSourceJoin, Field: "aggregate"}})
	if err != nil {
		t.Fatalf("marshal join inputs: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_transition_edges SET input_bindings_json = ? WHERE target_placement_id = ?`, joinInputsJSON, string(startedJoinInputs.PlacementID)); err != nil {
		t.Fatalf("set join transition edge inputs: %v", err)
	}
	joinInput, err := store.GetRunStartContext(ctx, startedJoinInputs.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext join inputs: %v", err)
	}
	if joinInput.InputValues["joined"] != "" {
		t.Fatalf("join input without aggregate = %+v, want empty value", joinInput.InputValues)
	}
}

func TestAttachRunSessionGenerationGuard(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation-1, "session-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale AttachRunSession error = %v, want sql.ErrNoRows", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession current generation: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, "session-second"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second AttachRunSession error = %v, want sql.ErrNoRows", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].SessionID != sessionID {
		t.Fatalf("attached session = %q, want %q", runs[0].SessionID, sessionID)
	}
}

func TestSetAndClearRunWaitingAskGenerationGuard(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}

	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation-1, "ask-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale SetRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk current generation: %v", err)
	}
	waiting, err := store.ListWaitingAskRuns(ctx)
	if err != nil {
		t.Fatalf("ListWaitingAskRuns: %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != started.RunID || waiting[0].WaitingAskID != "ask-1" || waiting[0].SessionID != sessionID {
		t.Fatalf("waiting runs = %+v", waiting)
	}
	if _, err := store.ClaimRun(ctx, started.RunID, claimed.Generation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ClaimRun while waiting error = %v, want sql.ErrNoRows", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation-1, "ask-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale ClearRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-other"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong ask ClearRunWaitingAsk error = %v, want sql.ErrNoRows", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("ClearRunWaitingAsk current ask: %v", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if runs[0].WaitingAskID != "" || runs[0].CompletedAt != 0 || runs[0].InterruptedAt != 0 {
		t.Fatalf("run after clear = %+v", runs[0])
	}
}

func TestSetAndClearRunWaitingAskPublishTaskEvents(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	store.now = func() time.Time { return time.UnixMilli(1234).UTC() }
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	sink := &recordingWorkflowEventPublisher{}
	store.SetWorkflowEventPublisher(sink)

	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if err := store.ClearRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("ClearRunWaitingAsk: %v", err)
	}

	if len(sink.records) != 2 {
		t.Fatalf("published records = %+v, want waiting and cleared events", sink.records)
	}
	for _, record := range sink.records {
		if record.ProjectID != binding.ProjectID || record.WorkflowID != string(task.WorkflowID) || record.Resource != "task" {
			t.Fatalf("published record identity = %+v", record)
		}
		if record.OccurredAtUnixMs != 1234 {
			t.Fatalf("published record time = %d, want 1234", record.OccurredAtUnixMs)
		}
		if len(record.ChangedIDs) != 3 || record.ChangedIDs[0] != string(task.ID) || record.ChangedIDs[1] != string(started.RunID) || record.ChangedIDs[2] != "ask-1" {
			t.Fatalf("published record changed ids = %+v", record.ChangedIDs)
		}
	}
	if sink.records[0].Action != "question_waiting" || sink.records[1].Action != "question_cleared" {
		t.Fatalf("published actions = %+v, want question_waiting then question_cleared", []string{sink.records[0].Action, sink.records[1].Action})
	}
}

func TestInterruptRunGenerationGuard(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation-1, "stale", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale InterruptRunGeneration error = %v, want sql.ErrNoRows", err)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns stale: %v", err)
	}
	if runs[0].InterruptedAt != 0 {
		t.Fatalf("stale generation interrupted run: %+v", runs[0])
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "current", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration current generation: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "second", "{}"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second InterruptRunGeneration error = %v, want sql.ErrNoRows", err)
	}
	runs, err = store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns current: %v", err)
	}
	if runs[0].InterruptedAt == 0 || runs[0].InterruptionReason != "current" {
		t.Fatalf("run after interrupt = %+v, want current interruption", runs[0])
	}
}

func TestResumeTaskRunRequeuesInterruptedRunWithSameSession(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", `{"reason":"test"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != started.RunID || resumed.SessionID != sessionID || resumed.StartedAt != 0 || resumed.InterruptedAt != 0 || resumed.Generation <= claimed.Generation {
		t.Fatalf("resumed run = %+v, want same run/session requeued with newer generation", resumed)
	}
	runnable, err := store.ListRunnableRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRunnableRuns: %v", err)
	}
	if len(runnable) != 1 || runnable[0].ID != started.RunID || runnable[0].SessionID != sessionID {
		t.Fatalf("runnable after resume = %+v, want same run/session", runnable)
	}
	reclaimed, err := store.ClaimRun(ctx, started.RunID, resumed.Generation)
	if err != nil {
		t.Fatalf("ClaimRun after resume: %v", err)
	}
	if err := store.AttachRunSession(ctx, started.RunID, reclaimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession same session after resume: %v", err)
	}
}

func TestResumeTaskRunRejectsRoleDrift(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	store.roleResolver = workflow.StaticRoleResolver{}

	var roleErr WorkflowValidationError
	if _, err := store.ResumeTaskRuns(ctx, task.ID); !errors.As(err, &roleErr) || !roleErr.HasCode(workflow.CodeAgentRoleMissing) {
		t.Fatalf("ResumeTaskRuns role drift error = %v, want %s", err, workflow.CodeAgentRoleMissing)
	}
}

func TestResumeTaskRunAllowsDefaultAgentRoleWithoutResolver(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKey(t, def, "agent")
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: agent.ID, WorkflowID: workflowID, Key: agent.Key, Kind: agent.Kind, DisplayName: agent.DisplayName, SubagentRole: workflow.DefaultAgentRole, PromptTemplate: agent.PromptTemplate, OutputFields: agent.OutputFields}); err != nil {
		t.Fatalf("UpdateNode default role: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	store.roleResolver = workflow.StaticRoleResolver{}

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns default role: %v", err)
	}
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != started.RunID || resumed.InterruptedAt != 0 || resumed.StartedAt != 0 {
		t.Fatalf("resumed run = %+v, want default-role run requeued", resumed)
	}
}

func TestResumeTaskRunCanResumeInterruptedWaitingAskRun(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	claimed, err := store.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	sessionID := createTestSession(t, ctx, store, binding, cfg)
	if err := store.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-missing"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "workflow_pending_ask_unavailable", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}

	resumedRuns, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumedRuns) != 1 {
		t.Fatalf("resumed runs = %+v, want one", resumedRuns)
	}
	resumed := resumedRuns[0]
	if resumed.ID != started.RunID || resumed.WaitingAskID != "" || resumed.InterruptedAt != 0 || resumed.StartedAt != 0 {
		t.Fatalf("resumed waiting ask run = %+v, want requeued same run without waiting ask", resumed)
	}
}

func TestInterruptTargetsBySessionAndResumeRequeuesAllRuns(t *testing.T) {
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task, branchRuns := startFanoutTask(t, ctx, store, binding.ProjectID, workflowID)
	runIDs := make([]workflow.RunID, 0, len(branchRuns))
	for _, runID := range branchRuns {
		runIDs = append(runIDs, runID)
	}
	if len(runIDs) != 2 {
		t.Fatalf("branch runs = %+v, want two", branchRuns)
	}
	sessions := make(map[workflow.RunID]string, len(runIDs))
	for _, runID := range runIDs {
		claimed, err := store.ClaimRun(ctx, runID, 0)
		if err != nil {
			t.Fatalf("ClaimRun %s: %v", runID, err)
		}
		sessionID := createTestSession(t, ctx, store, binding, cfg)
		if err := store.AttachRunSession(ctx, runID, claimed.Generation, sessionID); err != nil {
			t.Fatalf("AttachRunSession %s: %v", runID, err)
		}
		sessions[runID] = sessionID
	}
	interrupted, err := store.InterruptTaskRuns(ctx, task.ID, sessions[runIDs[0]], "manual")
	if err != nil {
		t.Fatalf("InterruptTaskRuns by session: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != runIDs[0] || interrupted[0].InterruptedAt == 0 {
		t.Fatalf("interrupted = %+v, want only %s", interrupted, runIDs[0])
	}
	interruptedRest, err := store.InterruptTaskRuns(ctx, task.ID, "", "manual")
	if err != nil {
		t.Fatalf("InterruptTaskRuns all: %v", err)
	}
	if len(interruptedRest) != 1 || interruptedRest[0].ID != runIDs[1] {
		t.Fatalf("interrupted rest = %+v, want only %s", interruptedRest, runIDs[1])
	}
	resumed, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumed) != 2 {
		t.Fatalf("resumed = %+v, want both runs", resumed)
	}
	for _, run := range resumed {
		if run.InterruptedAt != 0 || run.StartedAt != 0 {
			t.Fatalf("resumed run = %+v, want reset", run)
		}
	}
}

func TestTaskStartRejectsCurrentInvalidWorkflow(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-terminal-invalid", WorkflowID: workflowID, SourceNodeID: done.ID, TransitionID: "invalid", DisplayName: "Invalid"}); err != nil {
		t.Fatalf("AddTransitionGroup invalid terminal group: %v", err)
	}
	var terminalErr WorkflowValidationError
	if _, err := store.StartTask(ctx, task.ID); !errors.As(err, &terminalErr) || !terminalErr.HasCode(workflow.CodeTerminalHasOutgoingEdge) {
		t.Fatalf("expected current workflow validation error, got %v", err)
	}
}

func TestTaskCreateAllowsInvalidWorkflowBacklogButRejectsUnlinkedWorkflow(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	invalid, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Invalid"})
	if err != nil {
		t.Fatalf("CreateWorkflow invalid: %v", err)
	}
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, invalid.ID, true); err != nil {
		t.Fatalf("LinkWorkflow invalid: %v", err)
	}
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask invalid default workflow backlog: %v", err)
	}
	if _, err := store.StartTask(ctx, task.ID); !errors.Is(err, ErrWorkflowValidationFailed) {
		t.Fatalf("expected invalid workflow start error, got %v", err)
	}
	updatedTitle := "Updated"
	updatedBody := "Updated body"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{TaskID: task.ID, Title: &updatedTitle, Body: &updatedBody, SourceWorkspaceID: binding.WorkspaceID}); err != nil {
		t.Fatalf("UpdateTask invalid workflow backlog: %v", err)
	}
	if _, err := store.AddComment(ctx, task.ID, "Comment", "user", "operator"); err != nil {
		t.Fatalf("AddComment invalid workflow backlog: %v", err)
	}
	valid := createValidWorkflow(t, ctx, store)
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, valid, false); err != nil {
		t.Fatalf("LinkWorkflow valid explicit: %v", err)
	}
	if task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: valid, Title: "Explicit", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask explicit valid workflow: %v", err)
	} else if !strings.HasPrefix(task.ShortID, "WOR-2") {
		t.Fatalf("explicit task short id = %q, want WOR-2", task.ShortID)
	}
	unlinked, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Unlinked"})
	if err != nil {
		t.Fatalf("CreateWorkflow unlinked: %v", err)
	}
	if _, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: unlinked.ID, Title: "Task", Body: "Body"}); err == nil {
		t.Fatalf("expected unlinked workflow task creation to fail")
	}
}

func TestProjectWorkflowUnlinkHardDeletesUnusedLinksAndBlocksTaskReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	link, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true)
	if err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	otherWorkflowID := createValidWorkflow(t, ctx, store)
	otherLink, err := store.LinkWorkflow(ctx, binding.ProjectID, otherWorkflowID, false)
	if err != nil {
		t.Fatalf("LinkWorkflow other: %v", err)
	}
	spareWorkflowID := createValidWorkflow(t, ctx, store)
	spareLink, err := store.LinkWorkflow(ctx, binding.ProjectID, spareWorkflowID, false)
	if err != nil {
		t.Fatalf("LinkWorkflow spare: %v", err)
	}
	if _, err := store.UnlinkProjectWorkflow(ctx, link.ID, "missing-link"); !errors.Is(err, ErrReplacementDefaultInvalid) {
		t.Fatalf("expected invalid replacement default guard, got %v", err)
	}
	if _, err := store.UnlinkProjectWorkflow(ctx, link.ID, link.ID); !errors.Is(err, ErrReplacementDefaultInvalid) {
		t.Fatalf("expected self replacement default guard, got %v", err)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after invalid replacement: %v", err)
	}
	if len(links) != 3 || !links[0].IsDefault {
		t.Fatalf("links after invalid replacement = %+v, want original default preserved", links)
	}
	blockedDefault, err := store.UnlinkProjectWorkflow(ctx, link.ID, "")
	if err != nil {
		t.Fatalf("unlink default without replacement should return typed blocker, got error: %v", err)
	}
	if blockedDefault.Unlinked || !hasProjectWorkflowUnlinkBlocker(blockedDefault.Blockers, "default_replacement_required", 2) {
		t.Fatalf("blocked default unlink = %+v, want replacement-required blocker", blockedDefault)
	}
	links, err = store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after missing replacement: %v", err)
	}
	if len(links) != 3 || !links[0].IsDefault {
		t.Fatalf("links after missing replacement = %+v, want original default preserved", links)
	}
	if result, err := store.UnlinkProjectWorkflow(ctx, spareLink.ID, ""); err != nil || !result.Unlinked {
		t.Fatalf("unlink unused non-default link should physically delete: %v", err)
	}
	if result, err := store.UnlinkProjectWorkflow(ctx, link.ID, otherLink.ID); err != nil || !result.Unlinked {
		t.Fatalf("unlink default with valid replacement: %v", err)
	}
	links, err = store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after replacement: %v", err)
	}
	if len(links) != 1 || links[0].ID != otherLink.ID || !links[0].IsDefault {
		t.Fatalf("links after valid replacement = %+v, want replacement default", links)
	}
	link = otherLink
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	blocked, err := store.UnlinkProjectWorkflow(ctx, link.ID, "")
	if err != nil {
		t.Fatalf("task reference unlink guard should return typed blockers, got error: %v", err)
	}
	if blocked.Unlinked || !hasProjectWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want task reference blocker", blocked)
	}
	startTask(t, ctx, store, task.ID)
}

func TestProjectWorkflowUnlinkBlocksTerminalTaskHistory(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	link, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true)
	if err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	blocked, err := store.UnlinkProjectWorkflow(ctx, link.ID, "")
	if err != nil {
		t.Fatalf("terminal task history unlink guard should return typed blockers, got error: %v", err)
	}
	if blocked.Unlinked || !hasProjectWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want terminal task history blocker", blocked)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks: %v", err)
	}
	if len(links) != 1 || links[0].ID != link.ID || !links[0].IsDefault {
		t.Fatalf("links after blocked unlink = %+v", links)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); err != nil {
		t.Fatalf("task history should remain readable after soft unlink: %v", err)
	}
}

func hasProjectWorkflowUnlinkBlocker(blockers []ProjectWorkflowUnlinkBlocker, code string, count int) bool {
	for _, blocker := range blockers {
		if blocker.Code == code && blocker.Count == count {
			return true
		}
	}
	return false
}

func TestWorkflowDeletePreviewAndConfirmedApplyDeleteDatabaseRows(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	impact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	if impact.WorkflowID != workflowID || impact.Version != 6 || impact.ProjectCount != 1 || impact.LinkCount != 1 || impact.TaskCount != 1 || impact.ActiveRunCount != 0 || impact.RunnableRunCount != 0 || impact.BlockedTaskCount != 0 {
		t.Fatalf("delete impact = %+v, want one linked project/link/task and no run blockers", impact)
	}

	unconfirmed, err := store.DeleteWorkflow(ctx, WorkflowDeleteRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("DeleteWorkflow unconfirmed: %v", err)
	}
	if unconfirmed.Deleted || !hasWorkflowDeleteBlocker(unconfirmed.Blockers, "confirmation_required", 1) {
		t.Fatalf("unconfirmed delete result = %+v, want confirmation blocker", unconfirmed)
	}

	cleanup, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(impact, true))
	if err != nil {
		t.Fatalf("DeleteWorkflow cleanup: %v", err)
	}
	if cleanup.Deleted || !hasWorkflowDeleteBlocker(cleanup.Blockers, "artifact_cleanup_unsupported", 1) {
		t.Fatalf("cleanup delete result = %+v, want unsupported cleanup blocker", cleanup)
	}

	deleted, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(impact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow confirmed: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("confirmed delete result = %+v, want deletion without blockers", deleted)
	}
	if _, err := store.queries.GetTask(ctx, string(task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTask after workflow delete = %v, want sql.ErrNoRows", err)
	}
	if _, err := store.queries.GetWorkflow(ctx, string(workflowID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetWorkflow after workflow delete = %v, want sql.ErrNoRows", err)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after workflow delete: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links after workflow delete = %+v, want none", links)
	}
	var nodeCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_nodes WHERE workflow_id = ?`, string(workflowID)).Scan(&nodeCount); err != nil {
		t.Fatalf("count workflow nodes after delete: %v", err)
	}
	if nodeCount != 0 {
		t.Fatalf("workflow node count after delete = %d, want 0", nodeCount)
	}
}

func TestWorkflowDeleteBlocksRunnableAndActiveRuns(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)

	runnableImpact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete runnable: %v", err)
	}
	if runnableImpact.RunnableRunCount != 1 || runnableImpact.ActiveRunCount != 0 || runnableImpact.BlockedTaskCount != 1 {
		t.Fatalf("runnable impact = %+v, want one runnable blocked task", runnableImpact)
	}
	runnableDelete, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(runnableImpact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow runnable: %v", err)
	}
	if runnableDelete.Deleted || !hasWorkflowDeleteBlocker(runnableDelete.Blockers, "runnable_runs", 1) {
		t.Fatalf("runnable delete result = %+v, want runnable_runs blocker", runnableDelete)
	}

	if _, err := store.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	activeImpact, err := store.PreviewWorkflowDelete(ctx, workflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete active: %v", err)
	}
	if activeImpact.ActiveRunCount != 1 || activeImpact.RunnableRunCount != 0 || activeImpact.BlockedTaskCount != 1 {
		t.Fatalf("active impact = %+v, want one active blocked task", activeImpact)
	}
	activeDelete, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(activeImpact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow active: %v", err)
	}
	if activeDelete.Deleted || !hasWorkflowDeleteBlocker(activeDelete.Blockers, "active_runs", 1) {
		t.Fatalf("active delete result = %+v, want active_runs blocker", activeDelete)
	}
}

func TestWorkflowDeleteBlocksDefaultReplacementAndDetectsImpactChanges(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	defaultWorkflowID := createValidWorkflow(t, ctx, store)
	defaultLink, err := store.LinkWorkflow(ctx, binding.ProjectID, defaultWorkflowID, true)
	if err != nil {
		t.Fatalf("LinkWorkflow default: %v", err)
	}
	replacementWorkflowID := createValidWorkflow(t, ctx, store)
	replacementLink, err := store.LinkWorkflow(ctx, binding.ProjectID, replacementWorkflowID, false)
	if err != nil {
		t.Fatalf("LinkWorkflow replacement: %v", err)
	}

	defaultImpact, err := store.PreviewWorkflowDelete(ctx, defaultWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete default: %v", err)
	}
	if defaultImpact.DefaultReplacementProjectCount != 1 {
		t.Fatalf("default impact = %+v, want one project requiring replacement default", defaultImpact)
	}
	blockedDefault, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(defaultImpact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow default: %v", err)
	}
	if blockedDefault.Deleted || !hasWorkflowDeleteBlocker(blockedDefault.Blockers, "default_replacement_required", 1) {
		t.Fatalf("default delete result = %+v, want default replacement blocker", blockedDefault)
	}
	links, err := store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after default blocker: %v", err)
	}
	if len(links) != 2 || links[0].ID != defaultLink.ID || !links[0].IsDefault {
		t.Fatalf("links after default blocker = %+v, want original default preserved", links)
	}

	if _, err := store.SetDefaultProjectWorkflowLink(ctx, binding.ProjectID, replacementWorkflowID); err != nil {
		t.Fatalf("SetDefaultProjectWorkflowLink: %v", err)
	}
	deleteableImpact, err := store.PreviewWorkflowDelete(ctx, defaultWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete after replacement: %v", err)
	}
	if deleteableImpact.DefaultReplacementProjectCount != 0 {
		t.Fatalf("deleteable impact = %+v, want no replacement blocker", deleteableImpact)
	}
	deleted, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(deleteableImpact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow after replacement: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("delete after replacement = %+v, want deletion", deleted)
	}
	links, err = store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks after delete: %v", err)
	}
	if len(links) != 1 || links[0].ID != replacementLink.ID || !links[0].IsDefault {
		t.Fatalf("links after deleting old default = %+v, want replacement default preserved", links)
	}

	staleWorkflowID := createValidWorkflow(t, ctx, store)
	if _, err := store.LinkWorkflow(ctx, binding.ProjectID, staleWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow stale: %v", err)
	}
	staleImpact, err := store.PreviewWorkflowDelete(ctx, staleWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete stale: %v", err)
	}
	if _, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: staleWorkflowID, Title: "Stale", Body: "Body"}); err != nil {
		t.Fatalf("CreateTask stale: %v", err)
	}
	staleDelete, err := store.DeleteWorkflow(ctx, confirmedWorkflowDeleteRequest(staleImpact, false))
	if err != nil {
		t.Fatalf("DeleteWorkflow stale: %v", err)
	}
	if staleDelete.Deleted || !hasWorkflowDeleteBlocker(staleDelete.Blockers, "impact_changed", 1) || staleDelete.Impact.TaskCount != 1 {
		t.Fatalf("stale delete result = %+v, want impact_changed with refreshed task count", staleDelete)
	}
}

func confirmedWorkflowDeleteRequest(impact WorkflowDeleteImpact, cleanupArtifacts bool) WorkflowDeleteRequest {
	return WorkflowDeleteRequest{
		WorkflowID:           impact.WorkflowID,
		Confirmed:            true,
		ExpectedVersion:      impact.Version,
		ExpectedProjectCount: impact.ProjectCount,
		ExpectedLinkCount:    impact.LinkCount,
		ExpectedTaskCount:    impact.TaskCount,
		CleanupArtifacts:     cleanupArtifacts,
	}
}

func hasWorkflowDeleteBlocker(blockers []WorkflowDeleteBlocker, code string, count int64) bool {
	for _, blocker := range blockers {
		if blocker.Code == code && blocker.Count == count {
			return true
		}
	}
	return false
}

func TestWorkflowGraphSaveAppliesExpectedRevisionAndRemovalConfirmation(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	unconfirmed := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	unconfirmed.Edges = removeWorkflowGraphSaveEdge(unconfirmed.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	unconfirmed.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(unconfirmed.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	blocked, err := store.SaveWorkflowGraph(ctx, unconfirmed)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph unconfirmed: %v", err)
	}
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "confirmation_required") != 2 {
		t.Fatalf("unconfirmed graph save = %+v, want confirmation blocker for one removed edge and transition group", blocked)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after unconfirmed save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after unconfirmed save = %d, want %d", unchanged.Version, record.Version)
	}

	confirmed := unconfirmed
	confirmed.Confirmed = true
	confirmed = confirmWorkflowGraphSaveRequest(confirmed, blocked.Impact)
	saved, err := store.SaveWorkflowGraph(ctx, confirmed)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph confirmed: %v", err)
	}
	if !saved.Saved || len(saved.Blockers) != 0 || saved.Version != record.Version+1 {
		t.Fatalf("confirmed graph save = %+v, want single revision bump", saved)
	}
	updatedDef, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after confirmed save: %v", err)
	}
	if updatedRecord.Version != record.Version+1 || len(updatedDef.Edges) != len(def.Edges)-1 {
		t.Fatalf("updated graph = revision %d edges %d, want revision %d edges %d", updatedRecord.Version, len(updatedDef.Edges), record.Version+1, len(def.Edges)-1)
	}

	stale := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, updatedDef)
	staleResult, err := store.SaveWorkflowGraph(ctx, stale)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale no-op: %v", err)
	}
	if !staleResult.Saved || len(staleResult.Blockers) != 0 || staleResult.Version != updatedRecord.Version {
		t.Fatalf("stale no-op save = %+v, want successful no-op without workflow version check", staleResult)
	}
}

func TestWorkflowGraphSaveSupportsMetadataAndNoopRevisions(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	metadataOnly := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	metadataOnly.Metadata = &WorkflowGraphSaveMetadata{Name: "Renamed workflow", Description: "Updated description"}
	metadataSaved, err := store.SaveWorkflowGraph(ctx, metadataOnly)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph metadata-only: %v", err)
	}
	if !metadataSaved.Saved || metadataSaved.Version != record.Version+1 {
		t.Fatalf("metadata-only save = %+v, want version bump", metadataSaved)
	}
	_, afterMetadata, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after metadata-only: %v", err)
	}
	if afterMetadata.Name != "Renamed workflow" || afterMetadata.Description != "Updated description" || afterMetadata.Version != record.Version+1 {
		t.Fatalf("record after metadata-only = %+v, want metadata persisted with version bump", afterMetadata)
	}

	noop := workflowGraphSaveRequestFromDefinition(workflowID, afterMetadata.Version, false, def)
	noop.Metadata = &WorkflowGraphSaveMetadata{Name: afterMetadata.Name, Description: afterMetadata.Description}
	noopSaved, err := store.SaveWorkflowGraph(ctx, noop)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph noop: %v", err)
	}
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != afterMetadata.Version {
		t.Fatalf("noop save = %+v, want no revision bump", noopSaved)
	}
}

func TestWorkflowGraphSaveRoundTripsTransitionInvocationContract(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	for i := range req.Edges {
		switch req.Edges[i].Key {
		case "start":
			req.Edges[i].PromptTemplate = "Start from {{.TaskTitle}}."
		case "done":
			req.Edges[i].Parameters = []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history."}}
		}
	}
	saved, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || !saved.Changed || saved.Version != record.Version+1 {
		t.Fatalf("save result = %+v, want graph change with version bump", saved)
	}

	updated, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	startEdge := edgeByKey(t, updated, "start")
	if startEdge.PromptTemplate != "Start from {{.TaskTitle}}." {
		t.Fatalf("start edge prompt = %q, want transition prompt round-tripped", startEdge.PromptTemplate)
	}
	doneEdge := edgeByKey(t, updated, "done")
	if len(doneEdge.Parameters) != 1 || doneEdge.Parameters[0].Key != "summary" || doneEdge.Parameters[0].Description != "Summary for terminal history." {
		t.Fatalf("done edge parameters = %+v, want transition parameters round-tripped", doneEdge.Parameters)
	}

	noop := workflowGraphSaveRequestFromDefinition(workflowID, updatedRecord.Version, false, updated)
	noopSaved, err := store.SaveWorkflowGraph(ctx, noop)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph noop: %v", err)
	}
	if !noopSaved.Saved || noopSaved.Changed || noopSaved.Version != updatedRecord.Version {
		t.Fatalf("noop save = %+v, want prompt/parameters preserved as unchanged graph", noopSaved)
	}
}

func TestWorkflowGraphSaveAcceptsClientGeneratedTopologyIDsAndRejectsCollisions(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = append(req.Nodes, NodeRecord{
		ID:           "workflow-node-00000000-0000-4000-8000-000000000001",
		WorkflowID:   workflowID,
		Key:          "client_generated",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Client Generated",
		SubagentRole: workflow.DefaultAgentRole,
	})
	req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{
		ID:           "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		WorkflowID:   workflowID,
		SourceNodeID: workflow.NodeID("node-agent-" + string(workflowID)),
		TransitionID: "client_generated",
		DisplayName:  "Client Generated",
	})
	req.Edges = append(req.Edges, EdgeRecord{
		ID:                "workflow-edge-00000000-0000-4000-8000-000000000001",
		WorkflowID:        workflowID,
		TransitionGroupID: "workflow-transition-group-00000000-0000-4000-8000-000000000001",
		Key:               "client_generated",
		TargetNodeID:      "workflow-node-00000000-0000-4000-8000-000000000001",
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Client generated.",
	})

	saved, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph client ids: %v", err)
	}
	if !saved.Saved || len(saved.Blockers) != 0 {
		t.Fatalf("client id graph save = %+v, want saved without blockers", saved)
	}
	updated, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after client ids: %v", err)
	}
	if nodeByID(t, updated, "workflow-node-00000000-0000-4000-8000-000000000001").Key != "client_generated" {
		t.Fatalf("client-generated node id was not persisted")
	}

	colliding := workflowGraphSaveRequestFromDefinition(workflowID, saved.Version, false, updated)
	colliding.Nodes = append(colliding.Nodes, colliding.Nodes[len(colliding.Nodes)-1])
	collidingResult, err := store.SaveWorkflowGraph(ctx, colliding)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph duplicate client id: %v", err)
	}
	if collidingResult.Saved || workflowGraphSaveBlockerCount(collidingResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("duplicate client id graph save = %+v, want validation blocker", collidingResult)
	}
}

func TestWorkflowGraphSaveMetadataAndGraphAreAtomic(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(workflowID))

	combined := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	combined.Metadata = &WorkflowGraphSaveMetadata{Name: "Combined save", Description: "Graph and metadata"}
	combined.Nodes = renameWorkflowGraphSaveNode(combined.Nodes, agentID, "Edited Agent")
	saved, err := store.SaveWorkflowGraph(ctx, combined)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph combined: %v", err)
	}
	if !saved.Saved || saved.Version != record.Version+1 {
		t.Fatalf("combined save = %+v, want one version bump", saved)
	}
	updatedDef, updatedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after combined: %v", err)
	}
	if updatedRecord.Name != "Combined save" || nodeByID(t, updatedDef, agentID).DisplayName != "Edited Agent" {
		t.Fatalf("combined save persisted record=%+v node=%+v", updatedRecord, nodeByID(t, updatedDef, agentID))
	}

	blocked := workflowGraphSaveRequestFromDefinition(workflowID, updatedRecord.Version, false, updatedDef)
	blocked.Metadata = &WorkflowGraphSaveMetadata{Name: "Must not persist", Description: "Blocked"}
	blocked.Edges = removeWorkflowGraphSaveEdge(blocked.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	blockedResult, err := store.SaveWorkflowGraph(ctx, blocked)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph blocked combined: %v", err)
	}
	if blockedResult.Saved || workflowGraphSaveBlockerCount(blockedResult.Blockers, "confirmation_required") == 0 {
		t.Fatalf("blocked combined save = %+v, want confirmation blocker", blockedResult)
	}
	_, unchangedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after blocked combined: %v", err)
	}
	if unchangedRecord.Name != "Combined save" || unchangedRecord.Description != "Graph and metadata" || unchangedRecord.Version != updatedRecord.Version {
		t.Fatalf("record after blocked combined = %+v, want unchanged", unchangedRecord)
	}
}

func TestWorkflowGraphSaveValidatesAndPersistsV1NodeGroups(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	groupID := "group-parallel-" + string(workflowID)
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: groupID, WorkflowID: workflowID, Key: "parallel", DisplayName: "Parallel"})
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-a-"+string(workflowID)), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-impl-b-"+string(workflowID)), groupID)
	req.Nodes = setWorkflowGraphSaveNodeGroup(req.Nodes, workflow.NodeID("node-join-"+string(workflowID)), groupID)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph valid group: %v", err)
	}
	if !result.Saved || len(result.Blockers) != 0 {
		t.Fatalf("valid node group graph save = %+v, want saved", result)
	}
	savedDef, savedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after valid group: %v", err)
	}
	if savedRecord.Version != record.Version+1 {
		t.Fatalf("valid node group version = %d, want %d", savedRecord.Version, record.Version+1)
	}
	if len(savedDef.NodeGroups) != 1 || len(savedDef.NodeGroups[0].MemberNodeIDs) != 3 {
		t.Fatalf("saved node groups = %+v, want one group with three members", savedDef.NodeGroups)
	}
	if nodeByID(t, savedDef, workflow.NodeID("node-join-"+string(workflowID))).GroupID != groupID {
		t.Fatalf("saved join group id not persisted: %+v", nodeByID(t, savedDef, workflow.NodeID("node-join-"+string(workflowID))))
	}

	invalid := workflowGraphSaveRequestFromDefinition(workflowID, savedRecord.Version, false, savedDef)
	invalid.Nodes = setWorkflowGraphSaveNodeGroup(invalid.Nodes, workflow.NodeID("node-impl-b-"+string(workflowID)), "")
	invalidResult, err := store.SaveWorkflowGraph(ctx, invalid)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph invalid group: %v", err)
	}
	if invalidResult.Saved || workflowGraphSaveBlockerCount(invalidResult.Blockers, "validation_failed") == 0 {
		t.Fatalf("invalid node group graph save = %+v, want validation blocker", invalidResult)
	}
}

func TestWorkflowGraphSaveRejectsStaleVersion(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if err := store.UpdateWorkflowInfo(ctx, workflowID, "Remote rename", "Remote description"); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	_, remote, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition remote: %v", err)
	}

	stale := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	stale.Metadata = &WorkflowGraphSaveMetadata{Name: "Local rename", Description: "Local description"}
	result, err := store.SaveWorkflowGraph(ctx, stale)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale definition: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "version_changed") != remote.Version {
		t.Fatalf("stale metadata save = %+v, want current version blocker", result)
	}
	_, unchanged, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after stale definition: %v", err)
	}
	if unchanged.Name != "Remote rename" || unchanged.Version != remote.Version {
		t.Fatalf("record after stale metadata save = %+v, want remote metadata preserved", unchanged)
	}
}

func TestPreviewWorkflowGraphSaveDoesNotMutateWithoutBlockers(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, agentID, "Preview Agent")

	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if preview.Saved || len(preview.Blockers) != 0 || !preview.CanSave {
		t.Fatalf("preview graph save = %+v, want non-mutating savable preview without blockers", preview)
	}
	unchangedDef, unchangedRecord, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition after preview: %v", err)
	}
	if unchangedRecord.Version != record.Version {
		t.Fatalf("workflow version after preview = %d, want %d", unchangedRecord.Version, record.Version)
	}
	if nodeByID(t, unchangedDef, agentID).DisplayName == "Preview Agent" {
		t.Fatalf("preview mutated node display name")
	}
}

func TestWorkflowGraphSaveRejectsChangedConfirmationImpact(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if !preview.ConfirmationRequired || workflowGraphSaveBlockerCount(preview.Blockers, "confirmation_required") != 2 {
		t.Fatalf("preview graph save = %+v, want confirmation for removed edge and transition group", preview)
	}

	confirmed := confirmWorkflowGraphSaveRequest(req, preview.Impact)
	confirmed.ExpectedRemovedEdgeCount++
	result, err := store.SaveWorkflowGraph(ctx, confirmed)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph changed impact: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "impact_changed") != 1 {
		t.Fatalf("changed-impact graph save = %+v, want impact_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after changed-impact save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after changed-impact save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveBlocksActiveWorkButAllowsBacklogAndTerminalTasks(t *testing.T) {
	t.Run("backlog only task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Backlog", Body: "Body"})
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph backlog-only: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("backlog-only graph save = %+v, want saved without active-work blockers", result)
		}
	})

	t.Run("active task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Active", Body: "Body"})
		startTask(t, ctx, store, task.ID)
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph active task: %v", err)
		}
		if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "active_node_placements") == 0 {
			t.Fatalf("active-task graph save = %+v, want active_node_placements blocker", result)
		}
	})

	t.Run("terminal only task", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Terminal", Body: "Body"})
		started := startTask(t, ctx, store, task.ID)
		completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
		def, record, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
		req.Nodes = renameWorkflowGraphSaveNode(req.Nodes, workflow.NodeID("node-agent-"+string(workflowID)), "Agent Renamed")

		result, err := store.SaveWorkflowGraph(ctx, req)
		if err != nil {
			t.Fatalf("SaveWorkflowGraph terminal-only: %v", err)
		}
		if !result.Saved || len(result.Blockers) != 0 {
			t.Fatalf("terminal-only graph save = %+v, want saved without active-work blockers", result)
		}
	})
}

func TestWorkflowGraphSaveEditPolicyBlocksStartNodeChanges(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, start.ID, workflow.NodeKindAgent)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph start kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "start_node_changed") != 1 {
		t.Fatalf("start-node graph save = %+v, want start_node_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked start-node save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked start-node save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveEditPolicyBlocksLastTerminalRemovalOrKindChange(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, done.ID, workflow.NodeKindAgent)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph terminal kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "last_terminal_changed") != 1 {
		t.Fatalf("last-terminal graph save = %+v, want last_terminal_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked last-terminal save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked last-terminal save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveEditPolicyBlocksTaskReferencedNodeKindChange(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Nodes = changeWorkflowGraphSaveNodeKind(req.Nodes, agentID, workflow.NodeKindJoin)

	result, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph referenced node kind change: %v", err)
	}
	if result.Saved || workflowGraphSaveBlockerCount(result.Blockers, "task_referenced_node_kind_changed") == 0 {
		t.Fatalf("task-referenced kind-change graph save = %+v, want task_referenced_node_kind_changed blocker", result)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked kind-change save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked kind-change save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowPerEntityMutationsUseGraphEditPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("active work blocks mutation", func(t *testing.T) {
		store, binding := newTestStore(t)
		workflowID := createValidWorkflow(t, ctx, store)
		if _, err := store.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		if _, err := store.StartTask(ctx, task.ID); err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		agentID := workflow.NodeID("node-agent-" + string(workflowID))
		_, err = store.AddNode(ctx, NodeRecord{ID: "node-blocked-active", WorkflowID: workflowID, Key: "blocked_active", Kind: workflow.NodeKindAgent, DisplayName: "Blocked", SubagentRole: "coder", PromptTemplate: "Noop."})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
			t.Fatalf("AddNode active error = %v, want active_node_placements policy blocker", err)
		}
		if err := store.DeleteNode(ctx, agentID); !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
			t.Fatalf("DeleteNode active error = %v, want active_node_placements policy blocker", err)
		}
		if err := store.DeleteEdge(ctx, workflow.EdgeID("edge-start-"+string(workflowID))); !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
			t.Fatalf("DeleteEdge active error = %v, want active_node_placements policy blocker", err)
		}
	})

	t.Run("start node kind change blocks update", func(t *testing.T) {
		store, _ := newTestStore(t)
		workflowID := createValidWorkflow(t, ctx, store)
		def, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		start := nodeByKind(t, def, workflow.NodeKindStart)
		_, err = store.UpdateNode(ctx, NodeRecord{ID: start.ID, WorkflowID: workflowID, Key: start.Key, Kind: workflow.NodeKindAgent, DisplayName: start.DisplayName})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "start_node_changed") {
			t.Fatalf("UpdateNode start error = %v, want start_node_changed policy blocker", err)
		}
		if err := store.DeleteNode(ctx, start.ID); !workflowGraphEditPolicyErrorHasBlocker(err, "start_node_changed") {
			t.Fatalf("DeleteNode start error = %v, want start_node_changed policy blocker", err)
		}
	})

	t.Run("last terminal kind change blocks update", func(t *testing.T) {
		store, _ := newTestStore(t)
		workflowID := createValidWorkflow(t, ctx, store)
		def, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		_, err = store.UpdateNode(ctx, NodeRecord{ID: done.ID, WorkflowID: workflowID, Key: done.Key, Kind: workflow.NodeKindAgent, DisplayName: done.DisplayName})
		if !workflowGraphEditPolicyErrorHasBlocker(err, "last_terminal_changed") {
			t.Fatalf("UpdateNode terminal error = %v, want last_terminal_changed policy blocker", err)
		}
		if err := store.DeleteNode(ctx, done.ID); !workflowGraphEditPolicyErrorHasBlocker(err, "last_terminal_changed") {
			t.Fatalf("DeleteNode terminal error = %v, want last_terminal_changed policy blocker", err)
		}
	})
}

func TestWorkflowGraphSaveAllowsRemovedCompletedEdgeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	edgeRemoval := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	edgeRemoval.Edges = removeWorkflowGraphSaveEdge(edgeRemoval.Edges, workflow.EdgeID("edge-done-"+string(workflowID)))
	edgeRemoval.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(edgeRemoval.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	preview, err := store.PreviewWorkflowGraphSave(ctx, edgeRemoval)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave edge removal: %v", err)
	}
	if workflowGraphSaveBlockerCount(preview.Blockers, "edge_task_references") != 0 {
		t.Fatalf("edge removal preview = %+v, want no edge task-reference blocker", preview)
	}
	edgeSaved, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(edgeRemoval, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph edge removal: %v", err)
	}
	if !edgeSaved.Saved || workflowGraphSaveBlockerCount(edgeSaved.Blockers, "edge_task_references") != 0 {
		t.Fatalf("edge removal graph save = %+v, want saved without edge task-reference blocker", edgeSaved)
	}
}

func TestWorkflowGraphSaveBlocksRemovedPendingEdgeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createApprovalWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, workflow.EdgeID("edge-done-approval-"+string(workflowID)))
	req.TransitionGroups = removeWorkflowGraphSaveTransitionGroupByID(req.TransitionGroups, workflow.TransitionGroupID("group-done-"+string(workflowID)))
	preview, err := store.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave pending edge removal: %v", err)
	}
	blocked, err := store.SaveWorkflowGraph(ctx, confirmWorkflowGraphSaveRequest(req, preview.Impact))
	if err != nil {
		t.Fatalf("SaveWorkflowGraph pending edge removal: %v", err)
	}
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "edge_task_references") == 0 {
		t.Fatalf("pending edge removal graph save = %+v, want edge task-reference blocker", blocked)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked pending edge save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked pending edge save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveBlocksRemovedNodeTaskReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	nodeRemoval := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	nodeRemoval.Nodes = removeWorkflowGraphSaveNode(nodeRemoval.Nodes, agentID)
	nodeRemoval.TransitionGroups = removeWorkflowGraphSaveTransitionGroupsTouchingNode(def, nodeRemoval.TransitionGroups, agentID)
	nodeRemoval.Edges = removeWorkflowGraphSaveEdgesTouchingNode(def, nodeRemoval.Edges, agentID)
	nodeBlocked, err := store.SaveWorkflowGraph(ctx, nodeRemoval)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph node removal: %v", err)
	}
	if nodeBlocked.Saved || workflowGraphSaveBlockerCount(nodeBlocked.Blockers, "node_task_references") == 0 {
		t.Fatalf("node removal graph save = %+v, want node task-reference blocker", nodeBlocked)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked graph save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked graph save = %d, want %d", unchanged.Version, record.Version)
	}
}

func TestWorkflowGraphSaveBlocksRemovedParallelBranchEdgeReferences(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	removedEdgeID := workflow.EdgeID("edge-split-a-" + string(workflowID))
	req := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, true, def)
	req.Edges = removeWorkflowGraphSaveEdge(req.Edges, removedEdgeID)
	blocked, err := store.SaveWorkflowGraph(ctx, req)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph parallel branch edge removal: %v", err)
	}
	if blocked.Saved || workflowGraphSaveBlockerCount(blocked.Blockers, "edge_task_references") == 0 {
		t.Fatalf("parallel branch edge removal = %+v, want edge task-reference blocker", blocked)
	}
	var branchEdgeID sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT parallel_branch_edge_id FROM task_node_placements WHERE id = ?`, string(result.PlacementIDs[0])).Scan(&branchEdgeID); err != nil {
		t.Fatalf("query branch placement edge after blocked graph save: %v", err)
	}
	if !branchEdgeID.Valid || branchEdgeID.String == "" {
		t.Fatalf("branch placement edge after blocked graph save = %+v, want preserved reference", branchEdgeID)
	}
	if _, unchanged, err := store.GetDefinition(ctx, workflowID); err != nil {
		t.Fatalf("GetDefinition after blocked parallel branch save: %v", err)
	} else if unchanged.Version != record.Version {
		t.Fatalf("workflow version after blocked parallel branch save = %d, want %d", unchanged.Version, record.Version)
	}
}

func workflowGraphSaveRequestFromDefinition(workflowID workflow.WorkflowID, revision int64, confirmed bool, def workflow.Definition) WorkflowGraphSaveRequest {
	req := WorkflowGraphSaveRequest{WorkflowID: workflowID, ExpectedVersion: revision, Confirmed: confirmed}
	for _, group := range def.NodeGroups {
		req.NodeGroups = append(req.NodeGroups, NodeGroupRecord{ID: group.ID, WorkflowID: workflowID, Key: group.Key, DisplayName: group.DisplayName})
	}
	for _, node := range def.Nodes {
		req.Nodes = append(req.Nodes, NodeRecord{ID: node.ID, WorkflowID: workflowID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName, GroupID: node.GroupID, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, InputFields: node.InputFields, JoinInputProviders: node.JoinInputProviders, OutputFields: node.OutputFields})
	}
	for _, group := range def.TransitionGroups {
		req.TransitionGroups = append(req.TransitionGroups, TransitionGroupRecord{ID: group.ID, WorkflowID: workflowID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID, DisplayName: group.DisplayName})
	}
	for _, edge := range def.Edges {
		req.Edges = append(req.Edges, EdgeRecord{ID: edge.ID, WorkflowID: workflowID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key, TargetNodeID: edge.TargetNodeID, ContextMode: edge.ContextMode, ContextSource: edge.ContextSource, RequiresApproval: edge.RequiresApproval, PromptTemplate: edge.PromptTemplate, Parameters: edge.Parameters, InputBindings: edge.InputBindings, OutputRequirements: edge.OutputRequirements})
	}
	return req
}

func renameWorkflowGraphSaveNode(nodes []NodeRecord, nodeID workflow.NodeID, displayName string) []NodeRecord {
	renamed := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			node.DisplayName = displayName
		}
		renamed = append(renamed, node)
	}
	return renamed
}

func setWorkflowGraphSaveNodeGroup(nodes []NodeRecord, nodeID workflow.NodeID, groupID string) []NodeRecord {
	changed := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			node.GroupID = groupID
		}
		changed = append(changed, node)
	}
	return changed
}

func confirmWorkflowGraphSaveRequest(req WorkflowGraphSaveRequest, impact WorkflowGraphSaveImpact) WorkflowGraphSaveRequest {
	req.Confirmed = true
	req.ExpectedRemovedNodeCount = impact.RemovedNodeCount
	req.ExpectedRemovedTransitionGroupCount = impact.RemovedTransitionGroupCount
	req.ExpectedRemovedEdgeCount = impact.RemovedEdgeCount
	req.ExpectedNodeTaskReferenceCount = impact.NodeTaskReferenceCount
	req.ExpectedEdgeTaskReferenceCount = impact.EdgeTaskReferenceCount
	return req
}

func changeWorkflowGraphSaveNodeKind(nodes []NodeRecord, nodeID workflow.NodeID, kind workflow.NodeKind) []NodeRecord {
	changed := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == nodeID {
			node.Kind = kind
		}
		changed = append(changed, node)
	}
	return changed
}

func removeWorkflowGraphSaveNode(nodes []NodeRecord, nodeID workflow.NodeID) []NodeRecord {
	filtered := make([]NodeRecord, 0, len(nodes))
	for _, node := range nodes {
		if node.ID != nodeID {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveTransitionGroupsTouchingNode(def workflow.Definition, groups []TransitionGroupRecord, nodeID workflow.NodeID) []TransitionGroupRecord {
	touching := map[workflow.TransitionGroupID]bool{}
	for _, group := range def.TransitionGroups {
		if group.SourceNodeID == nodeID {
			touching[group.ID] = true
		}
	}
	for _, edge := range def.Edges {
		if edge.TargetNodeID == nodeID {
			touching[edge.TransitionGroupID] = true
		}
	}
	filtered := make([]TransitionGroupRecord, 0, len(groups))
	for _, group := range groups {
		if !touching[group.ID] {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveTransitionGroupByID(groups []TransitionGroupRecord, groupID workflow.TransitionGroupID) []TransitionGroupRecord {
	filtered := make([]TransitionGroupRecord, 0, len(groups))
	for _, group := range groups {
		if group.ID != groupID {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveEdge(edges []EdgeRecord, edgeID workflow.EdgeID) []EdgeRecord {
	filtered := make([]EdgeRecord, 0, len(edges))
	for _, edge := range edges {
		if edge.ID != edgeID {
			filtered = append(filtered, edge)
		}
	}
	return filtered
}

func removeWorkflowGraphSaveEdgesTouchingNode(def workflow.Definition, edges []EdgeRecord, nodeID workflow.NodeID) []EdgeRecord {
	sourceByGroup := map[workflow.TransitionGroupID]workflow.NodeID{}
	for _, group := range def.TransitionGroups {
		sourceByGroup[group.ID] = group.SourceNodeID
	}
	filtered := make([]EdgeRecord, 0, len(edges))
	for _, edge := range edges {
		if edge.TargetNodeID != nodeID && sourceByGroup[edge.TransitionGroupID] != nodeID {
			filtered = append(filtered, edge)
		}
	}
	return filtered
}

func workflowGraphSaveBlockerCount(blockers []WorkflowGraphSaveBlocker, code string) int64 {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return blocker.Count
		}
	}
	return 0
}

func nodeByID(t *testing.T, def workflow.Definition, nodeID workflow.NodeID) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	t.Fatalf("node %q not found in %+v", nodeID, def.Nodes)
	return workflow.Node{}
}

func workflowGraphEditPolicyErrorHasBlocker(err error, code string) bool {
	var policyErr WorkflowGraphEditPolicyError
	if !errors.As(err, &policyErr) {
		return false
	}
	for _, blocker := range policyErr.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func forceWorkflowGraphRowsForSnapshotTest(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, nodes []NodeRecord, groups []TransitionGroupRecord, edges []EdgeRecord) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx force graph rows: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := store.queries.WithTx(tx)
	for i, node := range nodes {
		if node.WorkflowID == "" {
			node.WorkflowID = workflowID
		}
		if err := upsertWorkflowNode(ctx, q, node, int64(10000+i*100)); err != nil {
			t.Fatalf("force workflow node %s: %v", node.ID, err)
		}
	}
	for i, group := range groups {
		if group.WorkflowID == "" {
			group.WorkflowID = workflowID
		}
		if err := upsertWorkflowTransitionGroup(ctx, q, group, int64(10000+i*100)); err != nil {
			t.Fatalf("force workflow transition group %s: %v", group.ID, err)
		}
	}
	for i, edge := range edges {
		if edge.WorkflowID == "" {
			edge.WorkflowID = workflowID
		}
		if err := upsertWorkflowEdge(ctx, q, edge, int64(10000+i*100)); err != nil {
			t.Fatalf("force workflow edge %s: %v", edge.ID, err)
		}
	}
	if _, err := q.IncrementWorkflowVersion(ctx, sqlitegen.IncrementWorkflowVersionParams{ID: string(workflowID), UpdatedAtUnixMs: store.now().UnixMilli()}); err != nil {
		t.Fatalf("increment forced workflow version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit forced graph rows: %v", err)
	}
}

func TestGuardedGraphDeletesRespectTaskHistory(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	if err := store.DeleteNode(ctx, agentID); !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
		t.Fatalf("expected active node delete policy guard, got %v", err)
	}
	if err := store.DeleteEdge(ctx, workflow.EdgeID("edge-start-"+string(workflowID))); !workflowGraphEditPolicyErrorHasBlocker(err, "active_node_placements") {
		t.Fatalf("expected active edge delete policy guard, got %v", err)
	}
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if err := store.DeleteNode(ctx, agentID); !errors.Is(err, ErrNodeHasTaskHistory) {
		t.Fatalf("expected node history delete guard, got %v", err)
	}
	if err := store.DeleteEdge(ctx, workflow.EdgeID("edge-done-"+string(workflowID))); err != nil {
		t.Fatalf("DeleteEdge completed history edge: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-unused", WorkflowID: workflowID, Key: "unused", Kind: workflow.NodeKindTerminal, DisplayName: "Unused"}); err != nil {
		t.Fatalf("AddNode unused: %v", err)
	}
	if err := store.DeleteNode(ctx, done.ID); !errors.Is(err, ErrNodeHasTaskHistory) {
		t.Fatalf("expected terminal physical delete guard, got %v", err)
	}
	if err := store.DeleteNode(ctx, "node-unused"); err != nil {
		t.Fatalf("DeleteNode unused: %v", err)
	}
	if _, err := store.queries.GetWorkflowNode(ctx, "node-unused"); err == nil {
		t.Fatalf("unused node still exists after guarded delete")
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-unused", WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "unused", DisplayName: "Unused"}); err != nil {
		t.Fatalf("AddTransitionGroup unused: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-unused", WorkflowID: workflowID, TransitionGroupID: "group-unused", Key: "unused", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge unused: %v", err)
	}
	if err := store.DeleteEdge(ctx, "edge-unused"); err != nil {
		t.Fatalf("DeleteEdge unused: %v", err)
	}
	if _, err := store.queries.GetWorkflowEdge(ctx, "edge-unused"); err == nil {
		t.Fatalf("unused edge still exists after guarded delete")
	}
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

func newTestStore(t *testing.T) (*Store, metadata.Binding) {
	t.Helper()
	store, binding, _ := newTestStoreWithConfig(t)
	return store, binding
}

func newTestStoreContext(t *testing.T) (context.Context, *Store, metadata.Binding) {
	t.Helper()
	store, binding := newTestStore(t)
	return context.Background(), store, binding
}

func newTestStoreWithConfigContext(t *testing.T) (context.Context, *Store, metadata.Binding, config.App) {
	t.Helper()
	store, binding, cfg := newTestStoreWithConfig(t)
	return context.Background(), store, binding, cfg
}

func newTestStoreWithConfig(t *testing.T) (*Store, metadata.Binding, config.App) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := New(metadataStore, WithRoleResolver(workflow.StaticRoleResolver{"coder": true, "reviewer": true}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return store, binding, cfg
}

func createTestSession(t *testing.T, ctx context.Context, store *Store, binding metadata.Binding, cfg config.App) string {
	t.Helper()
	sessionRoot := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sessionStore, err := session.Create(sessionRoot, filepath.Base(cfg.WorkspaceRoot), cfg.WorkspaceRoot, store.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	if _, err := store.metadata.ResolvePersistedSession(ctx, sessionStore.Meta().SessionID); err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	return sessionStore.Meta().SessionID
}

func linkWorkflow(t *testing.T, ctx context.Context, store *Store, projectID string, workflowID workflow.WorkflowID, isDefault bool) {
	t.Helper()
	if _, err := store.LinkWorkflow(ctx, projectID, workflowID, isDefault); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
}

func createLinkedValidWorkflow(t *testing.T, ctx context.Context, store *Store, projectID string) workflow.WorkflowID {
	t.Helper()
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, projectID, workflowID, true)
	return workflowID
}

func createTask(t *testing.T, ctx context.Context, store *Store, req CreateTaskRequest) TaskRecord {
	t.Helper()
	task, err := store.CreateTask(ctx, req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func createDefaultTask(t *testing.T, ctx context.Context, store *Store, projectID string) TaskRecord {
	t.Helper()
	return createTask(t, ctx, store, CreateTaskRequest{ProjectID: projectID, Title: "Task", Body: "Body"})
}

func startTask(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID) StartTaskResult {
	t.Helper()
	started, err := store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	return started
}

func completeRun(t *testing.T, ctx context.Context, store *Store, req CompleteRunRequest) CompleteRunResult {
	t.Helper()
	completed, err := store.CompleteRun(ctx, req)
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	return completed
}

func createValidWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: workflow.NodeID("node-agent-" + string(created.ID)), WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func createChainedContextModeWorkflow(t *testing.T, ctx context.Context, store *Store, contextMode workflow.ContextMode, targetRole string) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Chained Context Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implID := workflow.NodeID("node-impl-" + string(created.ID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implID, WorkflowID: created.ID, Key: "implement", Kind: workflow.NodeKindAgent, DisplayName: "Implement", SubagentRole: targetRole, PromptTemplate: "Implement {{.Inputs.prior_summary}}.", InputFields: []workflow.InputField{{Name: "prior_summary", Description: "Prior summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next", Description: "Continue after planning is complete."},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: implID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan work."},
		{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: implID, ContextMode: contextMode, PromptTemplate: "Implement {{.Params.prior_summary}}.", Parameters: []workflow.Parameter{{Key: "prior_summary", Description: "Prior summary."}}},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createPromptNodeReferenceWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Prompt Node Reference Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	reviewID := workflow.NodeID("node-review-" + string(created.ID))
	auditID := workflow.NodeID("node-audit-" + string(created.ID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Plan summary."}}},
		{ID: reviewID, WorkflowID: created.ID, Key: "review", Kind: workflow.NodeKindAgent, DisplayName: "Review", SubagentRole: "coder", PromptTemplate: "Review {{.Nodes.plan.summary}}."},
		{ID: auditID, WorkflowID: created.ID, Key: "audit", Kind: workflow.NodeKindAgent, DisplayName: "Audit", SubagentRole: "coder", PromptTemplate: "Audit."},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	nextGroup := workflow.TransitionGroupID("group-next-" + string(created.ID))
	auditGroup := workflow.TransitionGroupID("group-audit-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: nextGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next", Description: "Continue after planning is complete."},
		{ID: auditGroup, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "audit", DisplayName: "Audit"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: auditID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan work."},
		{ID: workflow.EdgeID("edge-next-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-audit-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: auditGroup, Key: "audit", TargetNodeID: auditID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Audit {{.Params.next.summary}}."},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createSelectedContextSourceWorkflow(t *testing.T, ctx context.Context, store *Store, contextMode workflow.ContextMode) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Selected Context Source Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implementationID := workflow.NodeID("node-implementation-" + string(created.ID))
	acceptanceID := workflow.NodeID("node-acceptance-" + string(created.ID))
	openPRID := workflow.NodeID("node-open-pr-" + string(created.ID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implementationID, WorkflowID: created.ID, Key: "implementation", Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder", PromptTemplate: "Implement.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: acceptanceID, WorkflowID: created.ID, Key: "acceptance", Kind: workflow.NodeKindAgent, DisplayName: "Acceptance", SubagentRole: "coder", PromptTemplate: "Accept.", InputFields: []workflow.InputField{{Name: "summary", Description: "Implementation summary."}}, OutputFields: []workflow.OutputField{{Name: "decision", Description: "Decision."}}},
		{ID: openPRID, WorkflowID: created.ID, Key: "open_pr", Kind: workflow.NodeKindAgent, DisplayName: "Open PR", SubagentRole: "coder", PromptTemplate: "Open PR {{.Inputs.acceptance_decision}}.", InputFields: []workflow.InputField{{Name: "acceptance_decision", Description: "Acceptance decision."}}, OutputFields: []workflow.OutputField{{Name: "pr_url", Description: "PR URL."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	implementGroup := workflow.TransitionGroupID("group-implement-" + string(created.ID))
	acceptGroup := workflow.TransitionGroupID("group-accept-" + string(created.ID))
	openPRGroup := workflow.TransitionGroupID("group-open-pr-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: implementGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "implement", DisplayName: "Implement"},
		{ID: acceptGroup, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "accept", DisplayName: "Accept"},
		{ID: openPRGroup, WorkflowID: created.ID, SourceNodeID: acceptanceID, TransitionID: "open_pr", DisplayName: "Open PR"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: openPRID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-implement-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: implementGroup, Key: "implement", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-accept-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: acceptGroup, Key: "accept", TargetNodeID: acceptanceID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Accept {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}},
		{ID: workflow.EdgeID("edge-open-pr-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: openPRGroup, Key: "open_pr", TargetNodeID: openPRID, ContextMode: contextMode, ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "implementation"}, PromptTemplate: "Open PR {{.Params.acceptance_decision}}.", Parameters: []workflow.Parameter{{Key: "acceptance_decision", Description: "Acceptance decision."}}},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func addPreviousTargetReworkEdge(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, acceptanceNodeID workflow.NodeID, implementationNodeID workflow.NodeID, requiresApproval bool) {
	t.Helper()
	reworkGroup := workflow.TransitionGroupID("group-previous-target-rework-" + string(workflowID))
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: reworkGroup, WorkflowID: workflowID, SourceNodeID: acceptanceNodeID, TransitionID: "rework", DisplayName: "Rework"}); err != nil {
		t.Fatalf("AddTransitionGroup rework: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{
		ID:                workflow.EdgeID("edge-previous-target-rework-" + string(workflowID)),
		WorkflowID:        workflowID,
		TransitionGroupID: reworkGroup,
		Key:               "rework",
		TargetNodeID:      implementationNodeID,
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
		RequiresApproval:  requiresApproval,
		PromptTemplate:    "Implement {{.Params.summary}}.",
		Parameters:        []workflow.Parameter{{Key: "summary", Description: "Rework summary."}},
	}); err != nil {
		t.Fatalf("AddEdge rework: %v", err)
	}
}

func addOutputFieldToNode(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, node workflow.Node, field workflow.OutputField) {
	t.Helper()
	outputFields := append([]workflow.OutputField(nil), node.OutputFields...)
	outputFields = append(outputFields, field)
	if _, err := store.UpdateNode(ctx, NodeRecord{
		ID:                 node.ID,
		WorkflowID:         workflowID,
		Key:                node.Key,
		Kind:               node.Kind,
		DisplayName:        node.DisplayName,
		GroupID:            node.GroupID,
		SubagentRole:       node.SubagentRole,
		PromptTemplate:     node.PromptTemplate,
		InputFields:        node.InputFields,
		JoinInputProviders: node.JoinInputProviders,
		OutputFields:       outputFields,
	}); err != nil {
		t.Fatalf("UpdateNode %s outputs: %v", node.Key, err)
	}
}

func createApprovalWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Approval Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.ID
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(workflowID))
	if _, err := store.AddNode(ctx, NodeRecord{ID: agentID, WorkflowID: workflowID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(workflowID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(workflowID)), WorkflowID: workflowID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-approval-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(workflowID)), Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession, RequiresApproval: true}); err != nil {
		t.Fatalf("AddEdge approval done: %v", err)
	}
	return workflowID
}

func createFanoutJoinWorkflow(t *testing.T, ctx context.Context, store *Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Fanout Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.ID
	def, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(workflowID))
	implAID := workflow.NodeID("node-impl-a-" + string(workflowID))
	implBID := workflow.NodeID("node-impl-b-" + string(workflowID))
	joinID := workflow.NodeID("node-join-" + string(workflowID))
	synthID := workflow.NodeID("node-synth-" + string(workflowID))
	joinAEdgeID := workflow.EdgeID("edge-join-a-" + string(workflowID))
	joinBEdgeID := workflow.EdgeID("edge-join-b-" + string(workflowID))
	for _, node := range []NodeRecord{
		{ID: planID, WorkflowID: workflowID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder", PromptTemplate: "Plan.", OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implAID, WorkflowID: workflowID, Key: "impl_a", Kind: workflow.NodeKindAgent, DisplayName: "Implement A", SubagentRole: "coder", PromptTemplate: "A.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: implBID, WorkflowID: workflowID, Key: "impl_b", Kind: workflow.NodeKindAgent, DisplayName: "Implement B", SubagentRole: "coder", PromptTemplate: "B.", InputFields: []workflow.InputField{{Name: "summary", Description: "Plan summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
		{ID: joinID, WorkflowID: workflowID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join", JoinInputProviders: []workflow.JoinInputProvider{{InputName: "joined", ProviderEdgeID: joinAEdgeID}}},
		{ID: synthID, WorkflowID: workflowID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synthesize", SubagentRole: "coder", PromptTemplate: "Synthesize {{.Inputs.joined}}.", InputFields: []workflow.InputField{{Name: "joined", Description: "Joined branch summary."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(workflowID))
	splitGroup := workflow.TransitionGroupID("group-split-" + string(workflowID))
	joinAGroup := workflow.TransitionGroupID("group-join-a-" + string(workflowID))
	joinBGroup := workflow.TransitionGroupID("group-join-b-" + string(workflowID))
	synthGroup := workflow.TransitionGroupID("group-join-synth-" + string(workflowID))
	doneGroup := workflow.TransitionGroupID("group-synth-done-" + string(workflowID))
	for _, group := range []TransitionGroupRecord{
		{ID: startGroup, WorkflowID: workflowID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: workflowID, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
		{ID: joinAGroup, WorkflowID: workflowID, SourceNodeID: implAID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinBGroup, WorkflowID: workflowID, SourceNodeID: implBID, TransitionID: "join", DisplayName: "Join"},
		{ID: synthGroup, WorkflowID: workflowID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
		{ID: doneGroup, WorkflowID: workflowID, SourceNodeID: synthID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-split-a-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: splitGroup, Key: "split_a", TargetNodeID: implAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "A {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-split-b-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: splitGroup, Key: "split_b", TargetNodeID: implBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "B {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: joinAEdgeID, WorkflowID: workflowID, TransitionGroupID: joinAGroup, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "joined", Description: "Joined branch summary."}}},
		{ID: joinBEdgeID, WorkflowID: workflowID, TransitionGroupID: joinBGroup, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-synth-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: synthGroup, Key: "synth", TargetNodeID: synthID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize {{.Params.joined}}."},
		{ID: workflow.EdgeID("edge-synth-done-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return workflowID
}

func startFanoutTask(t *testing.T, ctx context.Context, store *Store, projectID string, workflowID workflow.WorkflowID) (TaskRecord, map[workflow.NodeID]workflow.RunID) {
	t.Helper()
	task, err := store.CreateTask(ctx, CreateTaskRequest{ProjectID: projectID, WorkflowID: workflowID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started := startTask(t, ctx, store, task.ID)
	completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	branchRunsByNode := map[workflow.NodeID]workflow.RunID{}
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRunsByNode[run.NodeID] = run.ID
		}
	}
	if len(branchRunsByNode) != 2 {
		t.Fatalf("branch runs = %+v, want two branch runs", branchRunsByNode)
	}
	return task, branchRunsByNode
}

func placementParallelIDs(t *testing.T, ctx context.Context, store *Store, placementID workflow.PlacementID) (string, string) {
	t.Helper()
	var batchID sql.NullString
	var branchID sql.NullString
	if err := store.db.QueryRowContext(ctx, `
SELECT parallel_batch_transition_id, parallel_branch_edge_id
FROM task_node_placements
WHERE id = ?`, string(placementID)).Scan(&batchID, &branchID); err != nil {
		t.Fatalf("query placement parallel ids: %v", err)
	}
	return strings.TrimSpace(batchID.String), strings.TrimSpace(branchID.String)
}

func requireApprovalOnWorkflowEdge(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, edgeKey string) {
	t.Helper()
	result, err := store.db.ExecContext(ctx, `
UPDATE workflow_edges
SET requires_approval = 1
WHERE edge_key = ?
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups tg
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE tg.id = workflow_edges.transition_group_id
        AND source.workflow_id = ?
  )`, edgeKey, string(workflowID))
	if err != nil {
		t.Fatalf("require approval on edge %s: %v", edgeKey, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("require approval rows for edge %s: %v", edgeKey, err)
	}
	if updated != 1 {
		t.Fatalf("require approval on edge %s updated %d rows", edgeKey, updated)
	}
}

func currentWorkflowRevision(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID) int64 {
	t.Helper()
	_, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	return record.Version
}

func hasNode(def workflow.Definition, key string, kind workflow.NodeKind) bool {
	for _, node := range def.Nodes {
		if string(node.Key) == key && node.Kind == kind {
			return true
		}
	}
	return false
}

func nodeByKey(t *testing.T, def workflow.Definition, key string) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if string(node.Key) == key {
			return node
		}
	}
	t.Fatalf("missing node key %q in %+v", key, def.Nodes)
	return workflow.Node{}
}

func edgeByKey(t *testing.T, def workflow.Definition, key string) workflow.Edge {
	t.Helper()
	for _, edge := range def.Edges {
		if string(edge.Key) == key {
			return edge
		}
	}
	t.Fatalf("missing edge key %q in %+v", key, def.Edges)
	return workflow.Edge{}
}

func runForNode(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID) RunRecord {
	t.Helper()
	runs, err := store.ListRuns(ctx, taskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	for _, run := range runs {
		if run.NodeID == nodeID {
			return run
		}
	}
	t.Fatalf("run for node %q not found in %+v", nodeID, runs)
	return RunRecord{}
}

func latestRunForNode(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID) RunRecord {
	t.Helper()
	runs, err := store.ListRuns(ctx, taskID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	var latest RunRecord
	for _, run := range runs {
		if run.NodeID == nodeID {
			latest = run
		}
	}
	if latest.ID == "" {
		t.Fatalf("run for node %q not found in %+v", nodeID, runs)
	}
	return latest
}

func insertCompletedRunForNodeAfterTransition(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID, snapshotSourceRunID workflow.RunID, sessionID string, transitionID workflow.TransitionID) workflow.RunID {
	t.Helper()
	var transitionCreatedAt int64
	if err := store.db.QueryRowContext(ctx, `SELECT created_at_unix_ms FROM task_transitions WHERE id = ?`, string(transitionID)).Scan(&transitionCreatedAt); err != nil {
		t.Fatalf("query transition created_at: %v", err)
	}
	var snapshotJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT run_start_snapshot_json FROM task_runs WHERE id = ?`, string(snapshotSourceRunID)).Scan(&snapshotJSON); err != nil {
		t.Fatalf("query source run snapshot: %v", err)
	}
	placementID := prefixedID("placement")
	runID := prefixedID("run")
	completedAt := transitionCreatedAt + 1
	if err := store.queries.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: placementID, TaskID: string(taskID), NodeID: string(nodeID), State: "completed", CreatedAtUnixMs: completedAt, UpdatedAtUnixMs: completedAt}); err != nil {
		t.Fatalf("InsertTaskNodePlacement competing run: %v", err)
	}
	if err := store.queries.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{
		ID:                          runID,
		PlacementID:                 placementID,
		SessionID:                   sql.NullString{String: sessionID, Valid: sessionID != ""},
		RunGeneration:               1,
		WorkflowRevisionSeen:        1,
		AutomationRequestedAtUnixMs: completedAt,
		CreatedAtUnixMs:             completedAt,
		UpdatedAtUnixMs:             completedAt,
		StartedAtUnixMs:             completedAt,
		CompletedAtUnixMs:           completedAt,
		InterruptedAtUnixMs:         0,
		InterruptionDetailJson:      "{}",
		RunStartSnapshotJson:        snapshotJSON,
		MetadataJson:                "{}",
	}); err != nil {
		t.Fatalf("InsertTaskRun competing run: %v", err)
	}
	return workflow.RunID(runID)
}

func insertCompletedRunForNodeInBatch(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, nodeID workflow.NodeID, snapshotSourceRunID workflow.RunID, sessionID string, batchID string, completedAt int64) workflow.RunID {
	t.Helper()
	var snapshotJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT run_start_snapshot_json FROM task_runs WHERE id = ?`, string(snapshotSourceRunID)).Scan(&snapshotJSON); err != nil {
		t.Fatalf("query source run snapshot: %v", err)
	}
	placementID := prefixedID("placement")
	runID := prefixedID("run")
	if err := store.queries.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: placementID, TaskID: string(taskID), NodeID: string(nodeID), State: "completed", ParallelBatchTransitionID: sql.NullString{String: batchID, Valid: strings.TrimSpace(batchID) != ""}, CreatedAtUnixMs: completedAt, UpdatedAtUnixMs: completedAt}); err != nil {
		t.Fatalf("InsertTaskNodePlacement competing batch run: %v", err)
	}
	if err := store.queries.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{
		ID:                          runID,
		PlacementID:                 placementID,
		SessionID:                   sql.NullString{String: sessionID, Valid: sessionID != ""},
		RunGeneration:               1,
		WorkflowRevisionSeen:        1,
		AutomationRequestedAtUnixMs: completedAt,
		CreatedAtUnixMs:             completedAt,
		UpdatedAtUnixMs:             completedAt,
		StartedAtUnixMs:             completedAt,
		CompletedAtUnixMs:           completedAt,
		InterruptedAtUnixMs:         0,
		InterruptionDetailJson:      "{}",
		RunStartSnapshotJson:        snapshotJSON,
		MetadataJson:                "{}",
	}); err != nil {
		t.Fatalf("InsertTaskRun competing batch run: %v", err)
	}
	return workflow.RunID(runID)
}

func assertZeroTaskRows(t *testing.T, store *Store, table string, taskID string) {
	t.Helper()
	queries := map[string]string{
		"task_node_placements": `SELECT COUNT(*) FROM task_node_placements WHERE task_id = ?`,
		"task_transitions":     `SELECT COUNT(*) FROM task_transitions WHERE task_id = ?`,
		"task_comments":        `SELECT COUNT(*) FROM task_comments WHERE task_id = ?`,
	}
	query, ok := queries[table]
	if !ok {
		t.Fatalf("assertZeroTaskRows: unsupported table %q", table)
	}
	var count int
	if err := store.db.QueryRow(query, taskID).Scan(&count); err != nil {
		t.Fatalf("count %s rows for task %s: %v", table, taskID, err)
	}
	if count != 0 {
		t.Fatalf("%s rows for task %s = %d, want 0", table, taskID, count)
	}
}

func taskTransitionIDOtherThan(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, excludedID string) workflow.TransitionID {
	t.Helper()
	var transitionID string
	if err := store.db.QueryRowContext(ctx, `
SELECT id
FROM task_transitions
WHERE task_id = ? AND id != ?
LIMIT 1`, string(taskID), excludedID).Scan(&transitionID); err != nil {
		t.Fatalf("query task transition other than %s: %v", excludedID, err)
	}
	return workflow.TransitionID(transitionID)
}

func nodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind == kind {
			return node
		}
	}
	t.Fatalf("missing node kind %q in %+v", kind, def.Nodes)
	return workflow.Node{}
}
