package app

import (
	"errors"
	"testing"

	"core/shared/sessioncontract"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

type sessionPickerFailureProjection struct {
	status *startupPickerStatusModel
}

type sessionPickerStatusFailure struct {
	Tab        sessioncontract.SessionCategory
	Operation  sessionPickerOperationKind
	Generation uint64
	Kind       sessionPickerFailureKind
	Diagnostic error
}

func newSessionPickerFailureProjection() *sessionPickerFailureProjection {
	return &sessionPickerFailureProjection{status: newStartupPickerStatusModel()}
}

func (p *sessionPickerFailureProjection) Record(failure sessionPickerStatusFailure) {
	p.status.Record(startupPickerStatusFailure{
		Tab: failure.Tab, Operation: failure.Operation, Generation: failure.Generation,
		Kind: failure.Kind, Diagnostic: failure.Diagnostic,
	})
}

func (p *sessionPickerFailureProjection) Clear(tab sessioncontract.SessionCategory, operation sessionPickerOperationKind, generation uint64) {
	p.status.Clear(tab, operation, generation)
}

func (p *sessionPickerFailureProjection) Status(activeTab sessioncontract.SessionCategory) *startupPickerStatusFailure {
	p.status.activeTab = &activeTab
	return projectStartupPickerStatus(p.status).Failure
}

func TestSessionPickerFailureProjectionKeepsOverlappingSourcesKeyed(t *testing.T) {
	t.Parallel()

	failures := newSessionPickerFailureProjection()
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationBodyPage,
		Generation: 4,
		Kind:       sessionPickerFailurePageRequest,
		Diagnostic: errors.New("body failed"),
	})
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationDirectionalPage,
		Generation: 5,
		Kind:       sessionPickerFailurePageRequest,
		Diagnostic: errors.New("directional failed"),
	})
	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationDirectionalPage {
		t.Fatalf("newest failure = %q, want directional page", got.Operation)
	}
	failures.Clear(sessioncontract.SessionCategoryMain, sessionPickerOperationDirectionalPage, 5)
	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationBodyPage {
		t.Fatalf("after directional recovery = %q, want body page", got.Operation)
	}
	failures.Clear(sessioncontract.SessionCategoryMain, sessionPickerOperationBodyPage, 4)
	if got := failures.Status(sessioncontract.SessionCategoryMain); got != nil {
		t.Fatalf("cleared failure projection = %+v, want empty", got)
	}
}

func TestSessionPickerFailureProjectionPrefersActiveTabThenInactiveRecency(t *testing.T) {
	t.Parallel()

	failures := newSessionPickerFailureProjection()
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationBodyPage,
		Generation: 1,
		Kind:       sessionPickerFailurePageRequest,
		Diagnostic: errors.New("main"),
	})
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategorySubagent,
		Operation:  sessionPickerOperationDirectionalPage,
		Generation: 2,
		Kind:       sessionPickerFailurePageRequest,
		Diagnostic: errors.New("subagent"),
	})

	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationBodyPage {
		t.Fatalf("active tab failure = %q, want body page", got.Operation)
	}
	if got := failures.Status(sessioncontract.SessionCategorySubagent); got == nil || got.Operation != sessionPickerOperationDirectionalPage {
		t.Fatalf("switched tab failure = %q, want directional page", got.Operation)
	}
}

func TestSessionPickerFailureRenderProjectionExcludesDiagnostics(t *testing.T) {
	t.Parallel()

	for _, kind := range []sessionPickerFailureKind{
		sessionPickerFailurePageRequest,
		sessionPickerFailurePageContract,
	} {
		t.Run(string(kind), func(t *testing.T) {
			diagnostic := errors.New("opaque internal diagnostic /tmp/private read tcp 127.0.0.1:1")
			status := newStartupPickerStatusModel()
			status.Record(startupPickerStatusFailure{
				Tab:        sessioncontract.SessionCategoryMain,
				Operation:  sessionPickerOperationBodyPage,
				Generation: 1,
				Kind:       kind,
				Diagnostic: diagnostic,
			})

			projection := projectStartupPickerStatus(status)
			if projection.Failure == nil || !errors.Is(projection.Failure.Diagnostic, diagnostic) {
				t.Fatal("status projection discarded the typed diagnostic cause")
			}
			renderProjection := projectStartupPickerStatusRender(projection)
			if renderProjection == nil || renderProjection.Kind != startupPickerStatusRenderFailure || !renderProjection.IsError {
				t.Fatalf("render projection = %+v, want typed failure", renderProjection)
			}
		})
	}
}

func TestSessionPickerResponsiveTabsStackFullLabelsOnlyAtSupportedNarrowWidth(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		width   int
		stacked bool
	}{
		{name: "wide", width: 120, stacked: false},
		{name: "supported narrow", width: 40, stacked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := projectSessionPickerTabs(sessionPickerTabsProjectionInput{
				Width:     test.width,
				ActiveTab: sessioncontract.SessionCategoryMain,
				Geometry:  terminalGeometryKnown(test.width, 10),
				Theme:     "dark",
			})
			wantRows := 1
			if test.stacked {
				wantRows = 2
			}
			if len(projection.Rows) != wantRows {
				t.Fatalf("tab rows = %d, want %d", len(projection.Rows), wantRows)
			}
			for _, row := range projection.Rows {
				if width := lipgloss.Width(ansi.Strip(row)); width > test.width {
					t.Fatalf("tab row width = %d, want <= %d", width, test.width)
				}
			}
		})
	}
}
