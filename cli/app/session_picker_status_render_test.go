package app

import (
	"errors"
	"strings"
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
		ActiveEligible: true,
	})
}

func (p *sessionPickerFailureProjection) Clear(tab sessioncontract.SessionCategory, operation sessionPickerOperationKind, generation uint64) {
	p.status.Clear(tab, operation, generation)
}

func (p *sessionPickerFailureProjection) Status(activeTab sessioncontract.SessionCategory) *startupPickerStatusFailure {
	p.status.activeTab = &activeTab
	return projectStartupPickerStatus(p.status).Failure
}

func TestAuthPickerNoticeCurrentContractBeforeSharedExtraction(t *testing.T) {
	t.Parallel()

	model := newStartupPickerModel(
		"**Auth**",
		"Auth",
		"dark",
		startupPickerNotice{Text: "operation failed", Kind: startupPickerNoticeError},
		nil,
	)
	model.width = 40
	model.height = 10

	rendered := ansi.Strip(model.renderNotice())
	if strings.TrimSpace(rendered) == "" {
		t.Fatal("auth picker notice rendered empty")
	}
	if lipgloss.Width(rendered) > model.contentWidth() {
		t.Fatalf("auth picker notice width = %d, want <= %d", lipgloss.Width(rendered), model.contentWidth())
	}
}

func TestStartupPickersUseSharedStatusRenderer(t *testing.T) {
	t.Parallel()

	status := newStartupPickerStatusModel()
	status.Record(startupPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationBodyPage,
		Generation: 3,
		Kind:       sessionPickerFailurePageRequest,
		Diagnostic: errors.New("page failed"),
	})

	if got := renderStartupPickerStatus(projectStartupPickerStatus(status), 40); strings.TrimSpace(ansi.Strip(got)) == "" {
		t.Fatal("shared startup status renderer produced empty output")
	}
}

func TestSessionPickerResumeUsesSharedStartupStatusSurface(t *testing.T) {
	t.Parallel()

	authStatus := newStartupPickerStatusModel()
	authStatus.Record(startupPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationSelectedDetail,
		Generation: 7,
		Kind:       sessionPickerFailureDetailRequest,
		Diagnostic: errors.New("detail failed"),
	})
	resume := newSessionPickerStatusSurface(authStatus)
	if resume.model != authStatus {
		t.Fatal("resume picker did not reuse shared startup status model")
	}
	if got := resume.RenderStatus(80); strings.TrimSpace(ansi.Strip(got)) == "" {
		t.Fatal("resume picker shared status rendered empty")
	}
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
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationSelectedDetail,
		Generation: 6,
		Kind:       sessionPickerFailureDetailRequest,
		Diagnostic: errors.New("detail failed"),
	})

	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationSelectedDetail {
		t.Fatalf("newest active failure = %q, want selected detail", got.Operation)
	}
	failures.Clear(sessioncontract.SessionCategoryMain, sessionPickerOperationSelectedDetail, 6)
	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationDirectionalPage {
		t.Fatalf("after detail recovery = %q, want directional page", got.Operation)
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
		Operation:  sessionPickerOperationSelectedDetail,
		Generation: 2,
		Kind:       sessionPickerFailureDetailRequest,
		Diagnostic: errors.New("subagent"),
	})

	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationBodyPage {
		t.Fatalf("active tab failure = %q, want body page", got.Operation)
	}
	if got := failures.Status(sessioncontract.SessionCategorySubagent); got == nil || got.Operation != sessionPickerOperationSelectedDetail {
		t.Fatalf("switched tab failure = %q, want selected detail", got.Operation)
	}
}

func TestSessionPickerFailureProjectionRetryRetainsUnrelatedFailure(t *testing.T) {
	t.Parallel()

	failures := newSessionPickerFailureProjection()
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationBodyPage,
		Generation: 8,
		Kind:       sessionPickerFailurePageRequest,
		Diagnostic: errors.New("body"),
	})
	failures.Record(sessionPickerStatusFailure{
		Tab:        sessioncontract.SessionCategoryMain,
		Operation:  sessionPickerOperationSelectedDetail,
		Generation: 9,
		Kind:       sessionPickerFailureDetailRequest,
		Diagnostic: errors.New("detail"),
	})
	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationSelectedDetail {
		t.Fatalf("retry status = %q, want selected detail", got.Operation)
	}
	failures.Clear(sessioncontract.SessionCategoryMain, sessionPickerOperationSelectedDetail, 9)
	if got := failures.Status(sessioncontract.SessionCategoryMain); got == nil || got.Operation != sessionPickerOperationBodyPage {
		t.Fatalf("after detail recovery = %+v, want retained body failure", got)
	}
}

func TestSessionPickerFailureRenderingKeepsDiagnosticsInternal(t *testing.T) {
	t.Parallel()

	for _, kind := range []sessionPickerFailureKind{
		sessionPickerFailurePageRequest,
		sessionPickerFailurePageContract,
		sessionPickerFailureDetailRequest,
		sessionPickerFailureDetailContract,
		sessionPickerFailureDetailField,
	} {
		t.Run(string(kind), func(t *testing.T) {
			diagnostic := errors.New("opaque internal diagnostic /tmp/private read tcp 127.0.0.1:1")
			status := newStartupPickerStatusModel()
			status.Record(startupPickerStatusFailure{
				Tab:            sessioncontract.SessionCategoryMain,
				Operation:      sessionPickerOperationBodyPage,
				Generation:     1,
				Kind:           kind,
				Diagnostic:     diagnostic,
				ActiveEligible: true,
			})

			projection := projectStartupPickerStatus(status)
			if projection.Failure == nil || !errors.Is(projection.Failure.Diagnostic, diagnostic) {
				t.Fatal("status projection discarded the typed diagnostic cause")
			}
			rendered := ansi.Strip(renderStartupPickerStatus(projection, 120))
			if strings.Contains(rendered, diagnostic.Error()) {
				t.Fatal("operator status rendered its internal diagnostic")
			}
			if strings.TrimSpace(rendered) == "" {
				t.Fatal("operator status rendered no failure guidance")
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
		{name: "minimum width", width: 40, stacked: true},
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

func TestSessionPickerResponsiveTabsRemainHorizontalAboveMinimum(t *testing.T) {
	t.Parallel()

	projection := projectSessionPickerTabs(sessionPickerTabsProjectionInput{
		Width:     80,
		ActiveTab: sessioncontract.SessionCategorySubagent,
		Geometry:  terminalGeometryKnown(80, 24),
	})
	if len(projection.Rows) != 1 {
		t.Fatalf("wide tab rows = %d, want one", len(projection.Rows))
	}
}
