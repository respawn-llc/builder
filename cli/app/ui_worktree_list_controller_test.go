package app

import (
	"testing"

	"core/cli/app/internal/worktreeui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func newWorktreeListControllerTestModel(t *testing.T, client *worktreeCommandTestClient) *uiModel {
	t.Helper()
	model := newWorktreeControllerTestModel(t, client, uiWorktreeOverlayPhaseList)
	model.worktrees.entries = []worktreeui.Item{
		mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry("wt-feature", "feature", "/wt/feature", "feature", false, false, true, true)),
		mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry("wt-current", "current", "/wt/current", "current", false, true, true, true)),
	}
	model.setInputMode(uiInputModeWorktree)
	return model
}

func applyWorktreeListControllerKey(model *uiModel, key tea.KeyMsg) (*uiModel, tea.Cmd) {
	next, cmd := uiInputController{model: model}.handleWorktreeOverlayKey(key)
	return next.(*uiModel), cmd
}

func TestWorktreeListControllerNavigatesRows(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	updated, _ := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyDown})
	if updated.worktrees.selection != 1 {
		t.Fatalf("selection after down = %d, want 1", updated.worktrees.selection)
	}
	updated, _ = applyWorktreeListControllerKey(updated, tea.KeyMsg{Type: tea.KeyEnd})
	if updated.worktrees.selection != updated.worktreeRowCount()-1 {
		t.Fatalf("selection after end = %d, want last", updated.worktrees.selection)
	}
	updated, _ = applyWorktreeListControllerKey(updated, tea.KeyMsg{Type: tea.KeyHome})
	if updated.worktrees.selection != 0 {
		t.Fatalf("selection after home = %d, want create row", updated.worktrees.selection)
	}
}

func TestWorktreeListControllerEnterCreateRowOpensCreateDialogThroughUpdate(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 0
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.worktrees.phase != uiWorktreeOverlayPhaseCreate {
		t.Fatalf("phase after enter create row = %q, want create", updated.worktrees.phase)
	}
}

func TestWorktreeListControllerEnterWorktreeSubmitsSwitch(t *testing.T) {
	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	model := newWorktreeListControllerTestModel(t, client)
	model.worktrees.selection = 1
	updated, cmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected switch command")
	}
	if !updated.worktrees.switchPending {
		t.Fatal("expected switch pending state")
	}
	result := cmd()
	if _, ok := result.(worktreeSwitchDoneMsg); !ok {
		t.Fatalf("command message type = %T, want worktreeSwitchDoneMsg", result)
	}
	if len(client.enterRequests) != 1 || client.enterRequests[0].Selector != "wt-feature" {
		t.Fatalf("enter requests = %+v, want stable worktree ID", client.enterRequests)
	}
}

func TestWorktreeListControllerQueuesStableSwitchTarget(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	model.worktrees.switchPending = true

	updated, cmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("queued switch returned a command while another switch is pending")
	}
	if updated.worktrees.queuedSwitch.TargetToken != "wt-feature" {
		t.Fatalf("queued switch = %+v, want stable worktree ID", updated.worktrees.queuedSwitch)
	}
}

func TestWorktreeListControllerDeleteKeysSetIntent(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	updated, cmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected refresh command for delete intent")
	}
	wantIdentity, err := worktreeui.SelectionIdentityForItem(updated.worktrees.entries[0])
	if err != nil {
		t.Fatalf("SelectionIdentityForItem: %v", err)
	}
	target := updated.worktrees.intent.DeleteTarget
	if !updated.worktrees.intent.OpenDelete ||
		target.kind != uiWorktreeDeleteIntentTargetIdentity ||
		target.identity != wantIdentity ||
		!updated.worktrees.intent.PreferDeleteBranch {
		t.Fatalf("intent = %+v, want delete+branch for selected worktree identity", updated.worktrees.intent)
	}
}

func TestWorktreeListDeleteIntentSurvivesSelectorChangeDuringRefresh(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	updated, _ := applyWorktreeListControllerKey(
		model,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}},
	)
	response := serverapi.WorktreeListResponse{
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry(
				"wt-other",
				"other",
				"/wt/other",
				"feature",
				false,
				false,
				true,
				true,
			),
			testRegisteredWorktreeListEntry(
				"wt-feature",
				"renamed",
				"/wt/feature",
				"feature-renamed",
				false,
				false,
				true,
				true,
			),
		},
	}

	next, _ := updated.Update(worktreeListDoneMsg{
		token: updated.worktreeListGeneration,
		resp:  response,
	})
	refreshed := next.(*uiModel)
	if refreshed.worktrees.phase != uiWorktreeOverlayPhaseDeleteConfirm {
		t.Fatalf("phase = %q, want delete confirmation", refreshed.worktrees.phase)
	}
	target := refreshed.worktrees.deleteConfirm.target
	if worktreeui.WorktreeID(target) != "wt-feature" {
		t.Fatalf("delete target worktree id = %q, want wt-feature", worktreeui.WorktreeID(target))
	}
	if target.Entry.Projection.Selector != "feature-renamed" {
		t.Fatalf("delete target selector = %q, want feature-renamed", target.Entry.Projection.Selector)
	}
}

func TestWorktreeSlashDeleteResolvesSelectorsWithServer(t *testing.T) {
	for _, selector := range []string{"wt-feature", "/wt/feature-a"} {
		t.Run(selector, func(t *testing.T) {
			listResponse := testLinkedWorktreeListResponse()
			client := &worktreeCommandTestClient{
				listResp: listResponse,
				selectorResp: serverapi.WorktreeSelectorPreviewResponse{
					Worktree: listResponse.Worktrees[1].Topology,
					Selector: listResponse.Worktrees[1].Projection.Selector,
				},
			}
			model := newWorktreeTestModel(t, client)

			next, cmd := model.inputController().handleWorktreeCommand("delete " + selector)
			updated := applyWorktreeCmdMessages(t, next.(*uiModel), cmd)

			if len(client.selectorRequests) != 1 {
				t.Fatalf("selector requests = %+v, want one authoritative resolution", client.selectorRequests)
			}
			if got := client.selectorRequests[0]; got.SessionID != "session-1" || got.Selector != selector {
				t.Fatalf("selector request = %+v, want session-scoped selector %q", got, selector)
			}
			if updated.worktrees.phase != uiWorktreeOverlayPhaseDeleteConfirm {
				t.Fatalf("phase = %q, want delete confirmation", updated.worktrees.phase)
			}
			if target := updated.worktrees.deleteConfirm.target; worktreeui.WorktreeID(target) != "wt-feature" || !target.IsCurrent {
				t.Fatalf("delete target = %+v, want current listed worktree", target)
			}
		})
	}
}

func TestWorktreeSlashDeleteRejectsResolvedMainWorkspace(t *testing.T) {
	for _, selector := range []string{"wt-main", "/repo"} {
		t.Run(selector, func(t *testing.T) {
			listResponse := testMainWorktreeListResponse()
			client := &worktreeCommandTestClient{
				listResp: listResponse,
				selectorResp: serverapi.WorktreeSelectorPreviewResponse{
					Worktree: listResponse.Worktrees[0].Topology,
					Selector: listResponse.Worktrees[0].Projection.Selector,
				},
			}
			model := newWorktreeTestModel(t, client)

			next, cmd := model.inputController().handleWorktreeCommand("delete " + selector)
			updated := applyWorktreeCmdMessages(t, next.(*uiModel), cmd)

			if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
				t.Fatalf("phase = %q, want list after main-workspace rejection", updated.worktrees.phase)
			}
			if updated.worktrees.visibleErrorText() == "" {
				t.Fatal("expected main-workspace rejection to be visible")
			}
		})
	}
}

func TestWorktreeSlashDeleteKeepsResolvedTopologyAndListedCurrentFact(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	resolvedEntry := testRegisteredWorktreeListEntry(
		"wt-feature",
		"renamed",
		"/wt/feature-renamed",
		"feature/renamed",
		false,
		false,
		true,
		true,
	)
	client := &worktreeCommandTestClient{
		listResp: listResponse,
		selectorResp: serverapi.WorktreeSelectorPreviewResponse{
			Worktree: resolvedEntry.Topology,
			Selector: resolvedEntry.Projection.Selector,
		},
	}
	model := newWorktreeTestModel(t, client)

	next, cmd := model.inputController().handleWorktreeCommand("delete wt-feature")
	updated := applyWorktreeCmdMessages(t, next.(*uiModel), cmd)

	target := updated.worktrees.deleteConfirm.target
	if target.CanonicalRoot != "/wt/feature-renamed" ||
		worktreeui.DisplayName(target) != "renamed" ||
		worktreeui.BranchName(target) != "feature/renamed" ||
		target.Entry.Projection.Selector != "feature/renamed" {
		t.Fatalf("delete target = %+v, want authoritative resolved topology", target)
	}
	if !target.IsCurrent || !target.Entry.Projection.IsCurrent {
		t.Fatalf("delete target = %+v, want current fact preserved from list", target)
	}
	if got := updated.worktrees.entries[1]; got.CanonicalRoot != target.CanonicalRoot || got.Entry.Projection.Selector != target.Entry.Projection.Selector {
		t.Fatalf("listed target = %+v, want reconciliation with resolved target", got)
	}
}

func TestAbandonedWorktreeDeleteResolutionCannotReopenConfirmation(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	client := &worktreeCommandTestClient{
		selectorResp: serverapi.WorktreeSelectorPreviewResponse{
			Worktree: listResponse.Worktrees[1].Topology,
			Selector: listResponse.Worktrees[1].Projection.Selector,
		},
	}
	model := newWorktreeListControllerTestModel(t, client)
	model.worktrees.intent = uiWorktreeOpenIntent{
		OpenDelete: true,
		DeleteTarget: uiWorktreeDeleteIntentTarget{
			kind:     uiWorktreeDeleteIntentTargetSelector,
			selector: "wt-feature",
		},
	}
	resolveCmd := model.applyWorktreeIntent()

	created, _ := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	next, _ := created.Update(tea.KeyMsg{Type: tea.KeyEsc})
	reopened := next.(*uiModel)
	next, _ = reopened.Update(resolveCmd())
	updated := next.(*uiModel)

	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase = %q, want abandoned resolution to leave list open", updated.worktrees.phase)
	}
	if updated.worktrees.isLoading() {
		t.Fatal("abandoned selector resolution left list loading")
	}
}

func TestNewListDeleteIntentSupersedesPendingSelectorResolution(t *testing.T) {
	listResponse := serverapi.WorktreeListResponse{
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry("wt-feature", "feature", "/wt/feature", "feature", false, false, true, true),
			testRegisteredWorktreeListEntry("wt-current", "current", "/wt/current", "current", false, true, true, true),
		},
	}
	client := &worktreeCommandTestClient{
		listResp: listResponse,
		selectorResp: serverapi.WorktreeSelectorPreviewResponse{
			Worktree: listResponse.Worktrees[0].Topology,
			Selector: listResponse.Worktrees[0].Projection.Selector,
		},
	}
	model := newWorktreeListControllerTestModel(t, client)
	model.worktrees.intent = uiWorktreeOpenIntent{
		OpenDelete: true,
		DeleteTarget: uiWorktreeDeleteIntentTarget{
			kind:     uiWorktreeDeleteIntentTargetSelector,
			selector: "wt-feature",
		},
	}
	resolveCmd := model.applyWorktreeIntent()
	model.worktrees.selection = 2

	replaced, _ := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	next, _ := replaced.Update(resolveCmd())
	updated := next.(*uiModel)

	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase = %q, want newer list delete intent to retain ownership", updated.worktrees.phase)
	}
	wantIdentity, err := worktreeui.SelectionIdentityForItem(updated.worktrees.entries[1])
	if err != nil {
		t.Fatalf("SelectionIdentityForItem: %v", err)
	}
	if updated.worktrees.intent.DeleteTarget.identity != wantIdentity {
		t.Fatalf("delete intent = %+v, want selected current worktree", updated.worktrees.intent)
	}
}

func TestListRefreshCannotOverwriteResolvedDeleteTopology(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	resolvedEntry := testRegisteredWorktreeListEntry(
		"wt-feature",
		"renamed",
		"/wt/feature-renamed",
		"feature/renamed",
		false,
		false,
		true,
		true,
	)
	client := &worktreeCommandTestClient{
		listResp: listResponse,
		selectorResp: serverapi.WorktreeSelectorPreviewResponse{
			Worktree: resolvedEntry.Topology,
			Selector: resolvedEntry.Projection.Selector,
		},
	}
	model := newWorktreeListControllerTestModel(t, client)
	model.worktrees.intent = uiWorktreeOpenIntent{
		OpenDelete: true,
		DeleteTarget: uiWorktreeDeleteIntentTarget{
			kind:     uiWorktreeDeleteIntentTargetSelector,
			selector: "wt-feature",
		},
	}
	resolveCmd := model.applyWorktreeIntent()
	_, refreshCmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	next, _ := model.Update(resolveCmd())
	resolved := next.(*uiModel)
	var listDone worktreeListDoneMsg
	for _, msg := range collectCmdMessages(t, refreshCmd) {
		if typed, ok := msg.(worktreeListDoneMsg); ok {
			listDone = typed
		}
	}
	next, _ = resolved.Update(listDone)
	updated := next.(*uiModel)

	target := updated.worktrees.deleteConfirm.target
	if target.CanonicalRoot != "/wt/feature-renamed" ||
		worktreeui.DisplayName(target) != "renamed" ||
		worktreeui.BranchName(target) != "feature/renamed" ||
		target.Entry.Projection.Selector != "feature/renamed" {
		t.Fatalf("delete target = %+v, want resolved topology preserved", target)
	}
}

func TestUnorderedListWithoutResolvedIdentityCannotDismissDeleteConfirmation(t *testing.T) {
	resolvedEntry := testRegisteredWorktreeListEntry(
		"wt-feature",
		"renamed",
		"/wt/feature-renamed",
		"feature/renamed",
		false,
		false,
		true,
		true,
	)
	for _, tc := range []struct {
		name     string
		response serverapi.WorktreeListResponse
	}{
		{
			name:     "target omitted",
			response: testMainWorktreeListResponse(),
		},
		{
			name: "target reclassified external",
			response: serverapi.WorktreeListResponse{
				Worktrees: []serverapi.WorktreeListEntry{
					testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, true, true, false),
					testExternalWorktreeListEntry("/wt/feature-renamed", "feature-renamed", false),
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &worktreeCommandTestClient{
				listResp: tc.response,
				selectorResp: serverapi.WorktreeSelectorPreviewResponse{
					Worktree: resolvedEntry.Topology,
					Selector: resolvedEntry.Projection.Selector,
				},
			}
			model := newWorktreeListControllerTestModel(t, client)
			model.worktrees.intent = uiWorktreeOpenIntent{
				OpenDelete: true,
				DeleteTarget: uiWorktreeDeleteIntentTarget{
					kind:     uiWorktreeDeleteIntentTargetSelector,
					selector: "wt-feature",
				},
			}
			resolveCmd := model.applyWorktreeIntent()
			_, refreshCmd := applyWorktreeListControllerKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

			next, _ := model.Update(resolveCmd())
			resolved := next.(*uiModel)
			var listDone worktreeListDoneMsg
			for _, msg := range collectCmdMessages(t, refreshCmd) {
				if typed, ok := msg.(worktreeListDoneMsg); ok {
					listDone = typed
				}
			}
			next, _ = resolved.Update(listDone)
			updated := next.(*uiModel)

			if updated.worktrees.phase != uiWorktreeOverlayPhaseDeleteConfirm {
				t.Fatalf("phase = %q, want resolved confirmation retained", updated.worktrees.phase)
			}
			if target := updated.worktrees.deleteConfirm.target; worktreeui.WorktreeID(target) != "wt-feature" || target.CanonicalRoot != "/wt/feature-renamed" {
				t.Fatalf("delete target = %+v, want resolved target retained", target)
			}
		})
	}
}

func TestClosedWorktreeDeleteResolutionCannotOpenReplacementOverlay(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	client := &worktreeCommandTestClient{
		selectorResp: serverapi.WorktreeSelectorPreviewResponse{
			Worktree: listResponse.Worktrees[1].Topology,
			Selector: listResponse.Worktrees[1].Projection.Selector,
		},
	}
	model := newWorktreeTestModel(t, client)
	model.openWorktreeOverlay(uiWorktreeOpenIntent{})
	staleResult := model.worktreeDeleteTargetResolveCmd("wt-feature", false)()

	model.closeWorktreeOverlay()
	model.openWorktreeOverlay(uiWorktreeOpenIntent{})
	next, _ := model.Update(staleResult)
	updated := next.(*uiModel)

	if updated.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase = %q, want replacement list overlay", updated.worktrees.phase)
	}
}

func TestClosedWorktreeListResultCannotHydrateReplacementOverlay(t *testing.T) {
	client := &worktreeCommandTestClient{
		listResp: serverapi.WorktreeListResponse{
			Worktrees: []serverapi.WorktreeListEntry{
				testRegisteredWorktreeListEntry("wt-stale", "stale", "/wt/stale", "stale", false, false, true, true),
			},
		},
	}
	model := newWorktreeTestModel(t, client)
	model.openWorktreeOverlay(uiWorktreeOpenIntent{})
	staleResult := model.requestWorktreeListCmd()()

	model.closeWorktreeOverlay()
	model.openWorktreeOverlay(uiWorktreeOpenIntent{})
	_ = model.requestWorktreeListCmd()
	next, _ := model.Update(staleResult)
	updated := next.(*uiModel)

	if len(updated.worktrees.entries) != 0 {
		t.Fatalf("replacement entries = %+v, want pending replacement list", updated.worktrees.entries)
	}
	if !updated.worktrees.listPending {
		t.Fatal("stale list result cleared replacement list pending state")
	}
}

func TestWorktreeListRefreshPreservesSelectionAndDeleteTargetByKentID(t *testing.T) {
	model := newWorktreeListControllerTestModel(t, nil)
	model.worktrees.selection = 1
	model.openDeleteWorktreeDialog(
		model.worktrees.entries[0],
		false,
		uiWorktreeDeleteTargetAuthorityListed,
	)

	err := model.applyWorktreeListResponse(serverapi.WorktreeListResponse{
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry(
				"wt-other",
				"other",
				"/wt/other",
				"feature",
				false,
				false,
				true,
				true,
			),
			testRegisteredWorktreeListEntry(
				"wt-feature",
				"renamed",
				"/wt/feature",
				"feature-renamed",
				false,
				false,
				true,
				true,
			),
		},
	})
	if err != nil {
		t.Fatalf("applyWorktreeListResponse: %v", err)
	}

	if model.worktrees.selection != 2 {
		t.Fatalf("selection = %d, want row 2", model.worktrees.selection)
	}
	target := model.worktrees.deleteConfirm.target
	if worktreeui.WorktreeID(target) != "wt-feature" {
		t.Fatalf("delete target worktree id = %q, want wt-feature", worktreeui.WorktreeID(target))
	}
	if target.Entry.Projection.Selector != "feature-renamed" {
		t.Fatalf("delete target selector = %q, want feature-renamed", target.Entry.Projection.Selector)
	}
}
