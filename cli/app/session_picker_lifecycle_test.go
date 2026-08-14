package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPageLoadResult struct {
	response serverapi.SessionPageResponse
	err      error
}

type sessionPageLoadCall struct {
	context  context.Context
	request  serverapi.SessionPageRequest
	complete chan sessionPageLoadResult
}

type recordingSessionPageLoader struct {
	responses func(serverapi.SessionPageRequest) sessionPageLoadResult
	started   chan sessionPageLoadCall
}

func (*recordingSessionPageLoader) ProjectID() string {
	return "picker-test-project"
}

func (l *recordingSessionPageLoader) ListSessionPage(ctx context.Context, request serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	if l.started != nil {
		call := sessionPageLoadCall{context: ctx, request: request, complete: make(chan sessionPageLoadResult, 1)}
		l.started <- call
		select {
		case result := <-call.complete:
			return result.response, result.err
		case <-ctx.Done():
			return serverapi.SessionPageResponse{}, ctx.Err()
		}
	}
	if l.responses == nil {
		return serverapi.SessionPageResponse{}, nil
	}
	result := l.responses(request)
	return result.response, result.err
}

func TestSessionPickerLifecycleInitialJourneys(t *testing.T) {
	started := make(chan sessionPageLoadCall, 2)
	loader := &recordingSessionPageLoader{started: started}
	lifecycle := newTestSessionPickerLifecycle(t, loader)
	messages := startSessionPickerCommands(lifecycle.Init())
	calls := make(map[sessioncontract.SessionCategory]sessionPageLoadCall, 2)
	for range 2 {
		call := waitSessionPickerValue(t, started)
		requireSessionPicker(t, call.request.ProjectID == loader.ProjectID() &&
			call.request.PageSize == sessionPickerPageSize &&
			call.request.Position.Kind() == serverapi.SessionPagePositionNewest, "initial page request = %+v", call.request)
		calls[call.request.Category] = call
	}
	requireSessionPicker(t, len(calls) == 2, "initial categories = %+v, want main and subagent", calls)
	for category, call := range calls {
		call.complete <- sessionPageLoadResult{response: pickerPageResponse(t, call.request, string(category)+"-1")}
		lifecycle.Update(waitSessionPickerPageLoaded(t, messages, category))
	}
	lifecycle.Update(tea.KeyMsg{Type: tea.KeyEnter})
	requireSessionPickerResult(t, lifecycle.Result(), sessionPickerCreateResult{})

	empty := newTestSessionPickerLifecycle(t, &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}})
	runSessionPickerTestCommands(empty.Init(), empty)
	requireSessionPickerResult(t, empty.Result(), sessionPickerCreateResult{})
	selectionLoader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request, string(request.Category)+"-1")}
	}}
	for _, test := range []struct {
		keys []tea.Msg
		want sessionPickerResult
	}{
		{[]tea.Msg{tea.KeyDown, tea.KeyTab, tea.KeyDown, tea.KeyShiftTab, tea.KeyEnter}, newSessionPickerOpenResult(mustPickerValue(t, "main-1", runtimeids.ParseSessionID))},
		{[]tea.Msg{tea.KeyDown, tea.KeyTab, tea.KeyDown, tea.KeyShiftTab, tea.KeyTab, tea.KeyEnter}, newSessionPickerOpenResult(mustPickerValue(t, "subagent-1", runtimeids.ParseSessionID))},
	} {
		lifecycle := newTestSessionPickerLifecycle(t, selectionLoader)
		runSessionPickerTestCommands(lifecycle.Init(), lifecycle)
		updateSessionPickerLifecycle(t, lifecycle, test.keys...)
		requireSessionPickerResult(t, lifecycle.Result(), test.want)
	}
}

func TestSessionPickerLifecyclePageFailuresAndRetry(t *testing.T) {
	diagnostic := errors.New("page unavailable")
	request := serverapi.SessionPageRequest{ProjectID: (&recordingSessionPageLoader{}).ProjectID(), Category: sessioncontract.SessionCategoryMain}
	project, category := pickerPageResponse(t, request), pickerPageResponse(t, request)
	project.ProjectID = "other-project"
	category.Category = sessioncontract.SessionCategorySubagent
	ids := make([]string, sessionPickerPageSize+1)
	for index := range ids {
		ids[index] = fmt.Sprintf("oversized-%d", index)
	}
	tests := []struct {
		result sessionPageLoadResult
		kind   sessionPickerFailureKind
	}{
		{sessionPageLoadResult{response: project}, sessionPickerFailurePageContract},
		{sessionPageLoadResult{response: category}, sessionPickerFailurePageContract},
		{sessionPageLoadResult{response: pickerPageResponse(t, request, ids...)}, sessionPickerFailurePageContract},
		{sessionPageLoadResult{err: diagnostic}, sessionPickerFailurePageRequest},
	}
	for _, test := range tests {
		loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
			if len(test.result.response.Sessions) > sessionPickerPageSize {
				return sessionPageLoadResult{response: pickerPageResponse(t, request, ids...)}
			}
			result := test.result
			if result.response.ProjectID != request.ProjectID {
				result.response.Category = request.Category
			} else if result.response.Category == request.Category {
				result.response.Category = sessioncontract.SessionCategoryMain
			}
			return result
		}}
		lifecycle := newTestSessionPickerLifecycle(t, loader)
		runSessionPickerTestCommands(lifecycle.Init(), lifecycle)
		mainFailure, mainOK := lifecycle.picker.startupStatus.failure(sessioncontract.SessionCategoryMain, sessionPickerOperationBodyPage)
		subFailure, subOK := lifecycle.picker.startupStatus.failure(sessioncontract.SessionCategorySubagent, sessionPickerOperationBodyPage)
		requireSessionPicker(t, mainOK && subOK && mainFailure.Kind == test.kind && subFailure.Kind == test.kind,
			"page failures = %+v/%v %+v/%v", mainFailure, mainOK, subFailure, subOK)
		requireSessionPicker(t, test.kind != sessionPickerFailurePageRequest || errors.Is(mainFailure.Diagnostic, diagnostic),
			"request failure discarded its diagnostic")
	}

	older := mustPickerValue(t, "retry-older", serverapi.ParseSessionPageContinuation)
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		if request.Category == sessioncontract.SessionCategorySubagent {
			return sessionPageLoadResult{response: pickerPageResponse(t, request)}
		}
		if request.Position.Kind() == serverapi.SessionPagePositionOlder {
			return sessionPageLoadResult{err: diagnostic}
		}
		response := pickerPageResponse(t, request, "retry-1")
		response.Older = &older
		return sessionPageLoadResult{response: response}
	}}
	lifecycle := newTestSessionPickerLifecycle(t, loader)
	runSessionPickerTestCommands(lifecycle.Init(), lifecycle)
	updateSessionPickerLifecycle(t, lifecycle, tea.KeyDown, tea.KeyDown)
	_, failed := lifecycle.picker.startupStatus.failure(sessioncontract.SessionCategoryMain, sessionPickerOperationDirectionalPage)
	started := make(chan sessionPageLoadCall, 1)
	loader.started = started
	_, retry := lifecycle.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, obsolete := lifecycle.picker.startupStatus.failure(sessioncontract.SessionCategoryMain, sessionPickerOperationDirectionalPage)
	requireSessionPicker(t, failed && !obsolete, "fresh retry did not clear its directional failure")
	messages := startSessionPickerCommands(retry)
	call := waitSessionPickerValue(t, started)
	tick, ok := waitSessionPickerValue(t, messages).(sessionPickerSpinnerTickMsg)
	_, tickCommand := lifecycle.Update(tick)
	requireSessionPicker(t, call.request.Position.Kind() == serverapi.SessionPagePositionNewest && ok && tickCommand != nil,
		"retry did not start newest with a fresh spinner")
}

func TestSessionPickerLifecycleDirectionalTraversal(t *testing.T) {
	older1 := mustPickerValue(t, "older-1", serverapi.ParseSessionPageContinuation)
	older2 := mustPickerValue(t, "older-2", serverapi.ParseSessionPageContinuation)
	newer1 := mustPickerValue(t, "newer-1", serverapi.ParseSessionPageContinuation)
	newest := make([]string, sessionPickerPageSize)
	for index := range newest {
		newest[index] = fmt.Sprintf("newest-%02d", index)
	}
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		if request.Category == sessioncontract.SessionCategorySubagent {
			return sessionPageLoadResult{response: pickerPageResponse(t, request)}
		}
		token, _ := request.Position.Continuation()
		switch {
		case request.Position.Kind() == serverapi.SessionPagePositionNewest:
			response := pickerPageResponse(t, request, newest...)
			response.Older = &older1
			return sessionPageLoadResult{response: response}
		case token.String() == older1.String():
			response := pickerPageResponse(t, request, newest[len(newest)-1], "older-a")
			response.Older, response.Newer = &older2, &newer1
			return sessionPageLoadResult{response: response}
		case token.String() == older2.String():
			return sessionPageLoadResult{response: pickerPageResponse(t, request, "older-a", "older-b")}
		case request.Position.Kind() == serverapi.SessionPagePositionNewer && token.String() == newer1.String():
			response := pickerPageResponse(t, request, "older-a", "newer-new")
			response.Older = &older1
			return sessionPageLoadResult{response: response}
		default:
			t.Fatalf("unexpected page request: %+v", request)
			return sessionPageLoadResult{}
		}
	}}
	lifecycle := newTestSessionPickerLifecycle(t, loader)
	runSessionPickerTestCommands(lifecycle.Init(), lifecycle)
	for range sessionPickerPageSize + 1 {
		updateSessionPickerLifecycle(t, lifecycle, tea.KeyMsg{Type: tea.KeyDown})
	}
	updateSessionPickerLifecycle(t, lifecycle, tea.KeyPgDown)
	olderPages, olderIDs := len(lifecycle.picker.main.segments), lifecycle.picker.main.residentSessionCount()
	updateSessionPickerLifecycle(t, lifecycle, tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyUp})
	requireSessionPicker(t, [4]int{olderPages, olderIDs, len(lifecycle.picker.main.segments), lifecycle.picker.main.residentSessionCount()} ==
		[4]int{2, 2, 2, 2}, "picker exceeded its resident page bound")
	updateSessionPickerLifecycle(t, lifecycle, tea.KeyMsg{Type: tea.KeyEnter})
	requireSessionPickerResult(t, lifecycle.Result(), newSessionPickerOpenResult(mustPickerValue(t, "newer-new", runtimeids.ParseSessionID)))
}

func TestSessionPickerLifecycleGeometryAndResults(t *testing.T) {
	lifecycle := newTestSessionPickerLifecycle(t, &recordingSessionPageLoader{})
	requireSessionPicker(t, lifecycle.Init() != nil && lifecycle.View() == "",
		"unknown-geometry picker did not stay blank while starting effects")
	lifecycle.Update(tea.KeyMsg{Type: tea.KeyEsc})
	requireSessionPicker(t, lifecycle.Result() == nil, "Esc result = %+v, want no-op", lifecycle.Result())
	lifecycle.Update(tea.WindowSizeMsg{Width: 39, Height: 9})
	requireSessionPicker(t, lifecycle.View() == "", "sub-minimum picker rendered output")
	lifecycle.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	requireSessionPickerResult(t, lifecycle.Result(), sessionPickerCreateResult{})
	lifecycle.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	requireSessionPicker(t, lifecycle.View() != "", "exact 40x10 geometry remained blank")

	open := newTestSessionPickerLifecycle(t, &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request, "result-1")}
	}})
	runSessionPickerTestCommands(open.Init(), open)
	updateSessionPickerLifecycle(t, open, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	requireSessionPickerResult(t, open.Result(), newSessionPickerOpenResult(mustPickerValue(t, "result-1", runtimeids.ParseSessionID)))
	cancel := newTestSessionPickerLifecycle(t, &recordingSessionPageLoader{})
	updateSessionPickerLifecycle(t, cancel, tea.KeyMsg{Type: tea.KeyCtrlC})
	requireSessionPickerResult(t, cancel.Result(), sessionPickerCancelResult{})
	for _, result := range []sessionPickerResult{newSessionPickerCreateResult(), newSessionPickerCancelResult(), newSessionPickerOpenResult(mustPickerValue(t, "valid-open", runtimeids.ParseSessionID))} {
		requireSessionPicker(t, validateSessionPickerLifecycleResult(result) == nil, "validate %T failed", result)
	}
	for _, result := range []sessionPickerResult{nil, sessionPickerOpenResult{}} {
		requireSessionPicker(t, validateSessionPickerLifecycleResult(result) != nil, "validate %T unexpectedly succeeded", result)
	}
}

func TestSessionPickerLifecycleCloseCancelsOutstandingPageRequests(t *testing.T) {
	sessionID := mustPickerValue(t, "cancellation-open", runtimeids.ParseSessionID)
	for _, test := range []struct {
		name string
		key  *tea.KeyMsg
		want sessionPickerResult
	}{
		{"create", &tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, sessionPickerCreateResult{}},
		{"open", &tea.KeyMsg{Type: tea.KeyEnter}, newSessionPickerOpenResult(sessionID)},
		{"cancel", &tea.KeyMsg{Type: tea.KeyCtrlC}, sessionPickerCancelResult{}},
		{"direct-close", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan sessionPageLoadCall, 3)
			loader := &recordingSessionPageLoader{started: started}
			lifecycle := newTestSessionPickerLifecycle(t, loader)
			initialMessages := startSessionPickerCommands(lifecycle.Init())
			initial := make(map[sessioncontract.SessionCategory]sessionPageLoadCall, 2)
			for range 2 {
				call := waitSessionPickerValue(t, started)
				initial[call.request.Category] = call
			}
			older := mustPickerValue(t, "cancellation-older", serverapi.ParseSessionPageContinuation)
			response := pickerPageResponse(t, initial[sessioncontract.SessionCategoryMain].request, sessionID.String())
			response.Older = &older
			initial[sessioncontract.SessionCategoryMain].complete <- sessionPageLoadResult{response: response}
			lifecycle.Update(waitSessionPickerPageLoaded(t, initialMessages, sessioncontract.SessionCategoryMain))
			lifecycle.Update(tea.KeyMsg{Type: tea.KeyDown})
			_, directional := lifecycle.Update(tea.KeyMsg{Type: tea.KeyDown})
			directionalMessages := startSessionPickerCommands(directional)
			directionalCall := waitSessionPickerValue(t, started)
			requireSessionPicker(t, directionalCall.request.Position.Kind() == serverapi.SessionPagePositionOlder,
				"directional position = %q, want older", directionalCall.request.Position.Kind())
			if test.key != nil {
				lifecycle.Update(*test.key)
			}
			requireSessionPickerResult(t, lifecycle.Result(), test.want)
			lifecycle.Close()
			for _, pending := range []struct {
				call   sessionPageLoadCall
				loaded sessionPickerPageLoadedMsg
			}{
				{initial[sessioncontract.SessionCategorySubagent], waitSessionPickerPageLoaded(t, initialMessages, sessioncontract.SessionCategorySubagent)},
				{directionalCall, waitSessionPickerPageLoaded(t, directionalMessages, sessioncontract.SessionCategoryMain)},
			} {
				waitSessionPickerValue(t, pending.call.context.Done())
				requireSessionPicker(t, errors.Is(pending.call.context.Err(), context.Canceled) && errors.Is(pending.loaded.err, context.Canceled),
					"canceled page context/message = %v/%v", pending.call.context.Err(), pending.loaded.err)
			}
		})
	}
}

func pickerPageResponse(t *testing.T, request serverapi.SessionPageRequest, ids ...string) serverapi.SessionPageResponse {
	response := serverapi.SessionPageResponse{ProjectID: request.ProjectID, Category: request.Category}
	for index, raw := range ids {
		response.Sessions = append(response.Sessions, clientui.SessionSummary{
			SessionID: mustPickerValue(t, raw, runtimeids.ParseSessionID),
			Category:  request.Category,
			UpdatedAt: time.Unix(int64(len(ids)-index), 0).UTC(),
		})
	}
	return response
}

func mustPickerValue[T any](t *testing.T, raw string, parse func(string) (T, error)) T {
	value, err := parse(raw)
	if err != nil {
		t.Fatalf("parse picker value %q: %v", raw, err)
	}
	return value
}

func runSessionPickerCommands(t *testing.T, model *sessionPickerModel, command tea.Cmd) {
	runSessionPickerTestCommands(command, model)
}

func pickerUpdateCommand(t *testing.T, model *sessionPickerModel, message tea.Msg) tea.Cmd {
	_, command := model.Update(message)
	return command
}

func newTestSessionPickerLifecycle(t *testing.T, loader sessionPageLoader) *sessionPickerLifecycle {
	lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{Loader: loader, Theme: "dark"})
	t.Cleanup(lifecycle.Close)
	return lifecycle
}

func updateSessionPickerLifecycle(t *testing.T, lifecycle *sessionPickerLifecycle, messages ...tea.Msg) {
	for _, message := range messages {
		_, command := lifecycle.Update(message)
		runSessionPickerTestCommands(command, lifecycle)
	}
}

func requireSessionPickerResult(t *testing.T, got, want sessionPickerResult) {
	requireSessionPicker(t, got == want, "session picker result = %+v, want %+v", got, want)
}

func requireSessionPicker(t *testing.T, condition bool, format string, args ...any) {
	if !condition {
		t.Fatalf(format, args...)
	}
}

func runSessionPickerTestCommands(command tea.Cmd, updater tea.Model) {
	if command == nil {
		return
	}
	switch message := command().(type) {
	case tea.BatchMsg:
		for _, child := range message {
			runSessionPickerTestCommands(child, updater)
		}
	default:
		_, next := updater.Update(message)
		runSessionPickerTestCommands(next, updater)
	}
}

func startSessionPickerCommands(command tea.Cmd) <-chan tea.Msg {
	messages := make(chan tea.Msg, 16)
	var start func(tea.Cmd)
	start = func(command tea.Cmd) {
		go func() {
			switch message := command().(type) {
			case tea.BatchMsg:
				for _, child := range message {
					start(child)
				}
			default:
				messages <- message
			}
		}()
	}
	start(command)
	return messages
}

func waitSessionPickerValue[T any](t *testing.T, values <-chan T) T {
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("session picker operation timed out")
		return *new(T)
	}
}

func waitSessionPickerPageLoaded(t *testing.T, messages <-chan tea.Msg, category sessioncontract.SessionCategory) sessionPickerPageLoadedMsg {
	for {
		if loaded, ok := waitSessionPickerValue(t, messages).(sessionPickerPageLoadedMsg); ok && loaded.category == category {
			return loaded
		}
	}
}
