package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestQueuedUserMessageStorePreservesTypedAgentSteer(t *testing.T) {
	sourceID := runtimeids.NewSessionID()
	steer, err := NewAgentSteer(sourceID, "report status")
	if err != nil {
		t.Fatalf("NewAgentSteer: %v", err)
	}
	message := steer.Message()
	item, err := (&queuedUserMessageStore{}).QueueItem(QueuedUserMessage{
		Message:               message,
		CanonicalPresentation: "report status",
	})
	if err != nil {
		t.Fatalf("QueueItem: %v", err)
	}
	if item.Message.Role != llm.RoleDeveloper ||
		item.Message.MessageType == nil || *item.Message.MessageType != llm.MessageTypeAgentSteer {
		t.Fatalf("queued message lost typed agent steer: %+v", item.Message)
	}
}

func TestQueuedUserMessageStoreReturnsErrorsForInvalidPayloads(t *testing.T) {
	store := &queuedUserMessageStore{}
	if _, err := store.QueueItem(QueuedUserMessage{}); err == nil {
		t.Fatal("QueueItem accepted an invalid payload")
	}
	for _, content := range []string{"", " \t "} {
		if _, err := store.QueueItem(QueuedUserMessage{
			Message: llm.Message{
				Role:    llm.RoleUser,
				Content: textutil.Value(content),
			},
			CanonicalPresentation: content,
		}); err == nil {
			t.Fatalf("QueueItem accepted whitespace-only content %q", content)
		}
	}
	if _, err := (QueuedUserMessage{}).DisplayText(); err == nil {
		t.Fatal("DisplayText accepted an invalid payload")
	}
}

func TestQueuedUserMessageStoreRejectsDuplicatePendingIdentity(t *testing.T) {
	store := &queuedUserMessageStore{}
	item := QueuedUserMessage{
		ID:                    runtimeids.NewQueueItemID().String(),
		CanonicalPresentation: "first",
		Message: llm.Message{
			Role:    llm.RoleUser,
			Content: textutil.Value("first"),
		},
	}
	if _, err := store.QueueItem(item); err != nil {
		t.Fatalf("QueueItem first: %v", err)
	}
	item.Message.Content = textutil.Value("second")
	if _, err := store.QueueItem(item); !errors.Is(err, errDuplicateQueuedUserMessageID) {
		t.Fatalf("QueueItem duplicate error = %v, want duplicate identity rejection", err)
	}
}

func TestQueuedUserMessageFlushGroupsKeepAgentSteersSeparate(t *testing.T) {
	human := llm.Message{Role: llm.RoleUser, Content: textutil.Value("human")}
	first, err := NewAgentSteer(runtimeids.NewSessionID(), "first")
	if err != nil {
		t.Fatalf("NewAgentSteer first: %v", err)
	}
	second, err := NewAgentSteer(runtimeids.NewSessionID(), "second")
	if err != nil {
		t.Fatalf("NewAgentSteer second: %v", err)
	}
	firstMessage := first.Message()
	secondMessage := second.Message()
	pending := []queuedUserMessage{
		{message: QueuedUserMessage{ID: "h1", Message: human, CanonicalPresentation: "human"}},
		{message: QueuedUserMessage{ID: "a1", Message: firstMessage, CanonicalPresentation: "first"}},
		{message: QueuedUserMessage{ID: "a2", Message: secondMessage, CanonicalPresentation: "second"}},
		{message: QueuedUserMessage{ID: "h2", Message: human, CanonicalPresentation: "human"}},
	}
	groups, err := queuedUserMessageFlushGroups(pending)
	if err != nil {
		t.Fatalf("queuedUserMessageFlushGroups: %v", err)
	}
	if len(groups) != 4 {
		t.Fatalf("flush groups = %d, want 4", len(groups))
	}
	if groups[0].message.Role != llm.RoleUser || groups[1].message.Role != llm.RoleDeveloper || groups[2].message.Role != llm.RoleDeveloper || groups[3].message.Role != llm.RoleUser {
		t.Fatalf("flush group roles = %q, %q, %q, %q", groups[0].message.Role, groups[1].message.Role, groups[2].message.Role, groups[3].message.Role)
	}
}
