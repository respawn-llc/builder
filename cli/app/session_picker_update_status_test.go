package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"core/cli/app/internal/connectionstate"
	"core/shared/config"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type blockingUpdateStatusClient struct {
	mu      sync.Mutex
	calls   int
	started chan context.Context
	release chan serverapi.UpdateStatusResponse
}

func newBlockingUpdateStatusClient() *blockingUpdateStatusClient {
	return &blockingUpdateStatusClient{
		started: make(chan context.Context, 4),
		release: make(chan serverapi.UpdateStatusResponse, 4),
	}
}

func (c *blockingUpdateStatusClient) GetUpdateStatus(
	ctx context.Context,
	_ serverapi.UpdateStatusRequest,
) (serverapi.UpdateStatusResponse, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	c.started <- ctx
	select {
	case response := <-c.release:
		return response, nil
	case <-ctx.Done():
		return serverapi.UpdateStatusResponse{}, ctx.Err()
	}
}

func (*blockingUpdateStatusClient) GetServerReadiness(
	context.Context,
	serverapi.ServerReadinessRequest,
) (serverapi.ServerReadinessResponse, error) {
	return serverapi.ServerReadinessResponse{}, errors.New("unexpected readiness request")
}

func (c *blockingUpdateStatusClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSessionPickerUpdateStatusRunsIndependentlyOfInitialPageLoads(t *testing.T) {
	t.Parallel()

	updates := newBlockingUpdateStatusClient()
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}}
	model := newSessionPickerModel(context.Background(), loader, "dark", sessionPickerHeaderInfo{
		updateStatus: updates,
	})

	batchMessage := model.Init()()
	batch, ok := batchMessage.(tea.BatchMsg)
	if !ok {
		t.Fatalf("picker init message = %T, want batch", batchMessage)
	}

	updateCompleted := make(chan tea.Msg, 1)
	for _, command := range batch {
		go func(command tea.Cmd) {
			updateCompleted <- command()
		}(command)
	}

	select {
	case <-updates.started:
	case <-time.After(time.Second):
		t.Fatal("picker did not start update collection")
	}

	deadline := time.After(time.Second)
	for model.main.bodyPhase == sessionPickerBodyInitialLoading || model.subagents.bodyPhase == sessionPickerBodyInitialLoading {
		select {
		case message := <-updateCompleted:
			next, _ := model.Update(message)
			updated, ok := next.(*sessionPickerModel)
			if !ok || updated != model {
				t.Fatalf("picker update replacement = %T/%p, want %p", next, updated, model)
			}
		case <-deadline:
			t.Fatalf("page loads did not complete while update request remained blocked: main=%q subagents=%q", model.main.bodyPhase, model.subagents.bodyPhase)
		}
	}

	updates.release <- serverapi.UpdateStatusResponse{
		Result: serverapi.AvailableUpdateStatusResult("1.0.0", "1.1.0"),
	}
	select {
	case message := <-updateCompleted:
		next, _ := model.Update(message)
		if updated, ok := next.(*sessionPickerModel); !ok || updated != model {
			t.Fatalf("picker update replacement = %T/%p, want %p", next, updated, model)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked update request did not complete")
	}
	if model.updateStatus.kind != sessionPickerUpdateAvailable {
		t.Fatalf("update state = %q, want available", model.updateStatus.kind)
	}
}

func TestSessionPickerUpdateStatusMapsOnlyValidatedResultVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		resp serverapi.UpdateStatusResponse
		want sessionPickerUpdateKind
	}{
		{
			name: "current",
			resp: serverapi.UpdateStatusResponse{Result: serverapi.CurrentUpdateStatusResult("1.0.0", "1.0.0")},
			want: sessionPickerUpdateCurrent,
		},
		{
			name: "available",
			resp: serverapi.UpdateStatusResponse{Result: serverapi.AvailableUpdateStatusResult("1.0.0", "1.1.0")},
			want: sessionPickerUpdateAvailable,
		},
		{
			name: "network unavailable",
			resp: serverapi.UpdateStatusResponse{Result: serverapi.CheckUnavailableUpdateStatusResult()},
			want: sessionPickerUpdateCheckUnavailable,
		},
		{
			name: "release mode invariant failure",
			resp: serverapi.UpdateStatusResponse{Result: serverapi.FailedUpdateStatusResult("internal invariant")},
			want: sessionPickerUpdateCheckFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})
			next, command := model.Update(sessionPickerUpdateStatusMsg{
				response: &test.resp,
				outcome:  connectionstate.Classify(connectionstate.OperationUnary, nil),
			})
			if updated, ok := next.(*sessionPickerModel); !ok || updated != model {
				t.Fatalf("picker update replacement = %T/%p, want %p", next, updated, model)
			}
			if command != nil {
				t.Fatalf("validated result scheduled unexpected command %T", command)
			}
			if model.updateStatus.kind != test.want {
				t.Fatalf("update state = %q, want %q", model.updateStatus.kind, test.want)
			}
		})
	}
}

func TestSessionPickerUpdateStatusRequestsAgainForEachPickerOpen(t *testing.T) {
	t.Parallel()

	updates := newBlockingUpdateStatusClient()
	for range 2 {
		model := newSessionPickerModel(context.Background(), &recordingSessionPageLoader{}, "dark", sessionPickerHeaderInfo{
			updateStatus: updates,
		})
		command := model.collectUpdateStatusCmd()
		if command == nil {
			t.Fatal("picker update command is nil")
		}
		completed := make(chan tea.Msg, 1)
		go func() { completed <- command() }()
		select {
		case <-updates.started:
		case <-time.After(time.Second):
			t.Fatal("picker did not request update status")
		}
		updates.release <- serverapi.UpdateStatusResponse{
			Result: serverapi.CheckUnavailableUpdateStatusResult(),
		}
		select {
		case message := <-completed:
			model.Update(message)
		case <-time.After(time.Second):
			t.Fatal("picker update command did not complete")
		}
	}
	if got := updates.callCount(); got != 2 {
		t.Fatalf("update requests = %d, want one per picker open", got)
	}
}

func TestSessionPickerUpdateCompletionKeepsConnectionAndStatusOwnershipExhaustive(t *testing.T) {
	t.Parallel()

	owner := connectionstate.NewOwner()
	model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Notice:     &startupPickerNotice{Text: "surface notice"},
		connection: owner,
	})
	original := model.updateStatus

	apply := func(outcome connectionstate.Outcome) {
		t.Helper()
		next, command := model.Update(sessionPickerUpdateStatusMsg{outcome: outcome})
		if updated, ok := next.(*sessionPickerModel); !ok || updated != model {
			t.Fatalf("picker update replacement = %T/%p, want %p", next, updated, model)
		}
		if command != nil {
			t.Fatalf("operation outcome %q scheduled unexpected command", outcome.Kind())
		}
		if model.updateStatus != original {
			t.Fatalf("operation outcome %q mutated update-row state from %+v to %+v", outcome.Kind(), original, model.updateStatus)
		}
	}

	apply(connectionstate.Classify(connectionstate.OperationUnary, context.Canceled))
	if owner.IsDisconnected() {
		t.Fatal("picker-context cancellation marked shared connection disconnected")
	}
	apply(connectionstate.Classify(connectionstate.OperationUnary, io.EOF))
	if !owner.IsDisconnected() {
		t.Fatal("connection-loss completion did not update the shared owner")
	}
	projection := projectStartupPickerStatus(model.startupStatus)
	if projection.Notice.Text == "" || projection.Disconnected {
		t.Fatalf("surface-owned notice lost precedence after disconnect: %+v", projection)
	}
	model.startupStatus.notice = startupPickerNotice{}
	if !projectStartupPickerStatus(model.startupStatus).Disconnected {
		t.Fatal("disconnect did not fill empty startup projection")
	}
	apply(connectionstate.Classify(connectionstate.OperationUnary, context.DeadlineExceeded))
	if !owner.IsDisconnected() {
		t.Fatal("reachability-inconclusive update failure cleared disconnect")
	}
	if _, ok := model.startupStatus.failure(model.activeTab, sessionPickerOperationUpdateStatus); !ok {
		t.Fatal("reachability-inconclusive update failure did not use the startup error surface")
	}
	apply(connectionstate.Classify(connectionstate.OperationUnary, errors.New("server operation failed")))
	if owner.IsDisconnected() {
		t.Fatal("reachability-confirming update failure did not clear disconnect")
	}
}

func TestSessionPickerUpdateStatusInvalidContractFailsFastOrExitsWithoutRowMutation(t *testing.T) {
	t.Parallel()

	invalidOutcome := connectionstate.InvalidContract(errors.New("malformed update status response"))
	t.Run("release exits through startup error surface", func(t *testing.T) {
		model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})
		before := model.updateStatus

		next, command := model.Update(sessionPickerUpdateStatusMsg{outcome: invalidOutcome})
		if updated, ok := next.(*sessionPickerModel); !ok || updated != model {
			t.Fatalf("picker update replacement = %T/%p, want %p", next, updated, model)
		}
		if command == nil {
			t.Fatal("invalid update contract did not schedule clean picker exit")
		}
		if model.updateStatus != before {
			t.Fatalf("invalid update contract mutated update-row state from %+v to %+v", before, model.updateStatus)
		}
		if _, ok := model.startupStatus.failure(model.activeTab, sessionPickerOperationUpdateStatus); !ok {
			t.Fatal("invalid update contract did not reach the startup error surface")
		}
		if _, ok := model.result.(sessionPickerCancelResult); !ok {
			t.Fatalf("invalid update contract result = %T, want clean cancel", model.result)
		}
	})
	t.Run("debug panics", func(t *testing.T) {
		model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
			StatusRequest: uiStatusRequest{Settings: config.Settings{Debug: true}},
		})
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("debug invalid update contract did not panic")
			}
		}()
		model.Update(sessionPickerUpdateStatusMsg{outcome: invalidOutcome})
	})
}

func TestSessionPickerLifecycleCloseCancelsOnlyUpdateRequestAndIgnoresLateCompletion(t *testing.T) {
	t.Parallel()

	updates := newBlockingUpdateStatusClient()
	owner := connectionstate.NewOwner()
	lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
		Loader: &recordingSessionPageLoader{},
		Theme:  "dark",
		Header: sessionPickerHeaderInfo{
			updateStatus: updates,
			connection:   owner,
		},
	})
	command := lifecycle.picker.collectUpdateStatusCmd()
	completed := make(chan tea.Msg, 1)
	go func() { completed <- command() }()
	var requestContext context.Context
	select {
	case requestContext = <-updates.started:
	case <-time.After(time.Second):
		t.Fatal("picker update request did not start")
	}

	lifecycle.Close()
	select {
	case <-requestContext.Done():
		if !errors.Is(requestContext.Err(), context.Canceled) {
			t.Fatalf("picker update context error = %v, want canceled", requestContext.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("picker close did not cancel update request")
	}
	select {
	case message := <-completed:
		before := lifecycle.picker.updateStatus
		lifecycle.Update(message)
		if lifecycle.picker.updateStatus != before {
			t.Fatalf("late update completion mutated closed picker from %+v to %+v", before, lifecycle.picker.updateStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled picker update command did not complete")
	}
	if owner.IsDisconnected() {
		t.Fatal("picker-context cancellation marked shared connection disconnected")
	}
}
