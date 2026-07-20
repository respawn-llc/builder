package workflowview

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestBoardAndTaskDetailUseDurableWorkflowMetadataOnly(t *testing.T) {
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
	comment, err := workflowStore.AddComment(ctx, task.ID, "note", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	deletedComment, err := workflowStore.AddComment(ctx, task.ID, "deleted", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment deleted: %v", err)
	}
	if err := workflowStore.DeleteComment(ctx, deletedComment.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(board.WorkflowPicker) != 1 {
		t.Fatalf("board = %+v", board)
	}
	if len(board.Columns) < 2 || board.Columns[0].Node.Kind != string(workflow.NodeKindStart) {
		t.Fatalf("board column ordering = %+v", board.Columns)
	}
	foundDoneNodeTask := false
	for _, column := range board.Columns {
		if column.Node.Kind == string(workflow.NodeKindTerminal) && column.TaskCount == 1 {
			foundDoneNodeTask = true
		}
	}
	if !foundDoneNodeTask {
		t.Fatalf("board columns do not contain task on terminal node: %+v", board.Columns)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	donePage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: doneColumn.Node.NodeID})
	if err != nil {
		t.Fatalf("ListBoardNodeCards done: %v", err)
	}
	if len(donePage.Cards) != 1 || donePage.Cards[0].Status.Kind != "done" {
		t.Fatalf("done cards = %+v, want done task card", donePage.Cards)
	}

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !detail.Summary.Done || len(detail.Placements) != 3 || len(detail.Runs) != 1 || len(detail.Transitions) != 2 || len(detail.Comments) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Comments[0].ID != comment.ID || detail.Transitions[0].TransitionID != "start" || detail.Transitions[1].TransitionID != "done" || detail.Transitions[1].Edges[0].EdgeKey != "done" {
		t.Fatalf("detail history mismatch: %+v", detail)
	}
}

func TestBoardMetadataCountsDoneCardsWhileColumnEndpointOwnsCardData(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	for index := 0; index < 2; index++ {
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Done task", Body: "Body"})
		if err != nil {
			t.Fatalf("CreateTask %d: %v", index, err)
		}
		started, err := workflowStore.StartTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("StartTask %d: %v", index, err)
		}
		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
			t.Fatalf("CompleteRun %d: %v", index, err)
		}
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	if doneColumn.TaskCount != 2 {
		t.Fatalf("done metadata count = %d, want 2", doneColumn.TaskCount)
	}
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      doneColumn.Node.NodeID,
		PageSize:    1,
	})

	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(page.Cards) != 1 || page.NextPageToken == nil {
		t.Fatalf("done page = %+v, want one card and an older-page cursor", page)
	}
}

func TestWorkflowPickerAndAttentionIncludeScriptPathDiagnostics(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	created, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := workflowStore.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := workflowStore.AddNode(ctx, workflowstore.NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	if _, err := workflowStore.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := workflowStore.AddEdge(ctx, workflowstore.EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := workflowStore.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := workflowStore.AddEdge(ctx, workflowstore.EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, created.ID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(board.WorkflowPicker) != 1 || board.WorkflowPicker[0].ValidForTaskCreation {
		t.Fatalf("workflow picker = %+v, want script-path blocker", board.WorkflowPicker)
	}
	if len(board.WorkflowPicker[0].ValidationErrors) != 1 || board.WorkflowPicker[0].ValidationErrors[0].Code != workflowscript.CodeMissingPath {
		t.Fatalf("picker validation errors = %+v, want missing script path", board.WorkflowPicker[0].ValidationErrors)
	}
	attention, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{})
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(attention.Items) != 1 || attention.Items[0].Kind != "validation_blocker" || attention.Items[0].WorkflowID == nil || *attention.Items[0].WorkflowID != string(created.ID) {
		t.Fatalf("attention items = %+v, want workflow validation blocker", attention.Items)
	}
}

func TestBoardAndTaskDetailProjectTaskSourceWorkspaceAndBody(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	source, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	body := "\n  x" + strings.Repeat("界", 40) + " complete board body  \n"
	wantBody := strings.TrimSpace(body)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: body, SourceWorkspaceID: source.WorkspaceID})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	backlogPage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}})
	if err != nil {
		t.Fatalf("ListBoardNodeCards backlog: %v", err)
	}
	if len(backlogPage.Cards) != 1 || backlogPage.Cards[0].SourceWorkspace.WorkspaceID != source.WorkspaceID || backlogPage.Cards[0].Preview.Markdown != wantBody || backlogPage.Cards[0].Preview.Truncated {
		t.Fatalf("node cards = %+v, want source workspace %q and complete bounded preview %q", backlogPage.Cards, source.WorkspaceID, wantBody)
	}
	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Summary.SourceWorkspaceID != source.WorkspaceID || detail.SourceWorkspace.WorkspaceID != source.WorkspaceID || detail.Body != wantBody {
		t.Fatalf("detail = %+v, want source workspace %q and body", detail, source.WorkspaceID)
	}
	if detail.Summary.BodyPreview == "" || detail.Summary.CreatedAtUnixMs == 0 || detail.Summary.UpdatedAtUnixMs == 0 {
		t.Fatalf("detail summary missing preview/timestamps: %+v", detail.Summary)
	}
}

func TestBoardNodeCardsProjectBoundedUnicodeMarkdownPreview(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	cases := []struct {
		title         string
		body          string
		wantMarkdown  string
		wantTruncated bool
	}{
		{
			title:        "trimmed",
			body:         " \n\t**bounded preview**\t\n ",
			wantMarkdown: "**bounded preview**",
		},
		{
			title:        "exactly 512 Unicode code points",
			body:         "  " + strings.Repeat("界", 512) + "\n",
			wantMarkdown: strings.Repeat("界", 512),
		},
		{
			title:         "513 Unicode code points",
			body:          "\n" + strings.Repeat("界", 513) + "  ",
			wantMarkdown:  strings.Repeat("界", 512),
			wantTruncated: true,
		},
	}
	for _, testCase := range cases {
		if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(workflowID),
			Title:      testCase.title,
			Body:       testCase.body,
		}); err != nil {
			t.Fatalf("CreateTask %q: %v", testCase.title, err)
		}
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      backlogColumn.Node.NodeID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
	})

	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(page.Cards) != len(cases) {
		t.Fatalf("card count = %d, want %d", len(page.Cards), len(cases))
	}
	wantByTitle := make(map[string]struct {
		markdown  string
		truncated bool
	}, len(cases))
	for _, testCase := range cases {
		wantByTitle[testCase.title] = struct {
			markdown  string
			truncated bool
		}{markdown: testCase.wantMarkdown, truncated: testCase.wantTruncated}
	}
	for _, card := range page.Cards {
		raw, err := json.Marshal(card)
		if err != nil {
			t.Fatalf("marshal card %q: %v", card.Title, err)
		}
		var shape map[string]any
		if err := json.Unmarshal(raw, &shape); err != nil {
			t.Fatalf("unmarshal card %q JSON shape: %v", card.Title, err)
		}
		if _, ok := shape["body"]; ok {
			t.Fatalf("card %q transports full body: %#v", card.Title, shape)
		}
		preview, ok := shape["preview"].(map[string]any)
		if !ok {
			t.Fatalf("card %q preview = %#v, want nested object", card.Title, shape["preview"])
		}
		want, ok := wantByTitle[card.Title]
		if !ok {
			t.Fatalf("unexpected card title %q", card.Title)
		}
		if preview["markdown"] != want.markdown || preview["truncated"] != want.truncated {
			t.Fatalf("card %q preview = %#v, want markdown length %d and truncated=%t", card.Title, preview, len([]rune(want.markdown)), want.truncated)
		}
	}
}

func TestBoardNodeCardsDefaultPageSizeIs25(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	for index := 0; index < 26; index++ {
		if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(workflowID),
			Title:      "Task " + strconv.Itoa(index),
			Body:       "Body",
		}); err != nil {
			t.Fatalf("CreateTask %d: %v", index, err)
		}
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	page, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      backlogColumn.Node.NodeID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
	})

	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(page.Cards) != 25 {
		t.Fatalf("default card page length = %d, want 25", len(page.Cards))
	}
}

func TestTaskDetailProjectsExecutionTargetOnlyAfterLockAndNoneUsesSourceWorkspace(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Target detail", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	unlocked := mustTaskDetail(t, view, ctx, string(task.ID))
	if unlocked.ExecutionTarget != nil {
		t.Fatalf("unlocked execution target = %+v, want absent", unlocked.ExecutionTarget)
	}
	if unlocked.SourceWorkspace.WorkspaceID != binding.WorkspaceID || unlocked.SourceWorkspace.RootPath != binding.CanonicalRoot {
		t.Fatalf("source workspace = %+v, want %q at %q", unlocked.SourceWorkspace, binding.WorkspaceID, binding.CanonicalRoot)
	}

	candidate := workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}
	if _, err := workflowStore.StartTaskWithExecutionTarget(ctx, task.ID, &candidate); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}

	locked := mustTaskDetail(t, view, ctx, string(task.ID))
	if locked.ExecutionTarget == nil ||
		locked.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeNone ||
		locked.ExecutionTarget.EffectiveRoot == nil ||
		*locked.ExecutionTarget.EffectiveRoot != binding.CanonicalRoot ||
		locked.ExecutionTarget.Provenance != serverapi.WorkflowExecutionTargetProvenanceResolved {
		t.Fatalf("locked none execution target = %+v", locked.ExecutionTarget)
	}
	if locked.ExecutionTarget.RequestedRef != nil ||
		locked.ExecutionTarget.ResolvedRef != nil ||
		locked.ExecutionTarget.CommitOID != nil ||
		locked.ExecutionTarget.CurrentBranch != nil ||
		locked.ExecutionTarget.ManagedWorktree != nil {
		t.Fatalf("locked none execution target has managed facts: %+v", locked.ExecutionTarget)
	}
}

func TestTaskDetailProjectsHealthyManagedExecutionTargetWithCurrentOperatorBranch(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Managed target", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	runWorkflowViewGit(t, binding.CanonicalRoot, "init")
	runWorkflowViewGit(t, binding.CanonicalRoot, "config", "user.email", "kent@example.com")
	runWorkflowViewGit(t, binding.CanonicalRoot, "config", "user.name", "Kent Test")
	runWorkflowViewGit(t, binding.CanonicalRoot, "commit", "--allow-empty", "-m", "initial")
	worktreeRoot := filepath.Join(t.TempDir(), "managed")
	runWorkflowViewGit(t, binding.CanonicalRoot, "worktree", "add", "-b", task.ShortID, worktreeRoot, "HEAD")
	runWorkflowViewGit(t, worktreeRoot, "branch", "-m", "operator-renamed")
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}

	worktreeID := "worktree-managed-detail"
	if err := store.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		Availability:  "available",
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(task.ID),
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   1,
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}
	requestedRef := "HEAD"
	resolvedRef := "refs/heads/main"
	commitOID := runWorkflowViewGit(t, binding.CanonicalRoot, "rev-parse", "HEAD")
	candidate := workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			ResolvedRef:  &resolvedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       canonicalWorktreeRoot,
			},
		},
	}
	if _, err := workflowStore.StartTaskWithExecutionTarget(ctx, task.ID, &candidate); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}

	detail := mustTaskDetail(t, view, ctx, string(task.ID))
	target := detail.ExecutionTarget
	if target == nil ||
		target.Mode != serverapi.WorkflowExecutionTargetModeHead ||
		target.EffectiveRoot == nil ||
		*target.EffectiveRoot != canonicalWorktreeRoot ||
		target.RequestedRef == nil ||
		*target.RequestedRef != requestedRef ||
		target.ResolvedRef == nil ||
		*target.ResolvedRef != resolvedRef ||
		target.CommitOID == nil ||
		*target.CommitOID != commitOID ||
		target.Provenance != serverapi.WorkflowExecutionTargetProvenanceResolved ||
		target.CurrentBranch == nil ||
		*target.CurrentBranch != "operator-renamed" ||
		target.ManagedWorktree == nil ||
		target.ManagedWorktree.WorktreeID != worktreeID ||
		target.ManagedWorktree.CanonicalRoot != canonicalWorktreeRoot ||
		target.ManagedWorktree.Availability != serverapi.WorktreePathAvailabilityAvailable {
		t.Fatalf("managed execution target = mode:%q root:%v requested:%v resolved:%v commit:%v provenance:%q branch:%v worktree:%+v",
			target.Mode,
			target.EffectiveRoot,
			target.RequestedRef,
			target.ResolvedRef,
			target.CommitOID,
			target.Provenance,
			target.CurrentBranch,
			target.ManagedWorktree,
		)
	}
}

func TestTaskDetailPreservesManagedSelectionFactsWhenBindingIsMissing(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Missing managed binding", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	worktreeID := "worktree-missing-binding"
	worktreeRoot := t.TempDir()
	if err := store.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		Availability:  "available",
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(task.ID),
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   1,
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}
	requestedRef := "HEAD"
	resolvedRef := "refs/heads/main"
	commitOID := "0123456789abcdef"
	candidate := workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			ResolvedRef:  &resolvedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       worktreeRoot,
			},
		},
	}
	if _, err := workflowStore.StartTaskWithExecutionTarget(ctx, task.ID, &candidate); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
	if _, err := store.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(task.ID),
		ManagedWorktreeID: sql.NullString{},
		UpdatedAtUnixMs:   2,
	}); err != nil {
		t.Fatalf("clear managed binding: %v", err)
	}

	target := mustTaskDetail(t, view, ctx, string(task.ID)).ExecutionTarget
	if target == nil ||
		target.Mode != serverapi.WorkflowExecutionTargetModeHead ||
		target.RequestedRef == nil ||
		*target.RequestedRef != requestedRef ||
		target.ResolvedRef == nil ||
		*target.ResolvedRef != resolvedRef ||
		target.CommitOID == nil ||
		*target.CommitOID != commitOID ||
		target.Provenance != serverapi.WorkflowExecutionTargetProvenanceResolved {
		t.Fatalf("managed selection facts = %+v", target)
	}
	if target.EffectiveRoot != nil || target.CurrentBranch != nil || target.ManagedWorktree != nil {
		t.Fatalf("missing binding exposed current operational facts: %+v", target)
	}
}

func TestTaskDetailProjectsUnavailableLegacyObservedManagedTargetForNonDirectoryRoot(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Legacy target", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	worktreeID := "worktree-legacy-detail"
	worktreeRoot := filepath.Join(t.TempDir(), "legacy")
	if err := os.Mkdir(worktreeRoot, 0o755); err != nil {
		t.Fatalf("Mkdir worktree root: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if err := store.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: canonicalWorktreeRoot,
		Availability:  "available",
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID:                string(task.ID),
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   1,
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}
	requestedRef := "HEAD"
	observedCommit := "fedcba9876543210"
	candidate := workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &observedCommit,
			Provenance:   workflowstore.ExecutionTargetProvenanceLegacyObserved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       canonicalWorktreeRoot,
			},
		},
	}
	if _, err := workflowStore.StartTaskWithExecutionTarget(ctx, task.ID, &candidate); err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
	if err := os.Remove(worktreeRoot); err != nil {
		t.Fatalf("remove worktree root directory: %v", err)
	}
	if err := os.WriteFile(worktreeRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace worktree root with file: %v", err)
	}

	target := mustTaskDetail(t, view, ctx, string(task.ID)).ExecutionTarget
	if target == nil ||
		target.Provenance != serverapi.WorkflowExecutionTargetProvenanceLegacyObserved ||
		target.CommitOID == nil ||
		*target.CommitOID != observedCommit ||
		target.ManagedWorktree == nil ||
		target.ManagedWorktree.WorktreeID != worktreeID ||
		target.ManagedWorktree.CanonicalRoot != canonicalWorktreeRoot ||
		target.ManagedWorktree.Availability != serverapi.WorktreePathAvailabilityInaccessible ||
		!target.ManagedWorktree.Managed {
		t.Fatalf("legacy observed target = %+v", target)
	}
	if target.EffectiveRoot != nil || target.CurrentBranch != nil {
		t.Fatalf("unavailable legacy target exposed usable root or branch: %+v", target)
	}

	if err := os.Remove(worktreeRoot); err != nil {
		t.Fatalf("remove replaced worktree root file: %v", err)
	}
	missing := mustTaskDetail(t, view, ctx, string(task.ID)).ExecutionTarget
	if missing == nil ||
		missing.ManagedWorktree == nil ||
		missing.ManagedWorktree.Availability != serverapi.WorktreePathAvailabilityMissing ||
		missing.EffectiveRoot != nil ||
		missing.CurrentBranch != nil {
		t.Fatalf("missing legacy target = %+v", missing)
	}
}

func TestBoardProjectsCurrentWorkspaceFactsAndDetachedHistoricalSource(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}

	oneWorkspaceBoard, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard with one workspace: %v", err)
	}
	if oneWorkspaceBoard.Project.DefaultWorkspaceID != binding.WorkspaceID || oneWorkspaceBoard.Project.AttachedWorkspaceCount != 1 {
		t.Fatalf("one-workspace project facts = %+v, want default %q and count 1", oneWorkspaceBoard.Project, binding.WorkspaceID)
	}

	historicalSource, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject historical source: %v", err)
	}
	if _, err := store.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir()); err != nil {
		t.Fatalf("AttachWorkspaceToProject remaining attached workspace: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		Title:             "Historical source",
		Body:              "Body",
		SourceWorkspaceID: historicalSource.WorkspaceID,
	})
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
	if blockers, err := store.UnlinkProjectWorkspace(ctx, binding.ProjectID, historicalSource.WorkspaceID); err != nil {
		t.Fatalf("UnlinkProjectWorkspace: %v", err)
	} else if len(blockers) != 0 {
		t.Fatalf("unlink blockers = %+v, want none", blockers)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}})
	if err != nil {
		t.Fatalf("GetBoard with detached source: %v", err)
	}
	if board.Project.DefaultWorkspaceID != binding.WorkspaceID || board.Project.AttachedWorkspaceCount != 2 {
		t.Fatalf("multi-workspace project facts = %+v, want default %q and count 2", board.Project, binding.WorkspaceID)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	donePage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      doneColumn.Node.NodeID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
	})

	if err != nil {
		t.Fatalf("ListBoardNodeCards done: %v", err)
	}
	if len(donePage.Cards) != 1 || donePage.Cards[0].SourceWorkspace.Availability != string(clientui.ProjectAvailabilityUnlinked) {
		t.Fatalf("done node cards = %+v, want detached historical source availability", donePage.Cards)
	}
}

func TestBoardAndTaskDetailProjectParallelBranchPlacements(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewFanoutWorkflow(t, ctx, workflowStore)
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
	split, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "split", OutputValues: map[string]string{"summary": "plan"}})
	if err != nil {
		t.Fatalf("CompleteRun split: %v", err)
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	for _, column := range board.Columns {
		if column.Node.Kind == string(workflow.NodeKindJoin) || column.Node.Key == "join" {
			t.Fatalf("board columns include hidden join node: %+v", board.Columns)
		}
	}
	planColumn := workflowViewColumnByKey(t, board, "plan")
	if len(planColumn.Node.OutputFields) != 1 || planColumn.Node.OutputFields[0].Name != "summary" || planColumn.Node.OutputFields[0].Description != "Plan summary." {
		t.Fatalf("plan board output fields = %+v, want derived downstream summary", planColumn.Node.OutputFields)
	}
	branchColumn := workflowViewColumnByKey(t, board, "impl_a")
	if len(branchColumn.Node.TransitionOutputFields) != 1 || branchColumn.Node.TransitionOutputFields[0].Name != "summary" || branchColumn.Node.TransitionOutputFields[0].Description != "Plan summary." {
		t.Fatalf("branch transition output fields = %+v, want branch parameters", branchColumn.Node.TransitionOutputFields)
	}
	synthColumn := workflowViewColumnByKey(t, board, "synth")
	if len(synthColumn.Node.TransitionOutputFields) != 1 || synthColumn.Node.TransitionOutputFields[0].Name != "summary" || synthColumn.Node.TransitionOutputFields[0].Description != "Implementation summary." {
		t.Fatalf("synth transition output fields = %+v, want join aggregate parameters", synthColumn.Node.TransitionOutputFields)
	}
	branchPage, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: branchColumn.Node.NodeID, LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}})
	if err != nil {
		t.Fatalf("ListBoardNodeCards branch: %v", err)
	}
	if len(branchPage.Cards) != 1 || len(branchPage.Cards[0].ActiveNodeIDs) != 3 {
		t.Fatalf("board task summary = %+v, want three active branch nodes", branchPage.Cards)
	}
	activeBranchPlacements := 0
	for _, nodeID := range branchPage.Cards[0].ActiveNodeIDs {
		if nodeID != "" {
			activeBranchPlacements++
		}
	}
	if activeBranchPlacements != 3 {
		t.Fatalf("board active nodes = %+v, want three branch nodes", branchPage.Cards[0].ActiveNodeIDs)
	}

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	detailBranchPlacements := 0
	for _, placement := range detail.Placements {
		if placement.ParallelBatchTransitionID == string(split.TransitionID) && placement.ParallelBranchEdgeID != "" {
			detailBranchPlacements++
		}
	}
	if detailBranchPlacements != 3 {
		t.Fatalf("detail placements = %+v, want three branch placements with batch/branch ids", detail.Placements)
	}
}
