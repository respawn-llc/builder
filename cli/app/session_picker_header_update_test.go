package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

func TestSessionPickerHeaderProjectionPlacesAvailableUpdateBelowVersion(t *testing.T) {
	t.Parallel()

	model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version: "1.2.3",
	})
	response := serverapi.UpdateStatusResponse{
		Result: serverapi.AvailableUpdateStatusResult("1.2.3", "1.3.0"),
	}
	model.Update(sessionPickerUpdateStatusMsg{
		response: &response,
		outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
	})

	rows := model.projectHeaderRows(80)
	if len(rows) < 2 {
		t.Fatalf("header rows = %d, want version and update rows", len(rows))
	}
	if rows[0].kind != sessionPickerHeaderRowVersion {
		t.Fatalf("first header row identity = %d, want version", rows[0].kind)
	}
	if rows[1].kind != sessionPickerHeaderRowUpdateAvailable {
		t.Fatalf("second header row identity = %d, want available update", rows[1].kind)
	}
}

func TestSessionPickerHeaderProjectionPlacesFailedUpdateBelowVersion(t *testing.T) {
	t.Parallel()

	model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
		Version: "1.2.3",
	})
	response := serverapi.UpdateStatusResponse{
		Result: serverapi.FailedUpdateStatusResult("release metadata rejected"),
	}
	model.Update(sessionPickerUpdateStatusMsg{
		response: &response,
		outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
	})

	rows := model.projectHeaderRows(80)
	if len(rows) < 2 {
		t.Fatalf("header rows = %d, want version and update rows", len(rows))
	}
	if rows[0].kind != sessionPickerHeaderRowVersion {
		t.Fatalf("first header row identity = %d, want version", rows[0].kind)
	}
	if rows[1].kind != sessionPickerHeaderRowUpdateFailed {
		t.Fatalf("second header row identity = %d, want failed update", rows[1].kind)
	}
}

func TestSessionPickerHeaderProjectionOmitsRowlessUpdateStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		response *serverapi.UpdateStatusResponse
	}{
		{name: "pending"},
		{
			name: "current",
			response: &serverapi.UpdateStatusResponse{
				Result: serverapi.CurrentUpdateStatusResult("1.2.3", "1.2.3"),
			},
		},
		{
			name: "check unavailable",
			response: &serverapi.UpdateStatusResponse{
				Result: serverapi.CheckUnavailableUpdateStatusResult(),
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
				Version: "1.2.3",
			})
			if test.response != nil {
				model.Update(sessionPickerUpdateStatusMsg{
					response: test.response,
					outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
				})
			}
			for _, row := range model.projectHeaderRows(80) {
				if isSessionPickerUpdateHeaderRow(row.kind) {
					t.Fatalf("rowless update state projected update row identity %d", row.kind)
				}
			}
		})
	}
}

func TestSessionPickerUpdateHeaderRespectsSupportedNarrowWidth(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		result serverapi.UpdateStatusResult
		kind   sessionPickerHeaderRowKind
	}{
		{
			name:   "available",
			result: serverapi.AvailableUpdateStatusResult("1.2.3", "123456789.123456789.123456789"),
			kind:   sessionPickerHeaderRowUpdateAvailable,
		},
		{
			name:   "failed",
			result: serverapi.FailedUpdateStatusResult("release metadata could not be decoded from the remote response"),
			kind:   sessionPickerHeaderRowUpdateFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{
				Version: "123456789.123456789.123456789",
			})
			model.width = 40
			response := serverapi.UpdateStatusResponse{Result: test.result}
			model.Update(sessionPickerUpdateStatusMsg{
				response: &response,
				outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
			})

			rows := model.projectHeaderRows(36)
			if len(rows) < 2 || rows[1].kind != test.kind {
				t.Fatalf("update row projection = %+v, want %d immediately below version", rows, test.kind)
			}
			if width := lipgloss.Width(ansi.Strip(rows[1].render)); width > 36 {
				t.Fatalf("update row width = %d, want <= 36", width)
			}
			if width := lipgloss.Width(ansi.Strip(model.renderHeader())); width > model.width {
				t.Fatalf("rendered header width = %d, want <= %d", width, model.width)
			}
		})
	}
}

func TestSessionPickerUpdateHeaderRebudgetsVisibleRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	summaries := make([]clientui.SessionSummary, 0, 8)
	for index := 0; index < 8; index++ {
		summaries = append(summaries, pickerTestSummary(t, fmt.Sprintf("header-budget-%d", index), now.Add(-time.Duration(index)*time.Minute)))
	}
	for _, test := range []struct {
		name   string
		result serverapi.UpdateStatusResult
	}{
		{name: "available", result: serverapi.AvailableUpdateStatusResult("1.2.3", "1.3.0")},
		{name: "failed", result: serverapi.FailedUpdateStatusResult("release metadata rejected")},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newTestSessionPickerModel(t, summaries, sessionPickerHeaderInfo{
				Version: "1.2.3",
			})
			model.width = 80
			model.height = 12
			tab := model.tab(model.activeTab)
			tab.selectIndex(5)
			model.ensureSelectedVisible(tab)
			if tab.offset == 0 {
				t.Fatal("test setup did not place the selected row below the initial viewport")
			}
			beforeBudget := model.visibleLineBudget()
			beforeRows := model.visibleRowsFromOffset(tab, tab.offset)
			response := serverapi.UpdateStatusResponse{Result: test.result}
			model.Update(sessionPickerUpdateStatusMsg{
				response: &response,
				outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
			})

			afterBudget := model.visibleLineBudget()
			if afterBudget != beforeBudget-1 {
				t.Fatalf("visible line budget after update row = %d, want %d", afterBudget, beforeBudget-1)
			}
			afterRows := model.visibleRowsFromOffset(tab, tab.offset)
			if len(afterRows) >= len(beforeRows) {
				t.Fatalf("visible rows after update row = %d, want fewer than %d", len(afterRows), len(beforeRows))
			}
			selected := tab.selectedIndex()
			if selected == nil || !model.rowVisibleFromOffset(tab, tab.offset, *selected) {
				t.Fatalf("selected row is not visible after update row rebudget")
			}
		})
	}
}

func isSessionPickerUpdateHeaderRow(kind sessionPickerHeaderRowKind) bool {
	return kind == sessionPickerHeaderRowUpdateAvailable || kind == sessionPickerHeaderRowUpdateFailed
}

type sessionPickerHeaderReviewState string

const (
	sessionPickerHeaderReviewPending   sessionPickerHeaderReviewState = "pending"
	sessionPickerHeaderReviewAvailable sessionPickerHeaderReviewState = "available"
	sessionPickerHeaderReviewFailed    sessionPickerHeaderReviewState = "check-failed"
)

type sessionPickerHeaderReviewHarness struct {
	summaries []clientui.SessionSummary
}

func newSessionPickerHeaderReviewHarness(t *testing.T) sessionPickerHeaderReviewHarness {
	t.Helper()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	summaries := make([]clientui.SessionSummary, 0, 10)
	for index := 0; index < 10; index++ {
		summary := pickerTestSummary(t, fmt.Sprintf("review-session-%d", index), now.Add(-time.Duration(index)*time.Minute))
		summary.Name = fmt.Sprintf("Session %d", index)
		summary.FirstPromptPreview = "Review the responsive picker update row with a selected session below the initial viewport."
		summaries = append(summaries, summary)
	}
	return sessionPickerHeaderReviewHarness{summaries: summaries}
}

func (h sessionPickerHeaderReviewHarness) Render(
	t *testing.T,
	state sessionPickerHeaderReviewState,
	width int,
) string {
	t.Helper()
	model := newTestSessionPickerModel(t, h.summaries, sessionPickerHeaderInfo{
		Version:       "1.2.3",
		CWD:           "~/work/kent",
		Branch:        "feature/responsive-picker-update-row",
		Auth:          "OpenAI Subscription",
		Model:         "gpt-5 high",
		ServerAddress: "127.0.0.1:53082",
	})
	model.width = width
	model.height = 14
	model.tab(model.activeTab).selectIndex(5)
	model.ensureSelectedVisible(model.tab(model.activeTab))

	switch state {
	case sessionPickerHeaderReviewPending:
	case sessionPickerHeaderReviewAvailable:
		applySessionPickerHeaderReviewResult(
			t,
			model,
			serverapi.AvailableUpdateStatusResult("1.2.3", "1.3.0"),
		)
	case sessionPickerHeaderReviewFailed:
		applySessionPickerHeaderReviewResult(
			t,
			model,
			serverapi.FailedUpdateStatusResult("release metadata rejected"),
		)
	default:
		t.Fatalf("unknown review state %q", state)
	}
	return model.View()
}

func applySessionPickerHeaderReviewResult(
	t *testing.T,
	model *sessionPickerModel,
	result serverapi.UpdateStatusResult,
) {
	t.Helper()
	response := serverapi.UpdateStatusResponse{Result: result}
	model.Update(sessionPickerUpdateStatusMsg{
		response: &response,
		outcome:  classifyInteractiveConnection(interactiveConnectionOperationUnary, nil),
	})
}

func TestSessionPickerUpdateHeaderReviewHarness(t *testing.T) {
	withTrueColor(t)
	harness := newSessionPickerHeaderReviewHarness(t)
	for _, width := range []int{120, 40} {
		for _, state := range []sessionPickerHeaderReviewState{
			sessionPickerHeaderReviewPending,
			sessionPickerHeaderReviewAvailable,
			sessionPickerHeaderReviewFailed,
		} {
			t.Run(fmt.Sprintf("%s/%d-columns", state, width), func(t *testing.T) {
				rendered := harness.Render(t, state, width)
				for _, line := range strings.Split(rendered, "\n") {
					if lineWidth := lipgloss.Width(ansi.Strip(line)); lineWidth > width {
						t.Fatalf("rendered line width = %d, want <= %d", lineWidth, width)
					}
				}
				t.Logf("state=%s width=%d\n%s", state, width, rendered)
			})
		}
	}
}
