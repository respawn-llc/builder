package app

import (
	"errors"
	"testing"
	"time"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTranscriptPromptDeferredApprovalCommentaryUsesSubmittedSnapshotOnce(t *testing.T) {
	for _, queueErr := range []error{nil, errors.New("queue failed")} {
		t.Run("failure="+boolName(queueErr != nil), func(t *testing.T) {
			runtimeClient := &runtimeControlFakeClient{queueUserMessageErr: queueErr}
			control := newRecordingPromptControl()
			model := newDeferredApprovalTestModel(runtimeClient, control)
			prompt := testApprovalPrompt(
				"approval-deferred",
				"Allow?",
				clientui.ApprovalDecisionAllowOnce,
				clientui.ApprovalDecisionDeny,
			)
			model = deliverPromptHydration(t, model, prompt)
			model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
			model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("submitted")})
			next, queueCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			model = next.(*uiModel)
			if queueCmd == nil {
				t.Fatal("approval commentary did not start queue creation")
			}

			model = updateUIModel(t, model, ongoingTranscriptEvent{
				Kind:            ongoingTranscriptEventLoss,
				SourceSessionID: ongoingTestSessionID(),
				Err:             errors.New("scratch"),
			})
			refresh := ongoingHydrationMessage(1)
			refresh.Payload.Hydration.RuntimeReadModelUpdate.Activity = runningPromptTestActivity()
			refreshedPrompt := prompt
			refreshedPrompt.ApprovalOptions = []clientui.ApprovalDecision{
				clientui.ApprovalDecisionDeny,
				clientui.ApprovalDecisionAllowOnce,
			}
			refresh.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{refreshedPrompt}
			model = updateUIModel(t, model, ongoingTranscriptEvent{
				Kind:            ongoingTranscriptEventMessage,
				SourceSessionID: ongoingTestSessionID(),
				Message:         refresh,
			})
			model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

			done := queueCmd()
			model = updatePromptUIModel(t, model, done)
			answer := control.nextApproval(t)
			if answer.ApprovalID != string(prompt.PromptID) ||
				answer.Decision != clientui.ApprovalDecisionAllowOnce ||
				answer.Commentary != "submitted" {
				t.Fatalf("approval answer = %+v, want original typed snapshot", answer)
			}
			control.assertNoApproval(t)
		})
	}
}

func TestTranscriptPromptDeferredApprovalCommentarySubmitsEmptyDirectly(t *testing.T) {
	runtimeClient := &runtimeControlFakeClient{}
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(runtimeClient, control)
	prompt := testApprovalPrompt(
		"approval-empty-commentary",
		"Allow?",
		clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionDeny,
	)
	model = deliverPromptHydration(t, model, prompt)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	answer := control.nextApproval(t)
	if answer.ApprovalID != string(prompt.PromptID) ||
		answer.Decision != clientui.ApprovalDecisionAllowOnce ||
		answer.Commentary != "" {
		t.Fatalf("empty-commentary approval answer = %+v", answer)
	}
	if runtimeClient.queueUserMessageCalls != 0 {
		t.Fatalf("empty commentary queue calls = %d, want 0", runtimeClient.queueUserMessageCalls)
	}

	resolved := prompt
	resolved.State = clientui.TranscriptPromptStateResolved
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 2,
			Kind:     clientui.TranscriptMessagePromptResolved,
			Payload:  clientui.TranscriptPayload{PromptResolved: &resolved},
		},
	})
	nextPrompt := testQuestionPrompt("after-empty-commentary", "Next?", "yes")
	nextPrompt.CreatedAt = prompt.CreatedAt.Add(time.Second)
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 3,
			Kind:     clientui.TranscriptMessagePromptPending,
			Payload:  clientui.TranscriptPayload{PromptPending: &nextPrompt},
		},
	})
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if got := control.nextAsk(t).AskID; got != string(nextPrompt.PromptID) {
		t.Fatalf("next answered prompt = %q, want %q", got, nextPrompt.PromptID)
	}
}

func TestTranscriptPromptDeferredApprovalCommentaryDiscardsResolvedSnapshot(t *testing.T) {
	runtimeClient := &runtimeControlFakeClient{}
	control := newRecordingPromptControl()
	model := newDeferredApprovalTestModel(runtimeClient, control)
	prompt := testApprovalPrompt(
		"approval-stale",
		"Allow?",
		clientui.ApprovalDecisionAllowOnce,
		clientui.ApprovalDecisionDeny,
	)
	model = deliverPromptHydration(t, model, prompt)
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateUIModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("stale")})
	next, queueCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*uiModel)

	resolved := prompt
	resolved.State = clientui.TranscriptPromptStateResolved
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 2,
			Kind:     clientui.TranscriptMessagePromptResolved,
			Payload:  clientui.TranscriptPayload{PromptResolved: &resolved},
		},
	})
	model = updateUIModel(t, model, queueCmd())
	control.assertNoApproval(t)

	nextPrompt := testQuestionPrompt("next-after-stale", "Next?", "yes")
	nextPrompt.CreatedAt = prompt.CreatedAt.Add(time.Second)
	model = updateUIModel(t, model, ongoingTranscriptEvent{
		Kind:            ongoingTranscriptEventMessage,
		SourceSessionID: ongoingTestSessionID(),
		Message: clientui.TranscriptMessage{
			Sequence: 3,
			Kind:     clientui.TranscriptMessagePromptPending,
			Payload:  clientui.TranscriptPayload{PromptPending: &nextPrompt},
		},
	})
	model = updatePromptUIModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if got := control.nextAsk(t).AskID; got != string(nextPrompt.PromptID) {
		t.Fatalf("next answered prompt = %q, want %q", got, nextPrompt.PromptID)
	}
}
