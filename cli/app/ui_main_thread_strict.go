package app

import (
	"fmt"
	"strings"
)

type uiMainThreadState struct {
	depth    int
	activity string
}

func (m *uiModel) enterUIMainThread(activity string) func() {
	if m == nil {
		return func() {}
	}
	previous := m.uiMainThread.activity
	m.uiMainThread.depth++
	m.uiMainThread.activity = strings.TrimSpace(activity)
	return func() {
		if m.uiMainThread.depth > 0 {
			m.uiMainThread.depth--
		}
		if m.uiMainThread.depth == 0 {
			m.uiMainThread.activity = previous
		}
	}
}

func (m *uiModel) checkTUIBlockingOperation(kind, detail string) {
	if m == nil || m.uiMainThread.depth <= 0 {
		return
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "blocking operation"
	}
	detail = strings.TrimSpace(detail)
	message := fmt.Sprintf("TUI main-thread I/O violation during %s: %s", m.uiMainThread.activity, kind)
	if detail != "" {
		message += " (" + detail + ")"
	}
	m.logf("tui.main_thread_violation err=%q", message)
	panic(message)
}
