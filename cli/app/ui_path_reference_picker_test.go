package app

import (
	"reflect"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetectPathReferenceQuery(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		wantOK    bool
		wantQuery string
	}{
		{name: "empty token", fixture: "@|", wantOK: false},
		{name: "single rune", fixture: "@s|", wantOK: false},
		{name: "ascii valid", fixture: "@sea|", wantOK: true, wantQuery: "sea"},
		{name: "double at", fixture: "@@|", wantOK: false},
		{name: "space after at", fixture: "@ |", wantOK: false},
		{name: "unicode valid", fixture: "@прив|", wantOK: true, wantQuery: "прив"},
		{name: "digits valid", fixture: "@12|", wantOK: true, wantQuery: "12"},
		{name: "nested path valid", fixture: "@cli/app/u|", wantOK: true, wantQuery: "cli/app/u"},
		{name: "hidden path valid", fixture: "@.gith|", wantOK: true, wantQuery: ".gith"},
		{name: "punctuation cancels", fixture: "@ab!|", wantOK: false},
		{name: "email style rejected", fixture: "mail@ab|", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input, cursor := testPathReferenceFixture(tc.fixture)
			got := detectPathReferenceQuery(input, cursor)
			if got.Active != tc.wantOK {
				t.Fatalf("active = %v, want %v", got.Active, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.RawQuery != tc.wantQuery {
				t.Fatalf("query = %q, want %q", got.RawQuery, tc.wantQuery)
			}
		})
	}
}

func TestApplyPathReferenceCompletionReplacesMiddleSpan(t *testing.T) {
	tests := []struct {
		name          string
		candidatePath string
		safePath      string
		expectFailure bool
	}{
		{name: "ordinary path", candidatePath: "cli/app/ui.go", safePath: "cli/app/ui.go"},
		{name: "CSI", candidatePath: "cli/\x1b[31mapp\x1b[0m/ui.go", safePath: "cli/app/ui.go"},
		{name: "OSC terminated by BEL", candidatePath: "cli/\x1b]8;;https://evil.test\x07app\x1b]8;;\x07/ui.go", safePath: "cli/app/ui.go"},
		{name: "OSC terminated by ST", candidatePath: "cli/\x1b]0;owned\x1b\\app/ui.go", safePath: "cli/app/ui.go"},
		{name: "C0 control", candidatePath: "cli/app/\x01ui.go", safePath: "cli/app/ui.go"},
		{name: "printable Unicode and punctuation", candidatePath: "目录/[draft] #1?.txt", safePath: "目录/[draft] #1?.txt"},
		{name: "empty projection", candidatePath: "\x1b[31m\x1b[0m", expectFailure: true},
		{name: "blank projection", candidatePath: "\x1b[31m \t\x1b[0m", expectFailure: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input, cursor := testPathReferenceFixture("compare @cliap| with tests")
			query := detectPathReferenceQuery(input, cursor)
			updated, nextCursor, ok := applyPathReferenceCompletion(input, cursor, query, uiPathReferenceCandidate{Path: tc.candidatePath})
			if tc.expectFailure {
				if ok || updated != input || nextCursor != cursor {
					t.Fatalf("unusable projection result = (%q, %d, %v), want original input and cursor with false", updated, nextCursor, ok)
				}
				return
			}
			if !ok {
				t.Fatal("expected completion applied")
			}
			wantInput := "compare @" + tc.safePath + " with tests"
			if updated != wantInput {
				t.Fatalf("updated input = %q, want %q", updated, wantInput)
			}
			wantCursor := len([]rune("compare @" + tc.safePath))
			if nextCursor != wantCursor {
				t.Fatalf("cursor = %d, want %d", nextCursor, wantCursor)
			}
			for _, r := range updated {
				if unicode.IsControl(r) {
					t.Fatalf("updated input contains terminal control %U: %q", r, updated)
				}
			}
		})
	}
}

func TestPathReferenceSearchIgnoredWhileSlashPickerActive(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search))
	m.replaceMainInputAtEnd("/@ab")

	if !m.slashCommandPicker().visible {
		t.Fatal("expected slash picker visible")
	}
	if m.pathReferencePicker().visible {
		t.Fatal("did not expect path picker visible while slash picker is active")
	}
	if len(search.requests) != 0 {
		t.Fatalf("did not expect path search requests, got %+v", search.requests)
	}
}

func TestPathReferenceStartupPrewarmQueuedForWorkspace(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(
		WithUIPathReferenceSearch(search),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}),
	)

	if len(m.startupCmds) == 0 {
		t.Fatal("expected startup prewarm command")
	}
	for _, cmd := range m.startupCmds {
		if cmd != nil {
			_ = cmd()
		}
	}
	if len(search.prewarmRoots) != 1 || search.prewarmRoots[0] != "/tmp/workspace" {
		t.Fatalf("unexpected prewarm roots: %+v", search.prewarmRoots)
	}
}

func TestPathReferenceLoadingDelayDoesNotOverwriteFresherMatches(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))

	m.replaceMainInputAtEnd("@ab")
	firstToken := m.pathReference.queryToken
	firstDraft := m.pathReference.draftToken
	m.replaceMainInputAtEnd("@abc")
	secondToken := m.pathReference.queryToken
	secondDraft := m.pathReference.draftToken

	updated := updateUIModel(t, m, uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       secondDraft,
		QueryToken:       secondToken,
		NormalizedQuery:  "abc",
		Matches:          []uiPathReferenceCandidate{{Path: "cli/app/ui.go"}},
	})

	updated = updateUIModel(t, updated, uiPathReferenceLoadingDelayMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       firstDraft,
		QueryToken:       firstToken,
		NormalizedQuery:  "ab",
	})
	if updated.pathReference.loading {
		t.Fatal("did not expect stale loading event to overwrite fresher matches")
	}
	if len(updated.pathReference.matches) != 1 || updated.pathReference.matches[0].Path != "cli/app/ui.go" {
		t.Fatalf("unexpected matches after stale loading event: %+v", updated.pathReference.matches)
	}
}

func TestPathReferenceDropsStaleCorpusGenerationEvents(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}))
	m.replaceMainInputAtEnd("@ab")
	m.pathReference.corpusGeneration = 2

	updated := updateUIModel(t, m, uiPathReferenceCorpusReadyMsg{WorkspaceRoot: "/tmp/workspace", CorpusGeneration: 1})
	if updated.pathReference.corpusGeneration != 2 {
		t.Fatalf("stale corpus-ready changed generation to %d", updated.pathReference.corpusGeneration)
	}

	updated = updateUIModel(t, updated, uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       updated.pathReference.draftToken,
		QueryToken:       updated.pathReference.queryToken,
		NormalizedQuery:  updated.pathReference.normalizedQuery,
		Matches:          []uiPathReferenceCandidate{{Path: "stale.go"}},
	})
	if len(updated.pathReference.matches) != 0 {
		t.Fatalf("expected stale generation match dropped, got %+v", updated.pathReference.matches)
	}

	updated.pathReference.pending = true
	updated = updateUIModel(t, updated, uiPathReferenceLoadingDelayMsg{
		WorkspaceRoot:    "/tmp/workspace",
		CorpusGeneration: 1,
		DraftToken:       updated.pathReference.draftToken,
		QueryToken:       updated.pathReference.queryToken,
		NormalizedQuery:  updated.pathReference.normalizedQuery,
	})
	if updated.pathReference.loading {
		t.Fatal("expected stale generation loading event dropped")
	}
}

func TestPathReferenceWorkspaceSwitchDropsInFlightEvents(t *testing.T) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(WithUIPathReferenceSearch(search), WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace-a"}))
	m.replaceMainInputAtEnd("@ab")
	staleDraft := m.pathReference.draftToken
	staleToken := m.pathReference.queryToken

	m.statusConfig.WorkspaceRoot = "/tmp/workspace-b"
	m.replaceMainInputAtEnd("@ab")

	updated := updateUIModel(t, m, uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    "/tmp/workspace-a",
		CorpusGeneration: 1,
		DraftToken:       staleDraft,
		QueryToken:       staleToken,
		NormalizedQuery:  "ab",
		Matches:          []uiPathReferenceCandidate{{Path: "stale.go"}},
	})
	if len(updated.pathReference.matches) != 0 {
		t.Fatalf("expected stale workspace match dropped, got %+v", updated.pathReference.matches)
	}
	if updated.pathReference.workspaceRoot != "/tmp/workspace-b" {
		t.Fatalf("workspace root = %q", updated.pathReference.workspaceRoot)
	}
}

func TestPathReferenceUpDownNavigatesSelectionWithoutRewritingInput(t *testing.T) {
	m, _ := newPathReferenceTestModel("@ab")
	m.promptHistory = []string{"older prompt"}
	updated := deliverPathReferenceTestMatches(t, m, 1, []uiPathReferenceCandidate{
		{Path: "cli/app"},
		{Path: "cli/app/ui.go"},
	})
	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyDown})
	if updated.pathReference.selection != 1 {
		t.Fatalf("selection = %d, want 1", updated.pathReference.selection)
	}
	if testMainInput(updated) != "@ab" {
		t.Fatalf("input = %q, want unchanged draft", testMainInput(updated))
	}
	if updated.promptHistorySelection != nil {
		t.Fatalf("did not expect prompt history navigation, got %v", updated.promptHistorySelection)
	}
}

func TestPathReferencePickerScrollsSelectionAndReservesBoundedHeight(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.terminalGeometry = terminalGeometryKnown(24, 14)
	chatLinesWithoutPicker := m.layout().calcChatLines()
	m.pathReference.tracked = detectPathReferenceQuery("@ab", 3)
	m.pathReference.matches = []uiPathReferenceCandidate{
		{Path: "match-00.go"},
		{Path: "match-01.go"},
		{Path: "match-02.go"},
		{Path: "match-03.go"},
		{Path: "match-04.go"},
		{Path: "match-05.go"},
		{Path: "match-06.go"},
		{Path: "match-07.go"},
		{Path: "match-08.go"},
	}
	for range 7 {
		m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}

	state := m.pathReferencePicker()
	assertActivePickerHighlightedSelection(t, m)
	if state.start == 0 {
		t.Fatalf("picker did not scroll to offscreen selection: %+v", state)
	}
	if len(state.rows) != len(m.pathReference.matches) {
		t.Fatalf("picker rows = %d, want full absolute candidate list %d", len(state.rows), len(m.pathReference.matches))
	}
	if state.lineCount != slashCommandPickerLines {
		t.Fatalf("picker line count = %d, want bounded %d", state.lineCount, slashCommandPickerLines)
	}
	renderedPicker := m.layout().renderActivePicker(24)
	if len(renderedPicker) != state.lineCount {
		t.Fatalf("rendered picker height = %d, want %d", len(renderedPicker), state.lineCount)
	}
	if got := m.layout().calcChatLines(); got != chatLinesWithoutPicker-len(renderedPicker) {
		t.Fatalf("chat lines with picker = %d, want %d", got, chatLinesWithoutPicker-len(renderedPicker))
	}
	frame, ok := m.layout().composeStandardFrame(uiThemeStyles(m.theme))
	if !ok {
		t.Fatal("expected composed frame")
	}
	if !reflect.DeepEqual(frame.pickerPane, renderedPicker) {
		t.Fatalf("composed picker pane = %+v, want active picker", frame.pickerPane)
	}
}

func TestPathReferencePickerSanitizesControlCharactersForDisplay(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.pathReference.tracked = detectPathReferenceQuery("@ab", 3)
	rawPath := "safe/\x1b]52;evil\x07name\x01.txt"
	m.pathReference.matches = []uiPathReferenceCandidate{{Path: rawPath}}

	state := m.pathReferencePicker()
	if !state.visible || len(state.rows) != 1 {
		t.Fatalf("unexpected picker state: %+v", state)
	}
	if state.rows[0].primary != "safe/name.txt" {
		t.Fatalf("display path = %q", state.rows[0].primary)
	}
	if m.pathReference.matches[0].Path != rawPath {
		t.Fatalf("expected underlying candidate path preserved, got %q", m.pathReference.matches[0].Path)
	}
}

func testPathReferenceFixture(fixture string) (string, int) {
	runes := []rune(fixture)
	idx := -1
	for i, r := range runes {
		if r == '|' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fixture, -1
	}
	result := make([]rune, 0, len(runes)-1)
	for i, r := range runes {
		if i == idx {
			continue
		}
		result = append(result, r)
	}
	return string(result), idx
}

type stubUIPathReferenceSearch struct {
	events       chan uiPathReferenceSearchEvent
	prewarmRoots []string
	requests     []uiPathReferenceSearchRequest
}

func newStubUIPathReferenceSearch() *stubUIPathReferenceSearch {
	return &stubUIPathReferenceSearch{events: make(chan uiPathReferenceSearchEvent, 32)}
}

func (s *stubUIPathReferenceSearch) Events() <-chan uiPathReferenceSearchEvent {
	return s.events
}

func (s *stubUIPathReferenceSearch) StartPrewarm(workspaceRoot string) {
	s.prewarmRoots = append(s.prewarmRoots, workspaceRoot)
}

func (s *stubUIPathReferenceSearch) Search(req uiPathReferenceSearchRequest) {
	s.requests = append(s.requests, req)
}

func (s *stubUIPathReferenceSearch) Stop() {}

func newPathReferenceTestModel(input string) (*uiModel, *stubUIPathReferenceSearch) {
	search := newStubUIPathReferenceSearch()
	m := newProjectedStaticUIModel(
		WithUIPathReferenceSearch(search),
		WithUIStatusConfig(uiStatusConfig{WorkspaceRoot: "/tmp/workspace"}),
	)
	m.replaceMainInputAtEnd(input)
	return m, search
}

func deliverPathReferenceTestMatches(t *testing.T, m *uiModel, generation uint64, matches []uiPathReferenceCandidate) *uiModel {
	t.Helper()
	return updateUIModel(t, m, uiPathReferenceMatchResultMsg{
		WorkspaceRoot:    m.pathReference.workspaceRoot,
		CorpusGeneration: generation,
		DraftToken:       m.pathReference.draftToken,
		QueryToken:       m.pathReference.queryToken,
		NormalizedQuery:  m.pathReference.normalizedQuery,
		Matches:          matches,
	})
}
