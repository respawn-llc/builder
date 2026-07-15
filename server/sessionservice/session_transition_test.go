package sessionservice

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestInitialInputPrefersPersistedDraft(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/work", sessioncontract.SessionCategoryMain, persistence.Options()...)
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
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(root, "workspace-x", "/tmp/work", sessioncontract.SessionCategoryMain, persistence.Options()...)
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
	intent, preparation := requireSessionLifecycleLaunch(t, resolved)
	forkID, ok := intent.SessionID()
	if !ok || forkID.String() == store.Meta().SessionID {
		t.Fatalf("expected new fork session id, got %q/%v", forkID.String(), ok)
	}
	prompt, ok := preparation.InitialPrompt()
	if !ok || prompt.Text != "edited user message" {
		t.Fatalf("initial prompt = %+v/%v", prompt, ok)
	}
	child, err := persistence.Open(filepath.Join(root, forkID.String()))
	if err != nil {
		t.Fatalf("open forked session: %v", err)
	}
	if got := child.Meta().Name; got != "parent \u2192 edit u2" {
		t.Fatalf("forked session name = %q", got)
	}
}
