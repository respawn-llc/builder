package app

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	tuitest "core/internal/testharness/pty"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"
)

func TestAskProjectionAdmissionInitializesPromptWithoutRenderingOnUpdate(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	started := make(chan questionRenderRequest, 1)
	release := make(chan struct{})
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		started <- request
		<-release
		return questionRenderResultMsg{
			request: request,
			rows:    []string{"rendered question"},
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"# Choose carefully",
		"first",
		"second",
	)})
	updated := next.(*uiModel)

	select {
	case <-started:
		t.Fatal("question projector ran during Update")
	default:
	}
	if command == nil {
		t.Fatal("prompt admission did not return a projection command")
	}
	if updated.ask.current == nil {
		t.Fatal("prompt admission did not establish the current FIFO head")
	}
	if updated.ask.currentToken == 0 {
		t.Fatal("prompt admission did not establish a current token")
	}
	if updated.ask.cursor != 0 {
		t.Fatalf("prompt cursor = %d, want 0", updated.ask.cursor)
	}
	if updated.ask.freeform {
		t.Fatal("prompt with options initialized in freeform mode")
	}
	if updated.inputMode() != uiInputModeAsk {
		t.Fatalf("input mode = %q, want ask", updated.inputMode())
	}
	inputState := updated.inputModeState()
	if inputState.ShowsAskInput || inputState.ShowsMainInput {
		t.Fatalf("pending projection exposed input state %+v", inputState)
	}
	projection := updated.layout().inputPaneProjection(64, 20, uiThemeStyles(updated.theme))
	if len(projection.Lines) != 0 || projection.PanelHeight != 0 || projection.Cursor.Visible {
		t.Fatalf("pending projection exposed input pane %+v", projection)
	}

	result := make(chan tea.Msg, 1)
	go func() {
		result <- command()
	}()
	select {
	case request := <-started:
		if request.currentToken != updated.ask.currentToken {
			t.Fatalf("projection current token = %d, want %d", request.currentToken, updated.ask.currentToken)
		}
		if request.operationToken == uuid.Nil {
			t.Fatal("projection operation token is absent")
		}
		if request.identity.questionSource != "# Choose carefully" {
			t.Fatalf("projection question source = %q", request.identity.questionSource)
		}
	case <-time.After(time.Second):
		t.Fatal("projection command did not invoke projector")
	}
	close(release)
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("projection command did not return after projector release")
	}
}

func TestAskProjectionMatchingResultInstallsCompleteProjectionAtomically(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			rows:    []string{"first rendered row", "second rendered row"},
		}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"# Choose carefully",
		"first",
		"second",
	)})
	pending := next.(*uiModel)
	if command == nil {
		t.Fatal("prompt admission did not return a projection command")
	}

	next, _ = pending.Update(command())
	updated := next.(*uiModel)

	if updated.ask.activeProjection == nil {
		t.Fatal("matching render result did not install an active projection")
	}
	if got, want := updated.ask.activeProjection.rows, []string{"first rendered row", "second rendered row"}; !slices.Equal(got, want) {
		t.Fatalf("active projection rows = %q, want %q", got, want)
	}
	if updated.ask.inFlightProjection != nil {
		t.Fatal("matching render result left projection in flight")
	}
	inputState := updated.inputModeState()
	if !inputState.ShowsAskInput || inputState.ShowsMainInput {
		t.Fatalf("installed projection exposed input state %+v", inputState)
	}
	content, _ := testAskPaneContent(updated, 64)
	if len(content) < 2 || content[0].text != "first rendered row" || content[1].text != "second rendered row" {
		t.Fatalf("installed projection was not visible atomically: %+v", content)
	}
}

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

func TestAskProjectionUnrelatedUpdatesDoNotScheduleRendering(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		return questionRenderResultMsg{request: request, rows: []string{"rendered"}}
	}
	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?", "yes", "no")})
	pending := next.(*uiModel)
	next, _ = pending.Update(command())
	ready := next.(*uiModel)

	messages := []tea.Msg{
		tea.WindowSizeMsg{Width: 64, Height: 24},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")},
		tea.KeyMsg{Type: tea.KeyLeft},
		spinnerTickMsg{},
		clearTransientStatusMsg{},
	}
	for _, message := range messages {
		next, _ = ready.Update(message)
		ready = next.(*uiModel)
		if ready.ask.inFlightProjection != nil {
			t.Fatalf("message %T scheduled question rendering", message)
		}
	}
	if renderCount != 1 {
		t.Fatalf("render count = %d, want only the initial projection", renderCount)
	}
}

func TestAskProjectionSchedulerChangedQuestionIsSingleFlightLatestWins(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	started := make(chan questionRenderRequest, 2)
	releaseFirst := make(chan struct{})
	activeRenders := 0
	maxActiveRenders := 0
	var renderMu sync.Mutex
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderMu.Lock()
		activeRenders++
		if activeRenders > maxActiveRenders {
			maxActiveRenders = activeRenders
		}
		renderMu.Unlock()
		started <- request
		if request.questionSource == "Question one" {
			<-releaseFirst
		}
		renderMu.Lock()
		activeRenders--
		renderMu.Unlock()
		return questionRenderResultMsg{request: request, rows: []string{request.questionSource}}
	}

	next, firstCommand := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question one", "one")})
	pending := next.(*uiModel)
	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstCommand()
	}()
	firstRequest := <-started
	if firstRequest.questionSource != "Question one" {
		t.Fatalf("first render source = %q", firstRequest.questionSource)
	}

	next, replacementCommand := pending.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question two", "two")})
	coalescing := next.(*uiModel)
	if replacementCommand != nil {
		t.Fatal("changed question started a second render while the first was in flight")
	}
	if coalescing.ask.latestDesiredProjection == nil ||
		coalescing.ask.latestDesiredProjection.identity.questionSource != "Question two" {
		t.Fatal("changed question was not retained as the latest desired candidate")
	}
	close(releaseFirst)
	firstMessage := <-firstResult

	next, secondCommand := coalescing.Update(firstMessage)
	afterFirst := next.(*uiModel)
	if afterFirst.ask.activeProjection != nil {
		t.Fatal("obsolete first render installed")
	}
	if secondCommand == nil {
		t.Fatal("obsolete first result did not start the latest desired render")
	}
	secondMessage := secondCommand()
	secondRequest := <-started
	if secondRequest.questionSource != "Question two" {
		t.Fatalf("second render source = %q", secondRequest.questionSource)
	}
	next, _ = afterFirst.Update(secondMessage)
	updated := next.(*uiModel)
	if updated.ask.activeProjection == nil ||
		!slices.Equal(updated.ask.activeProjection.rows, []string{"Question two"}) {
		t.Fatalf("latest projection = %+v", updated.ask.activeProjection)
	}
	if maxActiveRenders != 1 {
		t.Fatalf("maximum concurrent renders = %d, want 1", maxActiveRenders)
	}
}

func TestAskProjectionSchedulerSameIdentityUsesLatestCompleteCandidate(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	started := make(chan struct{})
	release := make(chan struct{})
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		close(started)
		<-release
		return questionRenderResultMsg{request: request, rows: []string{"rendered"}}
	}

	next, command := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"Same question",
		"one",
		"two",
	)})
	pending := next.(*uiModel)
	result := make(chan tea.Msg, 1)
	go func() {
		result <- command()
	}()
	<-started

	pending.ask.cursor = 1
	pending.ask.freeform = true
	testSetAskInputAtRuneCursor(pending, "draft", 2)
	replacement := testQuestionAskEvent(
		"ask-1",
		"Same question",
		"new one",
		"new two",
		"new three",
	)
	recommended := 2
	replacement.prompt.RecommendedOptionIndex = &recommended
	next, replacementCommand := pending.Update(askEventMsg{event: replacement})
	coalescing := next.(*uiModel)
	if replacementCommand != nil {
		t.Fatal("same-identity replacement started a second renderer")
	}
	if !slices.Equal(coalescing.ask.current.prompt.Suggestions, replacement.prompt.Suggestions) {
		t.Fatal("same-identity replacement did not update the current payload immediately")
	}
	close(release)

	next, followUpCommand := coalescing.Update(<-result)
	updated := next.(*uiModel)
	if followUpCommand != nil {
		t.Fatal("same-identity result scheduled an unnecessary second renderer")
	}
	if renderCount != 1 {
		t.Fatalf("render count = %d, want 1", renderCount)
	}
	if !slices.Equal(updated.ask.current.prompt.Suggestions, replacement.prompt.Suggestions) ||
		updated.ask.current.prompt.RecommendedOptionIndex == nil ||
		*updated.ask.current.prompt.RecommendedOptionIndex != recommended {
		t.Fatalf("installed candidate = %+v, want latest complete payload", updated.ask.current.prompt)
	}
	if updated.ask.cursor != 1 || !updated.ask.freeform ||
		updated.ask.editor.Text() != "draft" || updated.ask.editor.Cursor() != 2 {
		t.Fatalf(
			"prompt-local state changed: cursor=%d freeform=%t editor=%q/%d",
			updated.ask.cursor,
			updated.ask.freeform,
			updated.ask.editor.Text(),
			updated.ask.editor.Cursor(),
		)
	}
}

func TestAskProjectionSameIDActiveReplacementPreservesPromptLocalState(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	initial := testQuestionAskEvent("ask-1", "Same question", "one", "two")
	testSetActiveAsk(model, &initial)
	model.ask.cursor = 1
	model.ask.freeform = true
	testSetAskInputAtRuneCursor(model, "draft text", 5)
	currentToken := model.ask.currentToken
	activeProjection := model.ask.activeProjection

	replacement := testQuestionAskEvent("ask-1", "Same question", "new one", "new two", "new three")
	recommended := 2
	replacement.prompt.RecommendedOptionIndex = &recommended
	next, command := model.Update(askEventMsg{event: replacement})
	updated := next.(*uiModel)

	if command != nil {
		t.Fatal("same-identity active replacement scheduled redundant projection")
	}
	if updated.ask.currentToken != currentToken || updated.ask.activeProjection != activeProjection {
		t.Fatal("same-ID replacement changed current or projection ownership")
	}
	if !slices.Equal(updated.ask.current.prompt.Suggestions, replacement.prompt.Suggestions) ||
		updated.ask.current.prompt.RecommendedOptionIndex == nil ||
		*updated.ask.current.prompt.RecommendedOptionIndex != recommended {
		t.Fatalf("same-ID replacement did not install the latest payload immediately: %+v", updated.ask.current.prompt)
	}
	if updated.ask.cursor != 1 || !updated.ask.freeform ||
		updated.ask.editor.Text() != "draft text" || updated.ask.editor.Cursor() != 5 {
		t.Fatalf(
			"same-ID replacement changed prompt-local state: cursor=%d freeform=%t editor=%q/%d",
			updated.ask.cursor,
			updated.ask.freeform,
			updated.ask.editor.Text(),
			updated.ask.editor.Cursor(),
		)
	}
}

func TestAskProjectionSchedulerProjectsQueuedPromptOnlyAfterPromotion(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	started := make(chan questionRenderRequest, 2)
	releaseFirst := make(chan struct{})
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		started <- request
		if request.questionSource == "Question A" {
			<-releaseFirst
		}
		return questionRenderResultMsg{request: request, rows: []string{request.questionSource}}
	}

	next, firstCommand := model.Update(askEventMsg{event: testQuestionAskEvent("ask-a", "Question A", "a")})
	pendingA := next.(*uiModel)
	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstCommand()
	}()
	<-started

	next, queuedCommand := pendingA.Update(askEventMsg{event: testQuestionAskEvent("ask-b", "Question B", "b")})
	withQueuedB := next.(*uiModel)
	if queuedCommand != nil {
		t.Fatal("queued prompt started projection before promotion")
	}
	select {
	case request := <-started:
		t.Fatalf("queued prompt projected early: %+v", request)
	default:
	}

	next, promotionCommand := withQueuedB.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-a"}})
	promotedB := next.(*uiModel)
	if promotionCommand != nil {
		t.Fatal("promotion started a second render while A was in flight")
	}
	if promotedB.ask.current == nil || promotedB.ask.current.prompt.PromptID != "ask-b" {
		t.Fatal("existing FIFO promotion did not make B current")
	}
	close(releaseFirst)

	next, secondCommand := promotedB.Update(<-firstResult)
	afterStaleA := next.(*uiModel)
	if afterStaleA.ask.activeProjection != nil {
		t.Fatal("stale A projection installed after B promotion")
	}
	if secondCommand == nil {
		t.Fatal("stale A result did not start promoted B projection")
	}
	secondResult := secondCommand()
	secondRequest := <-started
	if secondRequest.questionSource != "Question B" {
		t.Fatalf("promoted render source = %q, want Question B", secondRequest.questionSource)
	}
	next, _ = afterStaleA.Update(secondResult)
	updated := next.(*uiModel)
	if updated.ask.activeProjection == nil ||
		!slices.Equal(updated.ask.activeProjection.rows, []string{"Question B"}) {
		t.Fatalf("promoted B projection = %+v", updated.ask.activeProjection)
	}
}

func TestPromptProjectionSchedulerLiveAdmissionReturnsProjectionCommand(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		return questionRenderResultMsg{request: request, rows: []string{"rendered"}}
	}
	prompt := testQuestionPrompt("ask-1", "Live question", "yes")
	message := clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessagePromptPending,
		Payload: clientui.TranscriptPayload{
			PromptPending: &prompt,
		},
	}

	command := model.applyAdmittedTranscriptMessageState(message, runtimeTupleMergeResult{})
	if command == nil {
		t.Fatal("live prompt admission dropped the projection command")
	}
	if renderCount != 0 {
		t.Fatal("live prompt admission rendered Markdown on the UI thread")
	}
	next, _ := model.Update(command())
	updated := next.(*uiModel)
	if renderCount != 1 || updated.ask.activeProjection == nil {
		t.Fatal("live prompt projection did not install through its command result")
	}
}

func TestPromptProjectionSchedulerHydrationPromotesAfterStaleRender(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	started := make(chan questionRenderRequest, 2)
	releaseFirst := make(chan struct{})
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		started <- request
		if request.questionSource == "Question A" {
			<-releaseFirst
		}
		return questionRenderResultMsg{request: request, rows: []string{request.questionSource}}
	}

	next, firstCommand := model.Update(askEventMsg{event: testQuestionAskEvent("ask-a", "Question A", "a")})
	pendingA := next.(*uiModel)
	firstResult := make(chan tea.Msg, 1)
	go func() {
		firstResult <- firstCommand()
	}()
	<-started

	promptB := testQuestionPrompt("ask-b", "Question B", "b")
	hydrationCommand := pendingA.reconcileTranscriptPrompts([]clientui.TranscriptPrompt{promptB})
	if hydrationCommand != nil {
		t.Fatal("hydration started B while A projection remained in flight")
	}
	if pendingA.ask.current == nil || pendingA.ask.current.prompt.PromptID != "ask-b" {
		t.Fatal("hydration did not remove A and promote B through existing ownership")
	}
	close(releaseFirst)

	next, secondCommand := pendingA.Update(<-firstResult)
	afterStaleA := next.(*uiModel)
	if afterStaleA.ask.activeProjection != nil || secondCommand == nil {
		t.Fatal("stale hydration render installed or failed to schedule B")
	}
	secondResult := secondCommand()
	<-started
	next, _ = afterStaleA.Update(secondResult)
	readyB := next.(*uiModel)
	if readyB.ask.activeProjection == nil ||
		!slices.Equal(readyB.ask.activeProjection.rows, []string{"Question B"}) {
		t.Fatalf("hydrated B projection = %+v", readyB.ask.activeProjection)
	}

	readyB.ask.cursor = 0
	readyB.ask.freeform = true
	testSetAskInput(readyB, "draft")
	refreshedB := cloneTranscriptPromptForAsk(promptB)
	refreshedB.Suggestions = []string{"new b", "other"}
	refreshCommand := readyB.reconcileTranscriptPrompts([]clientui.TranscriptPrompt{refreshedB})
	if refreshCommand != nil {
		t.Fatal("same-question hydration refresh scheduled redundant projection")
	}
	if !slices.Equal(readyB.ask.current.prompt.Suggestions, refreshedB.Suggestions) ||
		!readyB.ask.freeform || readyB.ask.editor.Text() != "draft" {
		t.Fatal("hydration refresh did not preserve prompt-local state with latest controls")
	}
}

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

func TestAskProjectionSubsequentPromptStartsWithCleanViewportAndEditorState(t *testing.T) {
	const (
		width  = 32
		height = 9
	)
	model := sizedTestUIModel(newProjectedStaticUIModel(), width, height)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		rows := []string{request.questionSource + " rendered"}
		if request.questionSource == "First question" {
			rows = append(rows,
				"first overflow row two",
				"first overflow row three",
				"first overflow row four",
				"first overflow row five",
			)
		}
		return questionRenderResultMsg{request: request, rows: rows}
	}

	next, firstCommand := model.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-1",
		"First question",
		"First",
		"Second",
	)})
	firstPending := next.(*uiModel)
	next, _ = firstPending.Update(firstCommand())
	firstReady := next.(*uiModel)
	next, _ = firstReady.Update(tea.KeyMsg{Type: tea.KeyTab})
	firstReady = next.(*uiModel)
	next, _ = firstReady.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("old draft")})
	firstReady = next.(*uiModel)
	next, _ = firstReady.Update(tea.KeyMsg{Type: tea.KeyLeft})
	firstReady = next.(*uiModel)

	next, _ = firstReady.Update(askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
	resolved := next.(*uiModel)
	next, secondCommand := resolved.Update(askEventMsg{event: testQuestionAskEvent(
		"ask-2",
		"Second question",
		"Only",
	)})
	secondPending := next.(*uiModel)
	if secondCommand == nil {
		t.Fatal("subsequent prompt did not schedule its own projection")
	}
	if secondPending.ask.activeProjection != nil {
		t.Fatal("subsequent pending prompt inherited the previous active projection")
	}
	if secondPending.ask.freeform || secondPending.ask.cursor != 0 ||
		secondPending.ask.editor.Text() != "" || secondPending.ask.editor.Cursor() != 0 {
		t.Fatalf(
			"subsequent pending prompt inherited local state: cursor=%d freeform=%t editor=%q/%d",
			secondPending.ask.cursor,
			secondPending.ask.freeform,
			secondPending.ask.editor.Text(),
			secondPending.ask.editor.Cursor(),
		)
	}
	if pane := secondPending.layout().inputPaneProjection(width, height, uiThemeStyles(secondPending.theme)); len(pane.Lines) != 0 {
		t.Fatalf("subsequent pending prompt exposed previous viewport rows: %d", len(pane.Lines))
	}

	next, _ = secondPending.Update(secondCommand())
	secondReady := next.(*uiModel)
	visible, cursor := testVisibleAskPaneContent(secondReady, width)
	if cursor != nil {
		t.Fatalf("subsequent picker unexpectedly exposed an editor cursor: %v", cursor)
	}
	questionRows := make([]string, 0)
	for _, line := range visible {
		if line.prompt.Kind == askPromptLineKindQuestion {
			questionRows = append(questionRows, xansi.Strip(line.text))
		}
	}
	if got, want := questionRows, []string{"Second question rendered"}; !slices.Equal(got, want) {
		t.Fatalf("subsequent question rows = %q, want %q", got, want)
	}
	if len(visible) > inputContentLineLimit(height) {
		t.Fatalf("subsequent viewport rows = %d, content budget = %d", len(visible), inputContentLineLimit(height))
	}
}

func projectionFailureFinalModel(t *testing.T) *uiModel {
	t.Helper()
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{
			request: request,
			err:     errors.New("render failed"),
		}
	}
	next, command := model.Update(askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
	pending := next.(*uiModel)
	next, _ = pending.Update(command())
	failed := next.(*uiModel)
	if !failed.forcedLocalExit {
		t.Fatal("projection failure helper did not produce a forced local exit")
	}
	return failed
}
