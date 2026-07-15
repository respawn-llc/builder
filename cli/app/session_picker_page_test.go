package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
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

type recordingSessionPageLoader struct {
	mu        sync.Mutex
	projectID string
	calls     []serverapi.SessionPageRequest
	responses func(serverapi.SessionPageRequest) sessionPageLoadResult
}

func (l *recordingSessionPageLoader) ProjectID() string {
	if l.projectID != "" {
		return l.projectID
	}
	return "picker-test-project"
}

func (l *recordingSessionPageLoader) ListSessionPage(_ context.Context, request serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	l.mu.Lock()
	l.calls = append(l.calls, request)
	load := l.responses
	l.mu.Unlock()
	if load == nil {
		return serverapi.SessionPageResponse{}, nil
	}
	result := load(request)
	return result.response, result.err
}

func TestSessionPickerInitialHydrationKeepsCreateNewSelectedWithDetailClient(t *testing.T) {
	main := pickerTestSummary(t, "initial-main", time.Unix(1_900_000_000, 0).UTC())
	subagent := pickerTestSummary(t, "initial-subagent", time.Unix(1_899_999_000, 0).UTC())
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		switch request.Category {
		case sessioncontract.SessionCategoryMain:
			return sessionPageLoadResult{response: pickerPageResponse(t, request, main.SessionID.String())}
		case sessioncontract.SessionCategorySubagent:
			return sessionPageLoadResult{response: pickerPageResponse(t, request, subagent.SessionID.String())}
		default:
			t.Fatalf("unexpected category %q", request.Category)
			return sessionPageLoadResult{}
		}
	}}
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{
			main.SessionID:     sessionPickerDetailEnvironment(t, main.SessionID),
			subagent.SessionID: sessionPickerDetailEnvironment(t, subagent.SessionID),
		},
		errs: map[runtimeids.SessionID]error{},
	}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, environmentClient, "dark", sessionPickerHeaderInfo{})

	runSessionPickerCommands(t, model, model.Init())

	if _, ok := model.main.selected.(sessionPickerCreateSelection); !ok {
		t.Fatalf("main selection after hydration = %T, want create-new", model.main.selected)
	}
	if model.main.selectedDetail != nil || model.main.detailRequest != nil {
		t.Fatal("initial hydration started selected detail before a session row was selected")
	}
	if got := environmentClient.requestCount(); got != 0 {
		t.Fatalf("selected-detail requests after initial hydration = %d, want 0", got)
	}

	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyEnter}))
	if _, ok := model.result.(sessionPickerCreateResult); !ok {
		t.Fatalf("initial Enter result = %T, want create-new", model.result)
	}
}

func (l *recordingSessionPageLoader) snapshotCalls() []serverapi.SessionPageRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]serverapi.SessionPageRequest(nil), l.calls...)
}

func TestSessionPickerLoadsBothTabsAndKeepsTabLocalSelection(t *testing.T) {
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		switch request.Category {
		case sessioncontract.SessionCategoryMain:
			return sessionPageLoadResult{response: pickerPageResponse(t, request, "main-1", "main-2")}
		case sessioncontract.SessionCategorySubagent:
			return sessionPageLoadResult{response: pickerPageResponse(t, request, "subagent-1")}
		default:
			t.Fatalf("unexpected category %q", request.Category)
			return sessionPageLoadResult{}
		}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})
	runSessionPickerCommands(t, model, model.Init())

	calls := loader.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("initial page calls = %d, want 2: %+v", len(calls), calls)
	}
	seen := map[sessioncontract.SessionCategory]bool{}
	for _, call := range calls {
		if call.Position.Kind() != serverapi.SessionPagePositionNewest {
			t.Fatalf("initial position = %q, want newest", call.Position.Kind())
		}
		seen[call.Category] = true
	}
	if !seen[sessioncontract.SessionCategoryMain] || !seen[sessioncontract.SessionCategorySubagent] {
		t.Fatalf("initial categories = %+v", seen)
	}
	if model.activeTab != sessioncontract.SessionCategoryMain {
		t.Fatalf("active tab = %q, want main", model.activeTab)
	}

	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
	mainAnchor := model.tab(sessioncontract.SessionCategoryMain).selected
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyTab}))
	if model.activeTab != sessioncontract.SessionCategorySubagent || model.result != nil {
		t.Fatalf("tab switch active=%q result=%+v", model.activeTab, model.result)
	}
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
	subagentAnchor := model.tab(sessioncontract.SessionCategorySubagent).selected
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyShiftTab}))
	if model.tab(sessioncontract.SessionCategoryMain).selected != mainAnchor {
		t.Fatalf("main anchor changed across tab switch: got %+v want %+v", model.tab(sessioncontract.SessionCategoryMain).selected, mainAnchor)
	}
	if model.tab(sessioncontract.SessionCategorySubagent).selected != subagentAnchor {
		t.Fatalf("subagent anchor changed across tab switch: got %+v want %+v", model.tab(sessioncontract.SessionCategorySubagent).selected, subagentAnchor)
	}
}

func TestSessionPickerRejectsPageCategoryMismatch(t *testing.T) {
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		response := pickerPageResponse(t, request)
		if request.Category == sessioncontract.SessionCategoryMain {
			response.Category = sessioncontract.SessionCategorySubagent
			response.Sessions = []clientui.SessionSummary{{
				SessionID: mustPickerSessionID(t, "wrong-category"),
				Category:  sessioncontract.SessionCategorySubagent,
				UpdatedAt: time.Unix(1_900_000_000, 0).UTC(),
			}}
		}
		return sessionPageLoadResult{response: response}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})

	runSessionPickerCommands(t, model, model.Init())

	tab := model.tab(sessioncontract.SessionCategoryMain)
	if tab.bodyPhase != sessionPickerBodyFailed {
		t.Fatalf("main body phase = %q, want failed", tab.bodyPhase)
	}
	if got := tab.residentSessionCount(); got != 0 {
		t.Fatalf("resident sessions after category mismatch = %d, want 0", got)
	}
	failure, ok := model.startupStatus.failure(sessioncontract.SessionCategoryMain, sessionPickerOperationBodyPage)
	if !ok || failure.Kind != sessionPickerFailurePageContract {
		t.Fatalf("page mismatch failure = %+v/%v, want typed contract failure", failure, ok)
	}
}

func TestSessionPickerPageRequestFailureKeepsDiagnosticOutOfOperatorProjection(t *testing.T) {
	diagnostic := errors.New("read tcp 127.0.0.1:1234: connection reset by peer")
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		if request.Category == sessioncontract.SessionCategoryMain {
			return sessionPageLoadResult{err: diagnostic}
		}
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(
		context.Background(),
		loader,
		nil,
		"dark",
		sessionPickerHeaderInfo{},
	)

	runSessionPickerCommands(t, model, model.Init())

	failure, ok := model.startupStatus.failure(sessioncontract.SessionCategoryMain, sessionPickerOperationBodyPage)
	if !ok || failure.Kind != sessionPickerFailurePageRequest {
		t.Fatalf("page request failure = %+v/%v, want typed request failure", failure, ok)
	}
	if !errors.Is(failure.Diagnostic, diagnostic) {
		t.Fatal("page request failure discarded its diagnostic cause")
	}
}

func TestSessionPickerRejectsPageProjectMismatch(t *testing.T) {
	loader := &recordingSessionPageLoader{
		projectID: "requested-project",
		responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
			response := pickerPageResponse(t, request, "wrong-project")
			response.ProjectID = "different-project"
			return sessionPageLoadResult{response: response}
		},
	}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})

	runSessionPickerCommands(t, model, model.Init())

	for _, category := range []sessioncontract.SessionCategory{
		sessioncontract.SessionCategoryMain,
		sessioncontract.SessionCategorySubagent,
	} {
		tab := model.tab(category)
		if tab.bodyPhase != sessionPickerBodyFailed {
			t.Fatalf("%s body phase = %q, want failed", category, tab.bodyPhase)
		}
		if got := tab.residentSessionCount(); got != 0 {
			t.Fatalf("%s resident sessions after project mismatch = %d, want 0", category, got)
		}
	}
}

func TestSessionPickerResultsAreDiscriminated(t *testing.T) {
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request, "session-1")}
	}}
	tests := []struct {
		name   string
		keys   []tea.KeyMsg
		want   sessionPickerResult
		wantID string
	}{
		{name: "create", keys: []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'n'}}}, want: sessionPickerCreateResult{}},
		{name: "cancel", keys: []tea.KeyMsg{{Type: tea.KeyCtrlC}}, want: sessionPickerCancelResult{}},
		{name: "open", keys: []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyEnter}}, want: sessionPickerOpenResult{}, wantID: "session-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})
			runSessionPickerCommands(t, model, model.Init())
			for _, key := range test.keys {
				runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, key))
			}
			open, present := model.result.(sessionPickerOpenResult)
			if test.wantID == "" {
				if present {
					t.Fatalf("result unexpectedly carries session ID %q", open.sessionID)
				}
				if reflect.TypeOf(model.result) != reflect.TypeOf(test.want) {
					t.Fatalf("result = %T, want %T", model.result, test.want)
				}
				return
			}
			if !present || open.sessionID.String() != test.wantID {
				t.Fatalf("result = %+v, want session ID %q", model.result, test.wantID)
			}
		})
	}
}

func TestSessionPickerRetriesFailedTabFromNewest(t *testing.T) {
	var subagentAttempts int
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		if request.Category == sessioncontract.SessionCategorySubagent {
			subagentAttempts++
			if subagentAttempts == 1 {
				return sessionPageLoadResult{err: errors.New("subagent page unavailable")}
			}
		}
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})
	runSessionPickerCommands(t, model, model.Init())
	if model.tab(sessioncontract.SessionCategorySubagent).bodyPhase != sessionPickerBodyFailed {
		t.Fatalf("subagent body phase = %q, want failed", model.tab(sessioncontract.SessionCategorySubagent).bodyPhase)
	}

	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyTab}))
	if subagentAttempts != 2 {
		t.Fatalf("subagent attempts = %d, want 2", subagentAttempts)
	}
	if model.tab(sessioncontract.SessionCategorySubagent).bodyPhase != sessionPickerBodyEmpty {
		t.Fatalf("subagent body phase after retry = %q, want empty", model.tab(sessioncontract.SessionCategorySubagent).bodyPhase)
	}
	calls := loader.snapshotCalls()
	if calls[len(calls)-1].Position.Kind() != serverapi.SessionPagePositionNewest {
		t.Fatalf("retry position = %q, want newest", calls[len(calls)-1].Position.Kind())
	}
}

func TestSessionPickerFreshRetryClearsObsoleteDirectionalFailure(t *testing.T) {
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})
	tab := model.tab(sessioncontract.SessionCategorySubagent)
	tab.generation = 3
	tab.bodyPhase = sessionPickerBodyFailed
	model.recordPickerFailureForTab(
		tab,
		sessionPickerOperationDirectionalPage,
		tab.generation,
		sessionPickerFailurePageRequest,
		errors.New("directional page failed"),
	)

	model.startBodyRequest(sessioncontract.SessionCategorySubagent, sessionPickerBodyRequestRetry)

	if failure, ok := model.startupStatus.failure(sessioncontract.SessionCategorySubagent, sessionPickerOperationDirectionalPage); ok {
		t.Fatalf("directional failure survived fresh retry: %+v", failure)
	}
}

func TestSessionPickerKeepsTwoPageResidentBoundAcrossOlderAndNewerTraversal(t *testing.T) {
	older1 := mustPickerContinuation(t, "older-1")
	older2 := mustPickerContinuation(t, "older-2")
	newer1 := mustPickerContinuation(t, "newer-1")
	newer2 := mustPickerContinuation(t, "newer-2")
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		if request.Category == sessioncontract.SessionCategorySubagent {
			return sessionPageLoadResult{response: pickerPageResponse(t, request)}
		}
		switch request.Position.Kind() {
		case serverapi.SessionPagePositionNewest:
			response := pickerPageResponse(t, request, "session-6", "session-5")
			response.Older = &older1
			return sessionPageLoadResult{response: response}
		case serverapi.SessionPagePositionOlder:
			token, _ := request.Position.Continuation()
			switch token.String() {
			case older1.String():
				response := pickerPageResponse(t, request, "session-4", "session-3")
				response.Older = &older2
				response.Newer = &newer1
				return sessionPageLoadResult{response: response}
			case older2.String():
				response := pickerPageResponse(t, request, "session-2", "session-1")
				response.Newer = &newer2
				return sessionPageLoadResult{response: response}
			}
		case serverapi.SessionPagePositionNewer:
			token, _ := request.Position.Continuation()
			switch token.String() {
			case newer2.String():
				response := pickerPageResponse(t, request, "session-4", "session-3")
				response.Older = &older2
				response.Newer = &newer1
				return sessionPageLoadResult{response: response}
			case newer1.String():
				response := pickerPageResponse(t, request, "session-6", "session-5")
				response.Older = &older1
				return sessionPageLoadResult{response: response}
			}
		}
		t.Fatalf("unexpected page request: %+v", request)
		return sessionPageLoadResult{}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})
	runSessionPickerCommands(t, model, model.Init())
	for range 10 {
		runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
		if count := model.tab(sessioncontract.SessionCategoryMain).residentSessionCount(); count > 4 {
			t.Fatalf("resident session count = %d, want <= 4", count)
		}
	}
	for range 10 {
		runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyUp}))
		if count := model.tab(sessioncontract.SessionCategoryMain).residentSessionCount(); count > 4 {
			t.Fatalf("resident session count = %d, want <= 4", count)
		}
	}
	var olderCalls, newerCalls int
	for _, call := range loader.snapshotCalls() {
		switch call.Position.Kind() {
		case serverapi.SessionPagePositionOlder:
			olderCalls++
		case serverapi.SessionPagePositionNewer:
			newerCalls++
		}
	}
	if olderCalls < 2 || newerCalls < 1 {
		t.Fatalf("directional calls older=%d newer=%d, want >=2/>=1", olderCalls, newerCalls)
	}
}

func TestSessionPickerDirectionalOverlapSelectsFirstNewResidentRow(t *testing.T) {
	older := mustPickerContinuation(t, "overlap-older")
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		if request.Category == sessioncontract.SessionCategorySubagent {
			return sessionPageLoadResult{response: pickerPageResponse(t, request)}
		}
		switch request.Position.Kind() {
		case serverapi.SessionPagePositionNewest:
			response := pickerPageResponse(t, request, "session-3", "session-2")
			response.Older = &older
			return sessionPageLoadResult{response: response}
		case serverapi.SessionPagePositionOlder:
			return sessionPageLoadResult{response: pickerPageResponse(t, request, "session-2", "session-1")}
		default:
			t.Fatalf("unexpected page request: %+v", request)
			return sessionPageLoadResult{}
		}
	}}
	model := newSessionPickerModelWithExecutionEnvironmentClient(context.Background(), loader, nil, "dark", sessionPickerHeaderInfo{})
	runSessionPickerCommands(t, model, model.Init())

	for range 3 {
		runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
	}

	selected, ok := model.main.selected.(sessionPickerSessionSelection)
	if !ok || selected.sessionID.String() != "session-1" {
		t.Fatalf("selection after overlapping older page = %+v, want session-1", model.main.selected)
	}
	if got := model.main.sessions(); len(got) != 3 ||
		got[0].SessionID.String() != "session-3" ||
		got[1].SessionID.String() != "session-2" ||
		got[2].SessionID.String() != "session-1" {
		t.Fatalf("resident sessions after overlap = %+v", got)
	}
}

func pickerPageResponse(t *testing.T, request serverapi.SessionPageRequest, ids ...string) serverapi.SessionPageResponse {
	t.Helper()
	response := serverapi.SessionPageResponse{ProjectID: request.ProjectID, Category: request.Category}
	for index, raw := range ids {
		id, err := runtimeids.ParseSessionID(raw)
		if err != nil {
			t.Fatalf("ParseSessionID(%q): %v", raw, err)
		}
		response.Sessions = append(response.Sessions, clientui.SessionSummary{
			SessionID: id,
			Category:  request.Category,
			UpdatedAt: time.Unix(int64(len(ids)-index), 0).UTC(),
		})
	}
	return response
}

func mustPickerContinuation(t *testing.T, raw string) serverapi.SessionPageContinuation {
	t.Helper()
	token, err := serverapi.ParseSessionPageContinuation(raw)
	if err != nil {
		t.Fatalf("ParseSessionPageContinuation(%q): %v", raw, err)
	}
	return token
}

func runSessionPickerCommands(t *testing.T, model *sessionPickerModel, command tea.Cmd) {
	t.Helper()
	if command == nil {
		return
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			runSessionPickerCommands(t, model, child)
		}
		return
	}
	next, command := model.Update(message)
	updated, ok := next.(*sessionPickerModel)
	if !ok || updated != model {
		t.Fatalf("session picker model replacement = %T/%p, want %p", next, updated, model)
	}
	runSessionPickerCommands(t, model, command)
}

func pickerUpdateCommand(t *testing.T, model *sessionPickerModel, message tea.Msg) tea.Cmd {
	t.Helper()
	next, command := model.Update(message)
	updated, ok := next.(*sessionPickerModel)
	if !ok || updated != model {
		t.Fatalf("session picker model replacement = %T/%p, want %p", next, updated, model)
	}
	return command
}
