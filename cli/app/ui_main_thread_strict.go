package app

import (
	"fmt"
	"io"
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

func (m *uiModel) assertUITerminalMainThread(kind string) {
	if m == nil || m.uiMainThread.depth > 0 {
		return
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "terminal write"
	}
	m.logf("tui.main_thread_violation err=%q", "TUI terminal write outside main thread: "+kind)
}

type uiMainThreadTerminalWriter struct {
	model *uiModel
	out   io.Writer
	kind  string
}

func (w uiMainThreadTerminalWriter) Write(payload []byte) (int, error) {
	if w.model != nil {
		w.model.assertUITerminalMainThread(w.kind)
	}
	return w.out.Write(payload)
}
