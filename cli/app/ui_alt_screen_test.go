package app

import (
	"strings"
	"testing"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestToggleTranscriptModeUsesFixedDetailAltScreen(t *testing.T) {
	m := newProjectedStaticUIModel()

	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("mode=%q want ongoing", m.view.Mode())
	}
	if m.altScreenActive {
		t.Fatal("expected initial alt-screen inactive")
	}

	cmd := m.toggleTranscriptMode()
	if cmd == nil {
		t.Fatal("expected alt-screen command when toggling into detail")
	}
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", m.view.Mode())
	}
	if !m.altScreenActive {
		t.Fatal("expected alt-screen active when entering detail")
	}

	cmd = m.toggleTranscriptMode()
	if cmd == nil {
		t.Fatal("expected alt-screen command when toggling out of detail")
	}
	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("mode=%q want ongoing", m.view.Mode())
	}
	if m.altScreenActive {
		t.Fatal("expected alt-screen inactive after leaving detail")
	}
}

func TestFullscreenSurfaceOpenClosePolicy(t *testing.T) {
	tests := []struct {
		name         string
		wantSurface  uiSurface
		wantOpenMode tui.Mode
		open         func(t *testing.T, m *uiModel) tea.Cmd
		close        func(m *uiModel) tea.Cmd
	}{
		{
			name:         "status",
			wantSurface:  uiSurfaceStatus,
			wantOpenMode: tui.ModeOngoing,
			open: func(_ *testing.T, m *uiModel) tea.Cmd {
				m.openStatusOverlay()
				return m.activateSurface(uiSurfaceStatus)
			},
			close: func(m *uiModel) tea.Cmd {
				cmd := m.restoreTranscriptSurface()
				m.closeStatusOverlay()
				return cmd
			},
		},
		{
			name:         "process",
			wantSurface:  uiSurfaceProcessList,
			wantOpenMode: tui.ModeOngoing,
			open: func(_ *testing.T, m *uiModel) tea.Cmd {
				m.openProcessList()
				return m.activateSurface(uiSurfaceProcessList)
			},
			close: func(m *uiModel) tea.Cmd {
				cmd := m.restoreTranscriptSurface()
				m.closeProcessList()
				return cmd
			},
		},
		{
			name:         "goal",
			wantSurface:  uiSurfaceGoal,
			wantOpenMode: tui.ModeOngoing,
			open: func(_ *testing.T, m *uiModel) tea.Cmd {
				m.openGoalOverlay(nil, nil)
				return m.activateSurface(uiSurfaceGoal)
			},
			close: func(m *uiModel) tea.Cmd {
				cmd := m.restoreTranscriptSurface()
				m.closeGoalOverlay()
				return cmd
			},
		},
		{
			name:         "worktree",
			wantSurface:  uiSurfaceWorktree,
			wantOpenMode: tui.ModeOngoing,
			open: func(_ *testing.T, m *uiModel) tea.Cmd {
				m.openWorktreeOverlay(uiWorktreeOpenIntent{})
				return m.activateSurface(uiSurfaceWorktree)
			},
			close: func(m *uiModel) tea.Cmd {
				cmd := m.restoreTranscriptSurface()
				m.closeWorktreeOverlay()
				return cmd
			},
		},
		{
			name:         "rollback",
			wantSurface:  uiSurfaceRollbackSelection,
			wantOpenMode: tui.ModeDetail,
			open: func(t *testing.T, m *uiModel) tea.Cmd {
				m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt"}}
				m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, TotalEntries: 1})
				seedTestRollbackTargets(m)
				if !m.startRollbackSelectionMode() {
					t.Fatal("expected rollback selection to start")
				}
				return m.pushRollbackOverlayIfNeeded()
			},
			close: func(m *uiModel) tea.Cmd {
				cmd := m.popRollbackOverlay()
				m.stopRollbackSelectionMode()
				return cmd
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			m.windowSizeKnown = true
			m.termWidth = 80
			m.termHeight = 24

			openCmd := tt.open(t, m)
			if openCmd == nil {
				t.Fatal("expected open command")
			}
			if got := m.surface(); got != tt.wantSurface {
				t.Fatalf("surface=%q want %q", got, tt.wantSurface)
			}
			if got := m.view.Mode(); got != tt.wantOpenMode {
				t.Fatalf("mode=%q want %q", got, tt.wantOpenMode)
			}
			if !m.altScreenActive {
				t.Fatal("expected alt-screen active after open")
			}

			closeCmd := tt.close(m)
			if closeCmd == nil {
				t.Fatal("expected close command")
			}
			if got := m.surface(); got != uiSurfaceOngoingTranscript {
				t.Fatalf("surface=%q want ongoing", got)
			}
			if got := m.view.Mode(); got != tui.ModeOngoing {
				t.Fatalf("mode=%q want ongoing", got)
			}
			if m.altScreenActive {
				t.Fatal("expected alt-screen inactive after close")
			}
		})
	}
}

func TestOngoingOverlaySurfacesEnableAndDisableAlternateScroll(t *testing.T) {
	tests := []struct {
		name    string
		surface uiSurface
	}{
		{name: "status", surface: uiSurfaceStatus},
		{name: "goal", surface: uiSurfaceGoal},
		{name: "worktree", surface: uiSurfaceWorktree},
		{name: "process", surface: uiSurfaceProcessList},
	}

	originalWriteTerminalSequence := writeTerminalSequence
	defer func() { writeTerminalSequence = originalWriteTerminalSequence }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sequenceLog := strings.Builder{}
			writeTerminalSequence = func(sequence string) {
				sequenceLog.WriteString(sequence)
			}

			m := newProjectedStaticUIModel()
			m.windowSizeKnown = true
			m.termWidth = 80
			m.termHeight = 24
			if got := m.view.Mode(); got != tui.ModeOngoing {
				t.Fatalf("mode=%q want ongoing", got)
			}

			openCmd := m.activateSurface(tt.surface)
			if openCmd == nil {
				t.Fatal("expected open command")
			}
			_ = collectCmdMessages(t, openCmd)
			if got := sequenceLog.String(); strings.Count(got, "\x1b[?1007h") != 1 || strings.Contains(got, "\x1b[?1007l") {
				t.Fatalf("expected alternate-scroll enabled on overlay open, got sequences %q", got)
			}

			closeCmd := m.restoreTranscriptSurface()
			if closeCmd == nil {
				t.Fatal("expected close command")
			}
			_ = collectCmdMessages(t, closeCmd)

			if got := sequenceLog.String(); strings.Count(got, "\x1b[?1007h") != 1 || strings.Count(got, "\x1b[?1007l") != 1 {
				t.Fatalf("expected paired alternate-scroll enable/disable, got sequences %q", got)
			}
			if got := m.view.Mode(); got != tui.ModeOngoing {
				t.Fatalf("mode=%q want ongoing", got)
			}
			if m.altScreenActive {
				t.Fatal("expected alt-screen inactive after restore")
			}
		})
	}
}

func TestDetailOverlaySurfacesPreserveAlternateScroll(t *testing.T) {
	tests := []struct {
		name    string
		surface uiSurface
	}{
		{name: "status", surface: uiSurfaceStatus},
		{name: "goal", surface: uiSurfaceGoal},
		{name: "worktree", surface: uiSurfaceWorktree},
		{name: "process", surface: uiSurfaceProcessList},
	}

	originalWriteTerminalSequence := writeTerminalSequence
	defer func() { writeTerminalSequence = originalWriteTerminalSequence }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sequenceLog := strings.Builder{}
			writeTerminalSequence = func(sequence string) {
				sequenceLog.WriteString(sequence)
			}

			m := newProjectedStaticUIModel()
			m.windowSizeKnown = true
			m.termWidth = 80
			m.termHeight = 24
			enterDetailCmd := m.toggleTranscriptMode()
			if enterDetailCmd == nil {
				t.Fatal("expected detail-mode open command")
			}
			_ = collectCmdMessages(t, enterDetailCmd)
			if got := m.view.Mode(); got != tui.ModeDetail {
				t.Fatalf("mode=%q want detail", got)
			}
			if !m.altScreenActive {
				t.Fatal("expected alt-screen active after entering detail")
			}

			openCmd := m.activateSurface(tt.surface)
			if openCmd != nil {
				_ = collectCmdMessages(t, openCmd)
			}
			restoreCmd := m.restoreTranscriptSurface()
			if restoreCmd != nil {
				_ = collectCmdMessages(t, restoreCmd)
			}

			if got := sequenceLog.String(); strings.Count(got, "\x1b[?1007h") != 1 || strings.Contains(got, "\x1b[?1007l") {
				t.Fatalf("detail overlay did not preserve alternate scroll, got sequences %q", got)
			}
			if got := m.view.Mode(); got != tui.ModeDetail {
				t.Fatalf("mode=%q want detail", got)
			}
			if !m.altScreenActive {
				t.Fatal("expected alt-screen active after restore")
			}
		})
	}
}

func TestReturningFromDetailSyncsCommittedSuffixTailIntoOngoingView(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, TotalEntries: 1})

	_ = m.toggleTranscriptMode()
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", m.view.Mode())
	}

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "answer"}},
	}).cmd

	_ = m.toggleTranscriptMode()
	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("mode=%q want ongoing", m.view.Mode())
	}
	if got := stripANSIAndTrimRight(m.view.OngoingSnapshot()); !containsAny(got, "answer") {
		t.Fatalf("expected committed suffix in ongoing tail after detail restore, got %q", got)
	}
}

func TestReturningFromDetailPreservesLiveTransientTailInOngoingView(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, TotalEntries: 1})

	_ = m.toggleTranscriptMode()
	if m.view.Mode() != tui.ModeDetail {
		t.Fatalf("mode=%q want detail", m.view.Mode())
	}

	m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{
		Role:      tui.TranscriptRoleToolCall,
		Text:      "echo live",
		Transient: true,
	})
	m.transcriptTotalEntries = 2

	_ = m.toggleTranscriptMode()
	if m.view.Mode() != tui.ModeOngoing {
		t.Fatalf("mode=%q want ongoing", m.view.Mode())
	}
	if got := stripANSIAndTrimRight(m.view.OngoingSnapshot()); !strings.Contains(got, "echo live") {
		t.Fatalf("expected live transient tail after detail restore, got %q", got)
	}
}

func TestReturningFromDetailPreservesTransientTailAndNextLiveAppendInOngoingView(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.transcriptEntries = []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}}
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, TotalEntries: 1})

	_ = m.toggleTranscriptMode()
	m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{
		Role:      tui.TranscriptRoleToolCall,
		Text:      "echo live",
		Transient: true,
	})
	m.transcriptTotalEntries = 2

	_ = m.toggleTranscriptMode()
	if got := stripANSIAndTrimRight(m.view.OngoingSnapshot()); !strings.Contains(got, "echo live") {
		t.Fatalf("expected transient tail after detail restore, got %q", got)
	}

	m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{
		Role:      tui.TranscriptRoleAssistant,
		Text:      "post-restore live append",
		Transient: true,
	})
	m.transcriptTotalEntries = 3
	m.syncRecentTailViewFromRuntimeState()

	got := stripANSIAndTrimRight(m.view.OngoingSnapshot())
	if !strings.Contains(got, "echo live") || !strings.Contains(got, "post-restore live append") {
		t.Fatalf("expected transient tail and next live append after detail restore, got %q", got)
	}
}
