package app

import (
	"testing"

	"core/shared/clientui"
)

func TestSubmitDoneBlankFinalAbortsTurnQueueNotificationState(t *testing.T) {
	hook := &submitDoneTurnQueueHook{}
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hook))
	model.activeSubmit = activeSubmitState{token: 1, text: "turn"}
	resultKind := clientui.UserTurnResultKindSilentFinal

	next, _ := model.Update(submitDoneMsg{
		token:      1,
		message:    "",
		resultKind: &resultKind,
	})
	if next == nil {
		t.Fatal("submit completion returned nil model")
	}
	if hook.aborted != 1 {
		t.Fatalf("blank final queue aborts = %d, want one", hook.aborted)
	}
}

func TestSubmitDoneQueuedBlankFinalDoesNotAbortQueueNotificationState(t *testing.T) {
	hook := &submitDoneTurnQueueHook{}
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hook))
	model.activeSubmit = activeSubmitState{token: 1, text: "turn"}
	resultKind := clientui.UserTurnResultKindQueued

	next, _ := model.Update(submitDoneMsg{
		token:      1,
		message:    "",
		queued:     clientui.QueuedUserMessage{ID: "queued-1"},
		resultKind: &resultKind,
	})
	if next == nil {
		t.Fatal("submit completion returned nil model")
	}
	if hook.aborted != 0 {
		t.Fatalf("queued blank final queue aborts = %d, want zero", hook.aborted)
	}
}

type submitDoneTurnQueueHook struct {
	aborted int
}

func (h *submitDoneTurnQueueHook) OnTranscriptMessage(clientui.TranscriptMessage) {}
func (h *submitDoneTurnQueueHook) OnTurnQueueDrained()                            {}
func (h *submitDoneTurnQueueHook) OnTurnQueueAborted()                            { h.aborted++ }
func (h *submitDoneTurnQueueHook) OnUserCompactionCompleted(bool)                 {}
