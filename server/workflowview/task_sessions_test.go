package workflowview

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/runtimeactivity"
	"core/server/workflow"
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

func TestTaskSessionsProjectsActiveParallelOrdinaryAndIdleMetadata(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Task Session projection")
	namedIdleID := fixture.bindCurrentNodeSession(t, started)
	namedRole := "reviewer"
	if _, err := fixture.metadata.DB().ExecContext(fixture.ctx, `UPDATE sessions SET continuation_json = ? WHERE id = ?`, `{"agent_role":"reviewer"}`, namedIdleID.String()); err != nil {
		t.Fatalf("persist named role: %v", err)
	}
	runningID := associateTaskSessionForViewTest(t, fixture, started, "running")
	questionID := associateTaskSessionForViewTest(t, fixture, started, "question")
	ordinaryID := associateTaskSessionForViewTest(t, fixture, started, "")
	registeredIdleID := insertRetainedTaskSessionForViewTest(t, fixture, started, 2_000)
	missingNodeID := insertRetainedTaskSessionForViewTest(t, fixture, started, 1_000)
	if _, err := fixture.metadata.DB().ExecContext(
		fixture.ctx,
		`DELETE FROM session_workflow_node_associations WHERE session_id = ?`,
		missingNodeID.String(),
	); err != nil {
		t.Fatalf("delete Session Node association: %v", err)
	}
	fixture.setSessionCreatedAt(t, namedIdleID, 3_000)
	fixture.setSessionCreatedAt(t, runningID, 4_000)
	fixture.setSessionCreatedAt(t, questionID, 6_000)
	fixture.setSessionCreatedAt(t, ordinaryID, 5_000)

	readModel := newTaskSessionsForTest(t, fixture, taskSessionActivitySource{
		snapshots: []runtimeactivity.ActiveSessionSnapshot{
			taskSessionSnapshot(t, questionID, clientui.RuntimeActivityAwaitingPrompt),
			taskSessionSnapshot(t, registeredIdleID, clientui.RuntimeActivityRegisteredIdle),
			taskSessionSnapshot(t, runningID, clientui.RuntimeActivityRunning),
			taskSessionSnapshot(t, ordinaryID, clientui.RuntimeActivityRunning),
		},
	})
	response := listTaskSessionsForTest(t, readModel, string(started.task.ID), 0, 10)
	if len(response.Items) != 6 {
		t.Fatalf("items = %+v, want six retained Sessions", response.Items)
	}
	want := []struct {
		id     runtimeids.SessionID
		status serverapi.WorkflowTaskSessionStatus
	}{
		{id: ordinaryID, status: serverapi.WorkflowTaskSessionStatusRunning},
		{id: runningID, status: serverapi.WorkflowTaskSessionStatusRunning},
		{id: questionID, status: serverapi.WorkflowTaskSessionStatusQuestion},
		{id: namedIdleID, status: serverapi.WorkflowTaskSessionStatusIdle},
		{id: registeredIdleID, status: serverapi.WorkflowTaskSessionStatusIdle},
		{id: missingNodeID, status: serverapi.WorkflowTaskSessionStatusIdle},
	}
	for index, expected := range want {
		item := response.Items[index]
		if item.SessionID != expected.id.String() || item.Status != expected.status {
			t.Fatalf("item %d = %+v, want Session %s status %s", index, item, expected.id, expected.status)
		}
	}
	namedIdle := response.Items[3]
	if namedIdle.SessionName == nil || *namedIdle.SessionName != "Current Node session" ||
		namedIdle.NodeName == nil || *namedIdle.NodeName != "Agent" ||
		namedIdle.AgentRole != namedRole {
		t.Fatalf("named Idle metadata = %+v", namedIdle)
	}
	if response.Items[4].AgentRole != workflow.DefaultAgentRole || response.Items[5].NodeName != nil {
		t.Fatalf("fallback metadata = %+v, %+v", response.Items[4], response.Items[5])
	}
}

func TestTaskSessionsPaginatesActiveThenLargeIdleHistoryBoundedly(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Paginated Task Sessions")
	runningID := insertRetainedTaskSessionForViewTest(t, fixture, started, 2_000)
	questionID := insertRetainedTaskSessionForViewTest(t, fixture, started, 3_000)
	idleIDs := make([]runtimeids.SessionID, 0, 105)
	for index := range 105 {
		idleIDs = append(idleIDs, insertRetainedTaskSessionForViewTest(t, fixture, started, int64(1_000+index)))
	}
	activityCalls := 0
	readModel := newTaskSessionsForTest(t, fixture, taskSessionActivitySource{
		calls: &activityCalls,
		snapshots: []runtimeactivity.ActiveSessionSnapshot{
			taskSessionSnapshot(t, questionID, clientui.RuntimeActivityAwaitingPrompt),
			taskSessionSnapshot(t, runningID, clientui.RuntimeActivityRunning),
		},
	})

	tests := []struct {
		name      string
		offset    int
		limit     int
		wantFirst runtimeids.SessionID
		wantCount int
		wantNext  *int
	}{
		{name: "active page", offset: 0, limit: 1, wantFirst: runningID, wantCount: 1, wantNext: intPointer(1)},
		{name: "active offset", offset: 1, limit: 1, wantFirst: questionID, wantCount: 1, wantNext: intPointer(2)},
		{name: "spill into Idle", offset: 2, limit: 3, wantFirst: idleIDs[104], wantCount: 3, wantNext: intPointer(5)},
		{name: "past one hundred", offset: 100, limit: 5, wantFirst: idleIDs[6], wantCount: 5, wantNext: intPointer(105)},
		{name: "beyond end", offset: 117, limit: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := listTaskSessionsForTest(t, readModel, string(started.task.ID), test.offset, test.limit)
			if len(response.Items) != test.wantCount {
				t.Fatalf("items = %+v, want %d", response.Items, test.wantCount)
			}
			if test.wantCount > 0 && response.Items[0].SessionID != test.wantFirst.String() {
				t.Fatalf("first item = %s, want %s", response.Items[0].SessionID, test.wantFirst)
			}
			if !equalOptionalInt(response.NextOffset, test.wantNext) {
				t.Fatalf("next offset = %v, want %v", response.NextOffset, test.wantNext)
			}
		})
	}
	if activityCalls != len(tests) {
		t.Fatalf("active snapshot calls = %d, want one per request independent of %d Idle Sessions", activityCalls, len(idleIDs))
	}
}

func newTaskSessionsForTest(
	t *testing.T,
	fixture currentNodeViewFixture,
	activities taskSessionActivitySource,
) *TaskSessions {
	t.Helper()
	readModel, err := NewTaskSessions(fixture.metadata, activities)
	if err != nil {
		t.Fatalf("NewTaskSessions: %v", err)
	}
	return readModel
}

func listTaskSessionsForTest(
	t *testing.T,
	readModel *TaskSessions,
	taskID string,
	offset int,
	limit int,
) serverapi.WorkflowTaskSessionListResponse {
	t.Helper()
	response, err := readModel.List(t.Context(), serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: taskID,
		Offset: &offset,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("List Task Sessions: %v", err)
	}
	if len(response.Items) > limit {
		t.Fatalf("page contains %d items, limit %d", len(response.Items), limit)
	}
	return response
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
	associateHistoricalTaskSessionForViewTest(t, fixture, started.currentNode, sessionID, createdAtUnixMs)
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
	associateHistoricalTaskSessionForViewTest(t, fixture, reference, sessionID, time.Now().UnixMilli())
	return sessionID
}
func associateHistoricalTaskSessionForViewTest(t *testing.T, fixture currentNodeViewFixture, reference workflow.CurrentNodeReference, sessionID runtimeids.SessionID, associatedAt int64) {
	t.Helper()
	branch, scoped := reference.TransitionBranchKey()
	db := fixture.metadata.DB()
	if _, err := db.ExecContext(fixture.ctx, `UPDATE sessions SET task_id = ? WHERE id = ?`, string(reference.TaskID), sessionID.String()); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(fixture.ctx, `INSERT INTO session_workflow_node_associations
(task_id, session_id, node_id, transition_branch_key, association_status, source_session_id, associated_at_unix_ms)
VALUES (?, ?, ?, ?, 'historical', NULL, ?)`, string(reference.TaskID), sessionID.String(), string(reference.NodeID),
		sql.NullString{String: string(branch), Valid: scoped}, associatedAt)
	if err != nil {
		t.Fatalf("insert historical Task Session association: %v", err)
	}
}
func taskSessionSnapshot(
	t *testing.T,
	sessionID runtimeids.SessionID,
	state clientui.RuntimeActivityState,
) runtimeactivity.ActiveSessionSnapshot {
	t.Helper()
	return runtimeactivity.ActiveSessionSnapshot{
		SessionID: sessionID.String(),
		Activity:  taskSessionRuntimeActivity(t, state),
	}
}

func taskSessionRuntimeActivity(t *testing.T, state clientui.RuntimeActivityState) clientui.RuntimeActivity {
	t.Helper()
	if !(clientui.RuntimeActivity{State: state}).ActiveForControl() {
		return clientui.RuntimeActivity{State: state, QueueAccepting: state == clientui.RuntimeActivityRegisteredIdle}
	}
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
			RunID: runID, StepID: stepID, ActiveKind: clientui.RuntimeActivityActiveKindWorkflowTurn,
		},
		QueueAccepting: true,
	}
}

func equalOptionalInt(left *int, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
