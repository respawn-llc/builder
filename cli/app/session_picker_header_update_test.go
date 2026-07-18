package app

import (
	"testing"

	"core/shared/serverapi"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

func TestSessionPickerUpdateHeaderProjectsResponsiveRowPolicy(t *testing.T) {
	t.Parallel()

	const width = 40
	base := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{Version: "1.2.3"})
	base.width = width
	baseHeight := lipgloss.Height(base.renderHeader())

	tests := []struct {
		name          string
		response      serverapi.UpdateStatusResponse
		applyResponse bool
		additionalRow bool
	}{
		{name: "pending"},
		{
			name: "current",
			response: serverapi.UpdateStatusResponse{
				Result: serverapi.CurrentUpdateStatusResult("1.2.3", "1.2.3"),
			},
			applyResponse: true,
		},
		{
			name: "check unavailable",
			response: serverapi.UpdateStatusResponse{
				Result: serverapi.CheckUnavailableUpdateStatusResult(),
			},
			applyResponse: true,
		},
		{
			name: "available",
			response: serverapi.UpdateStatusResponse{
				Result: serverapi.AvailableUpdateStatusResult("1.2.3", "123456789.123456789.123456789"),
			},
			applyResponse: true,
			additionalRow: true,
		},
		{
			name: "check failed",
			response: serverapi.UpdateStatusResponse{
				Result: serverapi.FailedUpdateStatusResult("release metadata could not be decoded from the remote response"),
			},
			applyResponse: true,
			additionalRow: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newUninitializedTestSessionPickerModel(t, nil, sessionPickerHeaderInfo{Version: "1.2.3"})
			model.width = width
			if test.applyResponse {
				model.Update(sessionPickerUpdateStatusMsg{response: test.response})
			}

			rendered := model.renderHeader()
			if renderedWidth := lipgloss.Width(ansi.Strip(rendered)); renderedWidth > width {
				t.Fatalf("rendered header width = %d, want <= %d", renderedWidth, width)
			}
			wantHeight := baseHeight
			if test.additionalRow {
				wantHeight++
			}
			if height := lipgloss.Height(rendered); height != wantHeight {
				t.Fatalf("rendered header height = %d, want %d", height, wantHeight)
			}
		})
	}
}
