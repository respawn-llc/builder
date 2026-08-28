package app

import (
	"errors"
	"fmt"

	"core/cli/app/internal/runtimeattach"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"

	tea "github.com/charmbracelet/bubbletea"
)

type pendingWorkRefreshOwner struct {
	sessionID        runtimeids.SessionID
	generation       uint64
	inFlight         bool
	followUpRequired bool
	collection       runtimeinput.PendingWork
	successfulFetch  bool
}

type pendingWorkRefreshDoneMsg struct {
	generation  uint64
	pendingWork runtimeinput.PendingWork
	err         error
}

type pendingWorkListClient interface {
	ListPendingWork(runtimeids.SessionID) (runtimeinput.PendingWork, error)
}

func (m *uiModel) advancePendingWorkRefreshScope(sessionID runtimeids.SessionID) tea.Cmd {
	if m == nil || sessionID.IsZero() {
		return nil
	}
	owner := &m.pendingWorkRefresh
	owner.sessionID = sessionID
	owner.generation++
	if owner.generation == 0 {
		panic("Pending Work hydration generation overflow")
	}
	owner.inFlight = false
	owner.followUpRequired = false
	owner.collection = runtimeinput.PendingWork{}
	owner.successfulFetch = false
	return m.startPendingWorkRefresh()
}

func (m *uiModel) requestPendingWorkRefresh(sessionID runtimeids.SessionID) tea.Cmd {
	if m == nil || sessionID.IsZero() || sessionID != m.pendingWorkRefresh.sessionID {
		return nil
	}
	if m.pendingWorkRefresh.inFlight {
		m.pendingWorkRefresh.followUpRequired = true
		return nil
	}
	return m.startPendingWorkRefresh()
}

func (m *uiModel) startPendingWorkRefresh() tea.Cmd {
	if m == nil || m.pendingWorkRefresh.sessionID.IsZero() || m.pendingWorkRefresh.inFlight {
		return nil
	}
	sessionID := m.pendingWorkRefresh.sessionID
	generation := m.pendingWorkRefresh.generation
	m.pendingWorkRefresh.inFlight = true
	client, ok := m.runtimeClient().(pendingWorkListClient)
	return func() tea.Msg {
		if !ok {
			return pendingWorkRefreshDoneMsg{
				generation: generation,
				err:        errors.New("runtime Pending Work list is unavailable"),
			}
		}
		pendingWork, err := client.ListPendingWork(sessionID)
		return pendingWorkRefreshDoneMsg{
			generation:  generation,
			pendingWork: pendingWork,
			err:         err,
		}
	}
}

func (m *uiModel) applyPendingWorkRefreshDone(msg pendingWorkRefreshDoneMsg) tea.Cmd {
	if m == nil || msg.generation != m.pendingWorkRefresh.generation {
		return nil
	}
	owner := &m.pendingWorkRefresh
	owner.inFlight = false
	if msg.err == nil {
		owner.collection = msg.pendingWork
		owner.successfulFetch = true
	} else if !owner.successfulFetch {
		owner.collection = runtimeinput.PendingWork{}
	}
	followUpRequired := owner.followUpRequired
	owner.followUpRequired = false

	var errorCmd tea.Cmd
	if msg.err != nil {
		detail := runtimeattach.FormatSubmissionError(fmt.Errorf("refresh Pending Work: %w", msg.err))
		m.logf("pending_work.refresh.error err=%q", detail)
		errorCmd = m.sendTransientStatusWithNoticeID(
			detail,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
	var followUp tea.Cmd
	if followUpRequired {
		followUp = m.startPendingWorkRefresh()
	}
	return tea.Batch(errorCmd, followUp)
}
