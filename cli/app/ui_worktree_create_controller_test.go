package app

import (
	"strings"
	"testing"

	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func newWorktreeControllerTestModel(t *testing.T, client *worktreeCommandTestClient, phase uiWorktreeOverlayPhase) *uiModel {
	t.Helper()
	if client == nil {
		client = &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	}
	model := newWorktreeTestModel(t, client)
	model.worktrees.open = true
	model.worktrees.phase = phase
	return model
}

func newWorktreeCreateControllerTestModel(t *testing.T, client *worktreeCommandTestClient) *uiModel {
	t.Helper()
	model := newWorktreeControllerTestModel(t, client, uiWorktreeOverlayPhaseCreate)
	model.worktrees.create = newWorktreeCreateDialog("")
	return model
}

func applyWorktreeCreateControllerKey(model *uiModel, key tea.KeyMsg) (*uiModel, tea.Cmd) {
	next, cmd := uiInputController{model: model}.handleWorktreeCreateDialogKey(key)
	return next.(*uiModel), cmd
}

func TestWorktreeCreateControllerEscapeAndCancelCloseDialog(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	updated, cmd := applyWorktreeCreateControllerKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("escape should not return command")
	}
	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase after escape = %q, want list", updated.worktrees.phase)
	}

	model = newWorktreeCreateControllerTestModel(t, nil)
	model.worktrees.create.focus = uiWorktreeCreateFieldActions
	model.worktrees.create.action = uiWorktreeCreateActionCancel
	updated, cmd = applyWorktreeCreateControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("cancel should not return command")
	}
	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase after cancel = %q, want list", updated.worktrees.phase)
	}
}

func TestWorktreeCreateControllerNavigatesFields(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	updated, _ := applyWorktreeCreateControllerKey(model, tea.KeyMsg{Type: tea.KeyTab})
	if updated.worktrees.create.focus != uiWorktreeCreateFieldActions {
		t.Fatalf("focus after tab = %v, want actions", updated.worktrees.create.focus)
	}
	updated, _ = applyWorktreeCreateControllerKey(updated, tea.KeyMsg{Type: tea.KeyShiftTab})
	if updated.worktrees.create.focus != uiWorktreeCreateFieldBranchTarget {
		t.Fatalf("focus after shift+tab = %v, want branch target", updated.worktrees.create.focus)
	}

	updated.worktrees.create.resolution = serverapi.WorktreeCreateTargetResolution{Kind: serverapi.WorktreeCreateTargetResolutionKindNewBranch}
	updated, _ = applyWorktreeCreateControllerKey(updated, tea.KeyMsg{Type: tea.KeyDown})
	if updated.worktrees.create.focus != uiWorktreeCreateFieldBaseRef {
		t.Fatalf("focus after down with new branch = %v, want base ref", updated.worktrees.create.focus)
	}
}

func TestWorktreeCreateControllerCyclesActions(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	model.worktrees.create.focus = uiWorktreeCreateFieldActions
	model.worktrees.create.action = uiWorktreeCreateActionCreate

	updated, _ := applyWorktreeCreateControllerKey(model, tea.KeyMsg{Type: tea.KeyRight})
	if updated.worktrees.create.action != uiWorktreeCreateActionCancel {
		t.Fatalf("action after right = %v, want cancel", updated.worktrees.create.action)
	}
	updated, _ = applyWorktreeCreateControllerKey(updated, tea.KeyMsg{Type: tea.KeyLeft})
	if updated.worktrees.create.action != uiWorktreeCreateActionCreate {
		t.Fatalf("action after left = %v, want create", updated.worktrees.create.action)
	}
}

func TestWorktreeCreateControllerSubmitStartsResolution(t *testing.T) {
	client := &worktreeCommandTestClient{
		listResp:    testMainWorktreeListResponse(),
		resolveResp: serverapi.WorktreeCreateTargetResolveResponse{Resolution: serverapi.WorktreeCreateTargetResolution{Input: "feature/new", Kind: serverapi.WorktreeCreateTargetResolutionKindNewBranch}},
	}
	model := newWorktreeCreateControllerTestModel(t, client)
	model.worktrees.create.branchTarget.Replace(strings.NewReplacer("\r", "", "\n", "").Replace("feature/new"))
	model.worktrees.create.focus = uiWorktreeCreateFieldActions
	model.worktrees.create.action = uiWorktreeCreateActionCreate

	updated, cmd := applyWorktreeCreateControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected resolve command")
	}
	if !updated.worktrees.create.resolving || !updated.worktrees.create.submitPending {
		t.Fatalf("create state = %+v, want resolving submit-pending", updated.worktrees.create)
	}
	result := cmd()
	msg, ok := result.(worktreeCreateTargetResolveDoneMsg)
	if !ok {
		t.Fatalf("command message type = %T, want worktreeCreateTargetResolveDoneMsg", result)
	}
	if msg.token != updated.worktrees.create.resolveToken || msg.query != "feature/new" || msg.err != nil {
		t.Fatalf("resolve message = %+v, state token=%d", msg, updated.worktrees.create.resolveToken)
	}
	if len(client.resolveRequests) != 1 || client.resolveRequests[0].Target != "feature/new" {
		t.Fatalf("resolve requests = %+v, want feature/new", client.resolveRequests)
	}
}

func TestWorktreeCreateSetupEventUpdatesPendingOperationState(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()})
	model.worktrees.mutationToken = 7
	model.worktrees.create.submitting = true
	event := serverapi.WorktreeSetupEvent{
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SourceWorkspaceRoot: "/repo",
		WorktreeRoot:        "/repo/.worktrees/feature",
		ScriptPath:          "/repo/scripts/setup.sh",
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	}

	updated := model.worktreeReducer().Update(worktreeSetupEventMsg{token: 7, event: event}).model

	if updated.worktrees.create.setupEvent == nil {
		t.Fatal("setup event was not reduced into create state")
	}
	if updated.worktrees.create.setupEvent.ScriptPath != event.ScriptPath || updated.worktrees.create.setupEvent.WorktreeRoot != event.WorktreeRoot {
		t.Fatalf("setup event state = %+v, want script/worktree paths", updated.worktrees.create.setupEvent)
	}
	if !updated.worktrees.create.submitting {
		t.Fatal("create operation stopped submitting before create response")
	}
}
