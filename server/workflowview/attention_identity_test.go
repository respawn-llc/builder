package workflowview

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestMergeAttentionCandidatesPreservesQuestionStepIdentity(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	firstStepID := mustWorkflowViewStepID(t, "11111111-1111-4111-8111-111111111111")
	secondStepID := mustWorkflowViewStepID(t, "22222222-2222-4222-8222-222222222222")
	promptID := clientui.PromptID("shared-prompt")
	ids := []string{
		liveQuestionAttentionID(sessionID, firstStepID, promptID),
		liveQuestionAttentionID(sessionID, secondStepID, promptID),
	}
	if ids[0] == ids[1] {
		t.Fatal("Question attention identity ignored Step")
	}

	items := mergeAttentionCandidates(
		attentionPageCursor{},
		[]attentionCandidate{{item: serverapi.WorkflowAttentionItem{ID: ids[0], OccurredAtUnixMs: 1}}},
		[]attentionCandidate{{item: serverapi.WorkflowAttentionItem{ID: ids[1], OccurredAtUnixMs: 1}}},
	)
	if len(items) != 2 {
		t.Fatalf("merged Question attention = %+v, want both Step identities", items)
	}
	page := mergeAttentionCandidates(attentionPageCursor{
		occurredAtUnixMs: items[0].OccurredAtUnixMs,
		itemID:           items[0].ID,
		hasValue:         true,
	},
		[]attentionCandidate{{item: items[0]}, {item: items[1]}},
	)
	if len(page) != 1 || page[0].ID != items[1].ID {
		t.Fatalf("Question attention cursor page = %+v, want remaining full-key row", page)
	}
}
