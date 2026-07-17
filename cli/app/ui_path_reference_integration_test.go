package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPathReferenceCompletionKeys(t *testing.T) {
	tests := []struct {
		name       string
		key        tea.KeyMsg
		matches    []uiPathReferenceCandidate
		moveDown   bool
		nestedRune string
		wantInput  string
	}{
		{
			name:      "Tab selects file",
			key:       tea.KeyMsg{Type: tea.KeyTab},
			matches:   []uiPathReferenceCandidate{{Path: "cli/app", Directory: true}, {Path: "cli/app/ui.go"}},
			moveDown:  true,
			wantInput: "inspect @cli/app/ui.go",
		},
		{
			name:      "Enter selects directory",
			key:       tea.KeyMsg{Type: tea.KeyEnter},
			matches:   []uiPathReferenceCandidate{{Path: "cli/app", Directory: true}},
			wantInput: "inspect @cli/app/",
		},
		{
			name:       "directory selection reactivates nested search",
			key:        tea.KeyMsg{Type: tea.KeyEnter},
			matches:    []uiPathReferenceCandidate{{Path: "cli/app", Directory: true}},
			nestedRune: "u",
			wantInput:  "inspect @cli/app/",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, search := newPathReferenceTestModel("inspect @ab")
			m = deliverPathReferenceTestMatches(t, m, 1, test.matches)
			if test.moveDown {
				m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
			}
			m = updateUIModel(t, m, test.key)
			if testMainInput(m) != test.wantInput {
				t.Fatalf("input = %q, want %q", testMainInput(m), test.wantInput)
			}
			if m.isBusy() {
				t.Fatal("completion started submission")
			}
			if test.nestedRune == "" {
				return
			}
			m = updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(test.nestedRune)})
			if !m.pathReference.tracked.Active || m.pathReference.tracked.RawQuery != "cli/app/u" {
				t.Fatalf("nested query = %+v", m.pathReference.tracked)
			}
			if got := search.requests[len(search.requests)-1].NormalizedQuery; got != "cli/app/u" {
				t.Fatalf("search query = %q, want cli/app/u", got)
			}
		})
	}
}

func TestPathReferenceTabFallsThroughWhenNoMatches(t *testing.T) {
	m, _ := newPathReferenceTestModel("echo @ab")
	updated := updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if !updated.isBusy() {
		t.Fatal("expected normal tab submission when no matches exist")
	}
}

func TestPathReferenceEnterDoesNotSubmitWhileQueryIsPending(t *testing.T) {
	m, _ := newPathReferenceTestModel("echo @ab")
	m.pathReference.matches = []uiPathReferenceCandidate{{Path: "stale.go"}}
	m.pathReference.pending = true

	updated := updateUIModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if updated.isBusy() {
		t.Fatal("did not expect enter to submit while path-reference query is still pending")
	}
	if testMainInput(updated) != "echo @ab" {
		t.Fatalf("input = %q, want unchanged draft", testMainInput(updated))
	}
}

func TestPathReferenceUIRecoversAfterBuildFailureInSameWorkspace(t *testing.T) {
	m, search := newPathReferenceTestModel("@ab")
	initialRequests := len(search.requests)

	updated := updateUIModel(t, m, uiPathReferenceCorpusFailedMsg{WorkspaceRoot: "/tmp/workspace", CorpusGeneration: 1, Err: errPathReferenceWorkspaceUnavailable})
	if updated.pathReference.pending || updated.pathReference.loading {
		t.Fatal("expected failed build to clear pending/loading state")
	}

	updated = updateUIModel(t, updated, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if len(search.requests) != initialRequests+1 {
		t.Fatalf("expected retry search request after later query, got %d requests", len(search.requests))
	}
	last := search.requests[len(search.requests)-1]
	if last.NormalizedQuery != "abc" {
		t.Fatalf("expected retry query abc, got %+v", last)
	}

	updated = updateUIModel(t, updated, uiPathReferenceCorpusReadyMsg{WorkspaceRoot: "/tmp/workspace", CorpusGeneration: 2})
	updated = deliverPathReferenceTestMatches(t, updated, 2, []uiPathReferenceCandidate{{Path: "cli/app/ui.go"}})
	if updated.pathReference.loading || updated.pathReference.pending {
		t.Fatal("expected successful retry to clear loading/pending state")
	}
	if len(updated.pathReference.matches) != 1 || updated.pathReference.matches[0].Path != "cli/app/ui.go" {
		t.Fatalf("expected recovered matches after retry, got %+v", updated.pathReference.matches)
	}
}
