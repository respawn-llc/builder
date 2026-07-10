package app

import (
	"errors"
	"testing"
	"time"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailTranscriptRequestIsSingleFlight(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		WithUISessionID("session-1"),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.detailTranscript.replace(clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(25),
		HasMoreAbove: true,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "current page"}},
	})

	next, firstCmd := model.Update(tui.RequestDetailTranscriptPageMsg{Direction: tui.DetailTranscriptPageOlder})
	model = next.(*uiModel)
	if firstCmd == nil {
		t.Fatal("first adjacent-page request did not start")
	}
	firstDone := make(chan any, 1)
	go func() {
		firstDone <- firstCmd()
	}()
	defer func() {
		sessionViews.results <- controlledTranscriptPageResult{}
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Fatal("first adjacent-page request did not finish")
		}
	}()

	select {
	case <-sessionViews.started:
	case <-time.After(time.Second):
		t.Fatal("first adjacent-page request did not reach the session view client")
	}

	next, repeatedCmd := model.Update(tui.RequestDetailTranscriptPageMsg{Direction: tui.DetailTranscriptPageOlder})
	_ = next.(*uiModel)
	if repeatedCmd != nil {
		t.Fatal("repeated adjacent-page input started a second request while the first was pending")
	}
}

func TestDetailTranscriptEdgeInputsShareSingleFlightRequest(t *testing.T) {
	inputs := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "alternate scroll key", msg: tea.KeyMsg{Type: tea.KeyDown}},
		{name: "raw wheel", msg: tea.MouseMsg{Button: tea.MouseButtonWheelDown}},
	}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			sessionViews := newControlledTranscriptPageClient()
			model := newDetailTranscriptRequestTestModel(sessionViews)
			model.detailTranscript.replace(clientui.TranscriptPage{
				SessionID:    "session-1",
				NewerCursor:  appInt64Ptr(25),
				HasMoreBelow: true,
				Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "current page"}},
			})
			model.view = tui.NewModel()
			model.forwardToView(tui.SetViewportSizeMsg{Lines: 2, Width: 80})
			model.forwardToView(tui.SetDetailTranscriptPageMsg{Page: model.detailTranscript.page()})
			model.view = mustUpdateTUIModel(t, model.view, tui.SetModeMsg{Mode: tui.ModeDetail})

			model, firstCmd := startDetailTranscriptRequestFromInput(t, model, input.msg)
			firstDone := runDetailTranscriptCommand(firstCmd)
			waitForDetailTranscriptRequest(t, sessionViews)

			model, repeatedCmd := startDetailTranscriptRequestFromInput(t, model, input.msg)
			if repeatedCmd != nil {
				t.Fatal("repeated edge input started a second request while the first was pending")
			}
			sessionViews.results <- controlledTranscriptPageResult{err: errors.New("stop test request")}
			_ = waitForDetailTranscriptCompletion(t, firstDone)
		})
	}
}

func TestDetailTranscriptMatchingSuccessAllowsNextRequest(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)

	model, firstCmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	firstDone := runDetailTranscriptCommand(firstCmd)
	waitForDetailTranscriptRequest(t, sessionViews)
	sessionViews.results <- controlledTranscriptPageResult{response: serverapi.SessionTranscriptPageResponse{
		Transcript: clientui.TranscriptPage{
			SessionID:    "session-1",
			OlderCursor:  appInt64Ptr(10),
			HasMoreAbove: true,
			NewerCursor:  appInt64Ptr(25),
			HasMoreBelow: true,
			Entries:      []clientui.ChatEntry{{Role: "user", Text: "older page"}},
		},
	}}
	model = updateUIModel(t, model, waitForDetailTranscriptCompletion(t, firstDone))
	if model.transientStatus != "" || model.transientStatusRequestID != nil {
		t.Fatalf("matching success retained loading notice: text=%q request=%v", model.transientStatus, model.transientStatusRequestID)
	}

	model, secondCmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	if secondCmd == nil {
		t.Fatal("matching success did not permit the next adjacent-page request")
	}
	secondDone := runDetailTranscriptCommand(secondCmd)
	waitForDetailTranscriptRequest(t, sessionViews)
	sessionViews.results <- controlledTranscriptPageResult{err: errors.New("stop test request")}
	_ = waitForDetailTranscriptCompletion(t, secondDone)
}

func TestDetailTranscriptMatchingErrorAllowsNextRequest(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)

	model, firstCmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	firstDone := runDetailTranscriptCommand(firstCmd)
	waitForDetailTranscriptRequest(t, sessionViews)
	sessionViews.results <- controlledTranscriptPageResult{err: errors.New("page read failed")}
	model = updateUIModel(t, model, waitForDetailTranscriptCompletion(t, firstDone))
	if model.transientStatusKind != uiStatusNoticeError {
		t.Fatalf("matching error notice kind = %v, want error", model.transientStatusKind)
	}
	if model.transientStatusRequestID != nil {
		t.Fatalf("matching error retained loading request identity %v", *model.transientStatusRequestID)
	}

	_, secondCmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	if secondCmd == nil {
		t.Fatal("matching error did not permit the next adjacent-page request")
	}
	secondDone := runDetailTranscriptCommand(secondCmd)
	waitForDetailTranscriptRequest(t, sessionViews)
	sessionViews.results <- controlledTranscriptPageResult{err: errors.New("stop test request")}
	_ = waitForDetailTranscriptCompletion(t, secondDone)
}

func TestDetailTranscriptLoadingNoticeKeepsExpansionActionIndependent(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)
	model.view = tui.NewModel()
	model.forwardToView(tui.SetViewportSizeMsg{Lines: 4, Width: 80})
	model.forwardToView(tui.SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		SessionID: "session-1",
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "line one\nline two\nline three\nline four",
		}},
	}})
	model.view = mustUpdateTUIModel(t, model.view, tui.SetModeMsg{Mode: tui.ModeDetail})
	if action := model.view.DetailSelectionAction(); action != tui.DetailSelectionActionExpand {
		t.Fatalf("detail action before loading = %v, want expand", action)
	}

	model, cmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	if cmd == nil {
		t.Fatal("adjacent-page request did not start")
	}
	defer model.Close()
	if model.transientStatusKind != uiStatusNoticeInfo || model.transientStatusRequestID == nil {
		t.Fatalf("loading notice = kind:%v request:%v, want request-scoped info", model.transientStatusKind, model.transientStatusRequestID)
	}
	if version := model.transientStatusRequestID.Version(); version != 4 {
		t.Fatalf("loading request UUID version = %d, want 4", version)
	}
	if action := model.view.DetailSelectionAction(); action != tui.DetailSelectionActionExpand {
		t.Fatalf("detail action while loading = %v, want independent expand action", action)
	}
}

func TestDetailTranscriptMatchingSuccessClearsOnlyItsLoadingNotice(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)

	model, cmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	done := runDetailTranscriptCommand(cmd)
	waitForDetailTranscriptRequest(t, sessionViews)
	overtakingNotice := "newer warning"
	model.sendTransientStatusWithNoticeID(overtakingNotice, uiStatusNoticeWarning, transientStatusDuration, uiStatusNoticeReplace, "")
	sessionViews.results <- controlledTranscriptPageResult{response: serverapi.SessionTranscriptPageResponse{
		Transcript: clientui.TranscriptPage{
			SessionID:   "session-1",
			NewerCursor: appInt64Ptr(25),
			Entries:     []clientui.ChatEntry{{Role: "user", Text: "older page"}},
		},
	}}
	model = updateUIModel(t, model, waitForDetailTranscriptCompletion(t, done))

	if model.transientStatus != overtakingNotice || model.transientStatusKind != uiStatusNoticeWarning {
		t.Fatalf("notice after matching success = %q kind %v, want overtaking warning", model.transientStatus, model.transientStatusKind)
	}
	if model.transientStatusRequestID != nil {
		t.Fatalf("overtaking notice retained request identity %v", *model.transientStatusRequestID)
	}
}

func TestDetailTranscriptCloseCancelsAndInvalidatesPendingRequest(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)
	before := model.detailTranscript.page()

	model, cmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	done := runDetailTranscriptCommand(cmd)
	waitForDetailTranscriptRequest(t, sessionViews)
	model.Close()
	completion := waitForDetailTranscriptCompletion(t, done)
	model = updateUIModel(t, model, completion)

	if !sameChatEntries(model.detailTranscript.page().Entries, before.Entries) {
		t.Fatalf("late completion after Close mutated detail membership: %#v", model.detailTranscript.page().Entries)
	}
	if model.transientStatusRequestID != nil || model.transientStatusKind == uiStatusNoticeError {
		t.Fatalf("late completion after Close mutated notice state: request=%v kind=%v", model.transientStatusRequestID, model.transientStatusKind)
	}
	_, nextCmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	if nextCmd == nil {
		t.Fatal("Close did not invalidate pending request state")
	}
}

func TestDetailTranscriptVisibleSessionReplacementCancelsOldRequestAndHydratesNewTarget(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)
	model.view = tui.NewModel()
	model.forwardToView(tui.SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		SessionID: "session-1",
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "line one\nline two\nline three\nline four",
		}},
	}})
	model.view = mustUpdateTUIModel(t, model.view, tui.SetModeMsg{Mode: tui.ModeDetail})

	model, oldCmd := updateDetailTranscriptRequest(t, model, tui.DetailTranscriptPageOlder)
	oldDone := runDetailTranscriptCommand(oldCmd)
	waitForDetailTranscriptRequest(t, sessionViews)

	newCmd := model.runtimeAdapter().applyProjectedSessionMetadata(clientui.RuntimeSessionView{SessionID: "session-2"})
	if model.detailTranscript.loaded {
		t.Fatal("visible session replacement retained old app detail membership")
	}
	if action := model.view.DetailSelectionAction(); action != tui.DetailSelectionActionNone {
		t.Fatalf("visible session replacement retained TUI detail selection action %v", action)
	}
	if newCmd == nil {
		t.Fatal("visible session replacement did not hydrate the new target")
	}

	newDone := runDetailTranscriptCommand(newCmd)
	newRequest := waitForDetailTranscriptRequest(t, sessionViews)
	if newRequest.SessionID != "session-2" {
		t.Fatalf("replacement hydration session = %q, want session-2", newRequest.SessionID)
	}
	replacementNotice := "replacement target active"
	model.sendTransientStatusWithNoticeID(replacementNotice, uiStatusNoticeInfo, transientStatusDuration, uiStatusNoticeReplace, "")
	model = updateUIModel(t, model, waitForDetailTranscriptCompletion(t, oldDone))
	if model.transientStatus != replacementNotice || model.transientStatusKind != uiStatusNoticeInfo {
		t.Fatalf("late old completion replaced new-target notice: text=%q kind=%v", model.transientStatus, model.transientStatusKind)
	}
	sessionViews.results <- controlledTranscriptPageResult{response: serverapi.SessionTranscriptPageResponse{
		Transcript: clientui.TranscriptPage{
			SessionID: "session-2",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "replacement page"}},
		},
	}}
	model = updateUIModel(t, model, waitForDetailTranscriptCompletion(t, newDone))
	got := model.detailTranscript.page()
	if got.SessionID != "session-2" || len(got.Entries) != 1 || got.Entries[0].Text != "replacement page" {
		t.Fatalf("replacement detail page = %#v, want session-2 membership", got)
	}
}

func TestDetailTranscriptHiddenSessionReplacementResetsAndHydratesNewTarget(t *testing.T) {
	sessionViews := newControlledTranscriptPageClient()
	model := newDetailTranscriptRequestTestModel(sessionViews)
	model.view = tui.NewModel()
	model.forwardToView(tui.SetDetailTranscriptPageMsg{Page: clientui.TranscriptPage{
		SessionID: "session-1",
		Entries: []clientui.ChatEntry{{
			Role: "assistant",
			Text: "line one\nline two\nline three\nline four",
		}},
	}})

	cmd := model.runtimeAdapter().applyProjectedSessionMetadata(clientui.RuntimeSessionView{SessionID: "session-2"})
	if model.detailTranscript.loaded {
		t.Fatal("hidden session replacement retained old app detail membership")
	}
	detailView := mustUpdateTUIModel(t, model.view, tui.SetModeMsg{Mode: tui.ModeDetail})
	if action := detailView.DetailSelectionAction(); action != tui.DetailSelectionActionNone {
		t.Fatalf("hidden session replacement retained TUI detail selection action %v", action)
	}
	if cmd == nil {
		t.Fatal("hidden session replacement did not hydrate the new target")
	}

	done := runDetailTranscriptCommand(cmd)
	request := waitForDetailTranscriptRequest(t, sessionViews)
	if request.SessionID != "session-2" {
		t.Fatalf("hidden replacement hydration session = %q, want session-2", request.SessionID)
	}
	sessionViews.results <- controlledTranscriptPageResult{response: serverapi.SessionTranscriptPageResponse{
		Transcript: clientui.TranscriptPage{SessionID: "session-2"},
	}}
	_ = waitForDetailTranscriptCompletion(t, done)
}

func newDetailTranscriptRequestTestModel(sessionViews *controlledTranscriptPageClient) *uiModel {
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		WithUISessionID("session-1"),
		WithUIStatusConfig(uiStatusConfig{SessionViews: sessionViews}),
	)
	model.detailTranscript.replace(clientui.TranscriptPage{
		SessionID:    "session-1",
		OlderCursor:  appInt64Ptr(25),
		HasMoreAbove: true,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "current page"}},
	})
	return model
}

func updateDetailTranscriptRequest(t *testing.T, model *uiModel, direction tui.DetailTranscriptPageDirection) (*uiModel, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(tui.RequestDetailTranscriptPageMsg{Direction: direction})
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("updated model type = %T, want *uiModel", next)
	}
	return updated, cmd
}

func startDetailTranscriptRequestFromInput(t *testing.T, model *uiModel, input tea.Msg) (*uiModel, tea.Cmd) {
	t.Helper()
	next, intentCmd := model.Update(input)
	updated := next.(*uiModel)
	if intentCmd == nil {
		t.Fatal("detail edge input did not emit a paging intent")
	}
	intent := intentCmd()
	next, requestCmd := updated.Update(intent)
	return next.(*uiModel), requestCmd
}

func runDetailTranscriptCommand(cmd tea.Cmd) <-chan tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()
	return done
}

func waitForDetailTranscriptRequest(t *testing.T, client *controlledTranscriptPageClient) serverapi.SessionTranscriptPageRequest {
	t.Helper()
	select {
	case req := <-client.started:
		return req
	case <-time.After(time.Second):
		t.Fatal("adjacent-page request did not reach the session view client")
		return serverapi.SessionTranscriptPageRequest{}
	}
}

func waitForDetailTranscriptCompletion(t *testing.T, done <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-done:
		return msg
	case <-time.After(time.Second):
		t.Fatal("adjacent-page request did not finish")
		return nil
	}
}
