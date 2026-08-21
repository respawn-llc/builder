package runtime

import (
	"context"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func TestSubmitUserMessageConsumesPendingSessionRebindReminder(t *testing.T) {
	store := mustCreateTestSession(t)
	targetCWD := t.TempDir()
	reminder := session.SessionRebindReminder{
		SourceProject:    serverapi.ProjectReference{ID: "source", Name: "Source"},
		TargetProject:    serverapi.ProjectReference{ID: "target", Name: "Target"},
		WorkingDirectory: textutil.Value(targetCWD),
	}
	if err := store.SetSessionRebindReminder(&reminder); err != nil {
		t.Fatalf("SetSessionRebindReminder: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{finalOutputItemResponse("ok")}}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{})

	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	var rebind *llm.Message
	for _, message := range requestMessages(client.calls[0]) {
		if message.MessageType != nil && *message.MessageType == llm.MessageTypeSessionRebind {
			copyMessage := message
			rebind = &copyMessage
			break
		}
	}
	if rebind == nil || rebind.CompactContent == nil || *rebind.CompactContent != clientui.SessionRebindCompactLabel {
		t.Fatalf("typed Session rebind reminder = %+v", rebind)
	}
	content := messageContent(*rebind)
	for _, fact := range []string{"Source", "Target", targetCWD} {
		if !strings.Contains(content, fact) {
			t.Fatalf("Session rebind reminder %q does not contain fact %q", content, fact)
		}
	}
	if store.Meta().RebindReminder != nil {
		t.Fatalf("consumed Session rebind reminder remains durable: %+v", store.Meta().RebindReminder)
	}
}
