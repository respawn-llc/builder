package app

import (
	"errors"
	"testing"
	"time"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func scratchHydratePrompts(t *testing.T, model *uiModel, prompts []clientui.TranscriptPrompt) *uiModel {
	t.Helper()
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventLoss,
		SourceSessionID: ongoingTestSessionID(),
		Err:             errors.New("scratch"),
	})
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
	hydration.Payload.Hydration.PendingPrompts = append([]clientui.TranscriptPrompt(nil), prompts...)
	return updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         hydration,
	})
}

func resolvePromptForTest(t *testing.T, model *uiModel, prompt clientui.TranscriptPrompt, sequence uint64) *uiModel {
	t.Helper()
	resolved := prompt
	resolved.State = clientui.TranscriptPromptStateResolved
	return updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: sequence,
			Kind:     clientui.TranscriptMessagePromptResolved,
			Payload:  clientui.TranscriptPayload{PromptResolved: &resolved},
		},
	})
}

func TestOngoingTranscriptPromptHydrationPreservesFIFO(t *testing.T) {
	model, control, prompts := concurrentPromptOrderTestModel(t)
	model = scratchHydratePrompts(t, model, prompts)
	for index, prompt := range prompts {
		model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
		if got := control.nextAsk(t).AskID; got != string(prompt.PromptID) {
			t.Fatalf("answer %d prompt = %q, want %q", index, got, prompt.PromptID)
		}
		model = resolvePromptForTest(t, model, prompt, uint64(index+2))
	}
}

func TestOngoingTranscriptPromptExactIDsDoNotCollide(t *testing.T) {
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	first := testQuestionPrompt("exact-id", "First?", "yes")
	second := testQuestionPrompt(" exact-id", "Second?", "yes")
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{first, second}
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         hydration,
	})

	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if got := control.nextAsk(t).AskID; got != string(first.PromptID) {
		t.Fatalf("first exact prompt id = %q, want %q", got, first.PromptID)
	}
	model = resolvePromptForTest(t, model, first, 2)
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if got := control.nextAsk(t).AskID; got != string(second.PromptID) {
		t.Fatalf("second exact prompt id = %q, want %q", got, second.PromptID)
	}
	model = resolvePromptForTest(t, model, second, 3)
}

func TestOngoingTranscriptPromptHydrationRecoversConcurrentRegistrationWithConditionalStickiness(t *testing.T) {
	model, control, prompts := concurrentPromptOrderTestModel(t)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")})
	model = scratchHydratePrompts(t, model, prompts)

	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	first := control.nextAsk(t)
	if first.AskID != string(prompts[1].PromptID) || first.FreeformAnswer != "draft" {
		t.Fatalf("sticky answer = %+v, want prompt B with preserved draft", first)
	}
	model = resolvePromptForTest(t, model, prompts[1], 2)
	for index, prompt := range []clientui.TranscriptPrompt{prompts[0], prompts[2]} {
		model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
		if got := control.nextAsk(t).AskID; got != string(prompt.PromptID) {
			t.Fatalf("remainder answer %d prompt = %q, want %q", index, got, prompt.PromptID)
		}
		model = resolvePromptForTest(t, model, prompt, uint64(index+3))
	}
}

func TestTranscriptPromptRefreshPreservesApprovalDecision(t *testing.T) {
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	prompt := testApprovalPrompt("approval-refresh", "Allow?",
		clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionDeny)
	model = deliverPromptHydration(t, model, prompt)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("reason")})
	refreshed := prompt
	refreshed.ApprovalOptions = []clientui.ApprovalDecision{
		clientui.ApprovalDecisionDeny, clientui.ApprovalDecisionAllowOnce,
	}
	model = refreshSinglePrompt(t, model, refreshed)
	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)
	model = updateUIModel(t, model, cmd())
	answer := control.nextApproval(t)
	if answer.Decision != clientui.ApprovalDecisionDeny || answer.Commentary != "reason" {
		t.Fatalf("approval refresh answer = %+v", answer)
	}
}

func TestTranscriptPromptRefreshResetsRemovedApprovalDecision(t *testing.T) {
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	prompt := testApprovalPrompt("approval-removed", "Allow?",
		clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionDeny)
	model = deliverPromptHydration(t, model, prompt)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("stale")})
	refreshed := prompt
	refreshed.ApprovalOptions = []clientui.ApprovalDecision{clientui.ApprovalDecisionAllowOnce}
	model = refreshSinglePrompt(t, model, refreshed)
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	answer := control.nextApproval(t)
	if answer.Decision != clientui.ApprovalDecisionAllowOnce || answer.Commentary != "" {
		t.Fatalf("removed approval decision answer = %+v", answer)
	}
}

func TestTranscriptPromptRefreshResetsChangedQuestionChoices(t *testing.T) {
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	prompt := testQuestionPrompt("question-refresh", "Choose", "old-1", "old-2")
	model = deliverPromptHydration(t, model, prompt)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	refreshed := prompt
	refreshed.Suggestions = []string{"new-1", "new-2"}
	model = refreshSinglePrompt(t, model, refreshed)
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	answer := control.nextAsk(t)
	if answer.SelectedOptionNumber == nil || *answer.SelectedOptionNumber != 1 {
		t.Fatalf("changed question choice answer = %+v", answer)
	}
}

func TestTranscriptPromptRefreshPreservesGenericFreeformDraft(t *testing.T) {
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	prompt := testQuestionPrompt("question-freeform", "Choose", "duplicate", "duplicate")
	model = deliverPromptHydration(t, model, prompt)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	refreshed := prompt
	refreshed.Suggestions = []string{"duplicate", "new"}
	model = refreshSinglePrompt(t, model, refreshed)
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	answer := control.nextAsk(t)
	if answer.SelectedOptionNumber != nil || answer.FreeformAnswer != "custom" {
		t.Fatalf("freeform refresh answer = %+v", answer)
	}
}

func TestTranscriptPromptRefreshUpdatesQueuedPrompt(t *testing.T) {
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	active := testQuestionPrompt("active-refresh", "Active", "yes")
	queued := testQuestionPrompt("queued-refresh", "Queued", "old")
	queued.CreatedAt = active.CreatedAt.Add(time.Second)
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{active, queued}
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(), Message: hydration,
	})
	refreshed := queued
	refreshed.Suggestions = []string{"new"}
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{Sequence: 2, Kind: clientui.TranscriptMessagePromptPending,
			Payload: clientui.TranscriptPayload{PromptPending: &refreshed}},
	})
	model = resolvePromptForTest(t, model, active, 3)
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	answer := control.nextAsk(t)
	if answer.AskID != string(queued.PromptID) || answer.SelectedOptionNumber == nil || *answer.SelectedOptionNumber != 1 {
		t.Fatalf("queued refresh answer = %+v", answer)
	}
}

func refreshSinglePrompt(t *testing.T, model *uiModel, prompt clientui.TranscriptPrompt) *uiModel {
	t.Helper()
	return scratchHydratePrompts(t, model, []clientui.TranscriptPrompt{prompt})
}

func TestTranscriptPromptFallbackNotificationLifecycle(t *testing.T) {
	testCases := []struct {
		name   string
		prompt clientui.TranscriptPrompt
	}{
		{name: "question", prompt: testQuestionPrompt("notify-question", "Choose", "one")},
		{name: "approval", prompt: testApprovalPrompt("notify-approval", "Allow", clientui.ApprovalDecisionAllowOnce)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ringer := &countRinger{}
			hooks := newUnfocusedBellHooks(ringer)
			model := sizedTestUIModel(newProjectedStaticUIModel(WithUITurnQueueHook(hooks)), 80, 24)
			model.promptAttention = hooks
			model.ongoingTranscript = newPromptTestOngoingTranscriptController(model, &ongoingSurfaceSpy{})
			model = deliverPromptHydration(t, model, testCase.prompt)
			if got := ringer.total(); got != 1 {
				t.Fatalf("initial notification count = %d, want 1", got)
			}
			model = refreshSinglePrompt(t, model, testCase.prompt)
			if got := ringer.total(); got != 1 {
				t.Fatalf("repeated hydration notification count = %d, want 1", got)
			}
			refreshed := testCase.prompt
			refreshed.Question += " refreshed"
			model = updateUIModel(t, model, ongoingTranscriptEvent{
				Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(),
				Message: clientui.TranscriptMessage{Sequence: 2, Kind: clientui.TranscriptMessagePromptPending,
					Payload: clientui.TranscriptPayload{PromptPending: &refreshed}},
			})
			if got := ringer.total(); got != 1 {
				t.Fatalf("live refresh notification count = %d, want 1", got)
			}
			model = resolvePromptForTest(t, model, refreshed, 3)
			next := testCase.prompt
			next.PromptID = clientui.PromptID(string(next.PromptID) + "-next")
			next.CreatedAt = next.CreatedAt.Add(time.Second)
			model = updateUIModel(t, model, ongoingTranscriptEvent{
				Kind: ongoingTranscriptEventMessage, SourceSessionID: ongoingTestSessionID(),
				Message: clientui.TranscriptMessage{Sequence: 4, Kind: clientui.TranscriptMessagePromptPending,
					Payload: clientui.TranscriptPayload{PromptPending: &next}},
			})
			if got := ringer.total(); got != 2 {
				t.Fatalf("later prompt notification count = %d, want 2", got)
			}
		})
	}
}

func concurrentPromptOrderTestModel(t *testing.T) (*uiModel, *recordingPromptControl, []clientui.TranscriptPrompt) {
	t.Helper()
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(&runtimeControlFakeClient{}, control)
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message:         ongoingHydrationMessage(1),
	})
	base := time.Unix(10, 0).UTC()
	a := testQuestionPrompt("prompt-a", "A?", "yes")
	b := testQuestionPrompt("prompt-b", "B?", "yes")
	c := testQuestionPrompt("prompt-c", "C?", "yes")
	a.CreatedAt = base
	b.CreatedAt = base.Add(time.Second)
	c.CreatedAt = base.Add(2 * time.Second)
	for index, prompt := range []clientui.TranscriptPrompt{b, a} {
		copy := prompt
		model = updateUIModel(t, model, ongoingTranscriptEvent{
			Kind:            ongoingTranscriptEventMessage,
			SourceSessionID: ongoingTestSessionID(),
			Message: clientui.TranscriptMessage{
				Sequence: uint64(index + 2),
				Kind:     clientui.TranscriptMessagePromptPending,
				Payload:  clientui.TranscriptPayload{PromptPending: &copy},
			},
		})
	}
	return model, control, []clientui.TranscriptPrompt{a, b, c}
}
