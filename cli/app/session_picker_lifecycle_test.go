package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	tea "github.com/charmbracelet/bubbletea"
)

type blockingSessionPageLoader struct {
	started chan context.Context
}

func newBlockingSessionPageLoader() *blockingSessionPageLoader {
	return &blockingSessionPageLoader{started: make(chan context.Context, 1)}
}

func (*blockingSessionPageLoader) ProjectID() string {
	return "picker-cancellation-project"
}

func (l *blockingSessionPageLoader) ListSessionPage(ctx context.Context, _ serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	l.started <- ctx
	<-ctx.Done()
	return serverapi.SessionPageResponse{}, ctx.Err()
}

func lifecycleTestOptions(t *testing.T) sessionPickerLifecycleOptions {
	t.Helper()
	return sessionPickerLifecycleOptions{
		Loader: &recordingSessionPageLoader{responses: func(request serverapi.SessionPageRequest) sessionPageLoadResult {
			return sessionPageLoadResult{response: pickerPageResponse(t, request)}
		}},
		Theme:  "dark",
		Header: sessionPickerHeaderInfo{},
	}
}

func TestSessionPickerLifecycleEscIsPickerException(t *testing.T) {
	t.Parallel()

	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t))
	defer lifecycle.Close()
	lifecycle.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if lifecycle.Result() != nil {
		t.Fatalf("Esc changed picker result to %+v", lifecycle.Result())
	}
}

func TestSessionPickerLifecycleStartsUnknownAndBlanksUntilSupportedGeometry(t *testing.T) {
	t.Parallel()

	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t))
	defer lifecycle.Close()
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

	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t))
	defer lifecycle.Close()
	if initial := lifecycle.Init(); initial == nil {
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

	lifecycle := newSessionPickerLifecycle(lifecycleTestOptions(t))
	defer lifecycle.Close()
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

func TestSessionPickerLifecycleResultValidationIsExhaustive(t *testing.T) {
	t.Parallel()

	for _, result := range []sessionPickerResult{
		newSessionPickerCreateResult(),
		newSessionPickerCancelResult(),
		newSessionPickerOpenResult(mustPickerSessionID(t, "lifecycle-open")),
	} {
		if err := validateSessionPickerLifecycleResult(result); err != nil {
			t.Fatalf("validate %T: %v", result, err)
		}
	}
	for _, result := range []sessionPickerResult{nil, sessionPickerOpenResult{}} {
		if err := validateSessionPickerLifecycleResult(result); err == nil {
			t.Fatalf("validate %T unexpectedly succeeded", result)
		}
	}
}

func TestSessionPickerLifecycleCloseCancelsInitialAndDirectionalPageRequestsOnEveryExit(t *testing.T) {
	t.Parallel()

	type exitKind string
	const (
		exitCreate exitKind = "create"
		exitOpen   exitKind = "open"
		exitCancel exitKind = "cancel"
		exitClose  exitKind = "close"
	)
	for _, requestKind := range []string{"initial", "directional"} {
		for _, exit := range []exitKind{exitCreate, exitOpen, exitCancel, exitClose} {
			t.Run(requestKind+"/"+string(exit), func(t *testing.T) {
				t.Parallel()

				loader := newBlockingSessionPageLoader()
				lifecycle := newSessionPickerLifecycle(sessionPickerLifecycleOptions{
					Loader: loader,
					Theme:  "dark",
					Header: sessionPickerHeaderInfo{},
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

				switch exit {
				case exitCreate:
					lifecycle.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
				case exitOpen:
					lifecycle.picker.main.selected = newSessionPickerSessionSelection(sessionID)
					lifecycle.Update(tea.KeyMsg{Type: tea.KeyEnter})
				case exitCancel:
					lifecycle.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
				case exitClose:
				}
				lifecycle.Close()

				select {
				case <-requestContext.Done():
					if !errors.Is(requestContext.Err(), context.Canceled) {
						t.Fatalf("page request context error = %v, want canceled", requestContext.Err())
					}
				case <-time.After(time.Second):
					t.Fatal("picker close did not cancel page request")
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

func mustPickerSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
