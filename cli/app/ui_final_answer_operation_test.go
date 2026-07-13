package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type blockingClipboardCopier struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	writes  []string
	active  int
	max     int
}

func (c *blockingClipboardCopier) CopyText(ctx context.Context, text string) error {
	c.mu.Lock()
	c.active++
	if c.active > c.max {
		c.max = c.active
	}
	c.mu.Unlock()
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	c.active--
	c.writes = append(c.writes, text)
	c.mu.Unlock()
	return nil
}

func TestCopyFinalAnswerOperationBlocksInputUntilClipboardCompletes(t *testing.T) {
	answer := "exact durable answer"
	views := &countingSessionViewClient{finalAnswer: &answer}
	copier := &stubClipboardTextCopier{}
	m := newProjectedStaticUIModel(WithUISessionID("session-1"), WithUIClipboardTextCopier(copier))
	m.statusConfig.SessionViews = views

	_, cmd := m.inputController().handleCopyCommand()
	if cmd == nil || m.finalAnswerOperation == nil {
		t.Fatal("expected final-answer lookup operation")
	}
	op := *m.finalAnswerOperation
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("blocked")})
	m = next.(*uiModel)
	if m.input != "" {
		t.Fatalf("input = %q, want blocked input", m.input)
	}

	next, clipboardCmd := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, answer: &answer})
	m = next.(*uiModel)
	if clipboardCmd == nil || m.finalAnswerOperation == nil || m.finalAnswerOperation.phase != uiFinalAnswerOperationClipboard {
		t.Fatal("expected exclusive clipboard phase")
	}
	next, _ = m.Update(clipboardCmd())
	m = next.(*uiModel)
	if m.finalAnswerOperation != nil {
		t.Fatal("expected clipboard completion to release operation")
	}
	if copier.text != answer || copier.calls != 1 {
		t.Fatalf("clipboard = %q calls=%d, want exact one copy", copier.text, copier.calls)
	}
}

func TestFinalAnswerLookupTimeoutAndStaleResultDoNotNavigate(t *testing.T) {
	m := newProjectedStaticUIModel(WithUISessionID("child-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}
	m.input = "editable child draft"
	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationBack, "parent-1")
	op := *m.finalAnswerOperation

	next, _ := m.Update(latestFinalAnswerTimeoutMsg{token: op.token})
	m = next.(*uiModel)
	if m.finalAnswerOperation != nil {
		t.Fatal("timeout must restore input ownership")
	}
	if m.input != "editable child draft" {
		t.Fatalf("timeout child input = %q, want preserved draft", m.input)
	}
	answer := "late answer"
	next, _ = m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, answer: &answer})
	m = next.(*uiModel)
	if m.exitAction != UIActionNone {
		t.Fatalf("stale result transitioned with %q", m.exitAction)
	}
	next, editCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(*uiModel)
	if editCmd != nil || m.input != "editable child draftx" {
		t.Fatalf("child input after timeout edit = %q cmd=%v, want editable draft", m.input, editCmd)
	}
}

func TestFinalAnswerOperationRejectsEveryStaleLookupCorrelation(t *testing.T) {
	answer := "answer"
	m := newProjectedStaticUIModel(WithUISessionID("child-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}
	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationBack, "parent-1")
	op := *m.finalAnswerOperation

	stale := []latestFinalAnswerDoneMsg{
		{token: op.token + 1, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, answer: &answer},
		{token: op.token, purpose: uiFinalAnswerOperationCopy, sessionID: op.sessionID, parentSessionID: op.parentSessionID, answer: &answer},
		{token: op.token, purpose: op.purpose, sessionID: "other-session", parentSessionID: op.parentSessionID, answer: &answer},
		{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: "other-parent", answer: &answer},
	}
	for _, msg := range stale {
		next, _ := m.Update(msg)
		m = next.(*uiModel)
		if m.finalAnswerOperation == nil || m.exitAction != UIActionNone || m.transientStatus != "" {
			t.Fatalf("stale result mutated model: msg=%+v op=%+v transition=%+v status=%q", msg, m.finalAnswerOperation, m.Transition(), m.transientStatus)
		}
	}
}

func TestBackFinalAnswerOperationOpensParentWithExactPrefill(t *testing.T) {
	answer := "child final"
	m := newProjectedStaticUIModel(WithUISessionID("child-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}
	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationBack, "parent-1")
	op := *m.finalAnswerOperation

	next, cmd := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, answer: &answer})
	m = next.(*uiModel)
	if cmd == nil || m.exitAction != UIActionOpenSession || m.nextSessionID != "parent-1" || m.nextSessionInitialInput != answer {
		t.Fatalf("transition = %+v", m.Transition())
	}
}

func TestBackFinalAnswerOperationAbsenceOpensParentWithEmptyPrefill(t *testing.T) {
	m := newProjectedStaticUIModel(WithUISessionID("child-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}
	m.nextSessionInitialInput = "must not survive"
	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationBack, "parent-1")
	op := *m.finalAnswerOperation

	next, cmd := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID})
	m = next.(*uiModel)
	if cmd == nil || m.finalAnswerOperation != nil || m.exitAction != UIActionOpenSession || m.nextSessionID != "parent-1" || m.nextSessionInitialInput != "" {
		t.Fatalf("absence transition = %+v", m.Transition())
	}
}

func TestBackCommandMissingParentPreservesEditableChildWithoutLookupOrTransition(t *testing.T) {
	m := newProjectedStaticUIModel(WithUISessionID("child-1"))
	m.input = "editable child draft"

	next, errorCmd := m.inputController().handleBackCommand()
	m = next.(*uiModel)
	if errorCmd == nil || m.transientStatus == "" {
		t.Fatal("missing parent did not surface an operator-visible error")
	}
	if m.finalAnswerOperation != nil {
		t.Fatalf("missing parent started final-answer operation %+v", m.finalAnswerOperation)
	}
	if transition := m.Transition(); transition.Action != UIActionNone || transition.Exit {
		t.Fatalf("missing parent emitted transition %+v", transition)
	}
	if m.input != "editable child draft" {
		t.Fatalf("child input = %q, want preserved draft", m.input)
	}

	next, editCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(*uiModel)
	if editCmd != nil {
		t.Fatal("normal child edit after missing parent created a command")
	}
	if m.input != "editable child draftx" {
		t.Fatalf("edited child input = %q, want editable draft", m.input)
	}
}

func TestFinalAnswerLookupErrorRestoresInputWithoutNavigation(t *testing.T) {
	lookupErr := errors.New("durable lookup failed")
	m := newProjectedStaticUIModel(WithUISessionID("child-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}
	m.input = "editable child draft"
	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationBack, "parent-1")
	op := *m.finalAnswerOperation

	next, _ := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, parentSessionID: op.parentSessionID, err: lookupErr})
	m = next.(*uiModel)
	if m.finalAnswerOperation != nil || m.exitAction != UIActionNone || m.transientStatus == "" {
		t.Fatalf("lookup failure state op=%+v transition=%+v status=%q", m.finalAnswerOperation, m.Transition(), m.transientStatus)
	}
	if m.input != "editable child draft" {
		t.Fatalf("lookup failure child input = %q, want preserved draft", m.input)
	}
	next, editCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(*uiModel)
	if editCmd != nil || m.input != "editable child draftx" {
		t.Fatalf("child input after lookup failure edit = %q cmd=%v, want editable draft", m.input, editCmd)
	}
}

func TestFinalAnswerOperationRejectsWrongCorrelationAndBlocksNavigation(t *testing.T) {
	answer := "answer"
	m := newProjectedStaticUIModel(WithUISessionID("session-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}
	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
	op := *m.finalAnswerOperation

	next, _ := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: uiFinalAnswerOperationBack, sessionID: op.sessionID, parentSessionID: "parent-1", answer: &answer})
	m = next.(*uiModel)
	if m.finalAnswerOperation == nil || m.exitAction != UIActionNone {
		t.Fatalf("wrong-purpose result mutated state: op=%+v transition=%+v", m.finalAnswerOperation, m.Transition())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(*uiModel)
	if m.exitAction != UIActionNone {
		t.Fatalf("Ctrl+C escaped owned operation: %q", m.exitAction)
	}
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("/new")},
	} {
		next, _ = m.Update(key)
		m = next.(*uiModel)
		if m.input != "" || m.exitAction != UIActionNone || m.finalAnswerOperation == nil {
			t.Fatalf("key escaped owned operation: key=%+v input=%q transition=%+v op=%+v", key, m.input, m.Transition(), m.finalAnswerOperation)
		}
	}
}

func TestClipboardOwnershipSerializesCopies(t *testing.T) {
	answerA := "A"
	answerB := "B"
	copier := &blockingClipboardCopier{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	m := newProjectedStaticUIModel(WithUISessionID("session-1"), WithUIClipboardTextCopier(copier))
	m.statusConfig.SessionViews = &countingSessionViewClient{}

	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
	opA := *m.finalAnswerOperation
	next, cmdA := m.Update(latestFinalAnswerDoneMsg{token: opA.token, purpose: opA.purpose, sessionID: opA.sessionID, answer: &answerA})
	m = next.(*uiModel)
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- cmdA() }()
	<-copier.started

	if cmd := m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, ""); cmd != nil {
		t.Fatal("second copy started while clipboard ownership was active")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*uiModel)
	if m.exitAction != UIActionNone {
		t.Fatalf("navigation escaped clipboard ownership: %q", m.exitAction)
	}
	copier.release <- struct{}{}
	next, _ = m.Update(<-doneA)
	m = next.(*uiModel)
	if m.finalAnswerOperation != nil {
		t.Fatal("first clipboard completion did not release ownership")
	}

	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
	opB := *m.finalAnswerOperation
	next, cmdB := m.Update(latestFinalAnswerDoneMsg{token: opB.token, purpose: opB.purpose, sessionID: opB.sessionID, answer: &answerB})
	m = next.(*uiModel)
	doneB := make(chan tea.Msg, 1)
	go func() { doneB <- cmdB() }()
	<-copier.started
	next, _ = m.Update(clipboardTextCopyDoneMsg{operationToken: &opA.token})
	m = next.(*uiModel)
	if m.finalAnswerOperation == nil || m.finalAnswerOperation.token != opB.token {
		t.Fatal("stale clipboard completion released newer ownership")
	}
	copier.release <- struct{}{}
	next, _ = m.Update(<-doneB)
	m = next.(*uiModel)

	copier.mu.Lock()
	defer copier.mu.Unlock()
	if copier.max != 1 || len(copier.writes) != 2 || copier.writes[0] != answerA || copier.writes[1] != answerB {
		t.Fatalf("clipboard writes=%v max=%d", copier.writes, copier.max)
	}
}

func TestFinalAnswerOperationRejectsDuplicateStartsAndStaleLookupAfterRetry(t *testing.T) {
	answerA := "A"
	answerB := "B"
	m := newProjectedStaticUIModel(WithUISessionID("session-1"))
	m.statusConfig.SessionViews = &countingSessionViewClient{}

	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
	opA := *m.finalAnswerOperation
	if cmd := m.startFinalAnswerOperation(uiFinalAnswerOperationBack, "parent-1"); cmd != nil {
		t.Fatal("back lookup started while copy lookup was owned")
	}
	if _, cmd := m.inputController().handleCopyCommand(); cmd != nil {
		t.Fatal("duplicate copy lookup started while copy lookup was owned")
	}
	next, _ := m.Update(latestFinalAnswerTimeoutMsg{token: opA.token})
	m = next.(*uiModel)

	_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
	opB := *m.finalAnswerOperation
	next, _ = m.Update(latestFinalAnswerDoneMsg{token: opA.token, purpose: opA.purpose, sessionID: opA.sessionID, answer: &answerA})
	m = next.(*uiModel)
	if m.finalAnswerOperation == nil || m.finalAnswerOperation.token != opB.token {
		t.Fatalf("stale A lookup cleared B ownership: op=%+v", m.finalAnswerOperation)
	}
	next, clipboardCmd := m.Update(latestFinalAnswerDoneMsg{token: opB.token, purpose: opB.purpose, sessionID: opB.sessionID, answer: &answerB})
	m = next.(*uiModel)
	if clipboardCmd == nil || m.finalAnswerOperation == nil || m.finalAnswerOperation.token != opB.token {
		t.Fatalf("current B lookup did not start clipboard phase: op=%+v", m.finalAnswerOperation)
	}
}

func TestClipboardFailureAndTimeoutReleaseOwnershipOnlyWhenCopyReturns(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		answer := "answer"
		copyErr := errors.New("clipboard failed")
		m := newProjectedStaticUIModel(WithUISessionID("session-1"), WithUIClipboardTextCopier(&stubClipboardTextCopier{err: copyErr}))
		m.statusConfig.SessionViews = &countingSessionViewClient{}
		_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
		op := *m.finalAnswerOperation
		next, clipboardCmd := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, answer: &answer})
		m = next.(*uiModel)
		if m.finalAnswerOperation == nil {
			t.Fatal("clipboard failure cleared ownership before the command ran")
		}
		next, _ = m.Update(clipboardCmd())
		m = next.(*uiModel)
		if m.finalAnswerOperation != nil || m.transientStatus == "" {
			t.Fatalf("clipboard failure state op=%+v status=%q", m.finalAnswerOperation, m.transientStatus)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		previousTimeout := clipboardTextCopyTimeout
		clipboardTextCopyTimeout = time.Millisecond
		t.Cleanup(func() { clipboardTextCopyTimeout = previousTimeout })

		answer := "answer"
		copier := &blockingClipboardCopier{started: make(chan struct{}, 1), release: make(chan struct{})}
		m := newProjectedStaticUIModel(WithUISessionID("session-1"), WithUIClipboardTextCopier(copier))
		m.statusConfig.SessionViews = &countingSessionViewClient{}
		_ = m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
		op := *m.finalAnswerOperation
		next, clipboardCmd := m.Update(latestFinalAnswerDoneMsg{token: op.token, purpose: op.purpose, sessionID: op.sessionID, answer: &answer})
		m = next.(*uiModel)
		done := make(chan tea.Msg, 1)
		go func() { done <- clipboardCmd() }()
		<-copier.started
		if m.finalAnswerOperation == nil {
			t.Fatal("clipboard timeout released ownership before CopyText returned")
		}
		next, _ = m.Update(<-done)
		m = next.(*uiModel)
		if m.finalAnswerOperation != nil || m.transientStatus == "" {
			t.Fatalf("clipboard timeout state op=%+v status=%q", m.finalAnswerOperation, m.transientStatus)
		}
	})
}
