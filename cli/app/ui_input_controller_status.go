package app

import (
	"strings"
)

func (m *uiModel) reviewerInvocationState() (bool, string) {
	mode := strings.ToLower(strings.TrimSpace(m.reviewerMode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(m.cachedRuntimeStatus().ReviewerFrequency))
	}
	if mode == "" {
		mode = "off"
	}
	return mode != "off", mode
}

func (m *uiModel) fastModeState() (bool, bool) {
	return m.fastModeAvailable, m.fastModeEnabled
}
