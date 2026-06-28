package app

import (
	"context"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBackTeleportLifecycleSeedsParentDraftWithoutAutoSubmit(t *testing.T) {
	tests := []struct {
		name                string
		childMessages       []llm.Message
		childOngoing        string
		childActivity       uiActivity
		existingParentDraft string
		want                string
	}{
		{name: "copy idle child final assistant reply", childMessages: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}}, childActivity: uiActivityIdle, want: "test"},
		{name: "copy latest child final assistant reply past reminder entry", childMessages: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}, {Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSoonReminder, Content: "heads up"}}, childActivity: uiActivityIdle, want: "test"},
		{name: "copy latest child final assistant reply past trailing error feedback", childMessages: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}, {Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: "phase mismatch"}}, childActivity: uiActivityIdle, want: "test"},
		{name: "copy latest child final assistant reply past reviewer feedback", childMessages: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}, {Role: llm.RoleDeveloper, MessageType: llm.MessageTypeReviewerFeedback, Content: "reviewer suggestions"}}, childActivity: uiActivityIdle, want: "test"},
		{name: "ignore interrupted child streaming reply", childOngoing: "review findings", childActivity: uiActivityInterrupted, want: ""},
		{name: "preserve existing parent draft", childMessages: []llm.Message{{Role: llm.RoleAssistant, Content: "test", Phase: llm.MessagePhaseFinal}}, childActivity: uiActivityIdle, existingParentDraft: "keep existing", want: "keep existing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			parentStore := createAppRuntimeSessionAt(t, root, "workspace-x", "/tmp/work")
			if err := parentStore.SetInputDraft(tt.existingParentDraft); err != nil {
				t.Fatalf("set parent draft: %v", err)
			}

			childStore := createAppRuntimeSessionAt(t, root, "workspace-x", "/tmp/work")
			if err := childStore.SetParentSessionID(parentStore.Meta().SessionID); err != nil {
				t.Fatalf("set child parent id: %v", err)
			}

			for idx, message := range tt.childMessages {
				if _, _, err := childStore.AppendEvent("step-1", "message", message); err != nil {
					t.Fatalf("append child transcript message %d: %v", idx, err)
				}
			}
			childEngine := newAppRuntimeEngineWithStore(t, childStore, statusLineFakeClient{}, runtime.Config{})
			childModel := newProjectedEngineUIModel(childEngine)
			childModel.activity = tt.childActivity
			if tt.childOngoing != "" {
				childModel.forwardToView(tui.SetConversationMsg{Entries: childModel.transcriptEntries, Ongoing: tt.childOngoing})
			}
			childModel.input = "/back"
			server := &testEmbeddedServer{cfg: config.App{PersistenceRoot: root}, containerDir: root}

			next, cmd := childModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updatedChild := next.(*uiModel)
			if cmd == nil {
				t.Fatal("expected quit cmd for /back")
			}
			if err := persistSessionDraftToServer(context.Background(), server, childStore.Meta().SessionID, updatedChild); err != nil {
				t.Fatalf("persist child draft: %v", err)
			}

			resolved, err := resolveSessionAction(context.Background(), server, nil, childStore.Meta().SessionID, updatedChild.Transition())
			if err != nil {
				t.Fatalf("resolve session action: %v", err)
			}
			if !resolved.ShouldContinue {
				t.Fatal("expected lifecycle to continue")
			}
			if resolved.NextSessionID != parentStore.Meta().SessionID {
				t.Fatalf("expected parent session target, got %q", resolved.NextSessionID)
			}

			reopenedParent, err := session.Open(parentStore.Dir())
			if err != nil {
				t.Fatalf("reopen parent store: %v", err)
			}
			parentEngine := newAppRuntimeEngineWithStore(t, reopenedParent, statusLineFakeClient{}, runtime.Config{})
			parentModel := newProjectedEngineUIModel(
				parentEngine,
				WithUIInitialInput(sessionLaunchInitialInputFromServer(context.Background(), server, reopenedParent.Meta().SessionID, resolved.InitialInput)),
			)

			if parentModel.input != tt.want {
				t.Fatalf("expected parent draft %q, got %q", tt.want, parentModel.input)
			}
			if parentModel.isBusy() {
				t.Fatal("did not expect parent draft to auto-submit")
			}

			next, _ = parentModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			editable := next.(*uiModel)
			if editable.input != tt.want+"x" {
				t.Fatalf("expected editable parent draft, got %q", editable.input)
			}
		})
	}
}

func TestForkRollbackNativeStartupReplayUsesForkedHistory(t *testing.T) {
	root := t.TempDir()
	store := createAppRuntimeSessionAt(t, root, "workspace-x", "/tmp/work")
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append u1: %v", err)
	}
	if _, _, err := store.AppendEvent("s1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append a1: %v", err)
	}
	if _, _, err := store.AppendEvent("s2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append u2: %v", err)
	}
	if _, _, err := store.AppendEvent("s2", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2"}); err != nil {
		t.Fatalf("append a2: %v", err)
	}

	resolved, err := resolveSessionAction(
		context.Background(),
		&testEmbeddedServer{cfg: config.App{PersistenceRoot: root}, containerDir: root},
		nil,
		store.Meta().SessionID,
		UITransition{Action: UIActionForkRollback, InitialPrompt: "edited user message", ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 2))},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	if !resolved.ShouldContinue {
		t.Fatal("expected lifecycle to continue for fork rollback action")
	}

	forkedStore, err := session.Open(filepath.Join(root, resolved.NextSessionID))
	if err != nil {
		t.Fatalf("open fork session store: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, forkedStore, statusLineFakeClient{}, runtime.Config{})

	m := newProjectedEngineUIModel(eng)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	updated := next.(*uiModel)
	plain := stripANSIAndTrimRight(updated.view.View())
	if !strings.Contains(plain, "u1") || !strings.Contains(plain, "a1") {
		t.Fatalf("expected fork startup view to include fork base history, got %q", plain)
	}
	if strings.Contains(plain, "u2") || strings.Contains(plain, "a2") {
		t.Fatalf("expected fork startup view to exclude trimmed history after fork point, got %q", plain)
	}
	if len(updated.transcriptEntries) != 2 {
		t.Fatalf("expected forked transcript to include only two committed entries, got %d", len(updated.transcriptEntries))
	}
}
