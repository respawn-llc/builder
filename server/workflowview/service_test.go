package workflowview

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/runtime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestBoardAndTaskDetailUseDurableWorkflowMetadataOnly(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
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
	donePage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: doneColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards done: %v", err)
	}
	if len(donePage.Cards) != 1 || donePage.Cards[0].Status.Kind != "done" {
		t.Fatalf("done cards = %+v, want done task card", donePage.Cards)
	}

	detail, err := view.GetTask(ctx, string(task.ID))
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
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	if doneColumn.TaskCount != 2 {
		t.Fatalf("done metadata count = %d, want 2", doneColumn.TaskCount)
	}
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: string(workflowID),
		NodeID:     doneColumn.Node.NodeID,
		PageSize:   1,
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(page.Cards) != 1 || page.NextPageToken == nil {
		t.Fatalf("done page = %+v, want one card and an older-page cursor", page)
	}
}

func TestWorkflowPickerAndAttentionIncludeScriptPathDiagnostics(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(board.WorkflowPicker) != 1 || board.WorkflowPicker[0].ValidForTaskCreation {
		t.Fatalf("workflow picker = %+v, want script-path blocker", board.WorkflowPicker)
	}
	if len(board.WorkflowPicker[0].ValidationErrors) != 1 || board.WorkflowPicker[0].ValidationErrors[0].Code != workflowscript.CodeMissingPath {
		t.Fatalf("picker validation errors = %+v, want missing script path", board.WorkflowPicker[0].ValidationErrors)
	}
	attention, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(attention.Items) != 1 || attention.Items[0].Kind != "validation_blocker" || attention.Items[0].WorkflowID == nil || *attention.Items[0].WorkflowID != string(created.ID) {
		t.Fatalf("attention items = %+v, want workflow validation blocker", attention.Items)
	}
}

func TestBoardAndTaskDetailProjectTaskSourceWorkspaceAndBody(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	backlogPage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards backlog: %v", err)
	}
	if len(backlogPage.Cards) != 1 || backlogPage.Cards[0].SourceWorkspace.WorkspaceID != source.WorkspaceID || backlogPage.Cards[0].Preview.Markdown != wantBody || backlogPage.Cards[0].Preview.Truncated {
		t.Fatalf("node cards = %+v, want source workspace %q and complete bounded preview %q", backlogPage.Cards, source.WorkspaceID, wantBody)
	}
	detail, err := view.GetTask(ctx, string(task.ID))
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
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: string(workflowID),
		NodeID:     backlogColumn.Node.NodeID,
	}, testsetup.QuestionsEnabled("coder"))
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
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: string(workflowID),
		NodeID:     backlogColumn.Node.NodeID,
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(page.Cards) != 25 {
		t.Fatalf("default card page length = %d, want 25", len(page.Cards))
	}
}

func TestTaskDetailProjectsExecutionTargetOnlyAfterLockAndNoneUsesSourceWorkspace(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}

	oneWorkspaceBoard, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard with detached source: %v", err)
	}
	if board.Project.DefaultWorkspaceID != binding.WorkspaceID || board.Project.AttachedWorkspaceCount != 2 {
		t.Fatalf("multi-workspace project facts = %+v, want default %q and count 2", board.Project, binding.WorkspaceID)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	donePage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: string(workflowID),
		NodeID:     doneColumn.Node.NodeID,
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards done: %v", err)
	}
	if len(donePage.Cards) != 1 || donePage.Cards[0].SourceWorkspace.Availability != string(clientui.ProjectAvailabilityUnlinked) {
		t.Fatalf("done node cards = %+v, want detached historical source availability", donePage.Cards)
	}
}

func TestBoardAndTaskDetailProjectParallelBranchPlacements(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
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
	branchPage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: branchColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
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

	detail, err := view.GetTask(ctx, string(task.ID))
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
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID, WorkflowID: &selectedWorkflowID}, testsetup.QuestionsEnabled("coder"))
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
	selectedPage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(selected.ID), NodeID: selectedBacklog.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
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
	ctx, _, _, binding, view := newWorkflowViewTestContextService(t)

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
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
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
			{ProjectID: binding.ProjectID},
			{ProjectID: binding.ProjectID, WorkflowID: &unknownWorkflowID},
		} {
			board, err := view.GetBoard(ctx, request, testsetup.QuestionsEnabled("coder"))
			if err != nil {
				t.Fatalf("GetBoard: %v", err)
			}
			if board.SelectedWorkflow == nil || board.SelectedWorkflow.WorkflowID != string(defaultWorkflowID) {
				t.Fatalf("selected workflow = %+v, want active default %s", board.SelectedWorkflow, defaultWorkflowID)
			}
		}
	})

	t.Run("first active link", func(t *testing.T) {
		ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
		workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, false); err != nil {
			t.Fatalf("LinkWorkflow: %v", err)
		}
		unknownWorkflowID := "workflow-unknown"

		board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID, WorkflowID: &unknownWorkflowID}, testsetup.QuestionsEnabled("coder"))
		if err != nil {
			t.Fatalf("GetBoard: %v", err)
		}
		if board.SelectedWorkflow == nil || board.SelectedWorkflow.WorkflowID != string(workflowID) {
			t.Fatalf("selected workflow = %+v, want first active link %s", board.SelectedWorkflow, workflowID)
		}
	})
}

func TestTaskDetailPrefersActiveWorkflowLink(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	link, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true)
	if err != nil {
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
	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Workflow.WorkflowID != string(workflowID) || !detail.Workflow.IsProjectDefault || !detail.Workflow.ValidForTaskCreation {
		t.Fatalf("workflow link = %+v, want active default link", detail.Workflow)
	}
	_ = link
}

func TestBoardColumnTaskCountsUseFullSelectedWorkflow(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
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
	firstPage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID, PageSize: 1}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards first: %v", err)
	}
	if len(firstPage.Cards) != 1 || firstPage.NextPageToken == nil {
		t.Fatalf("first node page = %+v, want one card with next page", firstPage)
	}
	secondPage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID, PageSize: 1, PageToken: firstPage.NextPageToken}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards second: %v", err)
	}
	if len(secondPage.Cards) != 1 || secondPage.Cards[0].TaskID == firstPage.Cards[0].TaskID {
		t.Fatalf("second node page = %+v first=%+v, want distinct next card", secondPage, firstPage)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	if _, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: doneColumn.Node.NodeID, PageSize: 1, PageToken: firstPage.NextPageToken}, testsetup.QuestionsEnabled("coder")); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("ListBoardNodeCards with token from other node error = %v", err)
	}
}

func TestBoardNodeCardsBidirectionalPaginationRoundTripsWithoutGaps(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	listPage := func(pageToken *string) serverapi.WorkflowBoardNodeCardsListResponse {
		t.Helper()
		page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: string(workflowID),
			NodeID:     backlog.Node.NodeID,
			PageSize:   25,
			PageToken:  pageToken,
		}, testsetup.QuestionsEnabled("coder"))
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
			if _, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
				ProjectID:  binding.ProjectID,
				WorkflowID: string(workflowID),
				NodeID:     backlog.Node.NodeID,
				PageSize:   25,
				PageToken:  &token,
			}, testsetup.QuestionsEnabled("coder")); !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("%s error = %v, want ErrInvalidPageToken", testCase.name, err)
			}
		})
	}
}

func TestBoardNodeCardsArchiveCanceledTaskInDoneNode(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowIDPointerForTest(workflowID), Title: "Canceled backlog", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(t, ctx, store, task.ID, workflowID)
	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlogColumn.TaskCount != 0 {
		t.Fatalf("backlog count = %d, want canceled task archived out of backlog", backlogColumn.TaskCount)
	}
	backlogPage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
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
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: doneColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards done: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].TaskID != string(task.ID) || page.Cards[0].Status.Kind != "canceled" {
		t.Fatalf("done node cards = %+v, want canceled task", page.Cards)
	}
}

func TestBoardNodeCardsAllowRestartAfterDoneTaskResetToBacklog(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
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
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	backlogColumn := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlogColumn.TaskCount != 1 {
		t.Fatalf("backlog count = %d, want reset task", backlogColumn.TaskCount)
	}
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: backlogColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards backlog: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].TaskID != string(task.ID) || page.Cards[0].Status.Kind != "backlog" {
		t.Fatalf("backlog page = %+v, want reset backlog task", page)
	}
	if !page.Cards[0].Actions.CanStart || page.Cards[0].Actions.CanResume {
		t.Fatalf("reset backlog card actions = %+v, want start-only action state", page.Cards[0].Actions)
	}
	attention, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{}, testsetup.QuestionsEnabled("coder"))
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
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	if err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(t, ctx, store, task.ID, workflowID)
	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	archiveColumn := workflowViewColumnByKey(t, board, "archive")
	if archiveColumn.TaskCount != 0 {
		t.Fatalf("archive count = %d, want no fallback canceled tasks", archiveColumn.TaskCount)
	}
	page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: string(archiveNodeID)}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards archive: %v", err)
	}
	if len(page.Cards) != 0 {
		t.Fatalf("archive node cards = %+v, want no fallback canceled tasks", page.Cards)
	}
	workflowIDString := string(workflowID)
	done, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &binding.ProjectID, WorkflowID: &workflowIDString, ColumnKeys: []string{"done"}}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTasks done: %v", err)
	}
	if len(done.Tasks) != 1 || done.Tasks[0].TaskID != string(task.ID) || done.Tasks[0].ColumnKeys == nil || !reflect.DeepEqual(*done.Tasks[0].ColumnKeys, []string{"done"}) {
		t.Fatalf("done tasks = %+v, want canceled task only in done", done.Tasks)
	}
	archive, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &binding.ProjectID, WorkflowID: &workflowIDString, ColumnKeys: []string{"archive"}}, testsetup.QuestionsEnabled("coder"))
	if err != nil || len(archive.Tasks) != 0 {
		t.Fatalf("archive tasks = %+v/%v, want no canceled task", archive.Tasks, err)
	}
}

func TestBoardProjectsManualMoveTargetsFromServerPermissions(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	activeColumn := workflowViewColumnByKey(t, board, "agent")
	activePage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: activeColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
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

func TestBoardHidesManualMoveTargetsForStartedRun(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	activeColumn := workflowViewColumnByKey(t, board, "agent")
	activePage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: activeColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards active: %v", err)
	}
	if len(activePage.Cards) != 1 {
		t.Fatalf("node cards = %+v, want one active card", activePage.Cards)
	}
	if activePage.Cards[0].Status.Kind != "running" || !activePage.Cards[0].Actions.CanInterrupt {
		t.Fatalf("running card status/actions = %+v/%+v", activePage.Cards[0].Status, activePage.Cards[0].Actions)
	}
	if got := activePage.Cards[0].Actions.ManualMoveTargetNodeIDs; len(got) != 0 {
		t.Fatalf("manual move targets = %+v, want none while run is started", got)
	}
}

func TestTaskDetailProjectsCancellationAndInterruptedRun(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	if err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Summary.CanceledAt == nil || *detail.Summary.CanceledAt == 0 || detail.Summary.CancelReason == nil || *detail.Summary.CancelReason != "stop" {
		t.Fatalf("summary does not project cancellation: %+v", detail.Summary)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].InterruptedAtUnixMs == nil || detail.Runs[0].InterruptionReason == nil || *detail.Runs[0].InterruptionReason != "task_canceled" {
		t.Fatalf("runs do not project interruption: %+v", detail.Runs)
	}
	if detail.Actions.CanResume {
		t.Fatalf("canceled task should not expose resume actions: %+v", detail.Actions)
	}
}

func TestPendingApprovalTaskRemainsVisibleOnSourceBoardColumn(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	if pending.State != "pending_approval" {
		t.Fatalf("completion state = %q, want pending_approval", pending.State)
	}

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
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
	sourcePage, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: sourceColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
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
	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind != "waiting_approval" || len(detail.Summary.ActiveNodeIDs) != 1 || detail.Summary.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("task detail = %+v, want pending approval at source node %s", detail, sourceColumn.Node.NodeID)
	}
	byShortID, err := view.GetTaskByProjectShortID(ctx, binding.ProjectID, task.ShortID)
	if err != nil {
		t.Fatalf("GetTaskByProjectShortID: %v", err)
	}
	if byShortID.Status.Kind != "waiting_approval" || len(byShortID.Summary.ActiveNodeIDs) != 1 || byShortID.Summary.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("task detail by short id = %+v, want pending approval at source node %s", byShortID, sourceColumn.Node.NodeID)
	}
	byGlobalShortID, err := view.GetTaskByShortID(ctx, task.ShortID)
	if err != nil {
		t.Fatalf("GetTaskByShortID: %v", err)
	}
	if byGlobalShortID.Status.Kind != "waiting_approval" || len(byGlobalShortID.Summary.ActiveNodeIDs) != 1 || byGlobalShortID.Summary.ActiveNodeIDs[0] != sourceColumn.Node.NodeID {
		t.Fatalf("task detail by global short id = %+v, want pending approval at source node %s", byGlobalShortID, sourceColumn.Node.NodeID)
	}
}

func TestTaskStatusIgnoresHistoricalRunUnderCompletedPlacement(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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

	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind != serverapi.WorkflowTaskStatusKindWaitingApproval || slices.Contains(detail.Status.RunIDs, string(started.RunID)) {
		t.Fatalf("detail status = %+v, want waiting approval without stale run", detail.Status)
	}

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	sourceColumn := workflowViewColumnByKey(t, board, "agent")
	cards, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: string(workflowID), NodeID: sourceColumn.Node.NodeID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListBoardNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 || cards.Cards[0].Status.Kind != serverapi.WorkflowTaskStatusKindWaitingApproval || slices.Contains(cards.Cards[0].Status.RunIDs, string(started.RunID)) {
		t.Fatalf("board cards = %+v, want waiting approval without stale run", cards.Cards)
	}
}

func TestTaskDetailAndBoardUseCanonicalPrimaryStatusPrecedence(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	if queuedToRunningBefore.Status.Kind != serverapi.WorkflowTaskStatusKindQueued {
		t.Fatalf("queued task status before claim = %+v", queuedToRunningBefore.Status)
	}
	if _, err := workflowStore.ClaimRun(ctx, queuedToRunningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun queued to running: %v", err)
	}
	queuedToRunningAfter := mustTaskDetail(t, view, ctx, string(queuedToRunning.ID))
	queuedToRunningPlacementsAfter, err := workflowStore.ListPlacements(ctx, queuedToRunning.ID)
	if err != nil {
		t.Fatalf("ListPlacements queued to running after claim: %v", err)
	}
	if queuedToRunningAfter.Status.Kind != serverapi.WorkflowTaskStatusKindRunning || !reflect.DeepEqual(queuedToRunningBefore.Status.NodeIDs, queuedToRunningAfter.Status.NodeIDs) || !reflect.DeepEqual(queuedToRunningPlacementsBefore, queuedToRunningPlacementsAfter) {
		t.Fatalf("claim changed queued task placement or did not make it running: before=%+v/%+v after=%+v/%+v", queuedToRunningBefore.Status, queuedToRunningPlacementsBefore, queuedToRunningAfter.Status, queuedToRunningPlacementsAfter)
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
	if err := workflowStore.CancelTask(ctx, canceled.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	boardStatus := map[string]serverapi.WorkflowTaskStatus{}
	for _, column := range board.Columns {
		page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: string(workflowID),
			NodeID:     column.Node.NodeID,
		}, testsetup.QuestionsEnabled("coder"))
		if err != nil {
			t.Fatalf("ListBoardNodeCards %s: %v", column.Node.Key, err)
		}
		for _, card := range page.Cards {
			boardStatus[card.TaskID] = card.Status
		}
	}
	want := map[string]serverapi.WorkflowTaskStatusKind{
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
	for taskID, wantKind := range want {
		detail, err := view.GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("GetTask %s: %v", taskID, err)
		}
		if detail.Status.Kind != wantKind {
			t.Fatalf("detail status for %s = %+v, want %q", taskID, detail.Status, wantKind)
		}
		if cardStatus, ok := boardStatus[taskID]; !ok || !reflect.DeepEqual(cardStatus, detail.Status) {
			t.Fatalf("board status for %s = %+v, want exact detail status %+v", taskID, cardStatus, detail.Status)
		}
	}
	if !mustTaskDetail(t, view, ctx, string(canceled.ID)).Summary.Done {
		t.Fatal("canceled task must retain active terminal-sink Done position")
	}
	if queuedStarted.RunID == "" {
		t.Fatal("queued fixture must retain its unstarted run")
	}
}

func TestTaskDetailAndBoardPreserveFanoutStatusUnions(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	fixture := createWorkflowViewFanoutStatusFixture(t, ctx, workflowStore, binding)

	detail := mustTaskDetail(t, view, ctx, string(fixture.task.ID))
	want := fixture.status
	if detail.Status.Kind != want.Kind || detail.Status.NativeState != want.NativeState || !reflect.DeepEqual(detail.Status.RunIDs, want.RunIDs) || !reflect.DeepEqual(detail.Status.AttentionTypes, want.AttentionTypes) {
		t.Fatalf("detail status = %+v, want kind/native/run/attention unions %+v", detail.Status, want)
	}

	board, err := view.GetBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	cardStatuses := make([]serverapi.WorkflowTaskStatus, 0, 3)
	for _, key := range []string{"impl_a", "impl_b", "impl_c"} {
		column := workflowViewColumnByKey(t, board, key)
		page, err := view.ListBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: string(fixture.workflowID),
			NodeID:     column.Node.NodeID,
		}, testsetup.QuestionsEnabled("coder"))
		if err != nil {
			t.Fatalf("ListBoardNodeCards %s: %v", key, err)
		}
		for _, card := range page.Cards {
			if card.TaskID == string(fixture.task.ID) {
				cardStatuses = append(cardStatuses, card.Status)
			}
		}
	}
	if len(cardStatuses) != 3 {
		t.Fatalf("fanout board status projections = %+v, want every branch card", cardStatuses)
	}
	for _, status := range cardStatuses {
		if !reflect.DeepEqual(status, detail.Status) {
			t.Fatalf("board status = %+v, want detail status %+v", status, detail.Status)
		}
	}
	workflowIDString := string(fixture.workflowID)
	tasks, err := view.ListTasks(ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &binding.ProjectID,
		WorkflowID:  &workflowIDString,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
	}, testsetup.QuestionsEnabled("coder"))
	if err != nil || len(tasks.Tasks) != 1 || tasks.Tasks[0].TaskID != string(fixture.task.ID) || !reflect.DeepEqual(tasks.Tasks[0].Status, detail.Status) {
		t.Fatalf("fanout list status = %+v/%v, want exact detail status %+v", tasks.Tasks, err, detail.Status)
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

func mustTaskDetail(t *testing.T, view *Service, ctx context.Context, taskID string) serverapi.WorkflowTaskDetail {
	t.Helper()
	detail, err := view.GetTask(ctx, taskID)
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
	view, err := New(store, WithSessionTranscriptProvider(staticTranscriptProvider{entries: map[string][]runtime.ChatEntry{
		"session-view-waiting-ask": transcriptEntriesWithAskOptions("ask-view-1", "Waiting ask?", []string{"Trail mix", "Dark chocolate", "Pistachios"}, 2),
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sessionID := "session-view-waiting-ask"
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, store, workflowStore, binding, sessionID, "ask-view-1")

	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(detail.Runs) != 1 || detail.Runs[0].WaitingAskID == nil || *detail.Runs[0].WaitingAskID != "ask-view-1" || detail.Runs[0].SessionID != sessionID {
		t.Fatalf("runs do not project waiting ask: %+v", detail.Runs)
	}
	if detail.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.AttentionCount)
	}
	attention, err := view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(attention.Items) != 1 || attention.Items[0].Kind != "question" || attention.Items[0].AskID != "ask-view-1" || strings.TrimSpace(attention.Items[0].Message) == "" || len(attention.Items[0].Suggestions) != 3 || attention.Items[0].RecommendedOptionIndex != 2 {
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
	view, err := New(store, WithPendingPromptSource(staticPendingPromptSource{sessionID: {{
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
	}}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	task, started := createWorkflowViewWaitingAskTask(t, ctx, store, workflowStore, binding, sessionID, askID)

	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.AttentionCount)
	}
	taskAttention, err := view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	assertRuntimeApprovalQuestionAttention(t, taskAttention.Items, string(task.ID), string(started.RunID), sessionID, askID)
	list, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	assertRuntimeApprovalQuestionAttention(t, list.Items, string(task.ID), string(started.RunID), sessionID, askID)
}

func TestTaskDetailPendingQuestionFallsBackWhenTranscriptLookupFails(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	sessionID := "session-missing-question-transcript"
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, store, workflowStore, binding, sessionID, "ask-missing-transcript")

	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.AttentionCount)
	}
	attention, err := view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)}, testsetup.QuestionsEnabled("coder"))
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

func TestTaskActivityListMergesDurableTaskEventsAndPaginatesStably(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
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
	if err := workflowStore.ReplaceComment(ctx, comment.ID, "edited note"); err != nil {
		t.Fatalf("ReplaceComment: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	if err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_comments SET updated_at_unix_ms = 111 WHERE id = ?`, comment.ID); err != nil {
		t.Fatalf("force comment timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_runs SET started_at_unix_ms = 111, interrupted_at_unix_ms = 111, updated_at_unix_ms = 111 WHERE id = ?`, string(started.RunID)); err != nil {
		t.Fatalf("force run timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET canceled_at_unix_ms = 111, updated_at_unix_ms = 111 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force task timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_transitions SET created_at_unix_ms = 111, applied_at_unix_ms = 111 WHERE task_id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force transition timestamp: %v", err)
	}

	first, err := view.ListTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(task.ID), PageSize: 2})
	if err != nil {
		t.Fatalf("ListTaskActivity first: %v", err)
	}
	newComment, err := workflowStore.AddComment(ctx, task.ID, "newer note", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment newer: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_comments SET updated_at_unix_ms = 222 WHERE id = ?`, newComment.ID); err != nil {
		t.Fatalf("force newer comment timestamp: %v", err)
	}
	second, err := view.ListTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(task.ID), PageSize: 10, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatalf("ListTaskActivity second: %v", err)
	}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ActivityID] {
			t.Fatalf("duplicate activity item across pages: %s", item.ActivityID)
		}
		if item.ActivityID == "comment:"+newComment.ID {
			t.Fatalf("newer activity inserted between page fetches leaked into older page: %+v", item)
		}
		seen[item.ActivityID] = true
		kinds[item.Type] = true
	}
	for _, kind := range []string{"comment", "transition", "run_started", "run_interrupted", "task_canceled"} {
		if !kinds[kind] {
			t.Fatalf("activity kinds = %+v, missing %s; items=%+v/%+v", kinds, kind, first.Items, second.Items)
		}
	}
	if first.Items[0].OccurredAtUnixMs != 111 || first.Items[1].OccurredAtUnixMs != 111 || first.NextPageToken == "" {
		t.Fatalf("first page = %+v", first)
	}
}

func TestTaskActivityProjectsApprovalSnapshots(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	pending, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done", Commentary: "needs approval", Actor: "agent"})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	resp, err := view.ListTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTaskActivity: %v", err)
	}
	var transition serverapi.WorkflowTaskTransition
	hasRunCompleted := false
	for _, item := range resp.Items {
		if item.Type == "run_completed" && item.Run != nil && item.Run.ID == string(started.RunID) {
			hasRunCompleted = true
		}
		if item.Type == "transition" && item.Transition != nil && item.Transition.ID == string(pending.TransitionID) {
			transition = *item.Transition
		}
	}
	if !hasRunCompleted {
		t.Fatalf("activity missing run_completed item: %+v", resp.Items)
	}
	if transition.ID == "" || transition.SourceNodeID == "" || transition.SourceNodeDisplayName != "Agent" || transition.TransitionDisplayName != "Done" || transition.WorkflowRevisionSeen == 0 || transition.Actor != "agent" || transition.Commentary != "needs approval" || transition.AppliedAtUnixMs != nil {
		t.Fatalf("transition snapshot = %+v", transition)
	}
	if len(transition.Edges) != 1 || !transition.Edges[0].RequiresApproval || transition.Edges[0].TargetNodeDisplayName == "" || len(transition.Edges[0].OutputRequirements) != 0 || transition.Edges[0].WorkflowRevisionSeen == 0 {
		t.Fatalf("edge snapshot = %+v", transition.Edges)
	}
}

func TestAttentionListProjectsApprovalQuestionAndInterruptedRun(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	view, err := New(store, WithSessionTranscriptProvider(staticTranscriptProvider{entries: map[string][]runtime.ChatEntry{
		"session-attention-question": transcriptEntriesWithAsk("ask-attention", "Attention ask?"),
	}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	approvalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask approval: %v", err)
	}
	approvalStarted, err := workflowStore.StartTask(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	pendingApproval, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"})
	if err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}
	if pendingApproval.State != "pending_approval" {
		t.Fatalf("approval completion = %+v, want pending_approval", pendingApproval)
	}
	questionTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Question", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask question: %v", err)
	}
	questionStarted, err := workflowStore.StartTask(ctx, questionTask.ID)
	if err != nil {
		t.Fatalf("StartTask question: %v", err)
	}
	questionClaimed, err := workflowStore.ClaimRun(ctx, questionStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun question: %v", err)
	}
	sessionID := "session-attention-question"
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, previous_session_id, parent_agent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', NULL, NULL, 1, 1, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := workflowStore.AttachRunSession(ctx, questionStarted.RunID, questionClaimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession question: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, questionStarted.RunID, questionClaimed.Generation, "ask-attention"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	interruptedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Interrupted", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask interrupted: %v", err)
	}
	interruptedStarted, err := workflowStore.StartTask(ctx, interruptedTask.ID)
	if err != nil {
		t.Fatalf("StartTask interrupted: %v", err)
	}
	interruptedClaimed, err := workflowStore.ClaimRun(ctx, interruptedStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, interruptedStarted.RunID, interruptedClaimed.Generation, "manual", `{"error":"role missing"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	resp, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	kinds := map[string]serverapi.WorkflowAttentionItem{}
	for _, item := range resp.Items {
		kinds[item.Kind] = item
	}
	if kinds["approval"].TaskTransitionID != string(pendingApproval.TransitionID) || kinds["question"].AskID != "ask-attention" || kinds["interrupted_run"].TaskID != string(interruptedTask.ID) || kinds["interrupted_run"].RunID != string(interruptedStarted.RunID) || kinds["interrupted_run"].Message != "Run interrupted: manual: role missing" {
		t.Fatalf("attention items = %+v", resp.Items)
	}
	firstPage, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention first page: %v", err)
	}
	if len(firstPage.Items) != 1 || firstPage.NextPageToken == "" {
		t.Fatalf("first attention page = %+v, want one item and next token", firstPage)
	}
	secondPage, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1, PageToken: firstPage.NextPageToken}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention second page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second attention page = %+v first=%+v, want distinct next item", secondPage, firstPage)
	}
	taskResp, err := view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(questionTask.ID)}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(taskResp.Items) != 1 || taskResp.Items[0].Kind != "question" || taskResp.Items[0].TaskID != string(questionTask.ID) {
		t.Fatalf("task attention items = %+v", taskResp.Items)
	}
}

func TestCompletedPlacementQuestionRunIsExcludedFromTaskAndAttentionProjections(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextService(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Historical question", Body: "Body"})
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
	if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-historical"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_node_placements SET state = 'completed' WHERE id = ?`, string(started.PlacementID)); err != nil {
		t.Fatalf("seed completed-placement historical question run: %v", err)
	}

	detail, err := view.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind == serverapi.WorkflowTaskStatusKindWaitingQuestion || len(detail.Status.RunIDs) != 0 || len(detail.Status.AttentionTypes) != 0 || detail.AttentionCount != 0 {
		t.Fatalf("detail = %+v, want no historical question state or attention", detail)
	}
	global, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(global.Items) != 0 {
		t.Fatalf("global attention = %+v, want no historical question item", global.Items)
	}
	taskAttention, err := view.ListTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(taskAttention.Items) != 0 {
		t.Fatalf("task attention = %+v, want no historical question item", taskAttention.Items)
	}
	home, err := store.ListProjectHomeSummaries(ctx, binding.ProjectID, 1, 0)
	if err != nil {
		t.Fatalf("ListProjectHomeSummaries: %v", err)
	}
	if len(home) != 1 || home[0].AttentionCount != 0 {
		t.Fatalf("project home = %+v, want no historical question attention count", home)
	}
}

func TestAttentionListFillsPagePastDroppedCandidates(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	view, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	approvalWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, approvalWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow approval: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, approvalWorkflowID)
	approvalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask approval: %v", err)
	}
	approvalStarted, err := workflowStore.StartTask(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}

	// Two extra valid linked workflows produce validation_blocker candidates that
	// get dropped (they validate cleanly). Force them newest so they sort ahead
	// of the approval, spanning the first candidate fetch window.
	for i, title := range []string{"Clean A", "Clean B"} {
		cleanWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, cleanWorkflowID, false); err != nil {
			t.Fatalf("LinkWorkflow %s: %v", title, err)
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE workflows SET updated_at_unix_ms = ? WHERE id = ?`, int64(1_000_000_000_000+i), string(cleanWorkflowID)); err != nil {
			t.Fatalf("force clean workflow timestamp: %v", err)
		}
	}

	// With pageSize 1 the dropped candidates fill the first fetch; the page must
	// still surface the real approval item instead of coming back empty.
	page, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1}, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Kind != "approval" {
		t.Fatalf("attention page = %+v, want the approval item past dropped candidates", page.Items)
	}
	if page.NextPageToken == "" {
		t.Fatal("expected a next page token while candidates remain")
	}

}

func TestAttentionListExcludesNonActionableInterruptions(t *testing.T) {
	tests := []struct {
		name      string
		interrupt func(*testing.T, context.Context, *workflowstore.Store, workflowstore.TaskRecord, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord)
	}{
		{
			name: "user initiated",
			interrupt: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflowstore.TaskRecord, _ workflowstore.StartTaskResult, _ workflowstore.RunnableRunRecord) {
				if _, err := store.InterruptTaskRuns(ctx, task.ID, "", ""); err != nil {
					t.Fatalf("InterruptTaskRuns: %v", err)
				}
			},
		},
		{
			name: "blank reason",
			interrupt: func(t *testing.T, ctx context.Context, store *workflowstore.Store, _ workflowstore.TaskRecord, started workflowstore.StartTaskResult, claimed workflowstore.RunnableRunRecord) {
				if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, " \t ", "{}"); err != nil {
					t.Fatalf("InterruptRunGeneration: %v", err)
				}
			},
		},
		{
			name: "runtime canceled",
			interrupt: func(t *testing.T, ctx context.Context, store *workflowstore.Store, _ workflowstore.TaskRecord, started workflowstore.StartTaskResult, claimed workflowstore.RunnableRunRecord) {
				if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "workflow_runtime_canceled", "{}"); err != nil {
					t.Fatalf("InterruptRunGeneration: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _, workflowStore, binding, view := newWorkflowViewTestContextService(t)
			workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
			if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
				t.Fatalf("LinkWorkflow: %v", err)
			}
			task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: tt.name, Body: "Body"})
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
			tt.interrupt(t, ctx, workflowStore, task, started, claimed)

			resp, err := view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{}, testsetup.QuestionsEnabled("coder"))
			if err != nil {
				t.Fatalf("ListAttention: %v", err)
			}
			for _, item := range resp.Items {
				if item.Kind == "interrupted_run" {
					t.Fatalf("non-actionable interruption surfaced as attention: %+v", resp.Items)
				}
			}
		})
	}
}

func newWorkflowViewTestStore(t *testing.T) (*metadata.Store, *workflowstore.Store, metadata.Binding) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_PERSISTENCE_ROOT", filepath.Join(home, ".kent"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	workflowStore, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return metadataStore, workflowStore, binding
}

func newWorkflowViewTestContextStore(t *testing.T) (context.Context, *metadata.Store, *workflowstore.Store, metadata.Binding) {
	t.Helper()
	store, workflowStore, binding := newWorkflowViewTestStore(t)
	return context.Background(), store, workflowStore, binding
}

func newWorkflowViewTestService(t *testing.T) (*metadata.Store, *workflowstore.Store, metadata.Binding, *Service) {
	t.Helper()
	store, workflowStore, binding := newWorkflowViewTestStore(t)
	view, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, workflowStore, binding, view
}

func newWorkflowViewTestContextService(t *testing.T) (context.Context, *metadata.Store, *workflowstore.Store, metadata.Binding, *Service) {
	t.Helper()
	store, workflowStore, binding, view := newWorkflowViewTestService(t)
	return context.Background(), store, workflowStore, binding, view
}

func runWorkflowViewGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %q: %v\n%s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func forceCanceledBacklogPlacementWithoutTerminal(t *testing.T, ctx context.Context, store *metadata.Store, taskID workflow.TaskID, workflowID workflow.WorkflowID) {
	t.Helper()
	var startNodeID string
	if err := store.DB().QueryRowContext(ctx, `
SELECT id
FROM workflow_nodes
WHERE workflow_id = ?
  AND kind = 'start'`, string(workflowID)).Scan(&startNodeID); err != nil {
		t.Fatalf("resolve canceled backlog start node: %v", err)
	}
	if _, err := store.Queries().DeleteTaskNodePlacementsByTask(ctx, string(taskID)); err != nil {
		t.Fatalf("remove canceled task placements: %v", err)
	}
	if err := store.Queries().InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{
		ID:              "placement-canceled-backlog-" + string(taskID),
		TaskID:          string(taskID),
		NodeID:          sql.NullString{String: startNodeID, Valid: strings.TrimSpace(startNodeID) != ""},
		State:           "active",
		CreatedAtUnixMs: 1,
		UpdatedAtUnixMs: 1,
	}); err != nil {
		t.Fatalf("insert canceled backlog placement: %v", err)
	}
}

func requireDoneTransitionApproval(t *testing.T, ctx context.Context, store *metadata.Store, workflowID workflow.WorkflowID) {
	t.Helper()
	if _, err := store.DB().ExecContext(ctx, `
UPDATE workflow_edges
SET requires_approval = 1
WHERE edge_key = 'done'
  AND EXISTS (
      SELECT 1
      FROM workflow_transition_groups tg
      JOIN workflow_nodes source ON source.id = tg.source_node_id
      WHERE tg.id = workflow_edges.transition_group_id
        AND source.workflow_id = ?
  )`, string(workflowID)); err != nil {
		t.Fatalf("require approval: %v", err)
	}
}

func createWorkflowViewValidWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-start-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-start-" + string(created.ID)), Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(created.ID)), WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(created.ID)), Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func createWorkflowViewFanoutWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Fanout Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, def, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, def, workflow.NodeKindTerminal)
	planID := workflow.NodeID("node-plan-" + string(created.ID))
	implAID := workflow.NodeID("node-impl-a-" + string(created.ID))
	implBID := workflow.NodeID("node-impl-b-" + string(created.ID))
	implCID := workflow.NodeID("node-impl-c-" + string(created.ID))
	joinID := workflow.NodeID("node-join-" + string(created.ID))
	synthID := workflow.NodeID("node-synth-" + string(created.ID))
	for _, node := range []workflowstore.NodeRecord{
		{ID: planID, WorkflowID: created.ID, Key: "plan", Kind: workflow.NodeKindAgent, DisplayName: "Plan", SubagentRole: "coder"},
		{ID: implAID, WorkflowID: created.ID, Key: "impl_a", Kind: workflow.NodeKindAgent, DisplayName: "Implement A", SubagentRole: "coder"},
		{ID: implBID, WorkflowID: created.ID, Key: "impl_b", Kind: workflow.NodeKindAgent, DisplayName: "Implement B", SubagentRole: "coder"},
		{ID: implCID, WorkflowID: created.ID, Key: "impl_c", Kind: workflow.NodeKindAgent, DisplayName: "Implement C", SubagentRole: "coder"},
		{ID: joinID, WorkflowID: created.ID, Key: "join", Kind: workflow.NodeKindJoin, DisplayName: "Join"},
		{ID: synthID, WorkflowID: created.ID, Key: "synth", Kind: workflow.NodeKindAgent, DisplayName: "Synthesize", SubagentRole: "coder"},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("AddNode %s: %v", node.Key, err)
		}
	}
	startGroup := workflow.TransitionGroupID("group-start-" + string(created.ID))
	splitGroup := workflow.TransitionGroupID("group-split-" + string(created.ID))
	joinAGroup := workflow.TransitionGroupID("group-join-a-" + string(created.ID))
	joinBGroup := workflow.TransitionGroupID("group-join-b-" + string(created.ID))
	joinCGroup := workflow.TransitionGroupID("group-join-c-" + string(created.ID))
	synthGroup := workflow.TransitionGroupID("group-join-synth-" + string(created.ID))
	doneGroup := workflow.TransitionGroupID("group-synth-done-" + string(created.ID))
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroup, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
		{ID: splitGroup, WorkflowID: created.ID, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
		{ID: joinAGroup, WorkflowID: created.ID, SourceNodeID: implAID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinBGroup, WorkflowID: created.ID, SourceNodeID: implBID, TransitionID: "join", DisplayName: "Join"},
		{ID: joinCGroup, WorkflowID: created.ID, SourceNodeID: implCID, TransitionID: "join", DisplayName: "Join"},
		{ID: synthGroup, WorkflowID: created.ID, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
		{ID: doneGroup, WorkflowID: created.ID, SourceNodeID: synthID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Plan."},
		{ID: workflow.EdgeID("edge-split-a-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_a", TargetNodeID: implAID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement A.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-split-b-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_b", TargetNodeID: implBID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement B.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-split-c-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: splitGroup, Key: "split_c", TargetNodeID: implCID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement C.", Parameters: []workflow.Parameter{{Key: "summary", Description: "Plan summary."}}},
		{ID: workflow.EdgeID("edge-join-a-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: joinAGroup, Key: "join_a", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Implementation summary."}}},
		{ID: workflow.EdgeID("edge-join-b-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: joinBGroup, Key: "join_b", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-c-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: joinCGroup, Key: "join_c", TargetNodeID: joinID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-join-synth-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: synthGroup, Key: "synth", TargetNodeID: synthID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Synthesize."},
		{ID: workflow.EdgeID("edge-synth-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func workflowViewNodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("missing node kind %q in %+v", kind, def.Nodes)
	return nil
}

func workflowViewNodeByKey(t *testing.T, def workflow.Definition, key string) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if workflow.NodeKey(node) == workflow.ModelKey(key) {
			return node
		}
	}
	t.Fatalf("missing workflow node key %q in %+v", key, def.Nodes)
	return nil
}

func workflowViewColumnByKind(t *testing.T, board serverapi.WorkflowBoard, kind workflow.NodeKind) serverapi.WorkflowBoardColumn {
	t.Helper()
	for _, column := range board.Columns {
		if column.Node.Kind == string(kind) {
			return column
		}
	}
	t.Fatalf("missing board column kind %q in %+v", kind, board.Columns)
	return serverapi.WorkflowBoardColumn{}
}

func workflowViewColumnByKey(t *testing.T, board serverapi.WorkflowBoard, key string) serverapi.WorkflowBoardColumn {
	t.Helper()
	for _, column := range board.Columns {
		if column.Node.Key == key {
			return column
		}
	}
	t.Fatalf("missing board column key %q in %+v", key, board.Columns)
	return serverapi.WorkflowBoardColumn{}
}

func workflowViewBoardColumnKeys(columns []serverapi.WorkflowBoardColumn) []string {
	keys := make([]string, 0, len(columns))
	for _, column := range columns {
		keys = append(keys, column.Node.Key)
	}
	return keys
}

func workflowViewBoardCardIDs(cards []serverapi.WorkflowBoardTaskCard) []string {
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.TaskID)
	}
	return ids
}

type boardNodeCardsTokenFixture struct {
	Version         int    `json:"version"`
	ProjectID       string `json:"project_id"`
	WorkflowID      string `json:"workflow_id"`
	NodeID          string `json:"node_id"`
	UpdatedAtUnixMs int64  `json:"updated_at_unix_ms"`
	TaskID          string `json:"task_id"`
	Direction       string `json:"direction"`
}

func mutateBoardNodeCardsToken(t *testing.T, token *string, mutate func(*boardNodeCardsTokenFixture)) string {
	t.Helper()
	if token == nil {
		t.Fatal("page token is required")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*token)
	if err != nil {
		t.Fatalf("decode page token: %v", err)
	}
	var payload boardNodeCardsTokenFixture
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("unmarshal page token: %v", err)
	}
	mutate(&payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal page token: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func TestWorkflowViewRejectsMissingIDs(t *testing.T) {
	store, _, _ := newWorkflowViewTestStore(t)
	view, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := view.GetBoard(context.Background(), serverapi.WorkflowBoardRequest{ProjectID: " "}, testsetup.QuestionsEnabled()); !isWorkflowRequestValidationField(err, "project_id") {
		t.Fatalf("GetBoard missing id error = %v", err)
	}
	if _, err := view.ListBoardNodeCards(context.Background(), serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: "project-1", WorkflowID: "workflow-1", NodeID: "node-1", PageSize: -1}, testsetup.QuestionsEnabled()); !isWorkflowRequestValidationField(err, "page_size") {
		t.Fatalf("ListBoardNodeCards negative page size error = %v", err)
	}
	if _, err := view.GetTask(context.Background(), " "); !errors.Is(err, ErrTaskIDRequired) {
		t.Fatalf("GetTask missing id error = %v", err)
	}
}

func isWorkflowRequestValidationField(err error, field string) bool {
	var validationErr serverapi.WorkflowRequestValidationError
	return errors.As(err, &validationErr) && validationErr.Field == field
}

type staticTranscriptProvider struct {
	entries map[string][]runtime.ChatEntry
}

type staticPendingPromptSource map[string][]PendingPromptSnapshot

func (s staticPendingPromptSource) ListPendingPrompts(sessionID string) []PendingPromptSnapshot {
	return append([]PendingPromptSnapshot(nil), s[strings.TrimSpace(sessionID)]...)
}

func (p staticTranscriptProvider) SessionTranscriptTailEntries(_ context.Context, sessionID string) ([]runtime.ChatEntry, error) {
	return append([]runtime.ChatEntry(nil), p.entries[strings.TrimSpace(sessionID)]...), nil
}

func transcriptEntriesWithAsk(askID string, question string) []runtime.ChatEntry {
	return []runtime.ChatEntry{askTranscriptEntry(askID, question, nil, 0)}
}

func transcriptEntriesWithAskOptions(askID string, question string, suggestions []string, recommended int) []runtime.ChatEntry {
	return []runtime.ChatEntry{askTranscriptEntry(askID, question, suggestions, recommended)}
}

func askTranscriptEntry(askID string, question string, suggestions []string, recommended int) runtime.ChatEntry {
	return runtime.ChatEntry{
		Role:       "tool_call",
		ToolCallID: askID,
		ToolCall:   &transcript.ToolCallMeta{ToolName: string(toolspec.ToolAskQuestion), Question: question, Suggestions: suggestions, RecommendedOptionIndex: recommended},
	}
}
