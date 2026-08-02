package app

import (
	"slices"
	"sync"
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

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
	recommended := 3
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
	recommended := 3
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
	message := clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(prompt))

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
