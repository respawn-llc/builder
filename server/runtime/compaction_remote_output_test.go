package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestSanitizeRemoteCompactionOutputAcceptsEncryptedReasoningCheckpoint(t *testing.T) {
	const reasoningID = "reasoning-checkpoint"

	replacement, err := sanitizeRemoteCompactionOutput([]llm.ResponseItem{
		{
			Type:    llm.ResponseItemTypeMessage,
			Role:    textutil.Value(llm.RoleUser),
			Content: textutil.Value("summary"),
		},
		{
			Type:             llm.ResponseItemTypeReasoning,
			ID:               textutil.Value(reasoningID),
			EncryptedContent: textutil.Value("encrypted"),
		},
	})
	if err != nil {
		t.Fatalf("sanitize remote compaction output: %v", err)
	}

	reasoningItems := 0
	for _, item := range replacement {
		if item.Type != llm.ResponseItemTypeReasoning {
			continue
		}
		if item.ID == nil || *item.ID != reasoningID {
			t.Fatalf("reasoning checkpoint identity = %+v, want %q", item, reasoningID)
		}
		if _, present := textutil.OptionalTrimmed(item.EncryptedContent); !present {
			t.Fatalf("reasoning checkpoint encrypted content is absent: %+v", item)
		}
		reasoningItems++
	}
	if reasoningItems != 1 {
		t.Fatalf("sanitized reasoning checkpoints = %d, want one", reasoningItems)
	}
}
