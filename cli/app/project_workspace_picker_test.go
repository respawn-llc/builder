package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"core/cli/app/internal/projectbinding"
	projectpb "core/shared/protoapi/gen/kent/api/project"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"
)

type workspacePickerLoader struct {
	calls     []*projectpb.ProjectWorkspaceListRequest
	responses map[int32]*projectpb.ListProjectWorkspacesSuccess
	errors    map[int32]error
}

type delayedWorkspacePickerLoader struct {
	base        *workspacePickerLoader
	blockOffset int32
	started     chan struct{}
	release     chan struct{}
}

func (l *delayedWorkspacePickerLoader) ListProjectWorkspaces(ctx context.Context, request *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error) {
	if request.Offset == l.blockOffset {
		close(l.started)
		select {
		case <-l.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return l.base.ListProjectWorkspaces(ctx, request)
}

func (l *workspacePickerLoader) ListProjectWorkspaces(_ context.Context, request *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error) {
	l.calls = append(l.calls, request)
	if err := l.errors[request.Offset]; err != nil {
		return nil, err
	}
	return l.responses[request.Offset], nil
}

func workspacePickerResponse(projectID string, offset int32, rows int, next bool) *projectpb.ListProjectWorkspacesSuccess {
	response := &projectpb.ListProjectWorkspacesSuccess{ProjectId: projectID, Offset: offset}
	for index := 0; index < rows; index++ {
		id := fmt.Sprintf("workspace-%03d", int(offset)+index)
		response.Workspaces = append(response.Workspaces, &projectpb.ProjectWorkspaceCatalogSummary{
			WorkspaceId: id, DisplayName: id, RootPath: "/workspaces/" + id,
		})
	}
	if next {
		value := offset + projectWorkspacePickerPageSize
		response.NextOffset = &value
	}
	return response
}

func applyWorkspacePickerCommand(model *projectWorkspacePickerModel, command tea.Cmd) {
	if command == nil {
		return
	}
	switch message := command().(type) {
	case tea.BatchMsg:
		for _, child := range message {
			applyWorkspacePickerCommand(model, child)
		}
	default:
		_, next := model.Update(message)
		applyWorkspacePickerCommand(model, next)
	}
}

func TestProjectWorkspacePickerFirstPageLifecycle(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0: workspacePickerResponse("project-1", 0, 2, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())

	if model.phase != projectWorkspacePickerReady || len(model.workspaces()) != 2 {
		t.Fatalf("picker phase/rows = %d/%d, want ready/two", model.phase, len(model.workspaces()))
	}
	if _, ok := model.result.(projectbinding.WorkspacePickerSelected); ok {
		t.Fatal("multiple rows must remain selectable")
	}
	if model.workspaces()[0].WorkspaceId != "workspace-000" || model.workspaces()[1].WorkspaceId != "workspace-001" {
		t.Fatalf("catalog order was changed: %+v", model.workspaces())
	}
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if model.result != nil {
		t.Fatal("q must not produce a picker result")
	}
}

func TestProjectWorkspacePickerExitActions(t *testing.T) {
	newModel := func() *projectWorkspacePickerModel {
		loader := &workspacePickerLoader{
			responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
				0: workspacePickerResponse("project-1", 0, 2, false),
			},
			errors: map[int32]error{},
		}
		model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
		applyWorkspacePickerCommand(model, model.Init())
		return model
	}
	t.Run("select", func(t *testing.T) {
		model := newModel()
		model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if _, ok := model.result.(projectbinding.WorkspacePickerSelected); !ok {
			t.Fatalf("Enter result = %T, want selection", model.result)
		}
	})
	t.Run("back", func(t *testing.T) {
		model := newModel()
		model.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if _, ok := model.result.(projectbinding.WorkspacePickerBack); !ok {
			t.Fatalf("Esc result = %T, want Back", model.result)
		}
	})
	t.Run("exit", func(t *testing.T) {
		model := newModel()
		model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if _, ok := model.result.(projectbinding.WorkspacePickerExit); !ok {
			t.Fatalf("Ctrl+C result = %T, want Exit", model.result)
		}
	})
}

func TestProjectWorkspacePickerShowsOperationDiagnosticAtEdge(t *testing.T) {
	diagnostic := errors.New("next page is unavailable")
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0: workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{50: diagnostic},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	for range 49 {
		_, command := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		applyWorkspacePickerCommand(model, command)
		if model.phase == projectWorkspacePickerReadyWithEdgeFailure {
			break
		}
	}
	if model.phase != projectWorkspacePickerReadyWithEdgeFailure {
		t.Fatalf("phase after edge failure = %d", model.phase)
	}
	view := model.View()
	if !strings.Contains(view, diagnostic.Error()) {
		t.Fatalf("shared status omitted operation diagnostic %q", diagnostic)
	}
	if strings.Contains(ansi.Strip(model.renderEdgeStatus(&model.nextEdge)), diagnostic.Error()) {
		t.Fatal("edge feedback duplicated the operation diagnostic")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := model.result.(projectbinding.WorkspacePickerSelected); !ok {
		t.Fatalf("Enter with resident rows = %T, want selected workspace", model.result)
	}
}

func TestProjectWorkspacePickerSelectsExactSingleWorkspace(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0: workspacePickerResponse("project-1", 0, 1, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	selected, ok := model.result.(projectbinding.WorkspacePickerSelected)
	if !ok || selected.Workspace.WorkspaceId != "workspace-000" {
		t.Fatalf("single-row result = %#v, want selected workspace", model.result)
	}
}

func TestProjectWorkspacePickerTraversesCatalogInServerOrderWithTwoPageWindow(t *testing.T) {
	loader := &workspacePickerLoader{responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{}, errors: map[int32]error{}}
	for offset := int32(0); offset < 250; offset += projectWorkspacePickerPageSize {
		rows := projectWorkspacePickerPageSize
		next := true
		if offset == 200 {
			rows = 1
			next = false
		}
		loader.responses[offset] = workspacePickerResponse("project-1", offset, rows, next)
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	for range 220 {
		_, command := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		applyWorkspacePickerCommand(model, command)
		if len(model.segments) > startupPickerResidentPageLimit {
			t.Fatalf("resident pages = %d, want at most %d", len(model.segments), startupPickerResidentPageLimit)
		}
	}
	offsets := make([]int32, 0, len(loader.calls))
	for _, call := range loader.calls {
		offsets = append(offsets, call.Offset)
	}
	want := []int32{0, 50, 100, 150, 200}
	if len(offsets) != len(want) {
		t.Fatalf("page request offsets = %v, want %v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("page request offsets = %v, want %v", offsets, want)
		}
	}
	resident := model.workspaces()
	if resident[0].WorkspaceId != "workspace-150" || resident[len(resident)-1].WorkspaceId != "workspace-200" {
		t.Fatalf("resident order = %q ... %q", resident[0].WorkspaceId, resident[len(resident)-1].WorkspaceId)
	}
}

func TestProjectWorkspacePickerSuccessfulEmptyNextIsDurablyExhausted(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:  workspacePickerResponse("project-1", 0, 2, true),
			50: workspacePickerResponse("project-1", 50, 0, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	applyWorkspacePickerCommand(model, command)
	before := len(loader.calls)
	_, command = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	applyWorkspacePickerCommand(model, command)
	if len(loader.calls) != before {
		t.Fatalf("exhausted edge made another read: calls=%d before=%d", len(loader.calls), before)
	}
}

func TestProjectWorkspacePickerIgnoresStalePrefetchThatWouldEvictAnchors(t *testing.T) {
	base := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
			100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{},
	}
	loader := &delayedWorkspacePickerLoader{
		base: base, blockOffset: 100, started: make(chan struct{}), release: make(chan struct{}),
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
	command := model.startPageRequest(100, projectWorkspacePickerPageNext)
	messages := make(chan tea.Msg, 1)
	go func() { messages <- command() }()
	<-loader.started
	model.selectIndex(0)
	close(loader.release)
	updated, _ := model.Update(<-messages)
	if updated != model || len(model.segments) != 2 || model.segments[0].offset != 0 || model.segments[1].offset != 50 {
		t.Fatalf("stale page changed resident window: %+v", model.segments)
	}
	if model.selected == nil || model.selected.offset != 0 || model.selected.index != 0 || model.selected.workspace != "workspace-000" {
		t.Fatalf("stale page changed exact selection: %+v", model.selected)
	}
}

func TestProjectWorkspacePickerCrossesPreviousBoundaryOntoNearestRow(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:  workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50: workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	model.segments = []projectWorkspacePickerPageSegment{
		{generation: 10, offset: 50, workspaces: cloneProjectWorkspaceCatalogRows(loader.responses[50].Workspaces)},
	}
	model.selected = &projectWorkspacePickerOccurrence{generation: 10, offset: 50, index: 0, workspace: "workspace-050"}
	model.viewport = model.selected
	model.cursor = 0
	model.offset = 0
	model.phase = projectWorkspacePickerReady
	model.previousEdge.state = projectWorkspacePickerEdgeUnknown
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	applyWorkspacePickerCommand(model, command)
	if len(loader.calls) != 2 || loader.calls[1].Offset != 0 {
		t.Fatalf("previous request calls = %+v", loader.calls)
	}
	if model.cursor != projectWorkspacePickerPageSize-1 || model.selected == nil || model.selected.workspace != "workspace-049" {
		t.Fatalf("previous crossing cursor/selection = %d/%+v", model.cursor, model.selected)
	}
}

func TestProjectWorkspacePickerRequestUsesCancelableContext(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loader := &delayedWorkspacePickerLoader{
		base: &workspacePickerLoader{
			responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{},
			errors:    map[int32]error{},
		},
		blockOffset: 0, started: started, release: make(chan struct{}),
	}
	model := newProjectWorkspacePickerModel(ctx, loader, "project-1", "dark")
	command := model.startPageRequest(0, projectWorkspacePickerPageInitial)
	messages := make(chan tea.Msg, 1)
	go func() { messages <- command() }()
	<-started
	cancel()
	message := <-messages
	loaded, ok := message.(projectWorkspacePickerPageLoadedMsg)
	if !ok || !errors.Is(loaded.err, context.Canceled) {
		t.Fatalf("canceled catalog request = %#v", message)
	}
}

func TestWorkspacePickerStatusProjectsOriginalDiagnostic(t *testing.T) {
	diagnostic := errors.New("catalog transport failed")
	status := newStartupPickerStatusModel()
	status.Record(startupPickerStatusFailure{
		Operation:  startupPickerWorkspaceOperation{kind: startupPickerWorkspaceOperationNextEdge},
		Generation: 4, Kind: sessionPickerFailurePageRequest, Diagnostic: diagnostic,
	})
	projection := projectStartupPickerStatus(status)
	if projection.Failure == nil || projection.Failure.Diagnostic != diagnostic {
		t.Fatalf("projected diagnostic = %#v, want original error", projection.Failure)
	}
	rendered := projectStartupPickerStatusRender(projection)
	if rendered == nil || rendered.Text != diagnostic.Error() {
		t.Fatalf("rendered diagnostic = %#v, want diagnostic cause", rendered)
	}
	status.ClearWorkspace(startupPickerWorkspaceOperationNextEdge, 4)
	if projectStartupPickerStatus(status).Failure != nil {
		t.Fatal("successful retry did not clear matching operation failure")
	}
}

func TestProjectWorkspacePickerRetriesFirstPageAndPreservesDiagnostic(t *testing.T) {
	diagnostic := errors.New("catalog unavailable")
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0: workspacePickerResponse("project-1", 0, 2, false),
		},
		errors: map[int32]error{0: diagnostic},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	if model.phase != projectWorkspacePickerFirstPageFailed || !errors.Is(model.failure, diagnostic) {
		t.Fatalf("failure = phase %d diagnostic %v", model.phase, model.failure)
	}
	delete(loader.errors, 0)
	_, retry := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	applyWorkspacePickerCommand(model, retry)
	if model.phase != projectWorkspacePickerReady || len(loader.calls) != 2 {
		t.Fatalf("retry = phase %d calls %d", model.phase, len(loader.calls))
	}
}

func TestProjectWorkspacePickerEmptyCatalogCanReloadOrBack(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0: workspacePickerResponse("project-1", 0, 0, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	if model.phase != projectWorkspacePickerEmpty {
		t.Fatalf("empty phase = %d", model.phase)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := model.result.(projectbinding.WorkspacePickerBack); !ok {
		t.Fatalf("Esc result = %T, want Back", model.result)
	}
}

func TestProjectWorkspacePickerEmptyRetryClearsFirstPageFailure(t *testing.T) {
	diagnostic := errors.New("catalog unavailable")
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0: workspacePickerResponse("project-1", 0, 0, false),
		},
		errors: map[int32]error{0: diagnostic},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	if model.phase != projectWorkspacePickerFirstPageFailed {
		t.Fatalf("initial phase = %d, want first-page failure", model.phase)
	}
	delete(loader.errors, 0)
	_, retry := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	applyWorkspacePickerCommand(model, retry)
	if model.phase != projectWorkspacePickerEmpty {
		t.Fatalf("retry phase = %d, want empty", model.phase)
	}
	if projectStartupPickerStatus(model.startupStatus).Failure != nil {
		t.Fatal("successful empty retry retained first-page failure")
	}
}

func TestProjectWorkspacePickerKeepsDirectionalFailuresIndependent(t *testing.T) {
	nextDiagnostic := errors.New("next edge failed")
	previousDiagnostic := errors.New("previous edge failed")
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
			100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{150: nextDiagnostic},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
	model.selectIndex(len(model.workspaces()) - 1)
	applyWorkspacePickerCommand(model, model.startPageRequest(100, projectWorkspacePickerPageNext))
	model.selected = &projectWorkspacePickerOccurrence{
		generation: model.segments[0].generation,
		offset:     model.segments[0].offset,
		index:      0,
		workspace:  model.segments[0].workspaces[0].WorkspaceId,
	}
	model.viewport = model.selected
	model.nextEdge.state = projectWorkspacePickerEdgeUnknown
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPageNext, true, false, 0))
	loader.errors[0] = previousDiagnostic
	model.previousEdge.state = projectWorkspacePickerEdgeUnknown
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0))

	if model.nextEdge.state != projectWorkspacePickerEdgeFailed ||
		model.previousEdge.state != projectWorkspacePickerEdgeFailed ||
		!errors.Is(model.nextEdge.diagnostic, nextDiagnostic) ||
		!errors.Is(model.previousEdge.diagnostic, previousDiagnostic) {
		t.Fatalf("directional failures = next=%+v previous=%+v", model.nextEdge, model.previousEdge)
	}
	if model.nextEdge.failedRequest == nil || model.previousEdge.failedRequest == nil ||
		model.nextEdge.failedRequest.offset == model.previousEdge.failedRequest.offset {
		t.Fatalf("directional failed requests were not independent: next=%+v previous=%+v",
			model.nextEdge.failedRequest, model.previousEdge.failedRequest)
	}

	delete(loader.errors, 0)
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0))
	if model.previousEdge.state == projectWorkspacePickerEdgeFailed ||
		model.nextEdge.state != projectWorkspacePickerEdgeUnknown ||
		projectStartupPickerStatus(model.startupStatus).Failure != nil {
		t.Fatalf("opposite-edge recovery changed unrelated state: next=%+v previous=%+v",
			model.nextEdge, model.previousEdge)
	}
	previousRetries := 0
	nextRetries := 0
	for _, call := range loader.calls {
		switch call.Offset {
		case 0:
			previousRetries++
		case 150:
			nextRetries++
		}
	}
	if previousRetries != 3 || nextRetries != 1 {
		t.Fatalf("directional retry calls = previous:%d next:%d, want previous:3 next:1", previousRetries, nextRetries)
	}
}

func TestProjectWorkspacePickerRebasesFailedEdgeAfterBoundaryMoves(t *testing.T) {
	diagnostic := errors.New("previous edge failed")
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
			100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
			150: workspacePickerResponse("project-1", 150, 1, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
	model.selectIndex(50)
	model.viewport = model.selected
	model.offset = 50
	applyWorkspacePickerCommand(model, model.startPageRequest(100, projectWorkspacePickerPageNext))
	if len(model.segments) != 2 || model.segments[0].offset != 50 || model.segments[1].offset != 100 {
		t.Fatalf("setup resident offsets = %+v", model.segments)
	}

	loader.errors[0] = diagnostic
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0))
	if model.previousEdge.state != projectWorkspacePickerEdgeFailed {
		t.Fatalf("previous edge state = %d, want failed", model.previousEdge.state)
	}
	if projectStartupPickerStatus(model.startupStatus).Failure == nil {
		t.Fatal("previous edge failure was not projected")
	}

	delete(loader.errors, 0)
	model.selectIndex(50)
	model.viewport = model.selected
	model.offset = 50
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPageNext, true, false, 0))
	if model.previousEdge.state == projectWorkspacePickerEdgeFailed {
		t.Fatalf("boundary change retained stale previous failure: %+v", model.previousEdge)
	}
	if projectStartupPickerStatus(model.startupStatus).Failure != nil {
		t.Fatal("boundary change retained stale previous status failure")
	}

	before := len(loader.calls)
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0))
	if len(loader.calls) != before+1 || loader.calls[len(loader.calls)-1].Offset != 50 {
		t.Fatalf("rebased previous retry request = %+v, want offset 50", loader.calls[before:])
	}
	if model.previousEdge.state == projectWorkspacePickerEdgeFailed ||
		projectStartupPickerStatus(model.startupStatus).Failure != nil {
		t.Fatalf("rebased previous retry did not recover: edge=%+v status=%+v",
			model.previousEdge, projectStartupPickerStatus(model.startupStatus))
	}
}

func TestProjectWorkspacePickerClearsFailureAfterStaleRetryCompletes(t *testing.T) {
	diagnostic := errors.New("next edge failed")
	base := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
			100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
			150: workspacePickerResponse("project-1", 150, 1, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), base, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
	model.selectIndex(50)
	model.viewport = model.selected
	model.offset = 50
	applyWorkspacePickerCommand(model, model.startPageRequest(100, projectWorkspacePickerPageNext))
	model.selectIndex(50)
	model.viewport = model.selected
	model.offset = 50

	base.errors[150] = diagnostic
	applyWorkspacePickerCommand(model, model.requestEdge(projectWorkspacePickerPageNext, true, false, 0))
	if projectStartupPickerStatus(model.startupStatus).Failure == nil {
		t.Fatal("next edge failure was not projected")
	}
	delete(base.errors, 150)
	model.selectIndex(0)
	model.viewport = model.selected
	model.offset = 0

	loader := &delayedWorkspacePickerLoader{
		base:        base,
		blockOffset: 150,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	model.loader = loader
	retryCommand := model.requestEdge(projectWorkspacePickerPageNext, true, false, 0)
	if retryCommand == nil {
		t.Fatal("failed next edge did not start a retry")
	}
	retryMessages := make(chan tea.Msg, 1)
	go func() { retryMessages <- retryCommand() }()
	<-loader.started

	previousCommand := model.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0)
	if previousCommand == nil {
		t.Fatal("opposite edge was blocked while retry was loading")
	}
	model.Update(previousCommand())
	close(loader.release)
	model.Update(<-retryMessages)

	if len(model.segments) != 2 || model.segments[0].offset != 0 || model.segments[1].offset != 50 {
		t.Fatalf("stale retry changed resident window: %+v", model.segments)
	}
	if model.nextEdge.state == projectWorkspacePickerEdgeFailed ||
		model.nextEdge.state == projectWorkspacePickerEdgeLoading ||
		projectStartupPickerStatus(model.startupStatus).Failure != nil {
		t.Fatalf("stale retry retained failure: edge=%+v status=%+v",
			model.nextEdge, projectStartupPickerStatus(model.startupStatus))
	}
}

func TestProjectWorkspacePickerAllowsOppositeEdgeWhileOneEdgeLoads(t *testing.T) {
	base := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
			100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
			150: workspacePickerResponse("project-1", 150, 1, false),
		},
		errors: map[int32]error{},
	}
	loader := &delayedWorkspacePickerLoader{
		base: base, blockOffset: 150, started: make(chan struct{}), release: make(chan struct{}),
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	applyWorkspacePickerCommand(model, model.Init())
	applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
	model.selectIndex(len(model.workspaces()) - 1)
	applyWorkspacePickerCommand(model, model.startPageRequest(100, projectWorkspacePickerPageNext))
	model.selectIndex(0)

	nextCommand := model.requestEdge(projectWorkspacePickerPageNext, true, false, 0)
	nextMessage := make(chan tea.Msg, 1)
	go func() { nextMessage <- nextCommand() }()
	<-loader.started

	previousCommand := model.requestEdge(projectWorkspacePickerPagePrevious, true, false, 0)
	if previousCommand == nil {
		t.Fatal("previous edge was blocked by unrelated next-edge request")
	}
	previousMessage := previousCommand()
	model.Update(previousMessage)
	if model.previousEdge.state == projectWorkspacePickerEdgeLoading ||
		model.previousEdge.state == projectWorkspacePickerEdgeFailed {
		t.Fatalf("previous edge did not complete while next edge loaded: %+v", model.previousEdge)
	}

	close(loader.release)
	updated, _ := model.Update(<-nextMessage)
	if updated != model || model.nextEdge.state == projectWorkspacePickerEdgeLoading {
		t.Fatalf("next edge remained active after completion: %+v", model.nextEdge)
	}
}

func TestProjectWorkspacePickerRejectsOverlappingEdgeResults(t *testing.T) {
	for _, test := range []struct {
		name         string
		blockOffset  int32
		applyNext    bool
		empty        bool
		anchorOffset int
		wantOffsets  []int
	}{
		{
			name:         "previous completes first",
			blockOffset:  150,
			anchorOffset: 50,
			wantOffsets:  []int{0, 50},
			applyNext:    false,
		},
		{
			name:         "previous completes first with stale empty next result",
			blockOffset:  150,
			anchorOffset: 50,
			wantOffsets:  []int{0, 50},
			applyNext:    false,
			empty:        true,
		},
		{
			name:         "next completes first",
			blockOffset:  0,
			anchorOffset: 100,
			wantOffsets:  []int{100, 150},
			applyNext:    true,
		},
		{
			name:         "next completes first with stale empty previous result",
			blockOffset:  0,
			anchorOffset: 100,
			wantOffsets:  []int{100, 150},
			applyNext:    true,
			empty:        true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &workspacePickerLoader{
				responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
					0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
					50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
					100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
					150: workspacePickerResponse("project-1", 150, 1, false),
				},
				errors: map[int32]error{},
			}
			model := newProjectWorkspacePickerModel(context.Background(), base, "project-1", "dark")
			applyWorkspacePickerCommand(model, model.Init())
			applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
			model.selectIndex(50)
			model.viewport = model.selected
			model.offset = 50
			page100Command := model.startPageRequest(100, projectWorkspacePickerPageNext)
			applyWorkspacePickerCommand(model, page100Command)
			model.selectIndex(test.anchorOffset - 50)
			model.viewport = model.selected
			model.offset = model.cursor
			if test.empty {
				base.responses[test.blockOffset] = workspacePickerResponse("project-1", test.blockOffset, 0, false)
			}

			loader := &delayedWorkspacePickerLoader{
				base:        base,
				blockOffset: test.blockOffset,
				started:     make(chan struct{}),
				release:     make(chan struct{}),
			}
			model.loader = loader

			nextCommand := model.requestEdge(projectWorkspacePickerPageNext, false, false, 0)
			previousCommand := model.requestEdge(projectWorkspacePickerPagePrevious, false, false, 0)
			if previousCommand == nil {
				t.Fatal("overlapping opposite-edge request was blocked")
			}
			if test.applyNext {
				previousMessages := make(chan tea.Msg, 1)
				go func() { previousMessages <- previousCommand() }()
				<-loader.started
				model.Update(nextCommand())
				close(loader.release)
				model.Update(<-previousMessages)
			} else {
				nextMessages := make(chan tea.Msg, 1)
				go func() { nextMessages <- nextCommand() }()
				<-loader.started
				model.Update(previousCommand())
				close(loader.release)
				model.Update(<-nextMessages)
			}

			if len(model.segments) != len(test.wantOffsets) {
				t.Fatalf("resident segments = %+v, want offsets %v", model.segments, test.wantOffsets)
			}
			for index, wantOffset := range test.wantOffsets {
				if model.segments[index].offset != wantOffset {
					t.Fatalf("resident offsets = %+v, want %v", model.segments, test.wantOffsets)
				}
			}
			if model.nextEdge.state == projectWorkspacePickerEdgeLoading ||
				model.previousEdge.state == projectWorkspacePickerEdgeLoading {
				t.Fatalf("overlapping request remained loading: next=%+v previous=%+v",
					model.nextEdge, model.previousEdge)
			}
			if test.applyNext && model.previousEdge.state == projectWorkspacePickerEdgeExhausted {
				t.Fatalf("stale previous result changed the current edge authority: %+v", model.previousEdge)
			}
			if !test.applyNext && model.nextEdge.state == projectWorkspacePickerEdgeExhausted {
				t.Fatalf("stale next result changed the current edge authority: %+v", model.nextEdge)
			}
		})
	}
}

func TestProjectWorkspacePickerPageDownKeepsPreTransitionVisibleDistance(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:  workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50: workspacePickerResponse("project-1", 50, 3, false),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	model.height = 9
	applyWorkspacePickerCommand(model, model.Init())
	model.cursor = projectWorkspacePickerPageSize - 1
	model.offset = projectWorkspacePickerPageSize - 5
	model.selectIndex(model.cursor)
	visible := model.visiblePageDistance()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	applyWorkspacePickerCommand(model, command)
	if model.selected == nil || model.selected.offset != 50 || model.selected.index != visible-1 {
		t.Fatalf("PageDown selected occurrence = %+v, want next-page index %d", model.selected, visible-1)
	}
}

func TestProjectWorkspacePickerPageDownCrossingPreservesVisibleDistance(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:  workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50: workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	model.height = 9
	applyWorkspacePickerCommand(model, model.Init())
	model.cursor = projectWorkspacePickerPageSize - 1
	model.offset = projectWorkspacePickerPageSize - 5
	model.selectIndex(model.cursor)
	visible := model.visiblePageDistance()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	applyWorkspacePickerCommand(model, command)
	if model.selected == nil || model.selected.offset != 50 || model.selected.index != visible-1 {
		t.Fatalf("PageDown selected occurrence = %+v, want next-page index %d", model.selected, visible-1)
	}
}

func TestProjectWorkspacePickerPageDownAfterWindowAdvancePreservesVisibleDistance(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:   workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50:  workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
			100: workspacePickerResponse("project-1", 100, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	model.height = 9
	applyWorkspacePickerCommand(model, model.Init())
	applyWorkspacePickerCommand(model, model.startPageRequest(50, projectWorkspacePickerPageNext))
	model.selectIndex(2*projectWorkspacePickerPageSize - 1)
	visible := model.visiblePageDistance()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	applyWorkspacePickerCommand(model, command)
	if model.selected == nil || model.selected.offset != 100 || model.selected.index != visible-1 {
		t.Fatalf("PageDown after window advance selected occurrence = %+v, want next-page index %d", model.selected, visible-1)
	}
}

func TestProjectWorkspacePickerPageUpCrossingPreservesVisibleDistance(t *testing.T) {
	loader := &workspacePickerLoader{
		responses: map[int32]*projectpb.ListProjectWorkspacesSuccess{
			0:  workspacePickerResponse("project-1", 0, projectWorkspacePickerPageSize, true),
			50: workspacePickerResponse("project-1", 50, projectWorkspacePickerPageSize, true),
		},
		errors: map[int32]error{},
	}
	model := newProjectWorkspacePickerModel(context.Background(), loader, "project-1", "dark")
	model.height = 9
	applyWorkspacePickerCommand(model, model.Init())
	model.segments = []projectWorkspacePickerPageSegment{
		{generation: 10, offset: 50, workspaces: cloneProjectWorkspaceCatalogRows(loader.responses[50].Workspaces)},
	}
	model.selected = &projectWorkspacePickerOccurrence{
		generation: 10, offset: 50, index: 0, workspace: "workspace-050",
	}
	model.viewport = model.selected
	model.cursor = 0
	model.offset = 0
	model.phase = projectWorkspacePickerReady
	model.previousEdge.state = projectWorkspacePickerEdgeUnknown
	visible := model.visiblePageDistance()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	applyWorkspacePickerCommand(model, command)
	if model.selected == nil || model.selected.offset != 0 || model.selected.index != projectWorkspacePickerPageSize-visible {
		t.Fatalf("PageUp selected occurrence = %+v, want previous-page index %d", model.selected, projectWorkspacePickerPageSize-visible)
	}
}
