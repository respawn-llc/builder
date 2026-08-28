package app

import (
	"testing"

	"core/cli/app/internal/worktreeui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type worktreeListFixture struct {
	t     *testing.T
	model *uiModel
}

func newWorktreeListFixture(t *testing.T, client *worktreeCommandTestClient) *worktreeListFixture {
	t.Helper()
	model := newWorktreeControllerTestModel(t, client, uiWorktreeOverlayPhaseList)
	model.worktrees.entries = []worktreeui.Item{
		mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry("wt-feature", "feature", "/wt/feature", "feature", false, false, true, true)),
		mustProjectWorktreeItem(t, testRegisteredWorktreeListEntry("wt-current", "current", "/wt/current", "current", false, true, true, true)),
	}
	model.setInputMode(uiInputModeWorktree)
	return &worktreeListFixture{t: t, model: model}
}

func (f *worktreeListFixture) press(key tea.KeyMsg) tea.Cmd {
	f.t.Helper()
	next, cmd := uiInputController{model: f.model}.handleWorktreeOverlayKey(key)
	f.model = next.(*uiModel)
	return cmd
}

func (f *worktreeListFixture) update(msg tea.Msg) tea.Cmd {
	f.t.Helper()
	next, cmd := f.model.Update(msg)
	f.model = next.(*uiModel)
	return cmd
}

func (f *worktreeListFixture) beginSelectorDelete(selector string) tea.Cmd {
	f.model.worktrees.intent = uiWorktreeOpenIntent{
		OpenDelete: true,
		DeleteTarget: uiWorktreeDeleteIntentTarget{
			kind:     uiWorktreeDeleteIntentTargetSelector,
			selector: selector,
		},
	}
	return f.model.applyWorktreeIntent()
}

func (f *worktreeListFixture) listDone(cmd tea.Cmd) worktreeListDoneMsg {
	f.t.Helper()
	var result worktreeListDoneMsg
	found := false
	for _, msg := range collectCmdMessages(f.t, cmd) {
		if typed, ok := msg.(worktreeListDoneMsg); ok {
			if found {
				panic("worktree list command produced multiple worktreeListDoneMsg values")
			}
			result = typed
			found = true
		}
	}
	if !found {
		panic("worktree list command did not produce worktreeListDoneMsg")
	}
	return result
}

func TestWorktreeListControllerNavigatesRows(t *testing.T) {
	fixture := newWorktreeListFixture(t, nil)
	fixture.press(tea.KeyMsg{Type: tea.KeyDown})
	if fixture.model.worktrees.selection != 1 {
		t.Fatalf("selection after down = %d, want 1", fixture.model.worktrees.selection)
	}
	fixture.press(tea.KeyMsg{Type: tea.KeyEnd})
	if fixture.model.worktrees.selection != fixture.model.worktreeRowCount()-1 {
		t.Fatalf("selection after end = %d, want last", fixture.model.worktrees.selection)
	}
	fixture.press(tea.KeyMsg{Type: tea.KeyHome})
	if fixture.model.worktrees.selection != 0 {
		t.Fatalf("selection after home = %d, want create row", fixture.model.worktrees.selection)
	}
}

func TestWorktreeListControllerEnterCreateRowOpensCreateDialogThroughUpdate(t *testing.T) {
	fixture := newWorktreeListFixture(t, nil)
	fixture.model.worktrees.selection = 0
	fixture.update(tea.KeyMsg{Type: tea.KeyEnter})
	if fixture.model.worktrees.phase != uiWorktreeOverlayPhaseCreate {
		t.Fatalf("phase after enter create row = %q, want create", fixture.model.worktrees.phase)
	}
}

func TestWorktreeListControllerEnterWorktreeSubmitsSwitch(t *testing.T) {
	client := &worktreeCommandTestClient{listResp: testMainWorktreeListResponse()}
	fixture := newWorktreeListFixture(t, client)
	fixture.model.worktrees.selection = 1
	cmd := fixture.press(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected switch command")
	}
	if !fixture.model.worktrees.switchPending {
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

func TestWorktreeCommandSchedulesLeaveAndRejectsArguments(t *testing.T) {
	client := &worktreeCommandTestClient{}
	model := newWorktreeTestModel(t, client)

	_, cmd := model.inputController().handleWorktreeCommand("leave")
	if cmd == nil {
		t.Fatal("leave command did not schedule a mutation")
	}
	result := cmd()
	done, ok := result.(worktreeSwitchDoneMsg)
	if !ok {
		t.Fatalf("leave command message type = %T, want worktreeSwitchDoneMsg", result)
	}
	if len(client.leaveRequests) != 1 ||
		client.leaveRequests[0].OperationID.Validate() != nil ||
		done.ack.OperationID != client.leaveRequests[0].OperationID {
		t.Fatalf("leave scheduling = requests %+v acknowledgement %+v", client.leaveRequests, done.ack)
	}

	_, rejectedCmd := model.inputController().handleWorktreeCommand("leave unexpected")
	if rejectedCmd == nil {
		t.Fatal("leave arguments were not rejected with usage feedback")
	}
	_ = rejectedCmd()
	if len(client.leaveRequests) != 1 {
		t.Fatalf("leave arguments scheduled requests: %+v", client.leaveRequests)
	}
}

func TestWorktreeCommandSwitchUsesCompleteNormalizedSelector(t *testing.T) {
	client := &worktreeCommandTestClient{}
	model := newWorktreeTestModel(t, client)

	_, cmd := model.inputController().handleWorktreeCommand("switch   feature   with spaces")
	if cmd == nil {
		t.Fatal("switch command did not schedule a mutation")
	}
	_ = cmd()
	if len(client.enterRequests) != 1 || client.enterRequests[0].Selector != "feature with spaces" {
		t.Fatalf("enter requests = %+v, want complete normalized selector", client.enterRequests)
	}
}

func TestWorktreeListControllerQueuesStableSwitchTarget(t *testing.T) {
	fixture := newWorktreeListFixture(t, nil)
	fixture.model.worktrees.selection = 1
	fixture.model.worktrees.switchPending = true

	cmd := fixture.press(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("queued switch returned a command while another switch is pending")
	}
	queued := fixture.model.worktrees.queuedTransition
	if queued == nil ||
		queued.Transition != runtimeinput.PendingWorkWorktreeTransitionEnter ||
		queued.Selector == nil ||
		*queued.Selector != "wt-feature" {
		t.Fatalf("queued transition = %+v, want stable worktree ID", queued)
	}
}

func TestWorktreeListControllerDeleteKeysSetIntent(t *testing.T) {
	fixture := newWorktreeListFixture(t, nil)
	fixture.model.worktrees.selection = 1
	cmd := fixture.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected refresh command for delete intent")
	}
	wantIdentity, err := worktreeui.SelectionIdentityForItem(fixture.model.worktrees.entries[0])
	if err != nil {
		t.Fatalf("SelectionIdentityForItem: %v", err)
	}
	target := fixture.model.worktrees.intent.DeleteTarget
	if !fixture.model.worktrees.intent.OpenDelete ||
		target.kind != uiWorktreeDeleteIntentTargetIdentity ||
		target.identity != wantIdentity ||
		!fixture.model.worktrees.intent.PreferDeleteBranch {
		t.Fatalf("intent = %+v, want delete+branch for selected worktree identity", fixture.model.worktrees.intent)
	}
}

func TestWorktreeListDeleteIntentSurvivesSelectorChangeDuringRefresh(t *testing.T) {
	fixture := newWorktreeListFixture(t, nil)
	fixture.model.worktrees.selection = 1
	fixture.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	fixture.update(worktreeListDoneMsg{
		token: fixture.model.worktreeListGeneration,
		resp:  testRefreshedWorktreeListResponse(),
	})
	if fixture.model.worktrees.phase != uiWorktreeOverlayPhaseDeleteConfirm {
		t.Fatalf("phase = %q, want delete confirmation", fixture.model.worktrees.phase)
	}
	target := fixture.model.worktrees.deleteConfirm.target
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
				listResp:     listResponse,
				selectorResp: testSelectorPreview(listResponse.Worktrees[1]),
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
				listResp:     listResponse,
				selectorResp: testSelectorPreview(listResponse.Worktrees[0]),
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
	resolvedEntry := testResolvedFeatureWorktreeEntry()
	client := &worktreeCommandTestClient{
		listResp:     listResponse,
		selectorResp: testSelectorPreview(resolvedEntry),
	}
	model := newWorktreeTestModel(t, client)

	next, cmd := model.inputController().handleWorktreeCommand("delete wt-feature")
	updated := applyWorktreeCmdMessages(t, next.(*uiModel), cmd)

	target := updated.worktrees.deleteConfirm.target
	requireResolvedFeatureTopology(t, target)
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
		selectorResp: testSelectorPreview(listResponse.Worktrees[1]),
	}
	fixture := newWorktreeListFixture(t, client)
	resolveCmd := fixture.beginSelectorDelete("wt-feature")

	fixture.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	fixture.update(tea.KeyMsg{Type: tea.KeyEsc})
	fixture.update(resolveCmd())

	if fixture.model.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase = %q, want abandoned resolution to leave list open", fixture.model.worktrees.phase)
	}
	if fixture.model.worktrees.isLoading() {
		t.Fatal("abandoned selector resolution left list loading")
	}
}

func TestNewListDeleteIntentSupersedesPendingSelectorResolution(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	client := &worktreeCommandTestClient{
		selectorResp: testSelectorPreview(listResponse.Worktrees[1]),
	}
	fixture := newWorktreeListFixture(t, client)
	resolveCmd := fixture.beginSelectorDelete("wt-feature")
	fixture.model.worktrees.selection = 2

	fixture.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	fixture.update(resolveCmd())

	if fixture.model.worktrees.phase != uiWorktreeOverlayPhaseList {
		t.Fatalf("phase = %q, want newer list delete intent to retain ownership", fixture.model.worktrees.phase)
	}
	wantIdentity, err := worktreeui.SelectionIdentityForItem(fixture.model.worktrees.entries[1])
	if err != nil {
		t.Fatalf("SelectionIdentityForItem: %v", err)
	}
	if fixture.model.worktrees.intent.DeleteTarget.identity != wantIdentity {
		t.Fatalf("delete intent = %+v, want selected current worktree", fixture.model.worktrees.intent)
	}
}

func TestListRefreshCannotOverwriteResolvedDeleteTopology(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	resolvedEntry := testResolvedFeatureWorktreeEntry()
	client := &worktreeCommandTestClient{
		listResp:     listResponse,
		selectorResp: testSelectorPreview(resolvedEntry),
	}
	fixture := newWorktreeListFixture(t, client)
	resolveCmd := fixture.beginSelectorDelete("wt-feature")
	refreshCmd := fixture.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	fixture.update(resolveCmd())
	fixture.update(fixture.listDone(refreshCmd))

	requireResolvedFeatureTopology(t, fixture.model.worktrees.deleteConfirm.target)
}

func TestUnorderedListWithoutResolvedIdentityCannotDismissDeleteConfirmation(t *testing.T) {
	resolvedEntry := testResolvedFeatureWorktreeEntry()
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
				listResp:     tc.response,
				selectorResp: testSelectorPreview(resolvedEntry),
			}
			fixture := newWorktreeListFixture(t, client)
			resolveCmd := fixture.beginSelectorDelete("wt-feature")
			refreshCmd := fixture.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			fixture.update(resolveCmd())
			fixture.update(fixture.listDone(refreshCmd))

			if fixture.model.worktrees.phase != uiWorktreeOverlayPhaseDeleteConfirm {
				t.Fatalf("phase = %q, want resolved confirmation retained", fixture.model.worktrees.phase)
			}
			if target := fixture.model.worktrees.deleteConfirm.target; worktreeui.WorktreeID(target) != "wt-feature" || target.CanonicalRoot != "/wt/feature-renamed" {
				t.Fatalf("delete target = %+v, want resolved target retained", target)
			}
		})
	}
}

func TestClosedWorktreeDeleteResolutionCannotOpenReplacementOverlay(t *testing.T) {
	listResponse := testLinkedWorktreeListResponse()
	client := &worktreeCommandTestClient{
		selectorResp: testSelectorPreview(listResponse.Worktrees[1]),
	}
	model := newWorktreeTestModel(t, client)
	model.openWorktreeOverlay(uiWorktreeOpenIntent{})
	staleResult := model.worktreeDeleteTargetResolveCmd("wt-feature", false)()

	model.closeWorktreeOverlay()
	model.openWorktreeOverlay(uiWorktreeOpenIntent{})
	updated := updateUIModel(t, model, staleResult)

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
	updated := updateUIModel(t, model, staleResult)

	if len(updated.worktrees.entries) != 0 {
		t.Fatalf("replacement entries = %+v, want pending replacement list", updated.worktrees.entries)
	}
	if !updated.worktrees.listPending {
		t.Fatal("stale list result cleared replacement list pending state")
	}
}

func TestWorktreeListRefreshPreservesSelectionAndDeleteTargetByKentID(t *testing.T) {
	fixture := newWorktreeListFixture(t, nil)
	fixture.model.worktrees.selection = 1
	fixture.model.openDeleteWorktreeDialog(
		fixture.model.worktrees.entries[0],
		false,
		uiWorktreeDeleteTargetAuthorityListed,
	)

	err := fixture.model.applyWorktreeListResponse(testRefreshedWorktreeListResponse())
	if err != nil {
		t.Fatalf("applyWorktreeListResponse: %v", err)
	}

	if fixture.model.worktrees.selection != 2 {
		t.Fatalf("selection = %d, want row 2", fixture.model.worktrees.selection)
	}
	target := fixture.model.worktrees.deleteConfirm.target
	if worktreeui.WorktreeID(target) != "wt-feature" {
		t.Fatalf("delete target worktree id = %q, want wt-feature", worktreeui.WorktreeID(target))
	}
	if target.Entry.Projection.Selector != "feature-renamed" {
		t.Fatalf("delete target selector = %q, want feature-renamed", target.Entry.Projection.Selector)
	}
}

func testSelectorPreview(entry serverapi.WorktreeListEntry) serverapi.WorktreeSelectorPreviewResponse {
	return serverapi.WorktreeSelectorPreviewResponse{
		Worktree: entry,
	}
}

func testResolvedFeatureWorktreeEntry() serverapi.WorktreeListEntry {
	return testRegisteredWorktreeListEntry("wt-feature", "renamed", "/wt/feature-renamed", "feature/renamed", false, false, true, true)
}

func testRefreshedWorktreeListResponse() serverapi.WorktreeListResponse {
	return serverapi.WorktreeListResponse{
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry("wt-other", "other", "/wt/other", "feature", false, false, true, true),
			testRegisteredWorktreeListEntry("wt-feature", "renamed", "/wt/feature", "feature-renamed", false, false, true, true),
		},
	}
}

func requireResolvedFeatureTopology(t *testing.T, target worktreeui.Item) {
	t.Helper()
	if target.CanonicalRoot != "/wt/feature-renamed" ||
		worktreeui.DisplayName(target) != "renamed" ||
		worktreeui.BranchName(target) != "feature/renamed" ||
		target.Entry.Projection.Selector != "feature/renamed" {
		t.Fatalf("delete target = %+v, want authoritative resolved topology", target)
	}
}
