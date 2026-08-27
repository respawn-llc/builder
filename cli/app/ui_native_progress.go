package app

import (
	"errors"
	"sync"
	"time"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
)

const nativeProgressDelay = 500 * time.Millisecond

type uiNativeProgressPhase uint8

const (
	uiNativeProgressHidden uiNativeProgressPhase = iota
	uiNativeProgressWaiting
	uiNativeProgressVisible
)

type nativeProgressWriteKind uint8

const (
	uiNativeProgressShow nativeProgressWriteKind = iota
	uiNativeProgressReset
)

type nativeProgressWrite struct {
	kind nativeProgressWriteKind
	gate *nativeProgressWriteGate
}

type nativeProgressWriteGate struct {
	mu       sync.Mutex
	canceled bool
	written  bool
}

// The gate makes cancellation and the terminal write one ordered operation, so
// an exit reset cannot be overtaken by a stale asynchronous show command.
func (g *nativeProgressWriteGate) cancel() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.canceled = true
	return g.written
}

func (g *nativeProgressWriteGate) write(write func() error) (bool, error) {
	if g == nil {
		return false, errors.New("native progress write gate is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.canceled {
		return false, nil
	}
	g.written = true
	return true, write()
}

type uiNativeProgressState struct {
	phase        uiNativeProgressPhase
	generation   uint64
	delayElapsed bool
	pending      *nativeProgressWrite
}

type nativeProgressDelayMsg struct {
	generation uint64
}

type nativeProgressWriteDoneMsg struct {
	write    *nativeProgressWrite
	canceled bool
	err      error
}

func (m *uiModel) nativeProgressEligible() bool {
	if m == nil {
		return false
	}
	return m.isCompacting() ||
		m.runtimeActivityProjection.Reviewer == clientui.ReviewerActivityInvoking ||
		(m.pendingDetailTranscript != nil && m.pendingDetailTranscript.detailMode) ||
		m.worktrees.create.submitting ||
		m.worktrees.deleteConfirm.submitting
}

func (m *uiModel) reconcileNativeProgress() tea.Cmd {
	if m == nil || !m.tuiNativeProgressBar || m.terminalOutput == nil {
		if m != nil {
			m.cancelPendingNativeProgressWrite()
			m.nativeProgress = uiNativeProgressState{}
		}
		return nil
	}
	if m.exitAction != UIActionNone {
		m.cancelPendingNativeProgressWrite()
		m.nativeProgress.phase = uiNativeProgressHidden
		m.nativeProgress.delayElapsed = false
		m.nativeProgress.generation++
		return nil
	}
	if m.nativeProgress.pending != nil {
		if m.nativeProgress.pending.kind == uiNativeProgressShow && !m.nativeProgressEligible() {
			written := m.cancelPendingNativeProgressWrite()
			m.nativeProgress.phase = uiNativeProgressHidden
			m.nativeProgress.delayElapsed = false
			m.nativeProgress.generation++
			if written {
				m.nativeProgress.phase = uiNativeProgressVisible
				m.nativeProgress.pending = newNativeProgressWrite(uiNativeProgressReset)
				return m.nativeProgressWriteCmd(m.nativeProgress.pending)
			}
		}
		return nil
	}
	if !m.nativeProgressEligible() {
		switch m.nativeProgress.phase {
		case uiNativeProgressWaiting:
			m.nativeProgress.phase = uiNativeProgressHidden
			m.nativeProgress.delayElapsed = false
			m.nativeProgress.generation++
		case uiNativeProgressVisible:
			m.nativeProgress.pending = newNativeProgressWrite(uiNativeProgressReset)
			return m.nativeProgressWriteCmd(m.nativeProgress.pending)
		}
		return nil
	}
	switch m.nativeProgress.phase {
	case uiNativeProgressHidden:
		m.nativeProgress.phase = uiNativeProgressWaiting
		m.nativeProgress.delayElapsed = false
		m.nativeProgress.generation++
		generation := m.nativeProgress.generation
		return tea.Tick(nativeProgressDelay, func(time.Time) tea.Msg {
			return nativeProgressDelayMsg{generation: generation}
		})
	case uiNativeProgressWaiting:
		if m.nativeProgress.delayElapsed {
			m.nativeProgress.pending = newNativeProgressWrite(uiNativeProgressShow)
			return m.nativeProgressWriteCmd(m.nativeProgress.pending)
		}
	}
	return nil
}

func (m *uiModel) reduceNativeProgressMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case nativeProgressDelayMsg:
		if m == nil ||
			!m.tuiNativeProgressBar ||
			m.nativeProgress.phase != uiNativeProgressWaiting ||
			m.nativeProgress.generation != msg.generation {
			return handledUIFeatureUpdate(m, nil)
		}
		if !m.nativeProgressEligible() || m.exitAction != UIActionNone {
			m.cancelPendingNativeProgressWrite()
			m.nativeProgress.phase = uiNativeProgressHidden
			m.nativeProgress.delayElapsed = false
			m.nativeProgress.generation++
			return handledUIFeatureUpdate(m, nil)
		}
		m.nativeProgress.delayElapsed = true
		return handledUIFeatureUpdate(m, nil)
	case nativeProgressWriteDoneMsg:
		if m == nil || m.nativeProgress.pending == nil {
			return handledUIFeatureUpdate(m, nil)
		}
		if m.nativeProgress.pending != msg.write {
			if msg.err != nil && !msg.canceled {
				return handledUIFeatureUpdate(m, m.handleFatalUIError("native progress output failed", msg.err))
			}
			return handledUIFeatureUpdate(m, nil)
		}
		write := m.nativeProgress.pending
		m.nativeProgress.pending = nil
		if msg.err != nil {
			return handledUIFeatureUpdate(m, m.handleFatalUIError("native progress output failed", msg.err))
		}
		if msg.canceled {
			m.nativeProgress.phase = uiNativeProgressHidden
			m.nativeProgress.delayElapsed = false
			return handledUIFeatureUpdate(m, nil)
		}
		switch write.kind {
		case uiNativeProgressShow:
			m.nativeProgress.phase = uiNativeProgressVisible
			m.nativeProgress.delayElapsed = false
		case uiNativeProgressReset:
			m.nativeProgress.phase = uiNativeProgressHidden
			m.nativeProgress.delayElapsed = false
		default:
			panic("unknown native progress write kind")
		}
		return handledUIFeatureUpdate(m, nil)
	default:
		return uiFeatureUpdateResult{}
	}
}

func (m *uiModel) nativeProgressWriteCmd(write *nativeProgressWrite) tea.Cmd {
	if write == nil {
		return func() tea.Msg {
			return nativeProgressWriteDoneMsg{err: errors.New("native progress write is required")}
		}
	}
	var output *uiTerminalOutput
	if m != nil {
		output = m.terminalOutput
	}
	return func() tea.Msg {
		if output == nil {
			return nativeProgressWriteDoneMsg{write: write, err: errors.New("terminal output is required")}
		}
		sequence := xansi.ResetProgressBar
		if write.kind == uiNativeProgressShow {
			sequence = xansi.SetIndeterminateProgressBar
		}
		written, err := write.gate.write(func() error {
			_, err := output.Write([]byte(sequence))
			return err
		})
		return nativeProgressWriteDoneMsg{write: write, canceled: !written, err: err}
	}
}

func (m *uiModel) cancelPendingNativeProgressWrite() bool {
	if m == nil || m.nativeProgress.pending == nil {
		return false
	}
	pending := m.nativeProgress.pending
	m.nativeProgress.pending = nil
	return pending.gate.cancel()
}

func newNativeProgressWrite(kind nativeProgressWriteKind) *nativeProgressWrite {
	return &nativeProgressWrite{
		kind: kind,
		gate: &nativeProgressWriteGate{},
	}
}
