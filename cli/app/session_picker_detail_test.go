package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerEnvironmentClient struct {
	client.SessionViewClient

	mu         sync.Mutex
	requests   []serverapi.SessionExecutionEnvironmentRequest
	responses  map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse
	errs       map[runtimeids.SessionID]error
	cancelled  []runtimeids.SessionID
	requestCtx []context.Context
}

func (c *sessionPickerEnvironmentClient) GetSessionExecutionEnvironment(
	ctx context.Context,
	request serverapi.SessionExecutionEnvironmentRequest,
) (serverapi.SessionExecutionEnvironmentResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.requestCtx = append(c.requestCtx, ctx)
	response := c.responses[request.SessionID]
	err := c.errs[request.SessionID]
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		c.cancelled = append(c.cancelled, request.SessionID)
		c.mu.Unlock()
		return serverapi.SessionExecutionEnvironmentResponse{}, ctx.Err()
	default:
		return response, err
	}
}

func (c *sessionPickerEnvironmentClient) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *sessionPickerEnvironmentClient) requestIDs() []runtimeids.SessionID {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]runtimeids.SessionID, 0, len(c.requests))
	for _, request := range c.requests {
		ids = append(ids, request.SessionID)
	}
	return ids
}

func newPickerDetailTestModel(
	t *testing.T,
	summaries []clientui.SessionSummary,
	environmentClient *sessionPickerEnvironmentClient,
) *sessionPickerModel {
	t.Helper()
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		response := pickerPageResponse(t, request)
		if request.Category == sessioncontract.SessionCategoryMain {
			response.Sessions = append([]clientui.SessionSummary(nil), summaries...)
		}
		return sessionPageLoadResult{response: response}
	}}
	return newSessionPickerModelWithExecutionEnvironmentClient(
		context.Background(),
		loader,
		environmentClient,
		"dark",
		sessionPickerHeaderInfo{},
	)
}

func initializeSessionPickerPagesWithoutFollowup(t *testing.T, model *sessionPickerModel) {
	t.Helper()
	message := model.Init()()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("session picker Init message = %T, want tea.BatchMsg", message)
	}
	for _, child := range batch {
		message := child()
		if _, ok := message.(sessionPickerPageLoadedMsg); !ok {
			continue
		}
		next, _ := model.Update(message)
		updated, ok := next.(*sessionPickerModel)
		if !ok || updated != model {
			t.Fatalf("session picker model replacement = %T/%p, want %p", next, updated, model)
		}
	}
}

func sessionPickerDetailEnvironment(t *testing.T, id runtimeids.SessionID) serverapi.SessionExecutionEnvironmentResponse {
	t.Helper()
	return serverapi.SessionExecutionEnvironmentResponse{
		Environment: serverapi.SessionExecutionEnvironment{
			SessionID: id,
			Workspace: serverapi.AvailableSessionExecutionWorkspace("/workspaces/current"),
			Branch:    serverapi.AvailableSessionExecutionBranch("feature/current"),
			Auth: serverapi.AvailableSessionExecutionAuth(serverapi.SessionExecutionAuth{
				Provider: "openai",
				Method:   serverapi.SessionExecutionAuthMethodOAuth,
			}),
			Model: serverapi.AvailableSessionExecutionModel(serverapi.SessionExecutionModel{
				Name:     "gpt-5.6-sol",
				Provider: "openai",
			}),
		},
	}
}

func selectedDetailHasAnyAvailableValue(detail sessionPickerSelectedDetail) bool {
	ready, ok := detail.(sessionPickerSelectedDetailReadyState)
	if !ok {
		return false
	}
	_, workspace := ready.workspace.Value()
	_, branch := ready.branch.Value()
	_, auth := ready.auth.Value()
	_, model := ready.model.Value()
	return workspace || branch || auth || model
}

func selectedDetailReadyState(t *testing.T, detail sessionPickerSelectedDetail) sessionPickerSelectedDetailReadyState {
	t.Helper()
	ready, ok := detail.(sessionPickerSelectedDetailReadyState)
	if !ok {
		t.Fatalf("selected detail = %T, want ready", detail)
	}
	return ready
}

func selectedDetailIdentity(t *testing.T, detail sessionPickerSelectedDetail) sessionPickerSelectedDetailIdentity {
	t.Helper()
	switch typed := detail.(type) {
	case sessionPickerSelectedDetailLoadingState:
		return typed.identity
	case sessionPickerSelectedDetailReadyState:
		return typed.identity
	case sessionPickerSelectedDetailFailedState:
		return typed.identity
	default:
		t.Fatalf("selected detail = %T, want a detail state", detail)
		return sessionPickerSelectedDetailIdentity{}
	}
}

func selectedDetailFailure(model *sessionPickerModel) (startupPickerStatusFailure, bool) {
	return model.startupStatus.failure(model.activeTab, sessionPickerOperationSelectedDetail)
}

func TestSessionPickerHydratesSelectedDetailAfterSessionRowSelection(t *testing.T) {
	summary := pickerTestSummary(t, "initial-detail", time.Unix(1_900_000_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{
			summary.SessionID: sessionPickerDetailEnvironment(t, summary.SessionID),
		},
		errs: map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{summary}, environmentClient)

	runSessionPickerCommands(t, model, model.Init())
	if got := environmentClient.requestCount(); got != 0 {
		t.Fatalf("selected-detail requests before row selection = %d, want 0", got)
	}
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))

	if got := environmentClient.requestCount(); got != 1 {
		t.Fatalf("selected-detail requests after row selection = %d, want 1", got)
	}
	detail := model.tab(sessioncontract.SessionCategoryMain).selectedDetail
	ready := selectedDetailReadyState(t, detail)
	if got, ok := ready.workspace.Value(); !ok || got.Path != "/workspaces/current" {
		t.Fatalf("initial workspace projection = %+v/%v", got, ok)
	}
}

func TestSessionPickerRejectsSelectedDetailResponseIdentityMismatch(t *testing.T) {
	summary := pickerTestSummary(t, "requested-detail", time.Unix(1_900_000_000, 0).UTC())
	otherID := mustPickerSessionID(t, "different-detail")
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{
			summary.SessionID: sessionPickerDetailEnvironment(t, otherID),
		},
		errs: map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{summary}, environmentClient)

	runSessionPickerCommands(t, model, model.Init())
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))

	detail := model.tab(sessioncontract.SessionCategoryMain).selectedDetail
	if _, ok := detail.(sessionPickerSelectedDetailFailedState); !ok {
		t.Fatalf("selected detail = %T, want failed", detail)
	}
	if selectedDetailHasAnyAvailableValue(detail) {
		t.Fatal("identity-mismatched selected detail copied environment facts")
	}
	if _, ok := selectedDetailFailure(model); !ok {
		t.Fatal("identity mismatch did not enter selected-detail failure status")
	}
}

func TestSessionPickerSelectedDetailPlaceholdersDoNotBlockMovementOrOpening(t *testing.T) {
	first := pickerTestSummary(t, "detail-first", time.Unix(1_900_000_000, 0).UTC())
	second := pickerTestSummary(t, "detail-second", time.Unix(1_899_999_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{},
		errs:      map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{first, second}, environmentClient)
	initializeSessionPickerPagesWithoutFollowup(t, model)
	_ = pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown})

	tab := model.tab(sessioncontract.SessionCategoryMain)
	if _, ok := tab.selectedDetail.(sessionPickerSelectedDetailLoadingState); !ok {
		t.Fatalf("selected detail = %T, want loading placeholders", tab.selectedDetail)
	}
	if got := selectedDetailIdentity(t, tab.selectedDetail).sessionID; got != first.SessionID {
		t.Fatalf("selected detail session = %q, want %q", got, first.SessionID)
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*sessionPickerModel)
	tab = model.tab(sessioncontract.SessionCategoryMain)
	if selected := selectedPickerSessionID(t, tab.selected); selected != second.SessionID {
		t.Fatalf("selection = %q, want %q while detail is pending", selected, second.SessionID)
	}
	if model.result != nil {
		t.Fatalf("movement while detail is pending produced result %+v", model.result)
	}

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = next.(*sessionPickerModel)
	opened, ok := model.result.(sessionPickerOpenResult)
	if !ok || opened.sessionID != second.SessionID {
		t.Fatalf("opened session = %+v, want %q", model.result, second.SessionID)
	}
}

func TestSessionPickerSelectedDetailProjectsPartialEnvironmentWithoutStaleValues(t *testing.T) {
	summary := pickerTestSummary(t, "partial-detail", time.Unix(1_900_000_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{
			summary.SessionID: {
				Environment: serverapi.SessionExecutionEnvironment{
					SessionID: summary.SessionID,
					Workspace: serverapi.AvailableSessionExecutionWorkspace("/workspaces/project"),
					Branch:    serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableDetachedHead),
					Auth:      serverapi.AvailableSessionExecutionAuth(serverapi.SessionExecutionAuth{Provider: "openai", Method: serverapi.SessionExecutionAuthMethodNone}),
					Model:     serverapi.FailedSessionExecutionModel(serverapi.SessionExecutionFieldError{Code: serverapi.SessionExecutionFieldErrorSourceFailure, Message: "model lookup failed"}),
				},
			},
		},
		errs: map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{summary}, environmentClient)
	runSessionPickerCommands(t, model, model.Init())
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))

	detail := model.tab(sessioncontract.SessionCategoryMain).selectedDetail
	ready := selectedDetailReadyState(t, detail)
	if got, ok := ready.workspace.Value(); !ok || got.Path != "/workspaces/project" {
		t.Fatalf("workspace projection = %+v/%v", got, ok)
	}
	if got, ok := ready.branch.UnavailableReason(); !ok || got != serverapi.SessionExecutionBranchUnavailableDetachedHead {
		t.Fatalf("branch projection = %q/%v", got, ok)
	}
	if got, ok := ready.auth.Value(); !ok || got.Method != serverapi.SessionExecutionAuthMethodNone {
		t.Fatalf("auth projection = %+v/%v", got, ok)
	}
	if _, ok := ready.model.Value(); ok {
		t.Fatal("failed model field retained a stale available value")
	}
	if _, ok := selectedDetailFailure(model); !ok {
		t.Fatal("selected-detail field failure did not enter startup status")
	}
	failure, _ := selectedDetailFailure(model)
	if failure.Kind != sessionPickerFailureDetailField {
		t.Fatalf("selected-detail field failure kind = %q, want typed field failure", failure.Kind)
	}
}

func TestSessionPickerSelectedDetailFailureRemovesStaleValuesAndKeepsNavigationUsable(t *testing.T) {
	summary := pickerTestSummary(t, "failed-detail", time.Unix(1_900_000_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{},
		errs: map[runtimeids.SessionID]error{
			summary.SessionID: errors.New("environment lookup failed"),
		},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{summary}, environmentClient)
	runSessionPickerCommands(t, model, model.Init())
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))

	tab := model.tab(sessioncontract.SessionCategoryMain)
	if tab.bodyPhase != sessionPickerBodyReady {
		t.Fatalf("body phase = %q, want ready after detail failure", tab.bodyPhase)
	}
	if selected := selectedPickerSessionID(t, tab.selected); selected != summary.SessionID {
		t.Fatalf("selected session = %q, want %q", selected, summary.SessionID)
	}
	if _, ok := tab.selectedDetail.(sessionPickerSelectedDetailFailedState); !ok {
		t.Fatalf("detail = %T, want failed", tab.selectedDetail)
	}
	if selectedDetailHasAnyAvailableValue(tab.selectedDetail) {
		t.Fatal("failed selected detail retained stale facts")
	}
	failure, ok := selectedDetailFailure(model)
	if !ok {
		t.Fatal("selected-detail failure did not enter startup status projection")
	}
	if failure.Kind != sessionPickerFailureDetailRequest || !errors.Is(failure.Diagnostic, environmentClient.errs[summary.SessionID]) {
		t.Fatalf("selected-detail request failure = %+v, want typed request failure with diagnostic", failure)
	}
}

func TestSessionPickerSelectionReplacementClearsObsoleteDetailFailure(t *testing.T) {
	first := pickerTestSummary(t, "failed-first", time.Unix(1_900_000_000, 0).UTC())
	second := pickerTestSummary(t, "ready-second", time.Unix(1_899_999_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{
			second.SessionID: sessionPickerDetailEnvironment(t, second.SessionID),
		},
		errs: map[runtimeids.SessionID]error{
			first.SessionID: errors.New("first detail failed"),
		},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{first, second}, environmentClient)
	runSessionPickerCommands(t, model, model.Init())
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
	if _, ok := selectedDetailFailure(model); !ok {
		t.Fatal("initial selected-detail failure was not recorded")
	}

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*sessionPickerModel)

	if _, ok := selectedDetailFailure(model); ok {
		t.Fatal("obsolete selected-detail failure survived selection replacement")
	}
	if got := selectedPickerSessionID(t, model.tab(sessioncontract.SessionCategoryMain).selected); got != second.SessionID {
		t.Fatalf("selected session = %q, want %q", got, second.SessionID)
	}
}

func TestSessionPickerFreshRetryClearsObsoleteDetailFailure(t *testing.T) {
	summary := pickerTestSummary(t, "retry-detail", time.Unix(1_900_000_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{},
		errs: map[runtimeids.SessionID]error{
			summary.SessionID: errors.New("detail failed before retry"),
		},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{summary}, environmentClient)
	runSessionPickerCommands(t, model, model.Init())
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
	tab := model.tab(sessioncontract.SessionCategoryMain)
	tab.bodyPhase = sessionPickerBodyFailed
	if _, ok := selectedDetailFailure(model); !ok {
		t.Fatal("selected-detail failure was not recorded before retry")
	}

	command := pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("fresh retry did not schedule a page request")
	}

	if _, ok := selectedDetailFailure(model); ok {
		t.Fatal("obsolete selected-detail failure survived fresh retry")
	}
}

func TestSessionPickerSelectedDetailCancelsObsoleteRequestsAndIgnoresStaleCompletions(t *testing.T) {
	first := pickerTestSummary(t, "cancel-first", time.Unix(1_900_000_000, 0).UTC())
	second := pickerTestSummary(t, "cancel-second", time.Unix(1_899_999_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{},
		errs:      map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{first, second}, environmentClient)
	initializeSessionPickerPagesWithoutFollowup(t, model)
	_ = pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown})
	firstRequest := model.tab(model.activeTab).detailRequest

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*sessionPickerModel)
	secondRequest := model.tab(model.activeTab).detailRequest
	if firstRequest == secondRequest {
		t.Fatal("selection change reused the obsolete detail request")
	}
	if !firstRequest.IsCanceled() {
		t.Fatal("selection change did not cancel obsolete detail request")
	}
	if selectedDetailIdentity(t, model.tab(sessioncontract.SessionCategoryMain).selectedDetail).sessionID != second.SessionID {
		t.Fatal("selection change did not retain the new detail anchor")
	}

	next, _ = model.Update(sessionPickerSelectedDetailLoadedMsg{
		Category:   sessioncontract.SessionCategoryMain,
		SessionID:  first.SessionID,
		Generation: firstRequest.generation,
		Response:   sessionPickerDetailEnvironment(t, first.SessionID),
	})
	model = next.(*sessionPickerModel)
	detail := model.tab(sessioncontract.SessionCategoryMain).selectedDetail
	if selectedDetailIdentity(t, detail).sessionID != second.SessionID {
		t.Fatalf("stale completion overwrote current detail: %+v", detail)
	}
	_ = secondRequest
}

func TestSessionPickerSelectedDetailCancellationAcrossRetryTabsAndExit(t *testing.T) {
	main := pickerTestSummary(t, "cancel-main", time.Unix(1_900_000_000, 0).UTC())
	subagent := pickerTestSummary(t, "cancel-subagent", time.Unix(1_899_999_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{},
		errs:      map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{main}, environmentClient)
	initializeSessionPickerPagesWithoutFollowup(t, model)
	_ = pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown})
	mainRequest := model.tab(model.activeTab).detailRequest

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = next.(*sessionPickerModel)
	model.tab(sessioncontract.SessionCategorySubagent).replaceSegments(serverapi.SessionPageResponse{
		Category: sessioncontract.SessionCategorySubagent,
		Sessions: []clientui.SessionSummary{subagent},
	})
	_ = pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown})
	subagentRequest := model.tab(model.activeTab).detailRequest
	if mainRequest == subagentRequest {
		t.Fatal("tabs shared one selected-detail request")
	}

	model.startBodyRequest(sessioncontract.SessionCategorySubagent, sessionPickerBodyRequestRetry)
	if !subagentRequest.IsCanceled() {
		t.Fatal("fresh retry did not cancel discarded selected detail")
	}
	if selectedDetailIdentity(t, model.tab(sessioncontract.SessionCategoryMain).selectedDetail).sessionID != main.SessionID {
		t.Fatal("inactive tab detail state was not retained")
	}

	next, cmd := model.Update(tea.KeyCtrlC)
	model = next.(*sessionPickerModel)
	if _, ok := model.result.(sessionPickerCancelResult); cmd == nil || !ok {
		t.Fatalf("picker exit = result %T/cmd=%v, want cancellation", model.result, cmd != nil)
	}
	if model.main.detailRequest != nil || model.subagents.detailRequest != nil {
		t.Fatal("picker exit did not cancel both tab detail requests")
	}
}

func TestSessionPickerSelectedDetailRetainsInactiveTabStateAndRejectsCrossTabCompletion(t *testing.T) {
	main := pickerTestSummary(t, "retain-main", time.Unix(1_900_000_000, 0).UTC())
	subagent := pickerTestSummary(t, "retain-subagent", time.Unix(1_899_999_000, 0).UTC())
	environmentClient := &sessionPickerEnvironmentClient{
		responses: map[runtimeids.SessionID]serverapi.SessionExecutionEnvironmentResponse{
			main.SessionID:     sessionPickerDetailEnvironment(t, main.SessionID),
			subagent.SessionID: sessionPickerDetailEnvironment(t, subagent.SessionID),
		},
		errs: map[runtimeids.SessionID]error{},
	}
	model := newPickerDetailTestModel(t, []clientui.SessionSummary{main}, environmentClient)
	runSessionPickerCommands(t, model, model.Init())
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))
	mainDetail := model.tab(sessioncontract.SessionCategoryMain).selectedDetail

	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = next.(*sessionPickerModel)
	model.tab(sessioncontract.SessionCategorySubagent).replaceSegments(serverapi.SessionPageResponse{
		Category: sessioncontract.SessionCategorySubagent,
		Sessions: []clientui.SessionSummary{subagent},
	})
	runSessionPickerCommands(t, model, pickerUpdateCommand(t, model, tea.KeyMsg{Type: tea.KeyDown}))

	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	model = next.(*sessionPickerModel)
	retained := model.tab(sessioncontract.SessionCategoryMain).selectedDetail
	if !reflect.DeepEqual(retained, mainDetail) {
		t.Fatal("switching tabs copied or discarded inactive-tab detail")
	}
	next, _ = model.Update(sessionPickerSelectedDetailLoadedMsg{
		Category:   sessioncontract.SessionCategoryMain,
		SessionID:  main.SessionID,
		Generation: selectedDetailIdentity(t, mainDetail).generation,
		Response:   sessionPickerDetailEnvironment(t, main.SessionID),
	})
	model = next.(*sessionPickerModel)
	if selectedDetailIdentity(t, model.tab(sessioncontract.SessionCategorySubagent).selectedDetail).sessionID != subagent.SessionID {
		t.Fatal("cross-tab stale completion changed inactive tab detail")
	}
}

func TestSessionPickerRelativeAgeUsesSemanticBucketsAndControlledClock(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		age  time.Duration
		want sessionPickerRelativeAgeBucket
	}{
		{name: "just now", age: 10 * time.Second, want: sessionPickerRelativeAgeJustNow},
		{name: "minutes", age: 7 * time.Minute, want: sessionPickerRelativeAgeMinutes},
		{name: "hours", age: 3 * time.Hour, want: sessionPickerRelativeAgeHours},
		{name: "days", age: 2 * 24 * time.Hour, want: sessionPickerRelativeAgeDays},
		{name: "weeks", age: 3 * 7 * 24 * time.Hour, want: sessionPickerRelativeAgeWeeks},
		{name: "months", age: 70 * 24 * time.Hour, want: sessionPickerRelativeAgeMonths},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := relativeSessionAge(now.Add(-test.age), now)
			if got.Bucket != test.want {
				t.Fatalf("relative age bucket = %q, want %q", got.Bucket, test.want)
			}
		})
	}
}

func TestSessionPickerRelativeAgeHandlesFutureClockSkewSemantically(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	got := relativeSessionAge(now.Add(15*time.Minute), now)
	if got.Bucket != sessionPickerRelativeAgeFuture {
		t.Fatalf("future-skew bucket = %q, want %q", got.Bucket, sessionPickerRelativeAgeFuture)
	}
}

func TestSessionPickerRejectsInvalidRecencyBeforeRendering(t *testing.T) {
	for _, updatedAt := range []time.Time{
		time.Time{},
		time.Unix(0, 0).UTC(),
		time.Unix(-1, 0).UTC(),
	} {
		summary := pickerTestSummary(t, "invalid-recency", updatedAt)
		if err := summary.Validate(); err == nil {
			t.Fatalf("summary with updated_at=%v unexpectedly validated", updatedAt)
		}
	}
}
