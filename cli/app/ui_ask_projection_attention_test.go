package app

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAskVisibleActivationNormalizesMarkdownAfterProjectionCompletes(t *testing.T) {
	ringer := &countRinger{}
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.promptAttention = newUnfocusedBellHooks(ringer)
	lifecycle := &recordingLifecycleAttentionSink{}
	model.lifecycleAttention = lifecycle
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			rows:    []string{"projected notification copy"},
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"**raw Markdown source that must not reach activation notification rendering**",
		"yes",
	)})
	pending := next.(*uiModel)
	next, _ = pending.Update(command())
	ready := next.(*uiModel)

	if ready.ask.activeProjection == nil || ringer.notifications != 1 || len(ringer.messages) != 1 {
		t.Fatalf("activation state = projection %+v notifications %d messages %q", ready.ask.activeProjection, ringer.notifications, ringer.messages)
	}
	if len(lifecycle.facts) != 1 || lifecycle.facts[0].summary != "**raw Markdown source that must not reach activation notification rendering**" {
		t.Fatalf("activation lifecycle facts = %+v, want one Markdown-source fact", lifecycle.facts)
	}
}

func TestAskInitialProjectionReadinessBlocksPromptAndComposerKeys(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("typed")},
		{Type: tea.KeyBackspace},
		{Type: tea.KeyDelete},
		{Type: tea.KeyLeft},
		{Type: tea.KeyRight},
		{Type: tea.KeyUp},
		{Type: tea.KeyDown},
		{Type: tea.KeyHome},
		{Type: tea.KeyEnd},
		{Type: tea.KeyTab},
		{Type: tea.KeyCtrlV},
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
	}
	for _, key := range keys {
		t.Run(key.String(), func(t *testing.T) {
			model, control := newProjectedPromptTestUIModel(t)
			model = sizedTestUIModel(model, 64, 20)
			testSetMainInput(model, "main draft")
			next, _ := model.Update(askEventMsg{event: testQuestionAskEvent(
				"ask-1",
				"Question?",
				"yes",
				"no",
			)})
			pending := next.(*uiModel)
			currentToken := pending.ask.currentToken

			next, _ = pending.Update(key)
			updated := next.(*uiModel)
			if updated.ask.current == nil || updated.ask.currentToken != currentToken {
				t.Fatal("pending readiness key changed current prompt ownership")
			}
			if updated.ask.cursor != 0 || updated.ask.freeform ||
				updated.ask.editor.Text() != "" || updated.ask.activeDelivery != nil {
				t.Fatalf(
					"pending readiness key mutated ask state: cursor=%d freeform=%t editor=%q delivery=%+v",
					updated.ask.cursor,
					updated.ask.freeform,
					updated.ask.editor.Text(),
					updated.ask.activeDelivery,
				)
			}
			if testMainInput(updated) != "main draft" {
				t.Fatalf("pending readiness key mutated hidden composer: %q", testMainInput(updated))
			}
			if len(control.askRequests) != 0 || len(control.approvalRequests) != 0 {
				t.Fatal("pending readiness key sent an invisible prompt answer")
			}
		})
	}
}

func TestAskInitialProjectionReadinessKeepsHelpAndGlobalCtrlC(t *testing.T) {
	t.Run("help runs first", func(t *testing.T) {
		model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
		next, _ := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
		pending := next.(*uiModel)

		next, _ = pending.Update(tea.KeyMsg{Type: tea.KeyF1})
		updated := next.(*uiModel)
		if !updated.helpVisible {
			t.Fatal("pending readiness blocked global help handling")
		}
	})

	t.Run("ctrl c uses global runtime handling", func(t *testing.T) {
		model, control := newProjectedPromptTestUIModel(t)
		model = sizedTestUIModel(model, 64, 20)
		next, _ := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
		pending := next.(*uiModel)

		next, command := pending.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		updated := next.(*uiModel)
		if updated.ask.current == nil || updated.ask.activeDelivery != nil {
			t.Fatal("pending ctrl-c answered or cancelled the invisible prompt")
		}
		if updated.exitAction != UIActionExit || command == nil {
			t.Fatal("pending ctrl-c did not reach global runtime/terminal handling")
		}
		if len(control.askRequests) != 0 || len(control.approvalRequests) != 0 {
			t.Fatal("pending ctrl-c sent an invisible prompt answer")
		}
	})
}

func TestAskVisibleActivationOwnsNotificationTiming(t *testing.T) {
	ringer := &countRinger{}
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.promptAttention = newUnfocusedBellHooks(ringer)
	prompt := testQuestionPrompt("ask-1", "Question?", "yes")
	message := clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessagePromptPending,
		Payload: clientui.TranscriptPayload{
			PromptPending: &prompt,
		},
	}

	command := model.applyAdmittedTranscriptMessageState(message, runtimeTupleMergeResult{})
	if ringer.total() != 0 {
		t.Fatal("live prompt admission emitted attention before projection readiness")
	}
	next, _ := model.Update(command())
	ready := next.(*uiModel)
	if ready.ask.activeProjection == nil || ringer.total() != 1 {
		t.Fatalf("visible activation notifications = %d, want exactly one", ringer.total())
	}

	next, sameIdentityCommand := ready.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?", "new")})
	ready = next.(*uiModel)
	if sameIdentityCommand != nil || ringer.total() != 1 {
		t.Fatal("same-identity refresh emitted attention or scheduled rendering")
	}

	next, widthCommand := ready.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	reprojecting := next.(*uiModel)
	next, _ = reprojecting.Update(widthCommand())
	ready = next.(*uiModel)
	if ringer.total() != 1 {
		t.Fatal("visible width reprojection emitted duplicate attention")
	}

	next, queuedCommand := ready.Update(askEventMsg{event: testQuestionAskEvent("ask-2", "Second?", "yes")})
	ready = next.(*uiModel)
	if queuedCommand != nil || ringer.total() != 1 {
		t.Fatal("queued prompt emitted attention before promotion")
	}
	next, promotionCommand := ready.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
	promoted := next.(*uiModel)
	if promotionCommand == nil || ringer.total() != 1 {
		t.Fatal("queued prompt promotion did not defer attention until projection")
	}
	next, _ = promoted.Update(promotionCommand())
	ready = next.(*uiModel)
	if ringer.total() != 2 {
		t.Fatalf("promoted prompt notifications = %d, want 2 total", ringer.total())
	}

	next, _ = ready.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-2"}})
	reopenedBase := next.(*uiModel)
	next, reopenedCommand := reopenedBase.Update(askEventMsg{event: testQuestionAskEvent("ask-2", "Second?", "yes")})
	reopened := next.(*uiModel)
	next, _ = reopened.Update(reopenedCommand())
	if ringer.total() != 3 {
		t.Fatalf("resolved-then-reopened notifications = %d, want 3 total", ringer.total())
	}
}

func TestAskHydrationAdmissionEmitsNoAttentionBeforeProjection(t *testing.T) {
	ringer := &countRinger{}
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.promptAttention = newUnfocusedBellHooks(ringer)
	prompt := testQuestionPrompt("ask-1", "Hydrated question?", "yes")

	command := model.reconcileTranscriptPrompts([]clientui.TranscriptPrompt{prompt})
	if command == nil {
		t.Fatal("hydration did not return the initial projection command")
	}
	if ringer.total() != 0 {
		t.Fatal("hydration emitted attention before projection readiness")
	}
	next, _ := model.Update(command())
	ready := next.(*uiModel)
	if ready.ask.activeProjection == nil || ringer.total() != 1 {
		t.Fatalf("hydrated visible activation notifications = %d, want 1", ringer.total())
	}
}
