package workflowview

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type taskSessionActivitySource struct {
	snapshots []runtimeactivity.ActiveSessionSnapshot
	calls     *int
}

func (s taskSessionActivitySource) ActiveRuntimeActivitySnapshots(context.Context) ([]runtimeactivity.ActiveSessionSnapshot, error) {
	if s.calls != nil {
		(*s.calls)++
	}
	return append([]runtimeactivity.ActiveSessionSnapshot(nil), s.snapshots...), nil
}

func TestTaskSessionsListsRetainedIdleSession(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Task Session listing")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 1
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("items = %+v, want one retained Session", response.Items)
	}
	item := response.Items[0]
	if item.SessionID != sessionID.String() ||
		item.SessionName == nil || *item.SessionName != "Current Node session" ||
		item.NodeName == nil || *item.NodeName != "Agent" ||
		item.AgentRole != workflow.DefaultAgentRole ||
		item.Status != serverapi.WorkflowTaskSessionStatusIdle {
		t.Fatalf("item = %+v", item)
	}
}

func TestTaskSessionsOrdersIdleSessionsNewestFirstAndProjectsNamedRole(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Ordered Task Sessions")
	olderID := fixture.bindCurrentNodeSession(t, started)
	newerID := fixture.newCurrentNodeViewSession(t)
	namedRole := "reviewer"
	newerStore, err := session.OpenByID(
		fixture.cfg.PersistenceRoot,
		newerID.String(),
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if err := newerStore.SetName("Review Session"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if err := newerStore.SetContinuationContext(session.ContinuationContext{AgentRole: &namedRole}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}
	if _, err := fixture.store.AssociateTaskSession(fixture.ctx, workflowstore.TaskSessionAssociationRequest{
		SessionID:    newerID,
		CurrentNode:  started.currentNode,
		AssociatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	fixture.setSessionCreatedAt(t, olderID, 1_000)
	fixture.setSessionCreatedAt(t, newerID, 2_000)
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 2
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 2 ||
		response.Items[0].SessionID != newerID.String() ||
		response.Items[0].AgentRole != namedRole ||
		response.Items[1].SessionID != olderID.String() ||
		response.Items[1].AgentRole != workflow.DefaultAgentRole {
		t.Fatalf("items = %+v", response.Items)
	}
}

func TestTaskSessionsProjectsRunningFromActiveRuntimeActivity(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Running Task Session")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{
		snapshots: []runtimeactivity.ActiveSessionSnapshot{{
			SessionID: sessionID.String(),
			Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityRunning),
		}},
	})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 1
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 1 ||
		response.Items[0].SessionID != sessionID.String() ||
		response.Items[0].Status != serverapi.WorkflowTaskSessionStatusRunning {
		t.Fatalf("items = %+v", response.Items)
	}
}

func TestTaskSessionsProjectsQuestionFromAwaitingPrompt(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Question Task Session")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{
		snapshots: []runtimeactivity.ActiveSessionSnapshot{{
			SessionID: sessionID.String(),
			Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityAwaitingPrompt),
		}},
	})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 1
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 1 ||
		response.Items[0].SessionID != sessionID.String() ||
		response.Items[0].Status != serverapi.WorkflowTaskSessionStatusQuestion {
		t.Fatalf("items = %+v", response.Items)
	}
}

func TestTaskSessionsListsParallelAgentSessionsRunningBeforeQuestion(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Parallel Task Sessions")
	runningID := associateTaskSessionForViewTest(t, fixture, started, "branch-running")
	questionID := associateTaskSessionForViewTest(t, fixture, started, "branch-question")
	fixture.setSessionCreatedAt(t, runningID, 1_000)
	fixture.setSessionCreatedAt(t, questionID, 2_000)
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{
		snapshots: []runtimeactivity.ActiveSessionSnapshot{
			{
				SessionID: questionID.String(),
				Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityAwaitingPrompt),
			},
			{
				SessionID: runningID.String(),
				Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityRunning),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 2
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 2 ||
		response.Items[0].SessionID != runningID.String() ||
		response.Items[0].Status != serverapi.WorkflowTaskSessionStatusRunning ||
		response.Items[1].SessionID != questionID.String() ||
		response.Items[1].Status != serverapi.WorkflowTaskSessionStatusQuestion {
		t.Fatalf("items = %+v", response.Items)
	}
}

func TestTaskSessionsProjectsOrdinaryContinuedTaskSessionAsRunning(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Ordinary continued Task Session")
	sessionID := associateTaskSessionForViewTest(t, fixture, started, "")
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{
		snapshots: []runtimeactivity.ActiveSessionSnapshot{{
			SessionID: sessionID.String(),
			Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityRunning),
		}},
	})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 1
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 1 ||
		response.Items[0].SessionID != sessionID.String() ||
		response.Items[0].Status != serverapi.WorkflowTaskSessionStatusRunning {
		t.Fatalf("items = %+v", response.Items)
	}
}

func TestTaskSessionsPagesActiveSetLargerThanRequestedLimit(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Large active Task Session set")
	sessionIDs := []runtimeids.SessionID{
		associateTaskSessionForViewTest(t, fixture, started, "branch-a"),
		associateTaskSessionForViewTest(t, fixture, started, "branch-b"),
		associateTaskSessionForViewTest(t, fixture, started, "branch-c"),
	}
	snapshots := make([]runtimeactivity.ActiveSessionSnapshot, 0, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		fixture.setSessionCreatedAt(t, sessionID, int64(index+1)*1_000)
		snapshots = append(snapshots, runtimeactivity.ActiveSessionSnapshot{
			SessionID: sessionID.String(),
			Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityRunning),
		})
	}
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{snapshots: snapshots})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 1
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 1 ||
		response.Items[0].SessionID != sessionIDs[2].String() ||
		response.NextOffset == nil || *response.NextOffset != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestTaskSessionsPaginatesActiveThenLargeIdleHistory(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Paginated Task Sessions")
	runningID := insertRetainedTaskSessionForViewTest(t, fixture, started, 2_000)
	questionID := insertRetainedTaskSessionForViewTest(t, fixture, started, 3_000)
	idleIDs := make([]runtimeids.SessionID, 0, 105)
	for index := range 105 {
		idleIDs = append(idleIDs, insertRetainedTaskSessionForViewTest(
			t,
			fixture,
			started,
			int64(1_000+index),
		))
	}
	activityCalls := 0
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{
		calls: &activityCalls,
		snapshots: []runtimeactivity.ActiveSessionSnapshot{
			{
				SessionID: questionID.String(),
				Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityAwaitingPrompt),
			},
			{
				SessionID: runningID.String(),
				Activity:  taskSessionRuntimeActivity(t, clientui.RuntimeActivityRunning),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}
	expectedActivityCalls := 0

	one := 1
	offsetOne := 1
	expectedActivityCalls++
	withinActive, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Offset: &offsetOne,
		Limit:  &one,
	})
	if err != nil {
		t.Fatalf("List within active: %v", err)
	}
	if len(withinActive.Items) != 1 ||
		withinActive.Items[0].SessionID != questionID.String() ||
		withinActive.NextOffset == nil || *withinActive.NextOffset != 2 {
		t.Fatalf("within active = %+v", withinActive)
	}

	three := 3
	offsetTwo := 2
	expectedActivityCalls++
	spill, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Offset: &offsetTwo,
		Limit:  &three,
	})
	if err != nil {
		t.Fatalf("List spill: %v", err)
	}
	if len(spill.Items) != 3 ||
		spill.Items[0].SessionID != idleIDs[len(idleIDs)-1].String() ||
		spill.Items[1].SessionID != idleIDs[len(idleIDs)-2].String() ||
		spill.Items[2].SessionID != idleIDs[len(idleIDs)-3].String() ||
		spill.NextOffset == nil || *spill.NextOffset != 5 {
		t.Fatalf("spill = %+v", spill)
	}

	pageLimit := 19
	offset := 0
	walked := make([]string, 0, len(idleIDs)+2)
	for {
		pageOffset := offset
		expectedActivityCalls++
		page, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
			TaskID: string(started.task.ID),
			Offset: &pageOffset,
			Limit:  &pageLimit,
		})
		if err != nil {
			t.Fatalf("List walk offset %d: %v", offset, err)
		}
		if len(page.Items) > pageLimit {
			t.Fatalf("page at offset %d contains %d items, limit %d", offset, len(page.Items), pageLimit)
		}
		for _, item := range page.Items {
			walked = append(walked, item.SessionID)
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	want := []string{runningID.String(), questionID.String()}
	for index := len(idleIDs) - 1; index >= 0; index-- {
		want = append(want, idleIDs[index].String())
	}
	if fmt.Sprint(walked) != fmt.Sprint(want) {
		t.Fatalf("walked %d Sessions in the wrong order", len(walked))
	}

	beyondOffset := len(want) + 10
	expectedActivityCalls++
	beyond, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Offset: &beyondOffset,
		Limit:  &pageLimit,
	})
	if err != nil {
		t.Fatalf("List beyond end: %v", err)
	}
	if len(beyond.Items) != 0 || beyond.NextOffset != nil {
		t.Fatalf("beyond end = %+v", beyond)
	}
	if activityCalls != expectedActivityCalls {
		t.Fatalf("active snapshot calls = %d, want %d requests independent of %d Idle Sessions", activityCalls, expectedActivityCalls, len(idleIDs))
	}
}

func TestTaskSessionsKeepsDeletedNodeNameAbsent(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Deleted Node Task Session")
	sessionID := insertRetainedTaskSessionForViewTest(t, fixture, started, 1_000)
	if _, err := fixture.metadata.DB().ExecContext(
		fixture.ctx,
		`DELETE FROM session_workflow_node_associations WHERE session_id = ?`,
		sessionID.String(),
	); err != nil {
		t.Fatalf("delete Session Node association: %v", err)
	}
	readModel, err := NewTaskSessions(fixture.metadata, taskSessionActivitySource{})
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}

	limit := 1
	response, err := readModel.List(fixture.ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: string(started.task.ID),
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Items) != 1 ||
		response.Items[0].SessionID != sessionID.String() ||
		response.Items[0].NodeName != nil {
		t.Fatalf("items = %+v", response.Items)
	}
}

func insertRetainedTaskSessionForViewTest(
	t *testing.T,
	fixture currentNodeViewFixture,
	started startedCurrentNodeViewTask,
	createdAtUnixMs int64,
) runtimeids.SessionID {
	t.Helper()
	sessionID := runtimeids.NewSessionID()
	if err := fixture.metadata.Queries().UpsertSession(fixture.ctx, sqlitegen.UpsertSessionParams{
		ID:               sessionID.String(),
		ProjectID:        fixture.binding.ProjectID,
		WorkspaceID:      sql.NullString{String: fixture.binding.WorkspaceID, Valid: true},
		ArtifactRelpath:  "missing/" + sessionID.String(),
		Name:             "Session " + sessionID.String(),
		Category:         sql.NullString{String: string(sessioncontract.SessionCategoryMain), Valid: true},
		CreatedAtUnixMs:  createdAtUnixMs,
		UpdatedAtUnixMs:  createdAtUnixMs,
		CwdRelpath:       ".",
		ContinuationJson: "{}",
		LockedJson:       "{}",
		UsageStateJson:   "{}",
		MetadataJson:     "{}",
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if _, err := fixture.store.AssociateTaskSession(fixture.ctx, workflowstore.TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  started.currentNode,
		AssociatedAt: time.UnixMilli(createdAtUnixMs).UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	return sessionID
}

func associateTaskSessionForViewTest(
	t *testing.T,
	fixture currentNodeViewFixture,
	started startedCurrentNodeViewTask,
	branch string,
) runtimeids.SessionID {
	t.Helper()
	sessionID := fixture.newCurrentNodeViewSession(t)
	reference := started.currentNode
	if branch != "" {
		branchKey := workflow.TransitionBranchKey(branch)
		var err error
		reference, err = workflow.NewCurrentNodeReference(started.task.ID, started.currentNode.NodeID, &branchKey)
		if err != nil {
			t.Fatalf("NewCurrentNodeReference: %v", err)
		}
	}
	if _, err := fixture.store.AssociateTaskSession(fixture.ctx, workflowstore.TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  reference,
		AssociatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	return sessionID
}

func taskSessionRuntimeActivity(t *testing.T, state clientui.RuntimeActivityState) clientui.RuntimeActivity {
	t.Helper()
	runID, err := runtimeids.ParseRunID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseRunID: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return clientui.RuntimeActivity{
		State: state,
		ActiveStep: &clientui.RuntimeActiveStep{
			RunID:      runID,
			StepID:     stepID,
			ActiveKind: clientui.RuntimeActivityActiveKindWorkflowTurn,
		},
		QueueAccepting: true,
	}
}
