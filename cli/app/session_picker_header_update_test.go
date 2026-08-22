package app

import (
	"testing"

	serverpb "core/shared/protoapi/gen/kent/api/server"

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
		response      *serverpb.GetUpdateStatusSuccess
		applyResponse bool
		additionalRow bool
	}{
		{name: "pending"},
		{
			name:          "current",
			response:      currentUpdateStatusSuccess("1.2.3", "1.2.3"),
			applyResponse: true,
		},
		{
			name:          "check unavailable",
			response:      checkUnavailableUpdateStatusSuccess(),
			applyResponse: true,
		},
		{
			name:          "available",
			response:      availableUpdateStatusSuccess("1.2.3", "123456789.123456789.123456789"),
			applyResponse: true,
			additionalRow: true,
		},
		{
			name:          "check failed",
			response:      failedUpdateStatusSuccess("release metadata could not be decoded from the remote response"),
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
