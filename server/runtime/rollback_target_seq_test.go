package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/rollbacktarget"
	"core/shared/textutil"
)

func TestPostCompactionSegmentRollbackTargetEncodesGlobalEventSeq(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, "s1", llm.Message{Role: llm.RoleUser, Content: textutil.Value("u1")})
	mustAppendTestEvent(t, store, "s1", llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("a1")})
	mustAppendTestEvent(t, store, "s1", historyReplacementPayload{Engine: "compaction", Mode: "auto"})
	u2Evt := mustAppendTestEvent(t, store, "s2", llm.Message{Role: llm.RoleUser, Content: textutil.Value("u2")})

	page, err := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{}).TranscriptNewestSegmentPage()
	if err != nil {
		t.Fatalf("project newest segment: %v", err)
	}
	if !page.HasMoreAbove {
		t.Fatal("expected newest segment to report history above the compaction boundary")
	}

	var targetID *string
	for _, entry := range page.Snapshot.Entries {
		if entry.Role == "user" && entry.Text == "u2" {
			targetID = entry.RollbackTargetID
		}
	}
	if targetID == nil {
		t.Fatal("expected post-compaction user entry to carry a rollback target id")
	}
	seq, err := rollbacktarget.DecodeUserMessageSeq(*targetID)
	if err != nil {
		t.Fatalf("decode rollback target id: %v", err)
	}
	if seq != u2Evt.Seq() {
		t.Fatalf("rollback target seq = %d, want global event seq %d (segment-local index would be 1)", seq, u2Evt.Seq())
	}
}
