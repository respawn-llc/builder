package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAskProjectionInvalidationPreservesVisibleDeliveryStateAndKeys(t *testing.T) {
	model, _ := newProjectedPromptTestUIModel(t)
	model = sizedTestUIModel(model, 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{request: request, rows: []string{request.questionSource}}
	}
	next, initialProjection := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Original question",
		"one",
		"two",
	)})
	model = next.(*uiModel)
	next, _ = model.Update(initialProjection())
	model = next.(*uiModel)

	next, deliveryCommand := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	delivering := next.(*uiModel)
	if deliveryCommand == nil || delivering.ask.activeDelivery == nil {
		t.Fatal("visible prompt did not establish answer delivery")
	}
	activeDelivery := delivering.ask.activeDelivery
	requestID := activeDelivery.requestID
	currentToken := delivering.ask.currentToken

	replacement := testQuestionAskEvent("ask-1", "Updated question", "new one", "new two")
	next, projectionCommand := delivering.Update(askEventMsg{event: replacement})
	reprojecting := next.(*uiModel)
	if projectionCommand == nil {
		t.Fatal("same-ID question invalidation did not schedule projection")
	}
	if reprojecting.ask.activeDelivery != activeDelivery ||
		reprojecting.ask.activeDelivery.requestID != requestID ||
		reprojecting.ask.currentToken != currentToken {
		t.Fatal("projection invalidation changed delivery or current ownership")
	}
	if !reprojecting.askReadyForInteraction() {
		t.Fatal("visible reprojection incorrectly entered initial readiness")
	}

	next, _ = reprojecting.Update(tea.KeyMsg{Type: tea.KeyTab})
	reprojecting = next.(*uiModel)
	next, _ = reprojecting.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("retry")})
	reprojecting = next.(*uiModel)
	if !reprojecting.ask.freeform || reprojecting.ask.editor.Text() != "retry" {
		t.Fatal("visible delivery reprojection gated retry-draft interaction")
	}
	next, _ = reprojecting.Update(projectionCommand())
	updated := next.(*uiModel)
	if updated.ask.activeDelivery != activeDelivery ||
		updated.ask.activeDelivery.requestID != requestID ||
		updated.ask.editor.Text() != "retry" ||
		!updated.ask.freeform {
		t.Fatal("projection install mutated delivery ownership or retry draft")
	}
}

func TestAskProjectionInvalidationPreservesAnswerPendingLock(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{request: request, rows: []string{request.questionSource}}
	}
	next, initialProjection := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Original question",
		"one",
		"two",
	)})
	model = next.(*uiModel)
	next, _ = model.Update(initialProjection())
	model = next.(*uiModel)
	model.ask.answerPending = true

	next, projectionCommand := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Updated question",
		"new one",
		"new two",
	)})
	reprojecting := next.(*uiModel)
	if projectionCommand == nil || !reprojecting.ask.answerPending {
		t.Fatal("answer-pending invalidation did not preserve its lock and schedule projection")
	}
	next, _ = reprojecting.Update(tea.KeyMsg{Type: tea.KeyTab})
	locked := next.(*uiModel)
	if locked.ask.freeform || !locked.ask.answerPending {
		t.Fatal("answer-pending lock changed during visible reprojection")
	}
	next, _ = locked.Update(projectionCommand())
	updated := next.(*uiModel)
	if !updated.ask.answerPending || updated.ask.freeform {
		t.Fatal("projection install changed answer-pending behavior")
	}
}
