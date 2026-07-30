package sessionservice

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
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
	appendSessionMessage(t, store, "s1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, store, "s1", session.MessageRoleAssistant, "a1")
	u2Evt := appendSessionMessage(t, store, "s2", session.MessageRoleUser, "u2")
	appendSessionMessage(t, store, "s2", session.MessageRoleAssistant, "a2")

	resolved, err := resolveSessionTransition(context.Background(), sessionTransitionResolveRequest{
		Store: store,
		Transition: sessionTransition{
			Action:             serverapi.SessionTransitionActionForkRollback,
			InitialPrompt:      "edited user message",
			ForkUserMessageSeq: u2Evt.Seq(),
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

func TestResolveForkRollbackPreservesIntentionalEmptyDraftOverride(t *testing.T) {
	root := t.TempDir()
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(root, "workspace-x", "/tmp/work", sessioncontract.SessionCategoryMain, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	appendSessionMessage(t, store, "s1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, store, "s1", session.MessageRoleAssistant, "a1")
	u2Evt := appendSessionMessage(t, store, "s2", session.MessageRoleUser, "u2")
	appendSessionMessage(t, store, "s2", session.MessageRoleAssistant, "a2")

	resolved, err := resolveSessionTransition(context.Background(), sessionTransitionResolveRequest{
		Store: store,
		Transition: sessionTransition{
			Action:             serverapi.SessionTransitionActionForkRollback,
			InitialInput:       textutil.Value(""),
			ForkUserMessageSeq: u2Evt.Seq(),
		},
	})
	if err != nil {
		t.Fatalf("resolve fork rollback: %v", err)
	}
	_, preparation := requireSessionLifecycleLaunch(t, resolved)
	if _, present := preparation.InitialPrompt(); present {
		t.Fatal("rollback fork must not submit the selected user message")
	}
	disposition := preparation.DraftDisposition()
	if disposition.Kind() != serverapi.SessionDraftDispositionOverrideStoredDraft {
		t.Fatalf("rollback fork draft disposition = %q, want override stored draft", disposition.Kind())
	}
	text, present := disposition.OverrideText()
	if !present || text != "" {
		t.Fatalf("rollback fork draft = %q/%t, want intentional empty override", text, present)
	}
}
