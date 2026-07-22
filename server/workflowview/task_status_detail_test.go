package workflowview

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"core/server/metadata"
	"core/server/runtime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestPendingApprovalTaskRemainsVisibleOnSourceBoardColumn(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "BUI-7", Body: "Waiting approval"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	pending, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if pending.Result.State != "pending_approval" {
		t.Fatalf("completion state = %q, want pending_approval", pending.Result.State)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	sourceColumn := workflowViewColumnByKey(t, board, "agent")
	if sourceColumn.TaskCount != 1 {
		t.Fatalf("source column task count = %d, want pending approval task in source column: %+v", sourceColumn.TaskCount, board.Columns)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	if doneColumn.TaskCount != 0 {
		t.Fatalf("done column task count = %d, want pending approval task not done yet", doneColumn.TaskCount)
	}
	sourcePage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: sourceColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards source: %v", err)
	}
	if len(sourcePage.Cards) != 1 {
		t.Fatalf("source cards = %+v, want pending approval task", sourcePage.Cards)
	}
	card := sourcePage.Cards[0]
	if card.ShortID != task.ShortID || card.Status.Kind != "waiting_approval" || len(card.Status.AttentionTypes) != 1 || card.Status.AttentionTypes[0] != "approval" {
		t.Fatalf("pending approval card = %+v", card)
	}
	if len(card.ActiveNodeIDs) != 1 || card.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("pending approval active nodes = %+v, want source node %s", card.ActiveNodeIDs, sourceColumn.Node.NodeID)
	}
	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind != "waiting_approval" || len(detail.Summary.ActiveNodeIDs) != 1 || detail.Summary.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("task detail = %+v, want pending approval at source node %s", detail, sourceColumn.Node.NodeID)
	}
	byShortID, err := view.detail(t).GetTaskByProjectShortID(ctx, binding.ProjectID, task.ShortID)
	if err != nil {
		t.Fatalf("GetTaskByProjectShortID: %v", err)
	}
	if byShortID.Status.Kind != "waiting_approval" || len(byShortID.Summary.ActiveNodeIDs) != 1 || byShortID.Summary.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("task detail by short id = %+v, want pending approval at source node %s", byShortID, sourceColumn.Node.NodeID)
	}
	byGlobalShortID, err := view.detail(t).GetTaskByShortID(ctx, task.ShortID)
	if err != nil {
		t.Fatalf("GetTaskByShortID: %v", err)
	}
	if byGlobalShortID.Status.Kind != "waiting_approval" || len(byGlobalShortID.Summary.ActiveNodeIDs) != 1 || byGlobalShortID.Summary.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("task detail by global short id = %+v, want pending approval at source node %s", byGlobalShortID, sourceColumn.Node.NodeID)
	}
}

func TestTaskStatusIgnoresHistoricalRunUnderCompletedPlacement(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Awaiting approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	// Intentional historical-corruption fixture: prior binaries could leave an
	// unfinished run below this completed source placement. Read projections
	// must never treat that history as current task state.
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_runs SET completed_at_unix_ms = NULL, waiting_ask_id = 'stale-ask' WHERE id = ?`, string(started.RunID)); err != nil {
		t.Fatalf("create stale historical run fixture: %v", err)
	}

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindWaitingApproval || slices.Contains(detail.Status.RunIDs, string(started.RunID)) {
		t.Fatalf("detail status = %+v, want waiting approval without stale run", detail.Status)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	sourceColumn := workflowViewColumnByKey(t, board, "agent")
	cards, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: sourceColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 || cards.Cards[0].Status.Kind != serverapi.WorkflowTaskStatusKindWaitingApproval || slices.Contains(cards.Cards[0].Status.RunIDs, string(started.RunID)) {
		t.Fatalf("board cards = %+v, want waiting approval without stale run", cards.Cards)
	}
}

func TestTaskDetailRequiresExactLiveAuthorityForLivenessStatuses(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	createTask := func(title string) workflowstore.TaskRecord {
		t.Helper()
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: title, Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return task
	}

	backlog := createTask("Backlog")
	queued := createTask("Queued")
	queuedStarted, err := workflowStore.StartTask(ctx, queued.ID)
	if err != nil {
		t.Fatalf("StartTask queued: %v", err)
	}
	queuedToRunning := createTask("Queued to running")
	queuedToRunningStarted, err := workflowStore.StartTask(ctx, queuedToRunning.ID)
	if err != nil {
		t.Fatalf("StartTask queued to running: %v", err)
	}
	queuedToRunningBefore := mustTaskDetail(t, view, ctx, string(queuedToRunning.ID))
	queuedToRunningPlacementsBefore, err := workflowStore.ListPlacements(ctx, queuedToRunning.ID)
	if err != nil {
		t.Fatalf("ListPlacements queued to running before claim: %v", err)
	}
	if queuedToRunningBefore.Status.Kind != serverapi.WorkflowTaskStatusKindActive {
		t.Fatalf("queued task detail status before claim = %+v, want active without live authority", queuedToRunningBefore.Status)
	}
	if _, err := workflowStore.ClaimRun(ctx, queuedToRunningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun queued to running: %v", err)
	}
	queuedToRunningAfter := mustTaskDetail(t, view, ctx, string(queuedToRunning.ID))
	queuedToRunningPlacementsAfter, err := workflowStore.ListPlacements(ctx, queuedToRunning.ID)
	if err != nil {
		t.Fatalf("ListPlacements queued to running after claim: %v", err)
	}
	if queuedToRunningAfter.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		!reflect.DeepEqual(queuedToRunningBefore.Status.NodeIDs, queuedToRunningAfter.Status.NodeIDs) ||
		!reflect.DeepEqual(queuedToRunningPlacementsBefore, queuedToRunningPlacementsAfter) {
		t.Fatalf("durable claim changed exact-live detail or placement facts: before=%+v/%+v after=%+v/%+v", queuedToRunningBefore.Status, queuedToRunningPlacementsBefore, queuedToRunningAfter.Status, queuedToRunningPlacementsAfter)
	}
	active := createTask("Active")
	activeStarted, err := workflowStore.StartTask(ctx, active.ID)
	if err != nil {
		t.Fatalf("StartTask active: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM task_runs WHERE id = ?`, string(activeStarted.RunID)); err != nil {
		t.Fatalf("create active placement fixture: %v", err)
	}
	running := createTask("Running")
	runningStarted, err := workflowStore.StartTask(ctx, running.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, runningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun running: %v", err)
	}
	interrupted := createTask("Interrupted")
	interruptedStarted, err := workflowStore.StartTask(ctx, interrupted.ID)
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
	question := createTask("Question")
	questionStarted, err := workflowStore.StartTask(ctx, question.ID)
	if err != nil {
		t.Fatalf("StartTask question: %v", err)
	}
	questionClaimed, err := workflowStore.ClaimRun(ctx, questionStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun question: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, questionStarted.RunID, questionClaimed.Generation, "ask-status"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	done := createTask("Done")
	doneStarted, err := workflowStore.StartTask(ctx, done.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: doneStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	approval := createTask("Approval")
	approvalStarted, err := workflowStore.StartTask(ctx, approval.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}
	canceled := createTask("Canceled")
	if _, err := workflowStore.StartTask(ctx, canceled.ID); err != nil {
		t.Fatalf("StartTask canceled: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	boardStatus := map[string]serverapi.WorkflowTaskStatus{}
	for _, column := range board.Columns {
		page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   binding.ProjectID,
			WorkflowID:  string(workflowID),
			NodeID:      column.Node.NodeID,
		})

		if err != nil {
			t.Fatalf("ListBoardNodeCards %s: %v", column.Node.Key, err)
		}
		for _, card := range page.Cards {
			boardStatus[card.TaskID] = card.Status
		}
	}
	wantBoard := map[string]serverapi.WorkflowTaskStatusKind{
		string(backlog.ID):     serverapi.WorkflowTaskStatusKindBacklog,
		string(active.ID):      serverapi.WorkflowTaskStatusKindActive,
		string(queued.ID):      serverapi.WorkflowTaskStatusKindQueued,
		string(running.ID):     serverapi.WorkflowTaskStatusKindRunning,
		string(interrupted.ID): serverapi.WorkflowTaskStatusKindInterrupted,
		string(question.ID):    serverapi.WorkflowTaskStatusKindWaitingQuestion,
		string(done.ID):        serverapi.WorkflowTaskStatusKindDone,
		string(approval.ID):    serverapi.WorkflowTaskStatusKindWaitingApproval,
		string(canceled.ID):    serverapi.WorkflowTaskStatusKindCanceled,
	}
	wantDetail := map[string]serverapi.WorkflowTaskStatusKind{}
	for taskID, kind := range wantBoard {
		wantDetail[taskID] = kind
	}
	wantDetail[string(queued.ID)] = serverapi.WorkflowTaskStatusKindActive
	wantDetail[string(running.ID)] = serverapi.WorkflowTaskStatusKindActive
	wantDetail[string(question.ID)] = serverapi.WorkflowTaskStatusKindActive
	for taskID, wantKind := range wantDetail {
		detail, err := view.detail(t).GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("GetTask %s: %v", taskID, err)
		}
		if detail.Status.Kind != wantKind {
			t.Fatalf("detail status for %s = %+v, want %q", taskID, detail.Status, wantKind)
		}
		cardStatus, ok := boardStatus[taskID]
		if !ok || cardStatus.Kind != wantBoard[taskID] {
			t.Fatalf("board status for %s = %+v, want %q", taskID, cardStatus, wantBoard[taskID])
		}
	}
	if !mustTaskDetail(t, view, ctx, string(canceled.ID)).Summary.Done {
		t.Fatal("canceled task must retain active terminal-sink Done position")
	}
	if queuedStarted.RunID == "" {
		t.Fatal("queued fixture must retain its unstarted run")
	}
}

func TestDurableFanoutStatusAndExactLiveTaskDetailRemainDistinct(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	fixture := createWorkflowViewFanoutStatusFixture(t, ctx, workflowStore, binding)

	detail := mustTaskDetail(t, view, ctx, string(fixture.task.ID))
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		len(detail.Status.RunIDs) != 0 ||
		!reflect.DeepEqual(detail.Status.AttentionTypes, []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindInterrupted}) {
		t.Fatalf("detail status = %+v, want durable attention without unproven liveness", detail.Status)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	cards := make([]serverapi.WorkflowBoardTaskCard, 0, 3)
	for _, key := range []string{"impl_a", "impl_b", "impl_c"} {
		column := workflowViewColumnByKey(t, board, key)
		page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   binding.ProjectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      column.Node.NodeID,
		})

		if err != nil {
			t.Fatalf("ListBoardNodeCards %s: %v", key, err)
		}
		for _, card := range page.Cards {
			if card.TaskID == string(fixture.task.ID) {
				cards = append(cards, card)
			}
		}
	}
	if len(cards) != 3 {
		t.Fatalf("fanout board projections = %+v, want every branch card", cards)
	}
	for _, card := range cards {
		if card.Status.Kind != fixture.status.Kind ||
			card.Status.NativeState != fixture.status.NativeState ||
			!reflect.DeepEqual(card.Status.RunIDs, fixture.status.RunIDs) ||
			!reflect.DeepEqual(card.Status.AttentionTypes, fixture.status.AttentionTypes) {
			t.Fatalf("board status = %+v, want durable fanout status %+v", card.Status, fixture.status)
		}
		if !card.Actions.CanInterrupt || !card.Actions.CanResume {
			t.Fatalf("fanout board actions = %+v, want simultaneous interrupt and resume", card.Actions)
		}
	}
	workflowIDString := string(fixture.workflowID)
	tasks, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &binding.ProjectID,
		WorkflowID:  &workflowIDString,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
	})

	if err != nil ||
		len(tasks.Tasks) != 1 ||
		tasks.Tasks[0].TaskID != string(fixture.task.ID) ||
		tasks.Tasks[0].Status.Kind != fixture.status.Kind ||
		tasks.Tasks[0].Status.NativeState != fixture.status.NativeState ||
		!reflect.DeepEqual(tasks.Tasks[0].Status.RunIDs, fixture.status.RunIDs) ||
		!reflect.DeepEqual(tasks.Tasks[0].Status.AttentionTypes, fixture.status.AttentionTypes) {
		t.Fatalf("fanout list status = %+v/%v, want durable fanout status %+v", tasks.Tasks, err, fixture.status)
	}
	if tasks.Tasks[0].ColumnKeys == nil || !reflect.DeepEqual(*tasks.Tasks[0].ColumnKeys, []string{"impl_a", "impl_b", "impl_c"}) {
		t.Fatalf("fanout list column order = %+v", tasks.Tasks[0].ColumnKeys)
	}
}

type workflowViewFanoutStatusFixture struct {
	workflowID workflow.WorkflowID
	task       workflowstore.TaskRecord
	status     serverapi.WorkflowTaskStatus
}

func createWorkflowViewFanoutStatusFixture(t *testing.T, ctx context.Context, workflowStore *workflowstore.Store, binding metadata.Binding) workflowViewFanoutStatusFixture {
	t.Helper()
	workflowID := createWorkflowViewFanoutWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Fanout", Body: "Body"})
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
	runs, err := workflowStore.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	branchRuns := make(map[string]workflowstore.RunRecord, 3)
	for _, run := range runs {
		if run.ID != started.RunID {
			branchRuns[string(run.NodeID)] = run
		}
	}
	def, _, err := workflowStore.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	questionRun, ok := branchRuns[string(workflow.NodeIDOf(workflowViewNodeByKey(t, def, "impl_a")))]
	if !ok {
		t.Fatalf("missing impl_a run in %+v", runs)
	}
	interruptedRun, ok := branchRuns[string(workflow.NodeIDOf(workflowViewNodeByKey(t, def, "impl_b")))]
	if !ok {
		t.Fatalf("missing impl_b run in %+v", runs)
	}
	runningRun, ok := branchRuns[string(workflow.NodeIDOf(workflowViewNodeByKey(t, def, "impl_c")))]
	if !ok {
		t.Fatalf("missing impl_c run in %+v", runs)
	}
	questionClaimed, err := workflowStore.ClaimRun(ctx, questionRun.ID, 0)
	if err != nil {
		t.Fatalf("ClaimRun question: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, questionRun.ID, questionClaimed.Generation, "ask-fanout"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	interruptedClaimed, err := workflowStore.ClaimRun(ctx, interruptedRun.ID, 0)
	if err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, interruptedRun.ID, interruptedClaimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, runningRun.ID, 0); err != nil {
		t.Fatalf("ClaimRun running: %v", err)
	}
	runIDs := []string{string(questionRun.ID), string(interruptedRun.ID), string(runningRun.ID)}
	sort.Strings(runIDs)
	return workflowViewFanoutStatusFixture{
		workflowID: workflowID,
		task:       task,
		status: serverapi.WorkflowTaskStatus{
			Kind:        serverapi.WorkflowTaskStatusKindWaitingQuestion,
			NativeState: "waiting_ask",
			RunIDs:      runIDs,
			AttentionTypes: []serverapi.WorkflowTaskAttentionKind{
				serverapi.WorkflowTaskAttentionKindInterrupted,
				serverapi.WorkflowTaskAttentionKindQuestion,
			},
		},
	}
}

func mustTaskDetail(t *testing.T, view *workflowViewTestFixture, ctx context.Context, taskID string) serverapi.WorkflowTaskDetail {
	t.Helper()
	detail, err := view.detail(t).GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask %s: %v", taskID, err)
	}
	return detail
}

func createWorkflowViewWaitingAskTask(
	t *testing.T,
	ctx context.Context,
	store *metadata.Store,
	workflowStore *workflowstore.Store,
	binding metadata.Binding,
	sessionID string,
	askID string,
) (workflowstore.TaskRecord, workflowstore.StartTaskResult) {
	t.Helper()
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Task",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, previous_session_id, parent_agent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', NULL, NULL, 1, 1, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := workflowStore.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, askID); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	return task, started
}

func TestTaskDetailProjectsWaitingAskRun(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	view, err := newWorkflowViewTestFixture(store, workflowStore, staticTranscriptProvider{entries: map[string][]runtime.ChatEntry{
		"session-view-waiting-ask": transcriptEntriesWithAskOptions("ask-view-1", "Waiting ask?", []string{"Trail mix", "Dark chocolate", "Pistachios"}, 2),
	}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sessionID := "session-view-waiting-ask"
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, store, workflowStore, binding, sessionID, "ask-view-1")

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindActive || len(detail.Status.RunIDs) != 0 {
		t.Fatalf("status = %+v, want active without exact live pending-question evidence", detail.Status)
	}
	if detail.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.AttentionCount)
	}
	attention, err := view.taskAttention(t).ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(attention.Items) != 1 || attention.Items[0].Kind != "question" || attention.Items[0].AskID != "ask-view-1" || attention.Items[0].SessionID != sessionID || strings.TrimSpace(attention.Items[0].Message) == "" || len(attention.Items[0].Suggestions) != 3 || attention.Items[0].RecommendedOptionIndex != 2 {
		t.Fatalf("attention question options = %+v", attention.Items)
	}
	for _, suggestion := range attention.Items[0].Suggestions {
		if strings.TrimSpace(suggestion) == "" {
			t.Fatalf("attention contains blank suggestion: %+v", attention.Items)
		}
	}
}

func TestTaskDetailProjectsRuntimeApprovalWaitingAskPrompt(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	sessionID := "session-runtime-approval"
	askID := "ask-runtime-approval"
	view, err := newWorkflowViewTestFixture(store, workflowStore, nil, staticPendingPromptSource{sessionID: {{
		Request: askquestion.AskQuestionRequest{
			ID:       askID,
			Question: "Approve protected path?",
			Approval: true,
			ApprovalOptions: []askquestion.AskQuestionApprovalOption{
				{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
				{Decision: askquestion.AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"},
				{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
			},
		},
	}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	task, started := createWorkflowViewWaitingAskTask(t, ctx, store, workflowStore, binding, sessionID, askID)

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.AttentionCount)
	}
	taskAttention, err := view.taskAttention(t).ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	assertRuntimeApprovalQuestionAttention(t, taskAttention.Items, string(task.ID), string(started.RunID), sessionID, askID)
	list, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{})
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	assertRuntimeApprovalQuestionAttention(t, list.Items, string(task.ID), string(started.RunID), sessionID, askID)
}

func TestTaskDetailPendingQuestionFallsBackWhenTranscriptLookupFails(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	sessionID := "session-missing-question-transcript"
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, store, workflowStore, binding, sessionID, "ask-missing-transcript")

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.AttentionCount)
	}
	attention, err := view.taskAttention(t).ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(attention.Items) != 1 || attention.Items[0].Kind != "question" || attention.Items[0].AskID != "ask-missing-transcript" || attention.Items[0].Message != pendingQuestionFallbackMessage {
		t.Fatalf("attention = %+v", attention.Items)
	}
}

func assertRuntimeApprovalQuestionAttention(t *testing.T, items []serverapi.WorkflowAttentionItem, taskID string, runID string, sessionID string, askID string) {
	t.Helper()
	var item serverapi.WorkflowAttentionItem
	for _, candidate := range items {
		if candidate.Kind == "question" && candidate.AskID == askID {
			item = candidate
			break
		}
	}
	if item.AskID == "" {
		t.Fatalf("runtime approval question not found in attention: %+v", items)
	}
	if item.TaskID != taskID || item.RunID != runID || item.SessionID != sessionID || item.Message != "Approve protected path?" {
		t.Fatalf("runtime approval attention identity = %+v", item)
	}
	if len(item.Suggestions) != 0 || item.RecommendedOptionIndex != 0 {
		t.Fatalf("runtime approval attention ordinary fields = suggestions:%+v recommended:%d", item.Suggestions, item.RecommendedOptionIndex)
	}
	if item.Question == nil || item.Question.Kind != serverapi.WorkflowAttentionQuestionKindApproval {
		t.Fatalf("runtime approval question prompt = %+v", item.Question)
	}
	want := []clientui.ApprovalDecision{clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny}
	if len(item.Question.ApprovalDecisions) != len(want) {
		t.Fatalf("approval decisions = %+v, want %+v", item.Question.ApprovalDecisions, want)
	}
	for i := range want {
		if item.Question.ApprovalDecisions[i] != want[i] {
			t.Fatalf("approval decisions = %+v, want %+v", item.Question.ApprovalDecisions, want)
		}
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal attention item: %v", err)
	}
	if strings.Contains(string(raw), "Allow once") || strings.Contains(string(raw), "label") {
		t.Fatalf("runtime approval attention leaked server labels: %s", raw)
	}
}
