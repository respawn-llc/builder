package workflowview

import (
	"testing"

	"core/shared/serverapi"
)

func TestFillAttentionPageContinuesFromLastEmittedCandidate(t *testing.T) {
	candidates := []attentionCandidateRow{
		{id: "A", occurredAtUnixMs: 3},
		{id: "B", occurredAtUnixMs: 2},
		{id: "C", occurredAtUnixMs: 1},
	}
	fetch := func(cursor attentionPageCursor, limit int) ([]attentionCandidateRow, error) {
		start := 0
		if cursor.hasValue {
			for index, candidate := range candidates {
				if candidate.id == cursor.itemID {
					start = index + 1
					break
				}
			}
		}
		end := start + limit
		if end > len(candidates) {
			end = len(candidates)
		}
		return candidates[start:end], nil
	}
	project := func(candidate attentionCandidateRow) (serverapi.WorkflowAttentionItem, bool, error) {
		if candidate.id == "A" {
			return serverapi.WorkflowAttentionItem{}, false, nil
		}
		return serverapi.WorkflowAttentionItem{ID: candidate.id}, true, nil
	}

	first, err := fillAttentionPage(1, attentionPageCursor{}, fetch, project)
	if err != nil {
		t.Fatalf("fill first attention page: %v", err)
	}
	requireAttentionPageItemIDs(t, first.items, "B")
	if first.continuation == nil || first.continuation.itemID != "B" {
		t.Fatalf("first continuation = %+v, want B cursor", first.continuation)
	}

	second, err := fillAttentionPage(1, *first.continuation, fetch, project)
	if err != nil {
		t.Fatalf("fill second attention page: %v", err)
	}
	requireAttentionPageItemIDs(t, second.items, "C")
	if second.continuation == nil || second.continuation.itemID != "C" {
		t.Fatalf("second continuation = %+v, want C cursor", second.continuation)
	}
}

func requireAttentionPageItemIDs(t *testing.T, items []serverapi.WorkflowAttentionItem, want ...string) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("attention page item count = %d, want %d: %+v", len(items), len(want), items)
	}
	for index, wantID := range want {
		if items[index].ID != wantID {
			t.Fatalf("attention page item %d = %q, want %q", index, items[index].ID, wantID)
		}
	}
}
