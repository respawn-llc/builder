package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
)

func TestInputModePrioritizesExclusiveUIFlows(t *testing.T) {
	detailView := tui.NewModel()
	next, _ := detailView.Update(tui.SetModeMsg{Mode: tui.ModeDetail})
	detailView = next.(tui.Model)

	tests := []struct {
		name  string
		model uiModel
		want  uiInputMode
	}{
		{
			name:  "status mode",
			model: uiModel{uiConversationFeatureState: uiConversationFeatureState{interaction: uiInteractionState{Mode: uiInputModeStatus}}},
			want:  uiInputModeStatus,
		},
		{name: "process list mode", model: uiModel{uiConversationFeatureState: uiConversationFeatureState{interaction: uiInteractionState{Mode: uiInputModeProcessList}}}, want: uiInputModeProcessList},
		{name: "rollback selection mode", model: uiModel{uiConversationFeatureState: uiConversationFeatureState{interaction: uiInteractionState{Mode: uiInputModeRollbackSelection}}}, want: uiInputModeRollbackSelection},
		{name: "rollback edit mode", model: uiModel{uiConversationFeatureState: uiConversationFeatureState{interaction: uiInteractionState{Mode: uiInputModeRollbackEdit}}}, want: uiInputModeRollbackEdit},
		{name: "ask mode", model: uiModel{uiConversationFeatureState: uiConversationFeatureState{interaction: uiInteractionState{Mode: uiInputModeAsk}, ask: uiAskState{current: &askEvent{req: clientui.PendingPromptEvent{Question: "Proceed?"}}}}, uiRuntimeFeatureState: uiRuntimeFeatureState{view: detailView}}, want: uiInputModeAsk},
		{name: "main", model: uiModel{}, want: uiInputModeMain},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.model.inputMode(); got != tc.want {
				t.Fatalf("input mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRestorePrimaryInputModeFollowsAskAndTranscriptMode(t *testing.T) {
	detailView := tui.NewModel()
	next, _ := detailView.Update(tui.SetModeMsg{Mode: tui.ModeDetail})
	detailView = next.(tui.Model)

	tests := []struct {
		name  string
		model *uiModel
		want  uiInputMode
	}{
		{
			name:  "active ask in ongoing mode restores ask input",
			model: &uiModel{uiConversationFeatureState: uiConversationFeatureState{ask: uiAskState{current: &askEvent{req: clientui.PendingPromptEvent{Question: "Proceed?"}}}}},
			want:  uiInputModeAsk,
		},
		{
			name:  "active ask in detail mode restores main input",
			model: &uiModel{uiConversationFeatureState: uiConversationFeatureState{ask: uiAskState{current: &askEvent{req: clientui.PendingPromptEvent{Question: "Proceed?"}}}}, uiRuntimeFeatureState: uiRuntimeFeatureState{view: detailView}},
			want:  uiInputModeMain,
		},
		{
			name:  "no ask restores main input",
			model: &uiModel{},
			want:  uiInputModeMain,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.model.restorePrimaryInputMode()
			if got := tc.model.inputMode(); got != tc.want {
				t.Fatalf("input mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInputModeStateExposesRenderingAndInteractionFlags(t *testing.T) {
	m := &uiModel{
		uiConversationFeatureState: uiConversationFeatureState{interaction: uiInteractionState{Mode: uiInputModeRollbackEdit}},
	}
	m.setBusy(true)
	m.setInputSubmitLocked(true)
	state := m.inputModeState()

	if state.Mode != uiInputModeRollbackEdit {
		t.Fatalf("mode = %q, want %q", state.Mode, uiInputModeRollbackEdit)
	}
	if !state.InputLocked {
		t.Fatal("expected locked input state")
	}
	if !state.Busy {
		t.Fatal("expected busy input state")
	}
	if !state.ShowsMainInput {
		t.Fatal("expected rollback edit to keep main input visible")
	}
	if state.ShowsAskInput {
		t.Fatal("did not expect rollback edit to show ask input")
	}
}
