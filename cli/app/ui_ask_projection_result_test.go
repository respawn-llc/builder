package app

import (
	"errors"
	"testing"

	tuitest "core/internal/testharness/pty"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

func TestAskProjectionStaleProjectorPanicIsIgnored(t *testing.T) {
	ringer := &countRinger{}
	model := sizedTestUIModel(newProjectedStaticUIModel(WithUIDebug(true)), 64, 20)
	model.promptAttention = newUnfocusedBellHooks(ringer)
	model.questionProjector = func(questionRenderRequest) questionRenderResultMsg {
		panic("stale renderer panic")
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
	pending := next.(*uiModel)
	if command == nil {
		t.Fatal("prompt admission did not return a projection command")
	}
	next, _ = pending.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
	resolved := next.(*uiModel)

	result, ok := command().(questionRenderResultMsg)
	if !ok {
		t.Fatalf("projection command returned %T, want questionRenderResultMsg", command())
	}
	if result.err == nil || len(result.stack) == 0 {
		t.Fatalf("projector panic result = %+v, want error with stack", result)
	}

	next, quit := resolved.Update(result)
	updated := next.(*uiModel)
	if quit != nil {
		t.Fatal("stale projector panic requested UI exit")
	}
	if updated.forcedLocalExit {
		t.Fatal("stale projector panic forced local exit")
	}
	if ringer.total() != 0 {
		t.Fatal("stale projector panic emitted prompt attention")
	}
}

func TestAskProjectionWrongCurrentResultDoesNotConsumeInFlightOperation(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	next, projectionCommand := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Question?",
		"yes",
	)})
	pending := next.(*uiModel)
	if projectionCommand == nil || pending.ask.inFlightProjection == nil {
		t.Fatal("prompt admission did not establish an in-flight projection")
	}
	inFlight := *pending.ask.inFlightProjection
	wrongCurrent := inFlight
	wrongCurrent.currentToken = nextNonZeroToken(wrongCurrent.currentToken)

	next, command := pending.Update(questionRenderResultMsg{
		request: wrongCurrent,
		rows:    []string{"wrong current rows"},
	})
	updated := next.(*uiModel)

	if command != nil {
		t.Fatal("wrong-current result scheduled replacement work while the real operation remained in flight")
	}
	if updated.ask.inFlightProjection == nil || *updated.ask.inFlightProjection != inFlight {
		t.Fatalf("wrong-current result consumed in-flight request: got %+v want %+v", updated.ask.inFlightProjection, inFlight)
	}
	if updated.ask.activeProjection != nil {
		t.Fatal("wrong-current result installed an active projection")
	}

	next, command = updated.Update(projectionCommand())
	completed := next.(*uiModel)
	if command != nil {
		t.Fatal("legitimate in-flight result scheduled unexpected follow-up work")
	}
	if completed.ask.inFlightProjection != nil || completed.ask.activeProjection == nil {
		t.Fatal("wrong-current result prevented the legitimate operation from completing")
	}
}

func TestAskProjectionWrongOperationResultDoesNotConsumeInFlightRequest(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	next, projectionCommand := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Question?",
		"yes",
	)})
	pending := next.(*uiModel)
	if projectionCommand == nil || pending.ask.inFlightProjection == nil {
		t.Fatal("prompt admission did not establish an in-flight projection")
	}
	inFlight := *pending.ask.inFlightProjection
	wrongOperation := inFlight
	wrongOperation.operationToken = uuid.New()

	next, command := pending.Update(questionRenderResultMsg{
		request: wrongOperation,
		rows:    []string{"wrong operation rows"},
	})
	updated := next.(*uiModel)

	if command != nil {
		t.Fatal("wrong-operation result scheduled replacement work")
	}
	if updated.ask.inFlightProjection == nil || *updated.ask.inFlightProjection != inFlight {
		t.Fatalf("wrong-operation result consumed in-flight request: got %+v want %+v", updated.ask.inFlightProjection, inFlight)
	}
	if updated.ask.activeProjection != nil {
		t.Fatal("wrong-operation result installed an active projection")
	}
}

func TestAskProjectionAuthoritativeFailureExitsWithoutMutatingPrompt(t *testing.T) {
	logger := &testUILogger{}
	ringer := &countRinger{}
	model := sizedTestUIModel(newProjectedStaticUIModel(WithUILogger(logger)), 64, 20)
	model.promptAttention = newUnfocusedBellHooks(ringer)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			err:     errors.New("render failed"),
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?", "yes", "no")})
	pending := next.(*uiModel)
	result, ok := command().(questionRenderResultMsg)
	if !ok {
		t.Fatalf("projection command returned an unexpected message type")
	}
	if result.err == nil || len(result.stack) == 0 {
		t.Fatalf("projection failure did not carry an error and stack")
	}

	next, quit := pending.Update(result)
	updated := next.(*uiModel)
	if quit == nil {
		t.Fatal("authoritative projection failure did not return a quit command")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("authoritative projection failure did not request Bubble Tea quit")
	}
	if !updated.forcedLocalExit || !updated.Transition().Exit {
		t.Fatal("authoritative projection failure did not select forced local exit")
	}
	if updated.ask.current == nil || updated.ask.current.prompt.PromptID != "ask-1" {
		t.Fatal("authoritative projection failure mutated the unresolved prompt")
	}
	if updated.ask.activeProjection != nil || updated.ask.inFlightProjection != nil {
		t.Fatal("authoritative projection failure left render state active")
	}
	if updated.transientStatus == "" || updated.transientStatusKind != uiStatusNoticeError {
		t.Fatal("authoritative projection failure did not surface an operator error")
	}
	if len(logger.lines) != 1 {
		t.Fatalf("projection failure log count = %d, want 1", len(logger.lines))
	}
	if ringer.total() != 0 {
		t.Fatal("authoritative projection failure emitted prompt attention")
	}
}

func TestAskProjectionAuthoritativeFailureLogsThenPanicsInDebug(t *testing.T) {
	logger := &testUILogger{}
	model := sizedTestUIModel(newProjectedStaticUIModel(WithUILogger(logger), WithUIDebug(true)), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			err:     errors.New("render failed"),
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
	pending := next.(*uiModel)
	result := command()

	defer func() {
		if recover() == nil {
			t.Fatal("authoritative debug projection failure did not panic")
		}
		if len(logger.lines) != 1 {
			t.Fatalf("projection failure log count = %d, want 1", len(logger.lines))
		}
	}()
	_, _ = pending.Update(result)
}

func TestAskProjectionWidthInvalidationRunsOnlyThroughReturnedCommand(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		return questionRenderResultMsg{
			request: request,
			rows:    []string{"rendered"},
		}
	}
	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?", "yes", "no")})
	pending := next.(*uiModel)
	next, _ = pending.Update(command())
	ready := next.(*uiModel)
	if renderCount != 1 {
		t.Fatalf("initial render count = %d, want 1", renderCount)
	}

	next, resizeCommand := ready.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	resizing := next.(*uiModel)
	if renderCount != 1 {
		t.Fatal("width invalidation executed Markdown during resize Update")
	}
	if resizeCommand == nil {
		t.Fatal("width invalidation did not return a projection command")
	}
	if resizing.ask.activeProjection == nil || resizing.ask.activeProjection.renderedAt.terminalWidth != 64 {
		t.Fatal("width invalidation discarded the visible cached projection")
	}
	if resizing.ask.inFlightProjection == nil || resizing.ask.inFlightProjection.identity.terminalWidth != 40 {
		t.Fatal("width invalidation did not record the pending target width")
	}

	result := resizeCommand()
	if renderCount != 2 {
		t.Fatalf("render count after command = %d, want 2", renderCount)
	}
	next, _ = resizing.Update(result)
	updated := next.(*uiModel)
	if updated.ask.activeProjection == nil || updated.ask.activeProjection.renderedAt.terminalWidth != 40 {
		t.Fatal("width render result did not replace the cached projection")
	}
}

func TestAskProjectionStaleRowsAreWidthClampedUntilDesiredRenderInstalls(t *testing.T) {
	const (
		initialWidth = 64
		targetWidth  = 18
		height       = 12
	)
	model := sizedTestUIModel(newProjectedStaticUIModel(), initialWidth, height)
	next, initialCommand := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"[a long linked label](https://example.com/destination)",
		"yes",
	)})
	pending := next.(*uiModel)
	next, _ = pending.Update(initialCommand())
	ready := next.(*uiModel)

	next, resizeCommand := ready.Update(tea.WindowSizeMsg{Width: targetWidth, Height: height})
	reprojecting := next.(*uiModel)
	if resizeCommand == nil || reprojecting.ask.inFlightProjection == nil ||
		reprojecting.ask.latestDesiredProjection == nil {
		t.Fatal("width change did not establish an exact desired render")
	}
	desiredIdentity := reprojecting.ask.latestDesiredProjection.identity
	if reprojecting.ask.activeProjection == nil ||
		reprojecting.ask.activeProjection.renderedAt.terminalWidth != initialWidth ||
		desiredIdentity.terminalWidth != targetWidth {
		t.Fatal("width change did not retain stale rows while targeting current geometry")
	}

	visible, _ := testVisibleAskPaneContent(reprojecting, targetWidth)
	questionRows := 0
	for _, line := range visible {
		if line.prompt.Kind != askPromptLineKindQuestion {
			continue
		}
		questionRows++
		if got := lipgloss.Width(line.text); got > targetWidth {
			t.Fatalf("stale question row width = %d, want <= %d", got, targetWidth)
		}
		trace := tuitest.TraceTerminalHyperlinks(t, line.text+" plain")
		if last := trace.Fragments[len(trace.Fragments)-1]; last.Link != nil {
			t.Fatalf("content after stale clamped row inherited hyperlink: %+v", last)
		}
	}
	if questionRows == 0 {
		t.Fatal("stale visible projection omitted all question rows")
	}

	next, _ = reprojecting.Update(resizeCommand())
	installed := next.(*uiModel)
	if installed.ask.activeProjection == nil || installed.ask.activeProjection.renderedAt != desiredIdentity {
		t.Fatalf("installed rendered-at identity = %+v, want %+v", installed.ask.activeProjection, desiredIdentity)
	}
}

func TestAskProjectionVisibleWidthReprojectionFailureExits(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{request: request, rows: []string{"rendered"}}
	}
	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?", "yes", "no")})
	pending := next.(*uiModel)
	next, _ = pending.Update(command())
	ready := next.(*uiModel)
	model.questionProjector = nil
	ready.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{request: request, err: errors.New("width render failed")}
	}

	next, resizeCommand := ready.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	reprojecting := next.(*uiModel)
	if reprojecting.ask.activeProjection == nil {
		t.Fatal("visible reprojection discarded the cached projection")
	}
	next, quit := reprojecting.Update(resizeCommand())
	failed := next.(*uiModel)
	if quit == nil || !failed.forcedLocalExit {
		t.Fatal("authoritative visible reprojection failure did not exit")
	}
	if failed.ask.current == nil || failed.ask.activeProjection == nil {
		t.Fatal("visible reprojection failure mutated the unresolved visible prompt")
	}
}
