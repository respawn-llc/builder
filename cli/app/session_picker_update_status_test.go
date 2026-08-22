package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	serverpb "core/shared/protoapi/gen/kent/api/server"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"google.golang.org/protobuf/types/known/emptypb"
)

type updateStatusCompletion struct {
	response *serverpb.GetUpdateStatusSuccess
	err      error
}

type blockingUpdateStatusClient struct {
	mu         sync.Mutex
	calls      int
	started    chan context.Context
	completion chan updateStatusCompletion
}

func newBlockingUpdateStatusClient() *blockingUpdateStatusClient {
	return &blockingUpdateStatusClient{
		started:    make(chan context.Context, 4),
		completion: make(chan updateStatusCompletion, 4),
	}
}

func (c *blockingUpdateStatusClient) GetUpdateStatus(
	ctx context.Context,
	_ *emptypb.Empty,
) (*serverpb.GetUpdateStatusSuccess, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	c.started <- ctx
	select {
	case completion := <-c.completion:
		return completion.response, completion.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingUpdateStatusClient) GetReadiness(
	context.Context,
	*emptypb.Empty,
) (*serverpb.GetReadinessSuccess, error) {
	return nil, errors.New("unexpected readiness request")
}

func (c *blockingUpdateStatusClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSessionPickerUpdateStatusRunsIndependentlyOfInitialPageLoads(t *testing.T) {
	t.Parallel()

	updates := newBlockingUpdateStatusClient()
	loader := &recordingSessionPageLoader{responses: func(request sessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}}
	model := newSessionPickerModel(context.Background(), loader, "dark", sessionPickerHeaderInfo{
		updateStatus: updates,
	})

	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("picker initialization did not schedule independent operations")
	}
	completed := make(chan tea.Msg, len(batch))
	for _, command := range batch {
		go func(command tea.Cmd) {
			completed <- command()
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
		case message := <-completed:
			model.Update(message)
		case <-deadline:
			t.Fatal("session pages did not load while the update request remained blocked")
		}
	}
	headerHeight := lipgloss.Height(model.renderHeader())

	updates.completion <- updateStatusCompletion{
		response: availableUpdateStatusSuccess("1.0.0", "1.1.0"),
	}
	select {
	case message := <-completed:
		model.Update(message)
	case <-time.After(time.Second):
		t.Fatal("update request did not complete")
	}
	if height := lipgloss.Height(model.renderHeader()); height != headerHeight+1 {
		t.Fatalf("header height after update = %d, want %d", height, headerHeight+1)
	}
}

func TestSessionPickerRequestsUpdateStatusForEachOpen(t *testing.T) {
	t.Parallel()

	updates := newBlockingUpdateStatusClient()
	for range 2 {
		model := newSessionPickerModel(context.Background(), &recordingSessionPageLoader{}, "dark", sessionPickerHeaderInfo{
			updateStatus: updates,
		})
		completed := make(chan tea.Msg, 1)
		go func() { completed <- model.collectUpdateStatusCmd()() }()
		select {
		case <-updates.started:
		case <-time.After(time.Second):
			t.Fatal("picker did not request update status")
		}
		updates.completion <- updateStatusCompletion{
			response: checkUnavailableUpdateStatusSuccess(),
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

func currentUpdateStatusSuccess(current, latest string) *serverpb.GetUpdateStatusSuccess {
	return &serverpb.GetUpdateStatusSuccess{Status: &serverpb.UpdateStatus{
		Status: &serverpb.UpdateStatus_Current{Current: &serverpb.UpdateVersions{
			CurrentVersion: current,
			LatestVersion:  latest,
		}},
	}}
}

func availableUpdateStatusSuccess(current, latest string) *serverpb.GetUpdateStatusSuccess {
	return &serverpb.GetUpdateStatusSuccess{Status: &serverpb.UpdateStatus{
		Status: &serverpb.UpdateStatus_Available{Available: &serverpb.UpdateVersions{
			CurrentVersion: current,
			LatestVersion:  latest,
		}},
	}}
}

func checkUnavailableUpdateStatusSuccess() *serverpb.GetUpdateStatusSuccess {
	return &serverpb.GetUpdateStatusSuccess{Status: &serverpb.UpdateStatus{
		Status: &serverpb.UpdateStatus_CheckUnavailable{CheckUnavailable: &emptypb.Empty{}},
	}}
}

func failedUpdateStatusSuccess(cause string) *serverpb.GetUpdateStatusSuccess {
	return &serverpb.GetUpdateStatusSuccess{Status: &serverpb.UpdateStatus{
		Status: &serverpb.UpdateStatus_CheckFailed{CheckFailed: &serverpb.UpdateCheckFailed{Cause: cause}},
	}}
}

func TestSessionPickerUpdateOperationFailureStaysLocalToUpdateRow(t *testing.T) {
	t.Parallel()

	model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{})
	headerHeight := lipgloss.Height(model.renderHeader())
	model.Update(sessionPickerUpdateStatusMsg{err: errors.New("server status unavailable")})

	if height := lipgloss.Height(model.renderHeader()); height != headerHeight+1 {
		t.Fatalf("header height after local update failure = %d, want %d", height, headerHeight+1)
	}
	if rendered := newSessionPickerStatusSurface(model.startupStatus).RenderStatus(80); rendered != "" {
		t.Fatalf("update operation failure leaked into session-list status: %q", rendered)
	}
}

func TestSessionPickerLifecycleCloseCancelsUpdateRequestAndIgnoresCompletion(t *testing.T) {
	t.Parallel()

	updates := newBlockingUpdateStatusClient()
	lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
		Loader: &recordingSessionPageLoader{},
		Theme:  "dark",
		Header: sessionPickerHeaderInfo{
			updateStatus: updates,
		},
	})
	completed := make(chan tea.Msg, 1)
	go func() { completed <- lifecycle.picker.collectUpdateStatusCmd()() }()
	var requestContext context.Context
	select {
	case requestContext = <-updates.started:
	case <-time.After(time.Second):
		t.Fatal("picker update request did not start")
	}
	headerBeforeClose := lifecycle.picker.renderHeader()

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
		lifecycle.Update(message)
		if header := lifecycle.picker.renderHeader(); header != headerBeforeClose {
			t.Fatal("closed picker rendered late update completion")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled picker update command did not complete")
	}
}
