package workflowview

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestBoardColumnsUseWorkflowStructureInsteadOfDefinitionNodeOrder(t *testing.T) {
	def := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: "workflow-1"},
		Nodes: []serverapi.WorkflowNode{
			{ID: "node-start", Key: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog"},
			{ID: "node-done", Key: "done", Kind: string(workflow.NodeKindTerminal), DisplayName: "Done"},
			{ID: "node-plan", Key: "plan", Kind: string(workflow.NodeKindAgent), DisplayName: "Planning"},
			{ID: "node-implementation", Key: "implementation", Kind: string(workflow.NodeKindAgent), DisplayName: "Implementation"},
			{ID: "node-plan-review", Key: "plan_review", Kind: string(workflow.NodeKindAgent), DisplayName: "Plan Review"},
		},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{
			{ID: "transition-start", SourceNodeID: "node-start", TransitionID: "start"},
			{ID: "transition-plan", SourceNodeID: "node-plan", TransitionID: "plan_review"},
			{ID: "transition-review-approved", SourceNodeID: "node-plan-review", TransitionID: "approved"},
			{ID: "transition-review-rejected", SourceNodeID: "node-plan-review", TransitionID: "rejected"},
			{ID: "transition-implementation", SourceNodeID: "node-implementation", TransitionID: "done"},
		},
		Edges: []serverapi.WorkflowEdge{
			{ID: "edge-start", TransitionGroupID: "transition-start", Key: "start", TargetNodeID: "node-plan"},
			{ID: "edge-plan-review", TransitionGroupID: "transition-plan", Key: "plan_review", TargetNodeID: "node-plan-review"},
			{ID: "edge-approved", TransitionGroupID: "transition-review-approved", Key: "approved", TargetNodeID: "node-implementation"},
			{ID: "edge-rejected", TransitionGroupID: "transition-review-rejected", Key: "rejected", TargetNodeID: "node-plan"},
			{ID: "edge-done", TransitionGroupID: "transition-implementation", Key: "done", TargetNodeID: "node-done"},
		},
	}

	keys := workflowViewBoardColumnKeys(boardColumns(definitionSnapshot{api: def}))
	want := []string{"backlog", "plan", "plan_review", "implementation", "done"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("board column keys = %+v, want structural order %+v", keys, want)
	}
}

func TestBoardGroupsUseStructuralColumnOrderAndTraverseJoinNodes(t *testing.T) {
	def := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: "workflow-1"},
		NodeGroups: []serverapi.WorkflowNodeGroup{
			{GroupID: "group-implementation", GroupKey: "implementation", DisplayName: "Implementation"},
		},
		Nodes: []serverapi.WorkflowNode{
			{ID: "node-start", Key: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog"},
			{ID: "node-zeta", Key: "zeta", Kind: string(workflow.NodeKindAgent), DisplayName: "Zeta", GroupID: "group-implementation"},
			{ID: "node-alpha", Key: "alpha", Kind: string(workflow.NodeKindAgent), DisplayName: "Alpha", GroupID: "group-implementation"},
			{ID: "node-join", Key: "join", Kind: string(workflow.NodeKindJoin), DisplayName: "Join", GroupID: "group-implementation"},
			{ID: "node-synth", Key: "synth", Kind: string(workflow.NodeKindAgent), DisplayName: "Synthesize", GroupID: "group-implementation"},
			{ID: "node-done", Key: "done", Kind: string(workflow.NodeKindTerminal), DisplayName: "Done"},
		},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{
			{ID: "transition-start", SourceNodeID: "node-start", TransitionID: "start"},
			{ID: "transition-alpha", SourceNodeID: "node-alpha", TransitionID: "join"},
			{ID: "transition-zeta", SourceNodeID: "node-zeta", TransitionID: "join"},
			{ID: "transition-join", SourceNodeID: "node-join", TransitionID: "synth"},
			{ID: "transition-synth", SourceNodeID: "node-synth", TransitionID: "done"},
		},
		Edges: []serverapi.WorkflowEdge{
			{ID: "edge-zeta", TransitionGroupID: "transition-start", Key: "zeta", TargetNodeID: "node-zeta"},
			{ID: "edge-alpha", TransitionGroupID: "transition-start", Key: "alpha", TargetNodeID: "node-alpha"},
			{ID: "edge-alpha-join", TransitionGroupID: "transition-alpha", Key: "join", TargetNodeID: "node-join"},
			{ID: "edge-zeta-join", TransitionGroupID: "transition-zeta", Key: "join", TargetNodeID: "node-join"},
			{ID: "edge-synth", TransitionGroupID: "transition-join", Key: "synth", TargetNodeID: "node-synth"},
			{ID: "edge-done", TransitionGroupID: "transition-synth", Key: "done", TargetNodeID: "node-done"},
		},
	}

	keys := workflowViewBoardColumnKeys(boardColumns(definitionSnapshot{api: def}))
	wantKeys := []string{"backlog", "alpha", "zeta", "synth", "done"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("board column keys = %+v, want join-traversed order %+v", keys, wantKeys)
	}
	groups := boardGroups(def)
	wantNodeIDs := []string{"node-alpha", "node-zeta", "node-synth"}
	if len(groups) != 1 || strings.Join(groups[0].NodeIDs, ",") != strings.Join(wantNodeIDs, ",") {
		t.Fatalf("board groups = %+v, want structural visible node ids %+v", groups, wantNodeIDs)
	}
}

func TestBoardSelectsWorkflowAndReturnsPickerAndGroups(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	defaultWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, defaultWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow default: %v", err)
	}
	selected, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Selected Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow selected: %v", err)
	}
	if _, _, err := workflowStore.AddNodeGroup(ctx, workflowstore.NodeGroupRecord{WorkflowID: selected.ID, Key: "impl", DisplayName: "Implementation", SortOrder: 10}); err != nil {
		t.Fatalf("AddNodeGroup: %v", err)
	}
	def, _, err := workflowStore.GetDefinition(ctx, selected.ID)
	if err != nil {
		t.Fatalf("GetDefinition selected: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-selected-agent-" + string(selected.ID))
	if _, err := workflowStore.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: selected.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", GroupKey: "impl", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddNode selected: %v", err)
	}
	startGroup := workflow.TransitionGroupID("group-selected-start-" + string(selected.ID))
	doneGroup := workflow.TransitionGroupID("group-selected-done-" + string(selected.ID))
	if _, err := workflowStore.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: startGroup, WorkflowID: selected.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := workflowStore.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-selected-start-" + string(selected.ID)), WorkflowID: selected.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := workflowStore.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: doneGroup, WorkflowID: selected.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := workflowStore.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-selected-done-" + string(selected.ID)), WorkflowID: selected.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, selected.ID, false); err != nil {
		t.Fatalf("LinkWorkflow selected: %v", err)
	}
	defaultTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(defaultWorkflowID), Title: "Default task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask default: %v", err)
	}
	selectedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(selected.ID), Title: "Selected task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask selected: %v", err)
	}

	selectedWorkflowID := string(selected.ID)
	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: &selectedWorkflowID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if board.SelectedWorkflow == nil || board.SelectedWorkflow.WorkflowID != string(selected.ID) {
		t.Fatalf("selected workflow = %+v, want %s", board.SelectedWorkflow, selected.ID)
	}
	if len(board.WorkflowPicker) != 2 || !board.WorkflowPicker[0].IsProjectDefault {
		t.Fatalf("picker = %+v, want default first and two workflows", board.WorkflowPicker)
	}
	selectedBacklog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	selectedPage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(selected.ID), NodeID: selectedBacklog.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards selected: %v", err)
	}
	if len(selectedPage.Cards) != 1 || selectedPage.Cards[0].TaskID != string(selectedTask.ID) || selectedPage.Cards[0].TaskID == string(defaultTask.ID) {
		t.Fatalf("cards = %+v, want only selected workflow task %s", selectedPage.Cards, selectedTask.ID)
	}
	if len(board.Groups) != 1 || board.Groups[0].Key != "impl" || len(board.Groups[0].NodeIDs) != 1 || board.Groups[0].NodeIDs[0] != string(agentID) {
		t.Fatalf("groups = %+v, want implementation group with agent", board.Groups)
	}
	if board.Project.ProjectKey != "WOR" || board.GeneratedAtUnixMs == 0 {
		t.Fatalf("project/generated fields missing: %+v", board)
	}
}

func TestBoardWithoutActiveLinksReturnsNoSelectionOrContent(t *testing.T) {
	ctx, _, _, binding, view := newWorkflowViewTestContextFixture(t)

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if board.SelectedWorkflow != nil {
		t.Fatalf("selected workflow = %+v, want nil", board.SelectedWorkflow)
	}
	if len(board.WorkflowPicker) != 0 || len(board.Groups) != 0 || len(board.Columns) != 0 {
		t.Fatalf("board content = picker:%+v groups:%+v columns:%+v, want empty", board.WorkflowPicker, board.Groups, board.Columns)
	}
}

func TestBoardSelectorFallsBackToActiveSelection(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, firstWorkflowID, false); err != nil {
			t.Fatalf("LinkWorkflow first: %v", err)
		}
		defaultWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, defaultWorkflowID, true); err != nil {
			t.Fatalf("LinkWorkflow default: %v", err)
		}
		unknownWorkflowID := "workflow-unknown"
		for _, request := range []serverapi.WorkflowBoardRequest{
			{ProjectID: binding.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}},
			{ProjectID: binding.ProjectID, WorkflowID: &unknownWorkflowID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}},
		} {
			board, err := view.board(t).Get(ctx, request)
			if err != nil {
				t.Fatalf("GetBoard: %v", err)
			}
			if board.SelectedWorkflow == nil || board.SelectedWorkflow.WorkflowID != string(defaultWorkflowID) {
				t.Fatalf("selected workflow = %+v, want active default %s", board.SelectedWorkflow, defaultWorkflowID)
			}
		}
	})

	t.Run("first active link", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, false); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		unknownWorkflowID := "workflow-unknown"

		board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: &unknownWorkflowID})
		if err != nil {
			t.Fatalf("GetBoard: %v", err)
		}
		if board.SelectedWorkflow == nil || board.SelectedWorkflow.WorkflowID != string(workflowID) {
			t.Fatalf("selected workflow = %+v, want first active link %s", board.SelectedWorkflow, workflowID)
		}
	})
}

func TestTaskDetailProjectsWorkflowIdentityWithoutWorkflowValidity(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: "Historical", Body: "Body"})
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
	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Workflow.WorkflowID != string(workflowID) || detail.Workflow.DisplayName == "" || detail.Workflow.Version <= 0 {
		t.Fatalf("workflow summary = %+v, want current workflow identity", detail.Workflow)
	}
}

func TestBoardColumnTaskCountsUseFullSelectedWorkflow(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	taskIDs := []string{}
	for _, title := range []string{"Task A", "Task B"} {
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: title, Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		taskIDs = append(taskIDs, string(task.ID))
	}
	for _, taskID := range taskIDs {
		if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 123 WHERE id = ?`, taskID); err != nil {
			t.Fatalf("force task timestamp: %v", err)
		}
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogCount := 0
	for _, column := range board.Columns {
		if column.IsBacklog {
			backlogCount = column.TaskCount
			break
		}
	}
	if backlogCount != 2 {
		t.Fatalf("backlog count = %d, want full selected workflow count 2", backlogCount)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	firstPage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID, PageSize: 1})
	if err != nil {
		t.Fatalf("ListBoardNodeCards first: %v", err)
	}
	if len(firstPage.Cards) != 1 || firstPage.NextPageToken == nil {
		t.Fatalf("first node page = %+v, want one card with next page", firstPage)
	}
	secondPage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID, PageSize: 1, PageToken: firstPage.NextPageToken})
	if err != nil {
		t.Fatalf("ListBoardNodeCards second: %v", err)
	}
	if len(secondPage.Cards) != 1 || secondPage.Cards[0].TaskID == firstPage.Cards[0].TaskID {
		t.Fatalf("second node page = %+v first=%+v, want distinct next card", secondPage, firstPage)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	if _, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: doneColumn.Node.NodeID, PageSize: 1, PageToken: firstPage.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("ListBoardNodeCards with token from other node error = %v", err)
	}
}

func TestBoardNodeCardsBidirectionalPaginationRoundTripsWithoutGaps(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	type expectedCard struct {
		taskID          string
		updatedAtUnixMs int64
	}
	expected := make([]expectedCard, 0, 126)
	for index := 0; index < 126; index++ {
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(workflowID),
			Title:      "Paged task " + strconv.Itoa(index),
			Body:       "Body",
		})
		if err != nil {
			t.Fatalf("CreateTask %d: %v", index, err)
		}
		updatedAtUnixMs := int64(10_000 + index/3)
		if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = ? WHERE id = ?`, updatedAtUnixMs, string(task.ID)); err != nil {
			t.Fatalf("set task %d timestamp: %v", index, err)
		}
		expected = append(expected, expectedCard{taskID: string(task.ID), updatedAtUnixMs: updatedAtUnixMs})
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].updatedAtUnixMs != expected[j].updatedAtUnixMs {
			return expected[i].updatedAtUnixMs > expected[j].updatedAtUnixMs
		}
		return expected[i].taskID > expected[j].taskID
	})

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	listPage := func(pageToken *string) serverapi.WorkflowBoardNodeCardsListResponse {
		t.Helper()
		page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
			ProjectID:   binding.ProjectID,
			WorkflowID:  string(workflowID),
			NodeID:      backlog.Node.NodeID,
			PageSize:    25,
			PageToken:   pageToken,
		})

		if err != nil {
			t.Fatalf("ListBoardNodeCards: %v", err)
		}
		return page
	}
	assertPage := func(page serverapi.WorkflowBoardNodeCardsListResponse, expectedStart int) {
		t.Helper()
		expectedEnd := min(expectedStart+25, len(expected))
		wantIDs := make([]string, 0, expectedEnd-expectedStart)
		for _, card := range expected[expectedStart:expectedEnd] {
			wantIDs = append(wantIDs, card.taskID)
		}
		gotIDs := workflowViewBoardCardIDs(page.Cards)
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("page at %d IDs = %+v, want %+v", expectedStart, gotIDs, wantIDs)
		}
	}

	pages := []serverapi.WorkflowBoardNodeCardsListResponse{listPage(nil)}
	assertPage(pages[0], 0)
	if pages[0].PreviousPageToken != nil || pages[0].NextPageToken == nil {
		t.Fatalf("initial cursors = previous %v next %v, want only older", pages[0].PreviousPageToken, pages[0].NextPageToken)
	}
	allIDs := append([]string(nil), workflowViewBoardCardIDs(pages[0].Cards)...)
	for pages[len(pages)-1].NextPageToken != nil {
		next := listPage(pages[len(pages)-1].NextPageToken)
		pages = append(pages, next)
		assertPage(next, (len(pages)-1)*25)
		allIDs = append(allIDs, workflowViewBoardCardIDs(next.Cards)...)
	}
	if len(pages) != 6 {
		t.Fatalf("page count = %d, want 6 for 126 cards", len(pages))
	}
	for index, page := range pages {
		if index > 0 && page.PreviousPageToken == nil {
			t.Fatalf("page %d has no newer cursor", index)
		}
		if index < len(pages)-1 && page.NextPageToken == nil {
			t.Fatalf("page %d has no older cursor", index)
		}
	}
	wantAllIDs := make([]string, 0, len(expected))
	for _, card := range expected {
		wantAllIDs = append(wantAllIDs, card.taskID)
	}
	if !reflect.DeepEqual(allIDs, wantAllIDs) {
		t.Fatalf("older traversal IDs contain a gap or duplicate: got %d IDs, want %d", len(allIDs), len(wantAllIDs))
	}

	newerFromDeep := listPage(pages[4].PreviousPageToken)
	assertPage(newerFromDeep, 75)
	newerAgain := listPage(newerFromDeep.PreviousPageToken)
	assertPage(newerAgain, 50)
	olderAgain := listPage(newerAgain.NextPageToken)
	assertPage(olderAgain, 75)

	invalidTokens := []struct {
		name   string
		mutate func(*boardNodeCardsTokenFixture)
	}{
		{name: "version", mutate: func(payload *boardNodeCardsTokenFixture) { payload.Version = 1 }},
		{name: "direction", mutate: func(payload *boardNodeCardsTokenFixture) { payload.Direction = "sideways" }},
		{name: "project scope", mutate: func(payload *boardNodeCardsTokenFixture) { payload.ProjectID = "other-project" }},
		{name: "workflow scope", mutate: func(payload *boardNodeCardsTokenFixture) { payload.WorkflowID = "other-workflow" }},
		{name: "node scope", mutate: func(payload *boardNodeCardsTokenFixture) { payload.NodeID = "other-node" }},
		{name: "blank task ID", mutate: func(payload *boardNodeCardsTokenFixture) { payload.TaskID = " " }},
		{name: "negative timestamp", mutate: func(payload *boardNodeCardsTokenFixture) { payload.UpdatedAtUnixMs = -1 }},
	}
	for _, testCase := range invalidTokens {
		t.Run("rejects "+testCase.name, func(t *testing.T) {
			token := mutateBoardNodeCardsToken(t, pages[0].NextPageToken, testCase.mutate)
			if _, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
				LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
				ProjectID:   binding.ProjectID,
				WorkflowID:  string(workflowID),
				NodeID:      backlog.Node.NodeID,
				PageSize:    25,
				PageToken:   &token,
			}); !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("%s error = %v, want ErrInvalidPageToken", testCase.name, err)
			}
		})
	}
}

func TestBoardNodeCardsArchiveCanceledTaskInDoneNode(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: "Canceled backlog", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(t, ctx, store, task.ID, workflowID)
	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlogColumn.TaskCount != 0 {
		t.Fatalf("backlog count = %d, want canceled task archived out of backlog", backlogColumn.TaskCount)
	}
	backlogPage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards backlog: %v", err)
	}
	if len(backlogPage.Cards) != 0 {
		t.Fatalf("backlog node cards = %+v, want canceled task archived out of backlog", backlogPage.Cards)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	if doneColumn.TaskCount != 1 {
		t.Fatalf("done count = %d, want canceled task counted in Done", doneColumn.TaskCount)
	}
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: doneColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards done: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].TaskID != string(task.ID) || page.Cards[0].Status.Kind != "canceled" {
		t.Fatalf("done node cards = %+v, want canceled task", page.Cards)
	}
}

func TestBoardNodeCardsAllowRestartAfterDoneTaskResetToBacklog(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: "Restart", Body: "Body"})
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
	def, _, err := workflowStore.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	if _, err := workflowStore.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(start)}); err != nil {
		t.Fatalf("ManualMoveTask reset: %v", err)
	}
	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards backlog: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].TaskID != string(task.ID) {
		t.Fatalf("backlog page = %+v, want reset task", page)
	}
	if !page.Cards[0].Actions.CanStart {
		t.Fatalf("reset backlog card actions = %+v, want restart allowed", page.Cards[0].Actions)
	}
}

func TestBoardNodeCardsIgnoreInterruptedRunsFromCompletedPlacementsAfterResetToBacklog(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: "Restart", Body: "Body"})
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
	if err := workflowStore.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", "{}"); err != nil {
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

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlogColumn.TaskCount != 1 {
		t.Fatalf("backlog count = %d, want reset task", backlogColumn.TaskCount)
	}
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards backlog: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].TaskID != string(task.ID) || page.Cards[0].Status.Kind != "backlog" {
		t.Fatalf("backlog page = %+v, want reset backlog task", page)
	}
	if !page.Cards[0].Actions.CanStart || page.Cards[0].Actions.CanResume {
		t.Fatalf("reset backlog card actions = %+v, want start-only action state", page.Cards[0].Actions)
	}
	attention, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{})
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	for _, item := range attention.Items {
		if item.Kind == attentionKindInterruptedRun && item.TaskID == string(task.ID) {
			t.Fatalf("attention items = %+v, want no stale interrupted run attention after reset", attention.Items)
		}
	}
}

func TestBoardNodeCardsDoNotArchiveCanceledTaskInAlternateTerminalNode(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	archiveNodeID := workflow.NodeID("node-archive-" + string(workflowID))
	if _, err := workflowStore.AddNode(ctx, workflowstore.NodeRecord{ID: archiveNodeID, WorkflowID: workflowID, Key: "archive", Kind: workflow.NodeKindTerminal, DisplayName: "Archive"}); err != nil {
		t.Fatalf("AddNode archive: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: "Canceled backlog", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(t, ctx, store, task.ID, workflowID)
	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	archiveColumn := workflowViewColumnByKey(t, board, "archive")
	if archiveColumn.TaskCount != 0 {
		t.Fatalf("archive count = %d, want no fallback canceled tasks", archiveColumn.TaskCount)
	}
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: string(archiveNodeID)})
	if err != nil {
		t.Fatalf("ListBoardNodeCards archive: %v", err)
	}
	if len(page.Cards) != 0 {
		t.Fatalf("archive node cards = %+v, want no fallback canceled tasks", page.Cards)
	}
	workflowIDString := string(workflowID)
	done, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &binding.ProjectID, WorkflowID: &workflowIDString, ColumnKeys: []string{"done"}})
	if err != nil {
		t.Fatalf("ListTasks done: %v", err)
	}
	if len(done.Tasks) != 1 || done.Tasks[0].TaskID != string(task.ID) || done.Tasks[0].ColumnKeys == nil || !reflect.DeepEqual(*done.Tasks[0].ColumnKeys, []string{"done"}) {
		t.Fatalf("done tasks = %+v, want canceled task only in done", done.Tasks)
	}
	archive, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &binding.ProjectID, WorkflowID: &workflowIDString, ColumnKeys: []string{"archive"}})
	if err != nil || len(archive.Tasks) != 0 {
		t.Fatalf("archive tasks = %+v/%v, want no canceled task", archive.Tasks, err)
	}
}

func TestBoardProjectsManualMoveTargetsFromServerPermissions(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	def, _, err := workflowStore.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := workflowViewNodeByKind(t, def, workflow.NodeKindAgent)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	reviewID := workflow.NodeID("node-review-" + string(workflowID))
	if _, err := workflowStore.AddNode(ctx, workflowstore.NodeRecord{ID: reviewID, WorkflowID: workflowID, Key: "review", Kind: workflow.NodeKindAgent, DisplayName: "Review", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddNode review: %v", err)
	}
	reviewGroupID := workflow.TransitionGroupID("group-review-" + string(workflowID))
	if _, err := workflowStore.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: reviewGroupID, WorkflowID: workflowID, SourceNodeID: workflow.NodeIDOf(agent), TransitionID: "review", DisplayName: "Review"}); err != nil {
		t.Fatalf("AddTransitionGroup review: %v", err)
	}
	if _, err := workflowStore.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-review-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reviewGroupID, Key: "review", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review {{.Params.summary}}.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary."}}}); err != nil {
		t.Fatalf("AddEdge review: %v", err)
	}
	reviewDoneGroupID := workflow.TransitionGroupID("group-review-done-" + string(workflowID))
	if _, err := workflowStore.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: reviewDoneGroupID, WorkflowID: workflowID, SourceNodeID: reviewID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup review done: %v", err)
	}
	if _, err := workflowStore.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-review-done-" + string(workflowID)), WorkflowID: workflowID, TransitionGroupID: reviewDoneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge review done: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := workflowStore.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	activeColumn := workflowViewColumnByKey(t, board, "agent")
	activePage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: activeColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards active: %v", err)
	}
	if len(activePage.Cards) != 1 {
		t.Fatalf("node cards = %+v, want one active card", activePage.Cards)
	}
	if got := activePage.Cards[0].Actions.ManualMoveTargetNodeIDs; len(got) != 1 || got[0] != string(workflow.NodeIDOf(done)) {
		t.Fatalf("manual move targets = %+v, want %s", got, workflow.NodeIDOf(done))
	}
}

func TestBoardDoesNotOfferInterruptForDurableStartedRunWithoutExactExecution(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, started.RunID, 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	activeColumn := workflowViewColumnByKey(t, board, "agent")
	activePage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: activeColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards active: %v", err)
	}
	if len(activePage.Cards) != 1 {
		t.Fatalf("node cards = %+v, want one active card", activePage.Cards)
	}
	if activePage.Cards[0].Status.Kind == "running" || activePage.Cards[0].Actions.CanInterrupt {
		t.Fatalf("durable-only card status/actions = %+v/%+v, want no running or interrupt authority", activePage.Cards[0].Status, activePage.Cards[0].Actions)
	}
	if got := activePage.Cards[0].Actions.ManualMoveTargetNodeIDs; len(got) != 1 {
		t.Fatalf("manual move targets = %+v, want idle task targets", got)
	}
}

func TestBoardOffersInterruptOnlyWhileExactScriptExecutionIsActive(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
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
	cancellationGrace := 50 * time.Millisecond
	handle, err := view.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &sessionruntime.WorkflowExecutionRef{TaskID: task.ID, RunID: started.RunID, Generation: claimed.Generation},
		Command: sessionruntime.ScriptCommand{
			Path:              sleepPath,
			Args:              []string{"30"},
			CancellationGrace: &cancellationGrace,
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		if handle != nil {
			_ = handle.Stop(context.Background())
		}
	})

	card := workflowViewBoardCardForTask(t, ctx, view, binding.ProjectID, workflowID, "agent", string(task.ID))
	if card.Status.Kind != serverapi.WorkflowTaskStatusKindRunning || !card.Actions.CanInterrupt {
		t.Fatalf("live script card = %+v, want running with Interrupt", card)
	}

	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("Stop script: %v", err)
	}
	handle = nil
	card = workflowViewBoardCardForTask(t, ctx, view, binding.ProjectID, workflowID, "agent", string(task.ID))
	if card.Status.Kind == serverapi.WorkflowTaskStatusKindRunning || card.Actions.CanInterrupt {
		t.Fatalf("stopped script card = %+v, want no running or Interrupt", card)
	}
}

func workflowViewBoardCardForTask(t *testing.T, ctx context.Context, view *workflowViewTestFixture, projectID string, workflowID workflow.WorkflowID, nodeKey string, taskID string) serverapi.WorkflowBoardTaskCard {
	t.Helper()
	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		ProjectID:   projectID,
	})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	column := workflowViewColumnByKey(t, board, nodeKey)
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		ProjectID:   projectID,
		WorkflowID:  string(workflowID),
		NodeID:      column.Node.NodeID,
	})
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	return requireBoardCard(t, page.Cards, taskID)
}

func TestTaskDetailProjectsCancellationAndInterruptedRun(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := workflowStore.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Summary.CanceledAt == nil || *detail.Summary.CanceledAt == 0 || detail.Summary.CancelReason == nil || *detail.Summary.CancelReason != "stop" {
		t.Fatalf("summary does not project cancellation: %+v", detail.Summary)
	}
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindCanceled {
		t.Fatalf("status = %+v, want canceled", detail.Status)
	}
	if detail.Actions.CanResume {
		t.Fatalf("canceled task should not expose resume actions: %+v", detail.Actions)
	}
}
