package app

import (
	"errors"
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

type uiNativeProgressState struct {
	phase        uiNativeProgressPhase
	generation   uint64
	delayElapsed bool
	pending      *nativeProgressWriteKind
}

type nativeProgressDelayMsg struct {
	generation uint64
}

type nativeProgressWriteDoneMsg struct {
	kind nativeProgressWriteKind
	err  error
}

func (m *uiModel) nativeProgressEligible() bool {
	if m == nil {
		return false
	}
	return m.isCompacting() ||
		m.runtimeActivityProjection.Reviewer == clientui.ReviewerActivityInvoking ||
		m.pendingDetailTranscript != nil ||
		m.worktrees.create.submitting ||
		m.worktrees.deleteConfirm.submitting
}

func (m *uiModel) reconcileNativeProgress() tea.Cmd {
	if m == nil || !m.tuiNativeProgressBar || m.terminalOutput == nil {
		if m != nil {
			m.nativeProgress = uiNativeProgressState{}
		}
		return nil
	}
	if m.exitAction != UIActionNone {
		m.nativeProgress.phase = uiNativeProgressHidden
		m.nativeProgress.delayElapsed = false
		m.nativeProgress.generation++
		return nil
	}
	if m.nativeProgress.pending != nil {
		return nil
	}
	if !m.nativeProgressEligible() {
		switch m.nativeProgress.phase {
		case uiNativeProgressWaiting:
			m.nativeProgress.phase = uiNativeProgressHidden
			m.nativeProgress.delayElapsed = false
			m.nativeProgress.generation++
		case uiNativeProgressVisible:
			m.nativeProgress.pending = nativeProgressWritePointer(uiNativeProgressReset)
			return m.nativeProgressWriteCmd(uiNativeProgressReset)
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
			m.nativeProgress.pending = nativeProgressWritePointer(uiNativeProgressShow)
			return m.nativeProgressWriteCmd(uiNativeProgressShow)
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
		if *m.nativeProgress.pending != msg.kind {
			panic("native progress completion does not match pending write")
		}
		m.nativeProgress.pending = nil
		if msg.err != nil {
			return handledUIFeatureUpdate(m, m.handleFatalUIError("native progress output failed", msg.err))
		}
		switch msg.kind {
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

func (m *uiModel) nativeProgressWriteCmd(kind nativeProgressWriteKind) tea.Cmd {
	return func() tea.Msg {
		if m == nil || m.terminalOutput == nil {
			return nativeProgressWriteDoneMsg{kind: kind, err: errors.New("terminal output is required")}
		}
		sequence := xansi.ResetProgressBar
		if kind == uiNativeProgressShow {
			sequence = xansi.SetIndeterminateProgressBar
		}
		_, err := m.terminalOutput.Write([]byte(sequence))
		return nativeProgressWriteDoneMsg{kind: kind, err: err}
	}
}

func nativeProgressWritePointer(kind nativeProgressWriteKind) *nativeProgressWriteKind {
	return &kind
}
