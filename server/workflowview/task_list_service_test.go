package workflowview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestListTasksDefaultSelectionAndOrdering(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	backlogTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Backlog task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	doneTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Done task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, doneTask.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}
	for taskID, timestamp := range map[string]int64{string(backlogTask.ID): 100, string(doneTask.ID): 200} {
		if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET created_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`, timestamp, timestamp, taskID); err != nil {
			t.Fatalf("force task timestamp %s: %v", taskID, err)
		}
	}

	resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if resp.ProjectID != binding.ProjectID || resp.WorkflowID != string(workflowID) || resp.SelectedWorkflow == nil || resp.SelectedWorkflow.WorkflowID != string(workflowID) || resp.GeneratedAtUnixMs == 0 {
		t.Fatalf("response identity = %+v, want selected workflow %s for project %s", resp, workflowID, binding.ProjectID)
	}
	if len(resp.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want two tasks", resp.Tasks)
	}
	if resp.Tasks[0].TaskID != string(backlogTask.ID) || resp.Tasks[0].StatusKeys[0] != "backlog" || resp.Tasks[0].RunStatus != serverapi.WorkflowTaskRunStatusOpen || resp.Tasks[0].RunCount != 0 {
		t.Fatalf("first task = %+v, want backlog open task", resp.Tasks[0])
	}
	if resp.Tasks[1].TaskID != string(doneTask.ID) || resp.Tasks[1].StatusKeys[0] != "done" || resp.Tasks[1].RunStatus != serverapi.WorkflowTaskRunStatusDone || resp.Tasks[1].RunCount != 1 {
		t.Fatalf("second task = %+v, want done task", resp.Tasks[1])
	}
	if resp.Tasks[0].CreatedAtUnixMs == 0 || resp.Tasks[0].UpdatedAtUnixMs == 0 || resp.Tasks[1].CreatedAtUnixMs == 0 || resp.Tasks[1].UpdatedAtUnixMs == 0 {
		t.Fatalf("tasks missing timestamps: %+v", resp.Tasks)
	}
}

func TestListTasksExcludesTasksWithoutBoardPlacement(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	firstVisibleTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "First visible", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask first visible: %v", err)
	}
	secondVisibleTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Second visible", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask second visible: %v", err)
	}
	orphanTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Orphan", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask orphan: %v", err)
	}
	if _, err := store.Queries().DeleteTaskNodePlacementsByTask(ctx, string(orphanTask.ID)); err != nil {
		t.Fatalf("remove orphan placements: %v", err)
	}
	for taskID, timestamp := range map[string]int64{string(firstVisibleTask.ID): 100, string(orphanTask.ID): 200, string(secondVisibleTask.ID): 300} {
		if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET created_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`, timestamp, timestamp, taskID); err != nil {
			t.Fatalf("force task timestamp %s: %v", taskID, err)
		}
	}

	first, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: binding.ProjectID,
		PageSize:  1,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldUpdated, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if len(first.Tasks) != 1 || first.Tasks[0].TaskID != string(firstVisibleTask.ID) || first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want first visible task and next page", first)
	}
	second, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID: binding.ProjectID,
		PageSize:  1,
		PageToken: first.NextPageToken,
		Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldUpdated, Direction: serverapi.WorkflowTaskListSortDirectionAsc}},
	}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks second page: %v", err)
	}
	if len(second.Tasks) != 1 || second.Tasks[0].TaskID != string(secondVisibleTask.ID) || second.NextPageToken != "" {
		t.Fatalf("second page = %+v, want second visible task and no next page", second)
	}
	openResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, RunStatuses: []serverapi.WorkflowTaskRunStatus{serverapi.WorkflowTaskRunStatusOpen}}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks open run-status filter: %v", err)
	}
	if !reflect.DeepEqual(taskListIDs(openResp.Tasks), taskListIDSet{string(firstVisibleTask.ID): true, string(secondVisibleTask.ID): true}) {
		t.Fatalf("open-filtered tasks = %+v, want only visible open tasks", openResp.Tasks)
	}
}

func TestListTasksSelectsDefaultExplicitAndNoWorkflow(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	defaultWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, defaultWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow default: %v", err)
	}
	selectedWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, selectedWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow selected: %v", err)
	}
	defaultTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: defaultWorkflowID, Title: "Default", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask default: %v", err)
	}
	selectedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: selectedWorkflowID, Title: "Selected", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask selected: %v", err)
	}

	defaultResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks default: %v", err)
	}
	if defaultResp.WorkflowID != string(defaultWorkflowID) || len(defaultResp.Tasks) != 1 || defaultResp.Tasks[0].TaskID != string(defaultTask.ID) {
		t.Fatalf("default response = %+v, want only default task %s", defaultResp, defaultTask.ID)
	}

	selectedResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, WorkflowID: string(selectedWorkflowID)}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks selected: %v", err)
	}
	if selectedResp.WorkflowID != string(selectedWorkflowID) || selectedResp.SelectedWorkflow == nil || selectedResp.SelectedWorkflow.WorkflowID != string(selectedWorkflowID) || len(selectedResp.Tasks) != 1 || selectedResp.Tasks[0].TaskID != string(selectedTask.ID) {
		t.Fatalf("selected response = %+v, want only selected task %s", selectedResp, selectedTask.ID)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, WorkflowID: "workflow-missing"}, workflow.StaticRoleResolver{"coder": true}); !isWorkflowRequestValidationField(err, "workflow_id") {
		t.Fatalf("ListTasks unknown workflow error = %v, want workflow_id validation", err)
	}

	otherBinding, err := store.CreateProjectForWorkspace(ctx, t.TempDir(), "Other")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace other: %v", err)
	}
	emptyResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: otherBinding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks no selectable workflow: %v", err)
	}
	if emptyResp.WorkflowID != "" || emptyResp.SelectedWorkflow != nil || len(emptyResp.Tasks) != 0 || emptyResp.NextPageToken != "" {
		t.Fatalf("empty response = %+v, want no selected workflow and no tasks", emptyResp)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: otherBinding.ProjectID, WorkflowID: "workflow-missing"}, workflow.StaticRoleResolver{"coder": true}); !isWorkflowRequestValidationField(err, "workflow_id") {
		t.Fatalf("ListTasks unknown workflow without selectable workflows error = %v, want workflow_id validation", err)
	}
	raw, err := json.Marshal(emptyResp)
	if err != nil {
		t.Fatalf("marshal empty response: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("unmarshal empty response: %v", err)
	}
	if _, ok := shape["workflow_id"]; !ok {
		t.Fatalf("empty response JSON missing workflow_id: %s", raw)
	}
	if _, ok := shape["selected_workflow"]; ok {
		t.Fatalf("empty response JSON includes selected_workflow: %s", raw)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: "project-missing"}, workflow.StaticRoleResolver{"coder": true}); !errors.Is(err, serverapi.ErrProjectNotFound) {
		t.Fatalf("ListTasks missing project error = %v, want ErrProjectNotFound", err)
	}
}

func TestListTasksVisibleStatusPlacementSemantics(t *testing.T) {
	t.Run("fanout excludes joins and orders active statuses by board order", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewFanoutWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Fanout", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		started, err := workflowStore.StartTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}}); err != nil {
			t.Fatalf("CompleteRun split: %v", err)
		}

		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(resp.Tasks) != 1 || !reflect.DeepEqual(resp.Tasks[0].StatusKeys, []string{"impl_a", "impl_b"}) {
			t.Fatalf("tasks = %+v, want fanout status keys [impl_a impl_b]", resp.Tasks)
		}
	})

	t.Run("pending approval uses transition source status", func(t *testing.T) {
		ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		requireDoneTransitionApproval(t, ctx, store, workflowID)
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Approval", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		started, err := workflowStore.StartTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
			t.Fatalf("CompleteRun pending: %v", err)
		}

		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(resp.Tasks) != 1 || !reflect.DeepEqual(resp.Tasks[0].StatusKeys, []string{"agent"}) || resp.Tasks[0].RunStatus != serverapi.WorkflowTaskRunStatusRunning {
			t.Fatalf("tasks = %+v, want pending approval on agent with running status", resp.Tasks)
		}
	})

	t.Run("canceled tasks use terminal status", func(t *testing.T) {
		ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Canceled", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		if err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
		forceCanceledBacklogPlacementWithoutTerminal(t, ctx, store, task.ID, workflowID)

		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(resp.Tasks) != 1 || !reflect.DeepEqual(resp.Tasks[0].StatusKeys, []string{"done"}) || resp.Tasks[0].RunStatus != serverapi.WorkflowTaskRunStatusCanceled {
			t.Fatalf("tasks = %+v, want canceled task under done with canceled run status", resp.Tasks)
		}
	})
}

func TestEffectiveVisibleBoardStatusPlacementsForCanceledTaskWithoutTerminalFallsBackToActiveVisiblePlacements(t *testing.T) {
	def := serverapi.WorkflowDefinition{
		Nodes: []serverapi.WorkflowNode{
			{ID: "node-backlog", Key: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog"},
			{ID: "node-join", Key: "join", Kind: string(workflow.NodeKindJoin), DisplayName: "Join"},
		},
	}
	nodeKinds := map[string]workflow.NodeKind{"node-backlog": workflow.NodeKindStart, "node-join": workflow.NodeKindJoin}
	task := sqlitegen.TaskRecord{ID: "task-1", CanceledAtUnixMs: 10, UpdatedAtUnixMs: 10}
	placements := []sqlitegen.TaskNodePlacementRecord{
		{ID: "placement-join", TaskID: task.ID, NodeID: sql.NullString{String: "node-join", Valid: true}, State: "active", ParallelBatchTransitionID: sql.NullString{}, ParallelBranchEdgeID: sql.NullString{}},
		{ID: "placement-backlog", TaskID: task.ID, NodeID: sql.NullString{String: "node-backlog", Valid: true}, State: "active", ParallelBatchTransitionID: sql.NullString{}, ParallelBranchEdgeID: sql.NullString{}},
	}

	visible := effectiveVisibleBoardStatusPlacementsForTask(task, placements, def, nodeKinds)
	keys, _ := workflowTaskStatusKeysAndOrder(visible, boardColumns(def))
	if !reflect.DeepEqual(keys, []string{"backlog"}) {
		t.Fatalf("visible canceled no-terminal keys = %+v, want [backlog]", keys)
	}
}

func TestListTasksFiltersByStatusAndRunStatusBeforePagination(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	backlogTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Backlog", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	runningTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Running", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}
	runningStarted, err := workflowStore.StartTask(ctx, runningTask.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := view.metadata.DB().ExecContext(ctx, `UPDATE task_runs SET started_at_unix_ms = 123, updated_at_unix_ms = 123 WHERE id = ?`, string(runningStarted.RunID)); err != nil {
		t.Fatalf("force running task run started: %v", err)
	}
	doneTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Done", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, doneTask.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}

	statusResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, StatusKeys: []string{"backlog", "done"}}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks status filter: %v", err)
	}
	if !reflect.DeepEqual(taskListIDs(statusResp.Tasks), taskListIDSet{string(backlogTask.ID): true, string(doneTask.ID): true}) {
		t.Fatalf("status-filtered tasks = %+v, want backlog and done", statusResp.Tasks)
	}

	runningResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, RunStatuses: []serverapi.WorkflowTaskRunStatus{serverapi.WorkflowTaskRunStatusRunning}}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks run-status filter: %v", err)
	}
	if len(runningResp.Tasks) != 1 || runningResp.Tasks[0].TaskID != string(runningTask.ID) {
		t.Fatalf("running-filtered tasks = %+v, want running task %s", runningResp.Tasks, runningTask.ID)
	}

	andResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, StatusKeys: []string{"agent"}, RunStatuses: []serverapi.WorkflowTaskRunStatus{serverapi.WorkflowTaskRunStatusRunning}}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks composed filter: %v", err)
	}
	if len(andResp.Tasks) != 1 || andResp.Tasks[0].TaskID != string(runningTask.ID) {
		t.Fatalf("composed-filtered tasks = %+v, want running agent task %s", andResp.Tasks, runningTask.ID)
	}

	pagedDoneResp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, StatusKeys: []string{"done"}, PageSize: 1}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks filtered page: %v", err)
	}
	if len(pagedDoneResp.Tasks) != 1 || pagedDoneResp.Tasks[0].TaskID != string(doneTask.ID) {
		t.Fatalf("filtered page = %+v, want done task despite page size 1", pagedDoneResp.Tasks)
	}

	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, StatusKeys: []string{"missing"}}, workflow.StaticRoleResolver{"coder": true}); !isWorkflowRequestValidationField(err, "status_keys[0]") {
		t.Fatalf("unknown status key error = %#v, want invalid status_keys[0]", err)
	}
}

type taskListIDSet map[string]bool

func taskListIDs(tasks []serverapi.WorkflowTaskListItem) taskListIDSet {
	out := taskListIDSet{}
	for _, task := range tasks {
		out[task.TaskID] = true
	}
	return out
}

func TestListTasksRunStatusAndRunCountUseCurrentPlacements(t *testing.T) {
	t.Run("running statuses match current active states", func(t *testing.T) {
		ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		requireDoneTransitionApproval(t, ctx, store, workflowID)
		wantRunningIDs := taskListIDSet{}

		startedTask := createWorkflowViewTaskWithStartedRun(t, ctx, view, workflowStore, binding.ProjectID, workflowID, "Started")
		wantRunningIDs[string(startedTask.ID)] = true

		interruptedTask, interruptedRunID := createWorkflowViewTaskWithClaimedRun(t, ctx, workflowStore, binding.ProjectID, workflowID, "Interrupted")
		if err := workflowStore.InterruptRunGeneration(ctx, interruptedRunID, 1, "manual", "{}"); err != nil {
			t.Fatalf("InterruptRunGeneration: %v", err)
		}
		wantRunningIDs[string(interruptedTask.ID)] = true

		questionTask, questionRunID := createWorkflowViewTaskWithClaimedRun(t, ctx, workflowStore, binding.ProjectID, workflowID, "Question")
		if err := workflowStore.SetRunWaitingAsk(ctx, questionRunID, 1, "ask-1"); err != nil {
			t.Fatalf("SetRunWaitingAsk: %v", err)
		}
		wantRunningIDs[string(questionTask.ID)] = true

		waitingApprovalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Waiting approval placement", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask waiting approval: %v", err)
		}
		waitingApprovalStarted, err := workflowStore.StartTask(ctx, waitingApprovalTask.ID)
		if err != nil {
			t.Fatalf("StartTask waiting approval: %v", err)
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE task_node_placements SET state = 'waiting_approval' WHERE id = ?`, string(waitingApprovalStarted.PlacementID)); err != nil {
			t.Fatalf("force waiting approval placement: %v", err)
		}
		wantRunningIDs[string(waitingApprovalTask.ID)] = true

		pendingTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Pending approval transition", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask pending approval: %v", err)
		}
		pendingStarted, err := workflowStore.StartTask(ctx, pendingTask.ID)
		if err != nil {
			t.Fatalf("StartTask pending approval: %v", err)
		}
		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: pendingStarted.RunID, TransitionID: "done"}); err != nil {
			t.Fatalf("CompleteRun pending approval: %v", err)
		}
		wantRunningIDs[string(pendingTask.ID)] = true

		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, RunStatuses: []serverapi.WorkflowTaskRunStatus{serverapi.WorkflowTaskRunStatusRunning}}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks running: %v", err)
		}
		if !reflect.DeepEqual(taskListIDs(resp.Tasks), wantRunningIDs) {
			t.Fatalf("running ids = %+v, want %+v; tasks=%+v", taskListIDs(resp.Tasks), wantRunningIDs, resp.Tasks)
		}
	})

	t.Run("stale interrupted runs from superseded placements do not match running", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		task, runID := createWorkflowViewTaskWithClaimedRun(t, ctx, workflowStore, binding.ProjectID, workflowID, "Stale")
		if err := workflowStore.InterruptRunGeneration(ctx, runID, 1, "manual", "{}"); err != nil {
			t.Fatalf("InterruptRunGeneration: %v", err)
		}
		def, _, err := workflowStore.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
		if _, err := workflowStore.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start), AllowMissingEdge: true}); err != nil {
			t.Fatalf("ManualMoveTask reset: %v", err)
		}

		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, RunStatuses: []serverapi.WorkflowTaskRunStatus{serverapi.WorkflowTaskRunStatusRunning}}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks running: %v", err)
		}
		if len(resp.Tasks) != 0 {
			t.Fatalf("running-filtered tasks = %+v, want stale interrupted run ignored", resp.Tasks)
		}
	})

	t.Run("fanout run count is not multiplied by placements", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewFanoutWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Fanout", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask fanout: %v", err)
		}
		started, err := workflowStore.StartTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("StartTask fanout: %v", err)
		}
		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}}); err != nil {
			t.Fatalf("CompleteRun split: %v", err)
		}

		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(resp.Tasks) != 1 || resp.Tasks[0].RunCount != 3 {
			t.Fatalf("fanout tasks = %+v, want exact run_count 3 without placement multiplication", resp.Tasks)
		}
	})
}

func createWorkflowViewTaskWithStartedRun(t *testing.T, ctx context.Context, view *Service, workflowStore *workflowstore.Store, projectID string, workflowID workflow.WorkflowID, title string) workflowstore.TaskRecord {
	t.Helper()
	task, runID := createWorkflowViewTaskWithClaimedRun(t, ctx, workflowStore, projectID, workflowID, title)
	if _, err := view.metadata.DB().ExecContext(ctx, `UPDATE task_runs SET started_at_unix_ms = 123, updated_at_unix_ms = 123 WHERE id = ?`, string(runID)); err != nil {
		t.Fatalf("force started run: %v", err)
	}
	return task
}

func createWorkflowViewTaskWithClaimedRun(t *testing.T, ctx context.Context, workflowStore *workflowstore.Store, projectID string, workflowID workflow.WorkflowID, title string) (workflowstore.TaskRecord, workflow.RunID) {
	t.Helper()
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: projectID, WorkflowID: workflowID, Title: title, Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask %s: %v", title, err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask %s: %v", title, err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun %s: %v", title, err)
	}
	return task, workflow.RunID(claimed.ID)
}

func TestListTasksSortDescriptorsAndCursorPagination(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	backlogTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Alpha", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	runningTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Bravo", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}
	runningStarted, err := workflowStore.StartTask(ctx, runningTask.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_runs SET started_at_unix_ms = 123, updated_at_unix_ms = 123 WHERE id = ?`, string(runningStarted.RunID)); err != nil {
		t.Fatalf("force running task run started: %v", err)
	}
	doneTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Charlie", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	doneStarted, err := workflowStore.StartTask(ctx, doneTask.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: doneStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}
	for taskID, values := range map[string]struct {
		created int64
		updated int64
	}{
		string(backlogTask.ID): {created: 100, updated: 300},
		string(runningTask.ID): {created: 200, updated: 200},
		string(doneTask.ID):    {created: 300, updated: 100},
	} {
		if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET created_at_unix_ms = ?, updated_at_unix_ms = ? WHERE id = ?`, values.created, values.updated, taskID); err != nil {
			t.Fatalf("force timestamps %s: %v", taskID, err)
		}
	}

	cases := []struct {
		name      string
		field     serverapi.WorkflowTaskListSortField
		direction serverapi.WorkflowTaskListSortDirection
		wantNames []string
	}{
		{name: "created asc", field: serverapi.WorkflowTaskListSortFieldCreated, direction: serverapi.WorkflowTaskListSortDirectionAsc, wantNames: []string{"Alpha", "Bravo", "Charlie"}},
		{name: "created desc", field: serverapi.WorkflowTaskListSortFieldCreated, direction: serverapi.WorkflowTaskListSortDirectionDesc, wantNames: []string{"Charlie", "Bravo", "Alpha"}},
		{name: "updated asc", field: serverapi.WorkflowTaskListSortFieldUpdated, direction: serverapi.WorkflowTaskListSortDirectionAsc, wantNames: []string{"Charlie", "Bravo", "Alpha"}},
		{name: "updated desc", field: serverapi.WorkflowTaskListSortFieldUpdated, direction: serverapi.WorkflowTaskListSortDirectionDesc, wantNames: []string{"Alpha", "Bravo", "Charlie"}},
		{name: "status asc", field: serverapi.WorkflowTaskListSortFieldStatus, direction: serverapi.WorkflowTaskListSortDirectionAsc, wantNames: []string{"Alpha", "Bravo", "Charlie"}},
		{name: "status desc", field: serverapi.WorkflowTaskListSortFieldStatus, direction: serverapi.WorkflowTaskListSortDirectionDesc, wantNames: []string{"Charlie", "Bravo", "Alpha"}},
		{name: "title asc", field: serverapi.WorkflowTaskListSortFieldTitle, direction: serverapi.WorkflowTaskListSortDirectionAsc, wantNames: []string{"Alpha", "Bravo", "Charlie"}},
		{name: "title desc", field: serverapi.WorkflowTaskListSortFieldTitle, direction: serverapi.WorkflowTaskListSortDirectionDesc, wantNames: []string{"Charlie", "Bravo", "Alpha"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
				ProjectID: binding.ProjectID,
				Sort:      []serverapi.WorkflowTaskListSort{{Field: tt.field, Direction: tt.direction}},
			}, workflow.StaticRoleResolver{"coder": true})
			if err != nil {
				t.Fatalf("ListTasks: %v", err)
			}
			if got := taskListTitles(resp.Tasks); !reflect.DeepEqual(got, tt.wantNames) {
				t.Fatalf("titles = %+v, want %+v", got, tt.wantNames)
			}
		})
	}

	for _, direction := range []serverapi.WorkflowTaskListSortDirection{serverapi.WorkflowTaskListSortDirectionAsc, serverapi.WorkflowTaskListSortDirectionDesc} {
		resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
			ProjectID: binding.ProjectID,
			Sort:      []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldRunCount, Direction: direction}},
		}, workflow.StaticRoleResolver{"coder": true})
		if err != nil {
			t.Fatalf("ListTasks run_count %s: %v", direction, err)
		}
		if direction == serverapi.WorkflowTaskListSortDirectionAsc && resp.Tasks[0].RunCount != 0 {
			t.Fatalf("run_count asc tasks = %+v, want zero-run task first", resp.Tasks)
		}
		if direction == serverapi.WorkflowTaskListSortDirectionDesc && resp.Tasks[len(resp.Tasks)-1].RunCount != 0 {
			t.Fatalf("run_count desc tasks = %+v, want zero-run task last", resp.Tasks)
		}
	}

	first, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, PageSize: 1}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if len(first.Tasks) != 1 || first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want one task and next token", first)
	}
	second, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, PageSize: 2, PageToken: first.NextPageToken}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks second page with omitted workflow and changed page size: %v", err)
	}
	if len(second.Tasks) != 2 || second.Tasks[0].TaskID == first.Tasks[0].TaskID {
		t.Fatalf("second page = %+v first=%+v, want remaining tasks without duplicate", second, first)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, PageToken: first.NextPageToken, StatusKeys: []string{"done"}}, workflow.StaticRoleResolver{"coder": true}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("mismatched filter token error = %v, want ErrInvalidPageToken", err)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, PageToken: first.NextPageToken, Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}}, workflow.StaticRoleResolver{"coder": true}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("mismatched sort token error = %v, want ErrInvalidPageToken", err)
	}
	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID, PageSize: 1}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, PageToken: board.NextPageToken}, workflow.StaticRoleResolver{"coder": true}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("old board token error = %v, want ErrInvalidPageToken", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE workflows SET version = version + 1 WHERE id = ?`, string(workflowID)); err != nil {
		t.Fatalf("force workflow version change: %v", err)
	}
	if _, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, PageToken: first.NextPageToken}, workflow.StaticRoleResolver{"coder": true}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("workflow structure token error = %v, want ErrInvalidPageToken", err)
	}
}

func TestListTasksHiddenTaskIDTieBreaker(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	firstTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Same", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask first: %v", err)
	}
	secondTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Same", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask second: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET created_at_unix_ms = 100, updated_at_unix_ms = 100 WHERE id IN (?, ?)`, string(firstTask.ID), string(secondTask.ID)); err != nil {
		t.Fatalf("force matching timestamps: %v", err)
	}

	resp, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID, Sort: []serverapi.WorkflowTaskListSort{{Field: serverapi.WorkflowTaskListSortFieldTitle, Direction: serverapi.WorkflowTaskListSortDirectionAsc}}}, workflow.StaticRoleResolver{"coder": true})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	wantIDs := []string{string(firstTask.ID), string(secondTask.ID)}
	sort.Strings(wantIDs)
	if gotIDs := taskListTaskIDs(resp.Tasks); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("tie-break ids = %+v, want %+v", gotIDs, wantIDs)
	}
}

func taskListTitles(tasks []serverapi.WorkflowTaskListItem) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.Title)
	}
	return out
}

func taskListTaskIDs(tasks []serverapi.WorkflowTaskListItem) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.TaskID)
	}
	return out
}
