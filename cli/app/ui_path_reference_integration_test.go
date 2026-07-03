package app

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPathReferenceTabCompletesSelectedFile(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInput("inspect @ab", -1)

	next, _ := m.Update(uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       m.pathReference.draftToken,
		QueryToken:       m.pathReference.queryToken,
		NormalizedQuery:  "ab",
		Matches: []uiPathReferenceCandidate{
			{Path: "cli/app", Directory: true},
			{Path: "cli/app/ui.go"},
		},
	})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if updated.input != "inspect @cli/app/ui.go" {
		t.Fatalf("input = %q", updated.input)
	}
	if updated.isBusy() {
		t.Fatal("did not expect completion to start submission")
	}
}

func TestPathReferenceEnterCompletesSelectedDirectoryWithoutSubmitting(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInput("inspect @ab", -1)

	next, _ := m.Update(uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       m.pathReference.draftToken,
		QueryToken:       m.pathReference.queryToken,
		NormalizedQuery:  "ab",
		Matches:          []uiPathReferenceCandidate{{Path: "cli/app", Directory: true}},
	})
	updated := next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if updated.input != "inspect @cli/app/" {
		t.Fatalf("input = %q", updated.input)
	}
	if updated.isBusy() {
		t.Fatal("did not expect completion to start submission")
	}
}

func TestPathReferenceReactivatesAfterDirectoryCompletionAndNestedTyping(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInput("inspect @ab", -1)

	next, _ := m.Update(uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       m.pathReference.draftToken,
		QueryToken:       m.pathReference.queryToken,
		NormalizedQuery:  "ab",
		Matches:          []uiPathReferenceCandidate{{Path: "cli/app", Directory: true}},
	})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	updated = next.(*uiModel)
	if !updated.pathReference.tracked.Active {
		t.Fatal("expected path reference query reactivated after typing nested segment")
	}
	if updated.pathReference.tracked.RawQuery != "cli/app/u" {
		t.Fatalf("query = %q", updated.pathReference.tracked.RawQuery)
	}
	if len(search.requests) == 0 {
		t.Fatal("expected follow-up search request for nested segment")
	}
	last := search.requests[len(search.requests)-1]
	if last.NormalizedQuery != "cli/app/u" {
		t.Fatalf("search query = %q", last.NormalizedQuery)
	}
}

func TestPathReferenceTabFallsThroughWhenNoMatches(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInput("echo @ab", -1)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected normal tab submission when no matches exist")
	}
}

func TestPathReferenceEnterDoesNotSubmitWhileQueryIsPending(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInput("echo @ab", -1)
	m.pathReference.matches = []uiPathReferenceCandidate{{Path: "stale.go"}}
	m.pathReference.pending = true

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if updated.isBusy() {
		t.Fatal("did not expect enter to submit while path-reference query is still pending")
	}
	if updated.input != "echo @ab" {
		t.Fatalf("input = %q, want unchanged draft", updated.input)
	}
}

func TestPathReferencePickerSharedWithViewport(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.theme = "dark"
	m.termWidth = 24
	m.termHeight = 14
	m.windowSizeKnown = true
	m.input = "@ab"
	m.pathReference.tracked = uiPathReferenceQuery{Active: true, Start: 0, End: 3, RawQuery: "ab", NormalizedQuery: "ab"}
	m.pathReference.matches = []uiPathReferenceCandidate{{Path: "cli/app/ui.go"}}

	style := uiThemeStyles(m.theme)
	standard, ok := m.layout().composeStandardFrame(style)
	if !ok {
		t.Fatal("expected standard frame")
	}
	expectedPicker := m.layout().renderActivePicker(m.termWidth)
	if !reflect.DeepEqual(standard.pickerPane, expectedPicker) {
		t.Fatalf("standard picker pane = %+v, want %+v", standard.pickerPane, expectedPicker)
	}
	wantChat := m.termHeight - len(standard.inputPane) - len(standard.queuePane) - len(expectedPicker) - len(standard.helpPane) - 1
	if wantChat < 1 {
		wantChat = 1
	}
	if got := m.layout().calcChatLines(); got != wantChat {
		t.Fatalf("calcChatLines() = %d, want %d", got, wantChat)
	}
}

func TestPathReferenceUIRecoversAfterBuildFailureInSameWorkspace(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInput("@ab", -1)
	initialRequests := len(search.requests)

	next, _ := m.Update(uiPathReferenceCorpusFailedMsg{WorkspaceRoot: "/tmp/workspace", CorpusGeneration: 1, Err: errPathReferenceWorkspaceUnavailable})
	updated := next.(*uiModel)
	if updated.pathReference.pending || updated.pathReference.loading {
		t.Fatal("expected failed build to clear pending/loading state")
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	updated = next.(*uiModel)
	if len(search.requests) != initialRequests+1 {
		t.Fatalf("expected retry search request after later query, got %d requests", len(search.requests))
	}
	last := search.requests[len(search.requests)-1]
	if last.NormalizedQuery != "abc" {
		t.Fatalf("expected retry query abc, got %+v", last)
	}

	next, _ = updated.Update(uiPathReferenceCorpusReadyMsg{WorkspaceRoot: "/tmp/workspace", CorpusGeneration: 2})
	updated = next.(*uiModel)

	next, _ = updated.Update(uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 2,
		DraftToken:       updated.pathReference.draftToken,
		QueryToken:       updated.pathReference.queryToken,
		NormalizedQuery:  updated.pathReference.normalizedQuery,
		Matches:          []uiPathReferenceCandidate{{Path: "cli/app/ui.go"}},
	})
	updated = next.(*uiModel)
	if updated.pathReference.loading || updated.pathReference.pending {
		t.Fatal("expected successful retry to clear loading/pending state")
	}
	if len(updated.pathReference.matches) != 1 || updated.pathReference.matches[0].Path != "cli/app/ui.go" {
		t.Fatalf("expected recovered matches after retry, got %+v", updated.pathReference.matches)
	}
}
