package tui

import (
	"strings"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailBottomEdgeMovesSelectionBeyondCenterBeforePaging(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 8, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(5)
	beforeScroll := model.detailScroll

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := next.(Model)

	if cmd != nil {
		t.Fatal("down at the bottom edge requested a page before exhausting visible selection")
	}
	if updated.detailScroll != beforeScroll {
		t.Fatalf("bottom-edge camera moved from %d to %d", beforeScroll, updated.detailScroll)
	}
	if selected, ok := updated.selectedDetailIndex(); !ok || selected != 6 {
		t.Fatalf("bottom-edge selection = %d/%t, want next visible entry 6/true", selected, ok)
	}
}

func TestDetailBottomEdgeRequestsNewerPageOnlyAfterNewestVisibleEntry(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 8, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(5)

	for want := 6; want <= 7; want++ {
		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = next.(Model)
		if cmd != nil {
			t.Fatalf("down selecting visible entry %d requested a page", want)
		}
		if selected, ok := model.selectedDetailIndex(); !ok || selected != want {
			t.Fatalf("selection = %d/%t, want %d/true", selected, ok, want)
		}
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	assertDetailPageDirection(t, cmd, DetailTranscriptPageNewer)
}

func TestDetailBottomReverseWalksSelectionToCenterBeforeCameraMoves(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 10, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(9)
	beforeScroll := model.detailScroll

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(Model)

	if cmd != nil {
		t.Fatal("reverse input requested a page while visible selection could move toward center")
	}
	if updated.detailScroll != beforeScroll {
		t.Fatalf("reverse input moved camera from %d to %d before selection reached center", beforeScroll, updated.detailScroll)
	}
	if selected, ok := updated.selectedDetailIndex(); !ok || selected != 8 {
		t.Fatalf("reverse selection = %d/%t, want entry 8/true", selected, ok)
	}
}

func TestDetailBottomReverseResumesCameraAfterSelectionReachesCenter(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 10, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(9)
	beforeScroll := model.detailScroll

	for _, want := range []int{8, 7} {
		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
		model = next.(Model)
		if cmd != nil || model.detailScroll != beforeScroll {
			t.Fatalf("reverse path to center requested page or moved camera: cmd=%v scroll=%d", cmd != nil, model.detailScroll)
		}
		if selected, ok := model.selectedDetailIndex(); !ok || selected != want {
			t.Fatalf("reverse path selection = %d/%t, want %d/true", selected, ok, want)
		}
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = next.(Model)
	if cmd != nil {
		t.Fatal("reverse input after center requested a page")
	}
	if model.detailScroll != beforeScroll-1 {
		t.Fatalf("camera after center = %d, want %d", model.detailScroll, beforeScroll-1)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 6 {
		t.Fatalf("center owner after camera movement = %d/%t, want 6/true", selected, ok)
	}
}

func TestDetailTopEdgeReachesOldestVisibleEntryBeforePaging(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 8, DetailTranscriptAnchorTop)
	model.setSelectedDetailIndex(2)

	for _, want := range []int{1, 0} {
		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
		model = next.(Model)
		if cmd != nil {
			t.Fatalf("up selecting visible entry %d requested a page", want)
		}
		if model.detailScroll != 0 {
			t.Fatalf("top-edge camera moved to %d", model.detailScroll)
		}
		if selected, ok := model.selectedDetailIndex(); !ok || selected != want {
			t.Fatalf("top-edge selection = %d/%t, want %d/true", selected, ok, want)
		}
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	assertDetailPageDirection(t, cmd, DetailTranscriptPageOlder)
}

func TestDetailTopReverseWalksSelectionToCenterBeforeCameraMoves(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 8, DetailTranscriptAnchorTop)
	model.setSelectedDetailIndex(0)

	for _, want := range []int{1, 2} {
		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = next.(Model)
		if cmd != nil || model.detailScroll != 0 {
			t.Fatalf("top reverse path requested page or moved camera: cmd=%v scroll=%d", cmd != nil, model.detailScroll)
		}
		if selected, ok := model.selectedDetailIndex(); !ok || selected != want {
			t.Fatalf("top reverse selection = %d/%t, want %d/true", selected, ok, want)
		}
	}

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(Model)
	if cmd != nil {
		t.Fatal("top reverse input after center requested a page")
	}
	if model.detailScroll != 1 {
		t.Fatalf("top reverse camera after center = %d, want 1", model.detailScroll)
	}
	if selected, ok := model.selectedDetailIndex(); !ok || selected != 3 {
		t.Fatalf("top reverse center owner = %d/%t, want 3/true", selected, ok)
	}
}

func TestDetailWheelUsesSameBottomEdgeStateMachine(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 8, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(5)

	for _, want := range []int{6, 7} {
		next, cmd := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		model = next.(Model)
		if cmd != nil {
			t.Fatalf("wheel selecting visible entry %d requested a page", want)
		}
		if selected, ok := model.selectedDetailIndex(); !ok || selected != want {
			t.Fatalf("wheel selection = %d/%t, want %d/true", selected, ok, want)
		}
	}
	_, cmd := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	assertDetailPageDirection(t, cmd, DetailTranscriptPageNewer)
}

func TestOngoingModeIgnoresRawWheelNavigation(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 8, DetailTranscriptAnchorBottom)
	model = mustUpdateDetailNavigationModel(t, model, SetModeMsg{Mode: ModeOngoing})
	beforeScroll := model.detailScroll
	beforeSelection, beforeSelected := model.selectedDetailIndex()

	next, cmd := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	updated := next.(Model)

	if cmd != nil || updated.detailScroll != beforeScroll {
		t.Fatalf("ongoing raw wheel mutated detail camera or emitted command: scroll=%d cmd=%v", updated.detailScroll, cmd != nil)
	}
	if selected, ok := updated.selectedDetailIndex(); ok != beforeSelected || selected != beforeSelection {
		t.Fatalf("ongoing raw wheel mutated detail selection: %d/%t want %d/%t", selected, ok, beforeSelection, beforeSelected)
	}
}

func TestDetailWheelReverseUsesSameCenterReturnStateMachine(t *testing.T) {
	model := newDetailNavigationModel(t, 5, 10, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(9)
	beforeScroll := model.detailScroll

	next, cmd := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	updated := next.(Model)

	if cmd != nil || updated.detailScroll != beforeScroll {
		t.Fatalf("wheel reverse requested page or moved camera: cmd=%v scroll=%d", cmd != nil, updated.detailScroll)
	}
	if selected, ok := updated.selectedDetailIndex(); !ok || selected != 8 {
		t.Fatalf("wheel reverse selection = %d/%t, want 8/true", selected, ok)
	}
}

func TestDetailPageMovementSelectsPrecutCenterOwner(t *testing.T) {
	model := newDetailNavigationModel(t, 6, 20, DetailTranscriptAnchorTop)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	updated := next.(Model)

	if cmd != nil {
		t.Fatal("interior page movement requested a cursor page")
	}
	if updated.detailScroll != 5 {
		t.Fatalf("page movement scroll = %d, want viewport delta 5", updated.detailScroll)
	}
	if selected, ok := updated.selectedDetailIndex(); !ok || selected != 8 {
		t.Fatalf("page movement center owner = %d/%t, want precut lower-center entry 8/true", selected, ok)
	}
}

func TestDetailPinnedPageMovementUsesGenericSelectionDeltaBeforePaging(t *testing.T) {
	model := newDetailNavigationModel(t, 6, 20, DetailTranscriptAnchorBottom)
	model.setSelectedDetailIndex(14)
	beforeScroll := model.detailScroll

	for want := 15; want <= 19; want++ {
		next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		model = next.(Model)
		if cmd != nil {
			t.Fatalf("pinned page movement selecting %d requested a cursor page", want)
		}
		if model.detailScroll != beforeScroll {
			t.Fatalf("pinned page movement moved camera from %d to %d", beforeScroll, model.detailScroll)
		}
		if selected, ok := model.selectedDetailIndex(); !ok || selected != want {
			t.Fatalf("pinned page selection = %d/%t, want %d/true", selected, ok, want)
		}
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	assertDetailPageDirection(t, cmd, DetailTranscriptPageNewer)
}

func TestDetailExpansionKeepsSelectedRangeInView(t *testing.T) {
	model := NewModel()
	model = mustUpdateDetailNavigationModel(t, model, SetViewportSizeMsg{Lines: 4, Width: 80})
	model = mustUpdateDetailNavigationModel(t, model, SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.TranscriptCommittedRow{
			detailAssistant("intro"),
			detailAssistant(strings.Join([]string{"one", "two", "three", "four", "five", "six"}, "\n")),
		}},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = mustUpdateDetailNavigationModel(t, model, SetModeMsg{Mode: ModeDetail})
	model.setSelectedDetailIndex(1)

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(Model)

	if cmd != nil {
		t.Fatal("detail expansion emitted an unexpected command")
	}
	if updated.detailScroll != 3 {
		t.Fatalf("detail scroll after expansion = %d, want selected range bottom aligned at 3", updated.detailScroll)
	}
	if selected, ok := updated.selectedDetailIndex(); !ok || selected != 1 {
		t.Fatalf("selection after expansion = %d/%t, want 1/true", selected, ok)
	}
}

func TestDetailWidthReflowMakesMinimumScrollAdjustmentToKeepSelectionVisible(t *testing.T) {
	model := NewModel()
	model = mustUpdateDetailNavigationModel(t, model, SetViewportSizeMsg{Lines: 4, Width: 80})
	model = mustUpdateDetailNavigationModel(t, model, SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.TranscriptCommittedRow{
			detailUser(strings.Repeat("wrapping content ", 5)),
			detailAssistant("selected entry"),
		}},
		Anchor: DetailTranscriptAnchorBottom,
	})
	model = mustUpdateDetailNavigationModel(t, model, SetModeMsg{Mode: ModeDetail})
	if model.detailScroll != 0 {
		t.Fatalf("wide detail scroll = %d, want selected entry visible at scroll 0", model.detailScroll)
	}

	model = mustUpdateDetailNavigationModel(t, model, SetViewportSizeMsg{Lines: 4, Width: 20})

	selected, ok := model.selectedDetailIndex()
	if !ok || selected != 1 {
		t.Fatalf("selection after width reflow = %d/%t, want entry 1/true", selected, ok)
	}
	lineRange, ok := model.detailEntryLineRange(selected)
	if !ok {
		t.Fatal("selected entry has no reflowed line range")
	}
	wantScroll := lineRange.first - model.viewportLines + 1
	if model.detailScroll != wantScroll {
		t.Fatalf("detail scroll after width reflow = %d, want minimum adjustment %d", model.detailScroll, wantScroll)
	}
	if lineRange.first < model.detailScroll || lineRange.first >= model.detailScroll+model.viewportLines {
		t.Fatalf("selected range %+v is outside reflowed viewport [%d,%d)", lineRange, model.detailScroll, model.detailScroll+model.viewportLines)
	}
}

func TestDetailTallExpandedEntryOwnsCenterAcrossNavigationInputs(t *testing.T) {
	inputs := []struct {
		name       string
		msg        tea.Msg
		wantScroll int
	}{
		{name: "line", msg: tea.KeyMsg{Type: tea.KeyDown}, wantScroll: 1},
		{name: "wheel", msg: tea.MouseMsg{Button: tea.MouseButtonWheelDown}, wantScroll: 1},
		{name: "page", msg: tea.KeyMsg{Type: tea.KeyPgDown}, wantScroll: 5},
	}

	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			model := newTallExpandedDetailNavigationModel(t)

			next, cmd := model.Update(input.msg)
			updated := next.(Model)

			if cmd != nil {
				t.Fatalf("%s navigation emitted a page request", input.name)
			}
			if updated.detailScroll != input.wantScroll {
				t.Fatalf("%s navigation scroll = %d, want %d", input.name, updated.detailScroll, input.wantScroll)
			}
			if selected, ok := updated.selectedDetailIndex(); !ok || selected != 1 {
				t.Fatalf("%s navigation selection = %d/%t, want tall entry 1/true", input.name, selected, ok)
			}
			center, ok := updated.centerVisibleDetailEntry()
			if !ok || center != 1 {
				t.Fatalf("%s center owner = %d/%t, want tall entry 1/true", input.name, center, ok)
			}
		})
	}
}

func newTallExpandedDetailNavigationModel(t *testing.T) Model {
	t.Helper()
	model := NewModel()
	model = mustUpdateDetailNavigationModel(t, model, SetViewportSizeMsg{Lines: 6, Width: 80})
	model = mustUpdateDetailNavigationModel(t, model, SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{Entries: []clientui.TranscriptCommittedRow{
			detailAssistant("intro"),
			detailAssistant(strings.Join([]string{
				"line 00", "line 01", "line 02", "line 03", "line 04", "line 05",
				"line 06", "line 07", "line 08", "line 09", "line 10", "line 11",
			}, "\n")),
			detailAssistant("tail"),
		}},
		Anchor: DetailTranscriptAnchorTop,
	})
	model = mustUpdateDetailNavigationModel(t, model, SetModeMsg{Mode: ModeDetail})
	model.setSelectedDetailIndex(1)
	model = mustUpdateDetailNavigationModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model.detailScroll = 0
	return model
}

func assertDetailPageDirection(t *testing.T, cmd tea.Cmd, want DetailTranscriptPageDirection) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected detail page direction %v", want)
	}
	msg, ok := cmd().(RequestDetailTranscriptPageMsg)
	if !ok || msg.Direction != want {
		t.Fatalf("detail page request = %#v, want direction %v", msg, want)
	}
}

func newDetailNavigationModel(t *testing.T, viewportLines int, entryCount int, anchor DetailTranscriptPageAnchor) Model {
	t.Helper()
	entries := make([]clientui.TranscriptCommittedRow, 0, entryCount)
	for index := 0; index < entryCount; index++ {
		entries = append(entries, detailAssistant("entry"))
	}
	model := NewModel()
	model = mustUpdateDetailNavigationModel(t, model, SetViewportSizeMsg{Lines: viewportLines, Width: 80})
	model = mustUpdateDetailNavigationModel(t, model, SetDetailTranscriptPageMsg{
		Page:   clientui.TranscriptPage{Entries: entries},
		Anchor: anchor,
	})
	return mustUpdateDetailNavigationModel(t, model, SetModeMsg{Mode: ModeDetail})
}

func mustUpdateDetailNavigationModel(t *testing.T, model Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := model.Update(msg)
	updated, ok := next.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want tui.Model", next)
	}
	return updated
}
