package sessionservice

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/serverapi"
)

func TestInitialInputPrefersPersistedDraft(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/work")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	if err := store.SetInputDraft("persisted"); err != nil {
		t.Fatalf("set input draft: %v", err)
	}
	if got := initialSessionInput(store, "fallback"); got != "persisted" {
		t.Fatalf("initial input = %q, want persisted", got)
	}
}

func TestPersistInputDraftNoOpForNilStore(t *testing.T) {
	if err := persistSessionInputDraft(nil, "draft"); err != nil {
		t.Fatalf("persist input draft with nil store: %v", err)
	}
}

func TestResolveForkRollbackCreatesForkedSession(t *testing.T) {
	root := t.TempDir()
	store, err := session.Create(root, "workspace-x", "/tmp/work")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	if err := store.SetName("parent"); err != nil {
		t.Fatalf("set session name: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	u2Evt, _, err := store.AppendEvent("s2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"})
	if err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if _, _, err := store.AppendEvent("s2", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2"}); err != nil {
		t.Fatalf("append second assistant message: %v", err)
	}

	resolved, err := resolveSessionTransition(context.Background(), sessionTransitionResolveRequest{
		Store: store,
		Transition: sessionTransition{
			Action:             serverapi.SessionTransitionActionForkRollback,
			InitialPrompt:      "edited user message",
			ForkUserMessageSeq: u2Evt.Seq,
		},
	})
	if err != nil {
		t.Fatalf("resolve fork rollback: %v", err)
	}
	if !resolved.ShouldContinue {
		t.Fatal("expected fork rollback to continue")
	}
	if resolved.NextSessionID == "" || resolved.NextSessionID == store.Meta().SessionID {
		t.Fatalf("expected new fork session id, got %q", resolved.NextSessionID)
	}
	if resolved.InitialPrompt != "edited user message" {
		t.Fatalf("initial prompt = %q", resolved.InitialPrompt)
	}
	child, err := session.Open(filepath.Join(root, resolved.NextSessionID))
	if err != nil {
		t.Fatalf("open forked session: %v", err)
	}
	if got := child.Meta().Name; got != "parent \u2192 edit u2" {
		t.Fatalf("forked session name = %q", got)
	}
}
