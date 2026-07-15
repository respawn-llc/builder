package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionPickerLifecycleTerminalSpy struct {
	events       []string
	enterErr     error
	enableErr    error
	disableErr   error
	exitErr      error
	mouseCapture bool
}

type blockingSessionPageLoader struct {
	started chan context.Context
}

type blockingSessionDetailClient struct {
	client.SessionViewClient
	started chan context.Context
}

func newBlockingSessionPageLoader() *blockingSessionPageLoader {
	return &blockingSessionPageLoader{started: make(chan context.Context, 1)}
}

func newBlockingSessionDetailClient() *blockingSessionDetailClient {
	return &blockingSessionDetailClient{started: make(chan context.Context, 1)}
}

func (*blockingSessionPageLoader) ProjectID() string {
	return "picker-cancellation-project"
}

func (l *blockingSessionPageLoader) ListSessionPage(ctx context.Context, _ serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	l.started <- ctx
	<-ctx.Done()
	return serverapi.SessionPageResponse{}, ctx.Err()
}

func (c *blockingSessionDetailClient) GetSessionExecutionEnvironment(
	ctx context.Context,
	_ serverapi.SessionExecutionEnvironmentRequest,
) (serverapi.SessionExecutionEnvironmentResponse, error) {
	c.started <- ctx
	<-ctx.Done()
	return serverapi.SessionExecutionEnvironmentResponse{}, ctx.Err()
}

func (s *sessionPickerLifecycleTerminalSpy) EnterAltScreen() error {
	s.events = append(s.events, "enter-alt-screen")
	return s.enterErr
}

func (s *sessionPickerLifecycleTerminalSpy) EnableAlternateScroll() error {
	s.events = append(s.events, "enable-alternate-scroll")
	return s.enableErr
}

func (s *sessionPickerLifecycleTerminalSpy) DisableAlternateScroll() error {
	s.events = append(s.events, "disable-alternate-scroll")
	return s.disableErr
}

func (s *sessionPickerLifecycleTerminalSpy) ExitAltScreen() error {
	s.events = append(s.events, "exit-alt-screen")
	return s.exitErr
}

func (s *sessionPickerLifecycleTerminalSpy) EnableMouseCapture() {
	s.mouseCapture = true
}

func lifecycleTestOptions(
	t *testing.T,
	terminal *sessionPickerLifecycleTerminalSpy,
	run func(context.Context, *sessionPickerModel) (sessionPickerResult, error),
) sessionPickerLifecycleOptions {
	t.Helper()
	loader := &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
		return sessionPageLoadResult{response: pickerPageResponse(t, request)}
	}}
	return sessionPickerLifecycleOptions{
		Loader:     loader,
		Theme:      "dark",
		Header:     sessionPickerHeaderInfo{},
		Terminal:   terminal,
		RunProgram: run,
	}
}

func TestSessionPickerLifecycleHasOneExhaustiveResultAndOneCleanupPath(t *testing.T) {
	t.Parallel()

	results := []sessionPickerResult{
		newSessionPickerCreateResult(),
		newSessionPickerCancelResult(),
		newSessionPickerOpenResult(mustPickerSessionID(t, "lifecycle-open")),
	}
	for index, expected := range results {
		t.Run(fmt.Sprintf("result-%d", index), func(t *testing.T) {
			terminal := &sessionPickerLifecycleTerminalSpy{}
			lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t, terminal, func(context.Context, *sessionPickerModel) (sessionPickerResult, error) {
				return expected, nil
			}))
			got, err := lifecycle.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("result = %+v, want %+v", got, expected)
			}
			if !reflect.DeepEqual(terminal.events, []string{
				"enter-alt-screen",
				"enable-alternate-scroll",
				"disable-alternate-scroll",
				"exit-alt-screen",
			}) {
				t.Fatalf("terminal cleanup sequence = %v", terminal.events)
			}
			if terminal.mouseCapture {
				t.Fatal("session picker lifecycle enabled mouse capture")
			}
		})
	}
}

func TestSessionPickerLifecycleEscIsPickerException(t *testing.T) {
	t.Parallel()

	terminal := &sessionPickerLifecycleTerminalSpy{}
	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t, terminal, nil))
	lifecycle.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if lifecycle.Result() != nil {
		t.Fatalf("Esc changed picker result to %+v", lifecycle.Result())
	}
	if terminal.events != nil {
		t.Fatalf("Esc performed terminal lifecycle work before program run: %v", terminal.events)
	}
}

func TestSessionPickerLifecycleStartsUnknownAndBlanksUntilSupportedGeometry(t *testing.T) {
	t.Parallel()

	terminal := &sessionPickerLifecycleTerminalSpy{}
	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t, terminal, nil))
	if lifecycle.geometry.IsKnown() {
		t.Fatal("picker lifecycle started with known geometry")
	}
	if got := lifecycle.View(); got != "" {
		t.Fatalf("unknown-geometry picker view = %q, want blank", got)
	}

	lifecycle.Update(tea.WindowSizeMsg{Width: 39, Height: 9})
	if got := lifecycle.View(); got != "" {
		t.Fatalf("sub-minimum picker view = %q, want blank", got)
	}

	lifecycle.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if got := lifecycle.View(); got == "" {
		t.Fatal("supported picker geometry remained blank")
	}
}

func TestSessionPickerLifecyclePreservesReducerAndEffectsWhileBlank(t *testing.T) {
	t.Parallel()

	terminal := &sessionPickerLifecycleTerminalSpy{}
	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t, terminal, nil))
	initial := lifecycle.Init()
	if initial == nil {
		t.Fatal("blank picker did not start its picker effects")
	}

	lifecycle.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if _, ok := lifecycle.Result().(sessionPickerCreateResult); !ok {
		t.Fatalf("blank picker create shortcut result = %T, want create-new", lifecycle.Result())
	}
	if lifecycle.geometry.IsKnown() {
		t.Fatal("input processing changed unknown geometry")
	}
}

func TestSessionPickerLifecycleResumesAtSupportedGeometryWithoutLegacyDefaults(t *testing.T) {
	t.Parallel()

	terminal := &sessionPickerLifecycleTerminalSpy{}
	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t, terminal, nil))
	if lifecycle.geometry.Size() != nil {
		t.Fatalf("initial geometry = %+v, want Geometry.Unknown", lifecycle.geometry.Size())
	}
	lifecycle.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	size := lifecycle.geometry.Size()
	if size == nil || size.width != 40 || size.height != 10 {
		t.Fatalf("resumed geometry = %+v, want 40×10", size)
	}
	if size.width == 80 && size.height == 24 {
		t.Fatal("legacy 80×24 default authorized picker output")
	}
}

func TestSessionPickerLifecycleCleansUpAfterProgramAndTerminalFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		terminal   func(*sessionPickerLifecycleTerminalSpy)
		runErr     error
		wantErr    bool
		wantEvents []string
	}{
		{
			name:    "program failure",
			runErr:  errors.New("picker program failed"),
			wantErr: true,
			wantEvents: []string{
				"enter-alt-screen",
				"enable-alternate-scroll",
				"disable-alternate-scroll",
				"exit-alt-screen",
			},
		},
		{
			name: "alternate-scroll enable failure",
			terminal: func(spy *sessionPickerLifecycleTerminalSpy) {
				spy.enableErr = errors.New("enable alternate scroll failed")
			},
			wantErr: true,
			wantEvents: []string{
				"enter-alt-screen",
				"enable-alternate-scroll",
				"exit-alt-screen",
			},
		},
		{
			name: "alternate-scroll disable failure",
			terminal: func(spy *sessionPickerLifecycleTerminalSpy) {
				spy.disableErr = errors.New("disable alternate scroll failed")
			},
			wantErr: true,
			wantEvents: []string{
				"enter-alt-screen",
				"enable-alternate-scroll",
				"disable-alternate-scroll",
				"exit-alt-screen",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := &sessionPickerLifecycleTerminalSpy{}
			if test.terminal != nil {
				test.terminal(terminal)
			}
			lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t, terminal, func(context.Context, *sessionPickerModel) (sessionPickerResult, error) {
				return newSessionPickerCancelResult(), test.runErr
			}))
			_, err := lifecycle.Run(context.Background())
			if (err != nil) != test.wantErr {
				t.Fatalf("Run error = %v, want error=%t", err, test.wantErr)
			}
			if !reflect.DeepEqual(terminal.events, test.wantEvents) {
				t.Fatalf("terminal failure cleanup sequence = %v, want %v", terminal.events, test.wantEvents)
			}
		})
	}
}

func TestSessionPickerLifecycleCancelsInitialAndDirectionalPageRequestsOnEveryExit(t *testing.T) {
	t.Parallel()

	type exitKind string
	const (
		exitCreate  exitKind = "create"
		exitOpen    exitKind = "open"
		exitCancel  exitKind = "cancel"
		exitCleanup exitKind = "cleanup"
	)
	for _, requestKind := range []string{"initial", "directional"} {
		for _, exit := range []exitKind{exitCreate, exitOpen, exitCancel, exitCleanup} {
			t.Run(requestKind+"/"+string(exit), func(t *testing.T) {
				t.Parallel()

				loader := newBlockingSessionPageLoader()
				lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
					Loader:   loader,
					Theme:    "dark",
					Header:   sessionPickerHeaderInfo{},
					Terminal: &sessionPickerLifecycleTerminalSpy{},
				})
				sessionID := mustPickerSessionID(t, "page-cancellation-session")
				var command tea.Cmd
				switch requestKind {
				case "initial":
					command = lifecycle.picker.startBodyRequest(
						sessioncontract.SessionCategoryMain,
						sessionPickerBodyRequestInitial,
					)
				case "directional":
					tab := lifecycle.picker.tab(sessioncontract.SessionCategoryMain)
					older := mustPickerContinuation(t, "page-cancellation-older")
					tab.replaceSegments(serverapi.SessionPageResponse{
						ProjectID: loader.ProjectID(),
						Category:  sessioncontract.SessionCategoryMain,
						Sessions: []clientui.SessionSummary{{
							SessionID: sessionID,
							Category:  sessioncontract.SessionCategoryMain,
							UpdatedAt: time.Unix(1_900_000_000, 0).UTC(),
						}},
						Older: &older,
					})
					tab.selected = newSessionPickerSessionSelection(sessionID)
					batchMessage := lifecycle.picker.startDirectionalRequest(
						tab,
						serverapi.OlderSessionPagePosition(older),
						1,
					)()
					batch, ok := batchMessage.(tea.BatchMsg)
					if !ok || len(batch) == 0 {
						t.Fatalf("directional command message = %T, want non-empty batch", batchMessage)
					}
					command = batch[0]
				default:
					t.Fatalf("unknown request kind %q", requestKind)
				}

				completed := make(chan tea.Msg, 1)
				go func() {
					completed <- command()
				}()
				var requestContext context.Context
				select {
				case requestContext = <-loader.started:
				case <-time.After(time.Second):
					t.Fatal("page request did not start")
				}
				select {
				case <-requestContext.Done():
					t.Fatal("page request was canceled before picker exit")
				default:
				}

				switch exit {
				case exitCreate:
					lifecycle.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
					if _, ok := lifecycle.Result().(sessionPickerCreateResult); !ok {
						t.Fatalf("create exit result = %T", lifecycle.Result())
					}
				case exitOpen:
					lifecycle.picker.main.selected = newSessionPickerSessionSelection(sessionID)
					lifecycle.Update(tea.KeyMsg{Type: tea.KeyEnter})
					if _, ok := lifecycle.Result().(sessionPickerOpenResult); !ok {
						t.Fatalf("open exit result = %T", lifecycle.Result())
					}
				case exitCancel:
					lifecycle.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
					if _, ok := lifecycle.Result().(sessionPickerCancelResult); !ok {
						t.Fatalf("cancel exit result = %T", lifecycle.Result())
					}
				case exitCleanup:
					if lifecycle.Result() != nil {
						t.Fatalf("cleanup-only result = %T, want nil", lifecycle.Result())
					}
				}
				if err := lifecycle.Cleanup(); err != nil {
					t.Fatalf("Cleanup: %v", err)
				}

				select {
				case <-requestContext.Done():
					if !errors.Is(requestContext.Err(), context.Canceled) {
						t.Fatalf("page request context error = %v, want canceled", requestContext.Err())
					}
				case <-time.After(time.Second):
					t.Fatal("picker cleanup did not cancel page request")
				}
				select {
				case message := <-completed:
					loaded, ok := message.(sessionPickerPageLoadedMsg)
					if !ok || !errors.Is(loaded.err, context.Canceled) {
						t.Fatalf("page completion = %#v, want canceled load", message)
					}
				case <-time.After(time.Second):
					t.Fatal("canceled page command did not complete")
				}
			})
		}
	}
}

func TestSessionPickerLifecycleCancelsSelectedDetailRequestsOnFailureCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(context.Context, tea.Cmd) (sessionPickerResult, error)
	}{
		{
			name: "program failure",
			run: func(_ context.Context, command tea.Cmd) (sessionPickerResult, error) {
				go command()
				return nil, errors.New("forced picker program failure")
			},
		},
		{
			name: "caller context teardown",
			run: func(ctx context.Context, command tea.Cmd) (sessionPickerResult, error) {
				go command()
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			name: "lifecycle result validation failure",
			run: func(_ context.Context, command tea.Cmd) (sessionPickerResult, error) {
				go command()
				return nil, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			detailClient := newBlockingSessionDetailClient()
			callerContext, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
				Loader:                     &recordingSessionPageLoader{},
				ExecutionEnvironmentClient: detailClient,
				Theme:                      "dark",
				Header:                     sessionPickerHeaderInfo{},
				Terminal:                   &sessionPickerLifecycleTerminalSpy{},
			})
			sessionID := mustPickerSessionID(t, "detail-cleanup-"+test.name)
			lifecycle.options.RunProgram = func(ctx context.Context, picker *sessionPickerModel) (sessionPickerResult, error) {
				command := picker.startSelectedDetailForTabWithID(
					picker.tab(sessioncontract.SessionCategoryMain),
					sessionID,
				)
				if command == nil {
					t.Fatal("selected-detail command is nil")
				}
				if test.name == "caller context teardown" {
					cancelCaller()
				}
				return test.run(ctx, command)
			}

			_, err := lifecycle.Run(callerContext)
			if err == nil {
				t.Fatal("Run error = nil, want lifecycle failure")
			}
			var requestContext context.Context
			select {
			case requestContext = <-detailClient.started:
			case <-time.After(time.Second):
				t.Fatal("selected-detail request did not start")
			}
			select {
			case <-requestContext.Done():
				if !errors.Is(requestContext.Err(), context.Canceled) {
					t.Fatalf("selected-detail request context error = %v, want canceled", requestContext.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("lifecycle cleanup did not cancel selected-detail request")
			}
		})
	}
}

func TestSessionPickerLifecycleRepeatedCleanupCancelsSelectedDetailOnce(t *testing.T) {
	t.Parallel()

	detailClient := newBlockingSessionDetailClient()
	terminal := &sessionPickerLifecycleTerminalSpy{}
	lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
		Loader:                     &recordingSessionPageLoader{},
		ExecutionEnvironmentClient: detailClient,
		Theme:                      "dark",
		Header:                     sessionPickerHeaderInfo{},
		Terminal:                   terminal,
	})
	command := lifecycle.picker.startSelectedDetailForTabWithID(
		lifecycle.picker.tab(sessioncontract.SessionCategoryMain),
		mustPickerSessionID(t, "detail-repeated-cleanup"),
	)
	completed := make(chan tea.Msg, 1)
	go func() {
		completed <- command()
	}()
	var requestContext context.Context
	select {
	case requestContext = <-detailClient.started:
	case <-time.After(time.Second):
		t.Fatal("selected-detail request did not start")
	}

	if err := lifecycle.Cleanup(); err != nil {
		t.Fatalf("first Cleanup: %v", err)
	}
	if err := lifecycle.Cleanup(); err != nil {
		t.Fatalf("second Cleanup: %v", err)
	}
	select {
	case <-requestContext.Done():
		if !errors.Is(requestContext.Err(), context.Canceled) {
			t.Fatalf("selected-detail request context error = %v, want canceled", requestContext.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("repeated lifecycle cleanup did not cancel selected-detail request")
	}
	select {
	case message := <-completed:
		loaded, ok := message.(sessionPickerSelectedDetailLoadedMsg)
		if !ok || !errors.Is(loaded.Err, context.Canceled) {
			t.Fatalf("selected-detail completion = %#v, want canceled load", message)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled selected-detail command did not complete")
	}
	if terminal.events != nil {
		t.Fatalf("inactive repeated cleanup touched terminal: %v", terminal.events)
	}
}

func mustPickerSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
