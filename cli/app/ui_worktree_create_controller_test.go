package app

import (
	"errors"
	"strings"
	"testing"

	"core/cli/app/internal/worktreeui"
	"core/shared/invariant"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"

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

	updated.worktrees.create.resolution = &worktreepb.CreateTargetResolution{Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}
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
		resolveResp: &worktreepb.CreateTargetResolveSuccess{Resolution: &worktreepb.CreateTargetResolution{Input: "feature/new", Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}},
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
	event := &worktreepb.SetupEvent{
		SetupOperationId: worktreecontract.NewSetupOperationID().String(),
		Phase: &worktreepb.SetupEvent_Started{
			Started: &worktreepb.SetupStarted{
				SourceWorkspaceRoot: "/repo",
				WorktreeRoot:        "/repo/.worktrees/feature",
				ScriptPath:          "/repo/scripts/setup.sh",
			},
		},
	}

	updated := model.reduceWorktreeMessage(worktreeSetupEventMsg{token: 7, event: event}).model

	if updated.worktrees.create.setupEvent == nil {
		t.Fatal("setup event was not reduced into create state")
	}
	if updated.worktrees.create.setupEvent.GetStarted() == nil ||
		updated.worktrees.create.setupEvent.GetStarted().ScriptPath != event.GetStarted().ScriptPath ||
		updated.worktrees.create.setupEvent.GetStarted().WorktreeRoot != event.GetStarted().WorktreeRoot {
		t.Fatalf("setup event state = %+v, want script/worktree paths", updated.worktrees.create.setupEvent)
	}
	if !updated.worktrees.create.submitting {
		t.Fatal("create operation stopped submitting before create response")
	}
}

func TestWorktreeCreateCompletionSwitchesByStableWorktreeID(t *testing.T) {
	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeCreateControllerTestModel(t, client)
	model.worktrees.mutationToken = 7
	created := testRegisteredWorktreeListEntry(
		"wt-created",
		"created",
		"/wt/created",
		"feature/created",
		false,
		false,
		true,
		true,
	)

	next, cmd := model.Update(worktreeCreateDoneMsg{
		token: 7,
		resp:  &worktreepb.CreateSuccess{Worktree: created},
	})
	_ = next.(*uiModel)
	_ = collectCmdMessages(t, cmd)

	if len(client.enterRequests) != 1 || client.enterRequests[0].Selector != "wt-created" {
		t.Fatalf("enter requests = %+v, want created worktree ID", client.enterRequests)
	}
}

func TestWorktreeCreateErrorOwnershipStopsSpinnerAndPreservesForm(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	model.worktrees.create.resolution = &worktreepb.CreateTargetResolution{Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}
	model.worktrees.create.branchTarget.Replace(strings.NewReplacer("\r", "", "\n", "").Replace("feature/entered"))
	model.worktrees.create.baseRef.Replace(strings.NewReplacer("\r", "", "\n", "").Replace("HEAD"))
	model.worktrees.create.submitting = true
	model.worktrees.mutationToken = 7
	baseError := &worktreecontract.CreateError{
		Owner:      worktreecontract.CreateErrorOwnerBaseRef,
		Diagnostic: "base ref diagnostic",
	}

	updated := model.reduceWorktreeMessage(worktreeCreateDoneMsg{token: 7, err: baseError}).model

	if updated.worktrees.create.submitting {
		t.Fatal("create spinner remained active after failure")
	}
	if updated.worktrees.create.baseRefErrorText != baseError.Diagnostic {
		t.Fatalf("base-ref error = %q, want %q", updated.worktrees.create.baseRefErrorText, baseError.Diagnostic)
	}
	if updated.worktrees.create.errorText != "" {
		t.Fatalf("form error = %q, want empty for Base-ref-owned failure", updated.worktrees.create.errorText)
	}
	if updated.worktrees.create.branchTarget.Text() != "feature/entered" || updated.worktrees.create.baseRef.Text() != "HEAD" {
		t.Fatalf("entered values changed: branch=%q base=%q", updated.worktrees.create.branchTarget.Text(), updated.worktrees.create.baseRef.Text())
	}
}

func TestWorktreeCreateFormErrorsUseDialogErrorRegion(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	model.worktrees.create.submitting = true
	model.worktrees.mutationToken = 7
	formError := &worktreecontract.CreateError{
		Owner:      worktreecontract.CreateErrorOwnerForm,
		Diagnostic: "form diagnostic",
	}

	updated := model.reduceWorktreeMessage(worktreeCreateDoneMsg{token: 7, err: formError}).model

	if updated.worktrees.create.submitting {
		t.Fatal("create spinner remained active after failure")
	}
	if updated.worktrees.create.errorText != formError.Diagnostic {
		t.Fatalf("form error = %q, want %q", updated.worktrees.create.errorText, formError.Diagnostic)
	}
	if updated.worktrees.create.baseRefErrorText != "" {
		t.Fatalf("base-ref error = %q, want empty for form-owned failure", updated.worktrees.create.baseRefErrorText)
	}
}

func TestWorktreeCreateNonTypedAndSetupRetainedErrorsUseFormRegion(t *testing.T) {
	for _, source := range []error{
		errors.New("transport failed"),
		&worktreecontract.SetupRetainedError{Details: &worktreepb.SetupRetainedDetails{Diagnostic: "setup retained"}},
		&worktreecontract.CreateContractError{Cause: errors.New("invalid contract")},
	} {
		model := newWorktreeCreateControllerTestModel(t, nil)
		model.worktrees.create.submitting = true
		model.worktrees.mutationToken = 7

		updated := model.reduceWorktreeMessage(worktreeCreateDoneMsg{token: 7, err: source}).model

		if updated.worktrees.create.submitting || updated.worktrees.create.errorText == "" || updated.worktrees.create.baseRefErrorText != "" {
			t.Fatalf("source %T placed incorrectly: state=%+v", source, updated.worktrees.create)
		}
	}
}

func TestWorktreeCreateFieldErrorClearsWhenBaseRefIsEdited(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	model.worktrees.create.resolution = &worktreepb.CreateTargetResolution{Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}
	model.worktrees.create.focus = uiWorktreeCreateFieldBaseRef
	model.worktrees.create.baseRefErrorText = "old base ref error"

	updated, _ := applyWorktreeCreateControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})

	if updated.worktrees.create.baseRefErrorText != "" {
		t.Fatalf("base-ref error = %q, want cleared after edit", updated.worktrees.create.baseRefErrorText)
	}
}

func TestWorktreeCreateFieldErrorClearsWhenBaseRefBecomesHidden(t *testing.T) {
	model := newWorktreeCreateControllerTestModel(t, nil)
	model.worktrees.create.baseRefErrorText = "old base ref error"
	model.worktrees.create.resolution = &worktreepb.CreateTargetResolution{Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}

	model.worktrees.create.applyResolveState(worktreeui.State{
		Resolution: &worktreepb.CreateTargetResolution{Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH},
	})

	if model.worktrees.create.baseRefErrorText != "" {
		t.Fatalf("base-ref error = %q, want cleared when field is hidden", model.worktrees.create.baseRefErrorText)
	}
}

func TestWorktreeCreateInvalidTypedErrorUsesContractPolicy(t *testing.T) {
	tests := []struct {
		name    string
		invalid *worktreecontract.CreateError
	}{
		{name: "invalid owner", invalid: &worktreecontract.CreateError{Owner: "invalid", Diagnostic: "invalid owner"}},
		{name: "blank diagnostic", invalid: &worktreecontract.CreateError{Owner: worktreecontract.CreateErrorOwnerForm, Diagnostic: ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := test.invalid
			model := newWorktreeCreateControllerTestModel(t, nil)
			model.worktrees.create.submitting = true
			model.worktrees.create.baseRefErrorText = "stale"

			model.debugMode = false
			model.applyWorktreeCreateError(invalid)
			if model.worktrees.create.errorText == "" || model.worktrees.create.baseRefErrorText != "" {
				t.Fatalf("release invalid typed state = %+v, want form-level contract error", model.worktrees.create)
			}

			model.debugMode = true
			defer func() {
				recovered := recover()
				diagnostic, ok := recovered.(invariant.Diagnostic)
				if !ok {
					t.Fatalf("panic payload = %T, want invariant.Diagnostic", recovered)
				}
				if diagnostic.Fields[invariant.FieldOperation] == "" ||
					diagnostic.Fields[invariant.FieldRawOwner] == "" ||
					diagnostic.Fields[invariant.FieldValidationCause] == "" ||
					diagnostic.Stack == "" {
					t.Fatalf("invariant diagnostic = %+v, want operation/raw owner/cause/stack", diagnostic)
				}
			}()
			model.applyWorktreeCreateError(invalid)
		})
	}
}
