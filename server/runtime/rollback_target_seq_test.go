package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/config"
	"core/shared/rollbacktarget"
)

func TestPostCompactionSegmentRollbackTargetEncodesGlobalEventSeq(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append u1: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append a1: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "history_replaced", map[string]any{"engine": "compaction", "items": []map[string]any{}}); err != nil {
		t.Fatalf("append compaction boundary: %v", err)
	}
	u2Evt, _, err := store.AppendEvent("s2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"})
	if err != nil {
		t.Fatalf("append u2: %v", err)
	}

	page, err := TranscriptSegmentPageFromStore(store, 0, config.CacheWarningModeDefault)
	if err != nil {
		t.Fatalf("project newest segment: %v", err)
	}
	if !page.HasMoreAbove {
		t.Fatal("expected newest segment to report history above the compaction boundary")
	}

	var targetID string
	for _, entry := range page.Snapshot.Entries {
		if entry.Role == "user" && entry.Text == "u2" {
			targetID = entry.RollbackTargetID
		}
	}
	if targetID == "" {
		t.Fatal("expected post-compaction user entry to carry a rollback target id")
	}
	seq, err := rollbacktarget.DecodeUserMessageSeq(targetID)
	if err != nil {
		t.Fatalf("decode rollback target id: %v", err)
	}
	if seq != u2Evt.Seq {
		t.Fatalf("rollback target seq = %d, want global event seq %d (segment-local index would be 1)", seq, u2Evt.Seq)
	}
}
