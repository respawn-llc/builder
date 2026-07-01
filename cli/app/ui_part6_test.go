package app

import (
	"context"
	"core/cli/tui"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPSOverlayInlineAppendsOutputToInputAndReturnsToOngoing(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	start := func(label string) string {
		res, startErr := manager.Start(context.Background(), shelltool.ExecRequest{
			Command:        []string{"sh", "-c", fmt.Sprintf("printf '%s\\n'; sleep 1", label)},
			DisplayCommand: label,
			Workdir:        workdir,
			YieldTime:      fastBackgroundTestYield,
		})
		if startErr != nil {
			t.Fatalf("start %s: %v", label, startErr)
		}
		if !res.Backgrounded {
			t.Fatalf("expected %s to move to background", label)
		}
		return res.SessionID
	}

	firstID := start("first-job")
	secondID := start("second-job")

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.termWidth = 100
	m.termHeight = 14
	m.windowSizeKnown = true
	m.input = "/ps"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = completeProcessRefreshForTest(t, updated)
	selected, ok := updated.selectedProcess()
	if !ok {
		t.Fatal("expected a selected background process")
	}
	if selected.ID != secondID {
		t.Fatalf("expected newest process %s selected first, got %s", secondID, selected.ID)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	selected, ok = updated.selectedProcess()
	if !ok {
		t.Fatal("expected selection after moving down")
	}
	if selected.ID != firstID {
		t.Fatalf("expected moved selection to reach %s, got %s", firstID, selected.ID)
	}

	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	updated = applyProcessActionCommandForTest(t, updated, cmd)
	if testProcessListOpen(updated) {
		t.Fatal("expected inline paste to close the process overlay")
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected inline paste to return to ongoing mode, got %q", updated.view.Mode())
	}
	if !strings.Contains(updated.input, "Output of bg shell "+firstID+":") {
		t.Fatalf("expected inline paste prefix in input buffer, got %q", updated.input)
	}
	if !strings.Contains(updated.input, "first-job") {
		t.Fatalf("expected pasted shell content in input buffer, got %q", updated.input)
	}
	if !strings.Contains(stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark"))), "Pasted shell transcript") {
		t.Fatal("expected ongoing status line to show pasted shell transcript notice")
	}
}

func TestPSOverlayInlineUnlocksLockedInputBeforeAppending(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'locked-job\n'; sleep 1"},
		DisplayCommand: "locked-job",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start locked-job: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.termWidth = 100
	m.termHeight = 14
	m.windowSizeKnown = true
	m.setRuntimeActivityBusyForTest(true)
	m.input = "queued draft"
	m.setInputSubmitLocked(true)
	m.lockedInjectText = "queued draft"
	m.lockedInjectID = "queue-test-0"
	m.pendingInjected = queuedUserMessagesForTest("queued draft")
	controller := uiInputController{model: m}
	_ = controller.startProcessListFlowCmd()
	updated := m
	updated = completeProcessRefreshForTest(t, updated)
	var next tea.Model
	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	updated = applyProcessActionCommandForTest(t, updated, cmd)

	if updated.isInputSubmitLocked() {
		t.Fatal("expected inline paste to unlock the input box")
	}
	if updated.lockedInjectText != "" {
		t.Fatalf("expected lockedInjectText cleared, got %q", updated.lockedInjectText)
	}
	if len(updated.pendingInjected) != 0 {
		t.Fatalf("expected pending injected messages cleared, got %d", len(updated.pendingInjected))
	}
	if !strings.Contains(updated.input, "Output of bg shell "+res.SessionID+":") {
		t.Fatalf("expected pasted shell output in unlocked draft, got %q", updated.input)
	}
	if !strings.Contains(updated.input, "locked-job") {
		t.Fatalf("expected shell preview content in unlocked draft, got %q", updated.input)
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected inline paste to end in ongoing mode, got %q", updated.view.Mode())
	}
}

func TestDirectPSInlineCommandPastesTranscriptIntoInput(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'direct-inline\n'; sleep 1"},
		DisplayCommand: "direct-inline",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start direct-inline: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.input = "/ps inline " + res.SessionID

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyProcessActionCommandForTest(t, updated, cmd)

	if updated.isBusy() {
		t.Fatal("did not expect /ps inline to start a normal run")
	}
	if testProcessListOpen(updated) {
		t.Fatal("did not expect /ps inline direct command to leave process overlay open")
	}
	if !strings.Contains(updated.input, "Output of bg shell "+res.SessionID+":") {
		t.Fatalf("expected inline shell transcript pasted into input, got %q", updated.input)
	}
	if !strings.Contains(updated.input, "direct-inline") {
		t.Fatalf("expected pasted shell transcript content in input, got %q", updated.input)
	}
	if !strings.Contains(stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark"))), "Pasted shell transcript") {
		t.Fatal("expected direct /ps inline to show pasted shell transcript notice")
	}
}

func TestDirectPSInlineCompletionDoesNotPasteIntoEditedDraft(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'stale-inline\n'; sleep 1"},
		DisplayCommand: "stale-inline",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start stale-inline: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.input = "/ps inline " + res.SessionID
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated.insertInputRunes([]rune("typed while inline pending"))
	updated = applyProcessActionCommandForTest(t, updated, cmd)

	if strings.Contains(updated.input, "Output of bg shell "+res.SessionID+":") || strings.Contains(updated.input, "stale-inline") {
		t.Fatalf("did not expect stale inline completion to paste into edited draft, got %q", updated.input)
	}
	if !strings.Contains(updated.input, "typed while inline pending") {
		t.Fatalf("expected edited draft preserved, got %q", updated.input)
	}
}

func TestDirectPSLogsCommandUsesDefaultOpenSuccess(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'direct-logs\n'; sleep 1"},
		DisplayCommand: "direct-logs",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start direct-logs: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	originalOpenDefault := openDefault
	var openedPath string
	openDefault = func(path string) error {
		openedPath = path
		return nil
	}
	defer func() { openDefault = originalOpenDefault }()

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.input = "/ps logs " + res.SessionID

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyProcessActionCommandForTest(t, updated, cmd)

	if openedPath != res.OutputPath {
		t.Fatalf("expected direct /ps logs to open %q, got %q", res.OutputPath, openedPath)
	}
	if !strings.Contains(stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark"))), "Opened logs") {
		t.Fatal("expected direct /ps logs to show opened logs notice")
	}
	if updated.input != "" {
		t.Fatalf("did not expect /ps logs to modify the input buffer, got %q", updated.input)
	}
}

func TestDirectPSKillCommandSignalsBackgroundProcess(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'direct-kill\n'; sleep 1"},
		DisplayCommand: "direct-kill",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start direct-kill: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected background process")
	}

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.input = "/ps kill " + res.SessionID

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyProcessActionCommandForTest(t, updated, cmd)

	if !strings.Contains(stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark"))), "sent terminate signal to "+res.SessionID) {
		t.Fatalf("expected direct /ps kill to show kill notice, got %q", stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark"))))
	}
	waitForTestCondition(t, 2*time.Second, "background process kill request to be reflected in manager state", func() bool {
		snapshot, ok := findBackgroundSnapshot(manager.List(), res.SessionID)
		return ok && (snapshot.KillRequested || !snapshot.Running)
	})
	snapshot, ok := findBackgroundSnapshot(manager.List(), res.SessionID)
	if !ok {
		t.Fatalf("expected killed process %s to remain visible in manager list", res.SessionID)
	}
	if !snapshot.KillRequested && snapshot.Running {
		t.Fatalf("expected process %s to be kill-requested or stopped, got %+v", res.SessionID, snapshot)
	}
	if updated.input != "" {
		t.Fatalf("did not expect /ps kill to modify the input buffer, got %q", updated.input)
	}
}

func findBackgroundSnapshot(entries []shelltool.Snapshot, id string) (shelltool.Snapshot, bool) {
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return shelltool.Snapshot{}, false
}

func TestPSOverlayRefreshTickUpdatesEntriesWhileOpen(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.termWidth = 100
	m.termHeight = 14
	m.windowSizeKnown = true
	m.input = "/ps"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if got := len(updated.processList.entries); got != 0 {
		t.Fatalf("expected empty /ps list before refresh tick, got %d", got)
	}

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'tick-job\n'; sleep 1"},
		DisplayCommand: "tick-job",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start tick-job: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected tick-job to move to background")
	}

	next, cmd := updated.Update(processListRefreshTickMsg{})
	updated = next.(*uiModel)
	updated = completeProcessRefreshForTest(t, updated)
	if got := len(updated.processList.entries); got != 1 {
		t.Fatalf("expected refresh tick to pull new process entry, got %d", got)
	}
	if updated.processList.entries[0].ID != res.SessionID {
		t.Fatalf("expected refresh tick to load session %s, got %s", res.SessionID, updated.processList.entries[0].ID)
	}
	if cmd == nil {
		t.Fatal("expected refresh tick to schedule the next refresh")
	}
}

func TestPSOverlayRefreshPreservesSelectionByProcessID(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	first, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'first\n'; sleep 1"},
		DisplayCommand: "first",
		Workdir:        workdir,
		YieldTime:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start first job: %v", err)
	}
	second, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'second\n'; sleep 1"},
		DisplayCommand: "second",
		Workdir:        workdir,
		YieldTime:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start second job: %v", err)
	}

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	refreshProcessEntriesForTest(t, m)
	if len(m.processList.entries) != 2 {
		t.Fatalf("expected two process entries, got %d", len(m.processList.entries))
	}
	selectedID := first.SessionID
	if m.processList.entries[0].ID == selectedID {
		selectedID = second.SessionID
	}
	for idx, entry := range m.processList.entries {
		if entry.ID == selectedID {
			m.processList.selection = idx
			break
		}
	}

	_, err = manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'third\n'; sleep 1"},
		DisplayCommand: "third",
		Workdir:        workdir,
		YieldTime:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start third job: %v", err)
	}

	refreshProcessEntriesForTest(t, m)
	if m.processList.entries[m.processList.selection].ID != selectedID {
		t.Fatalf("expected selection to remain on process %s, got %s", selectedID, m.processList.entries[m.processList.selection].ID)
	}
}

func TestOpenLogsReportsErrorWhenDefaultOpenFails(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'log-job\n'; sleep 1"},
		DisplayCommand: "log-job",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start log-job: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected log-job to move to background")
	}

	originalOpenDefault := openDefault
	openDefault = func(string) error { return errors.New("forced open failure") }
	defer func() { openDefault = originalOpenDefault }()
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.input = "/ps logs " + res.SessionID
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	updated = applyProcessActionCommandForTest(t, updated, cmd)
	status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if !strings.Contains(status, "open logs failed") {
		t.Fatalf("expected open failure status, got %q", status)
	}
}

func TestOpenLogsFallsBackToEditorCommandWhenDefaultOpenFails(t *testing.T) {
	manager := newFastBackgroundTestManager(t)

	workdir := t.TempDir()
	res, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"sh", "-c", "printf 'log-job\n'; sleep 1"},
		DisplayCommand: "log-job",
		Workdir:        workdir,
		YieldTime:      fastBackgroundTestYield,
	})
	if err != nil {
		t.Fatalf("start log-job: %v", err)
	}
	if !res.Backgrounded {
		t.Fatal("expected log-job to move to background")
	}

	originalOpenDefault := openDefault
	openDefault = func(string) error { return errors.New("forced open failure") }
	defer func() { openDefault = originalOpenDefault }()
	marker := t.TempDir() + "/editor-opened"
	t.Setenv("VISUAL", "touch "+marker)
	t.Setenv("EDITOR", "")
	t.Setenv("SHELL", "/bin/sh")

	m := newProjectedStaticUIModel(withUIBackgroundManagerForTest(manager))
	m.input = "/ps logs " + res.SessionID
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msgs := collectCmdMessages(t, cmd)
	var done processActionDoneMsg
	found := false
	for _, msg := range msgs {
		if typed, ok := msg.(processActionDoneMsg); ok {
			done = typed
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected process action completion, got %+v", msgs)
	}
	if done.editorCmd == nil {
		t.Fatal("expected editor fallback command")
	}
	if err := done.editorCmd.Run(); err != nil {
		t.Fatalf("run editor fallback: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected editor fallback to create marker: %v", err)
	}
}

func TestPSOverlayIgnoresTranscriptModeTogglesWhileOpen(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.termWidth = 100
	m.termHeight = 14
	m.windowSizeKnown = true
	m.input = "/ps"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if !testProcessListOpen(updated) || updated.surface() != uiSurfaceProcessList {
		t.Fatalf("expected /ps overlay surface open, visible=%t surface=%q", testProcessListOpen(updated), updated.surface())
	}

	next, cmd := updated.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	updated = next.(*uiModel)
	if !testProcessListOpen(updated) || !testProcessListSurfaceActive(updated) {
		t.Fatalf("expected shift+tab ignored while /ps overlay open, visible=%t surface=%t", testProcessListOpen(updated), testProcessListSurfaceActive(updated))
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected shift+tab to keep ongoing transcript mode while /ps overlay open, got %q", updated.view.Mode())
	}
	if cmd != nil {
		t.Fatal("expected no transcript toggle command while /ps overlay is open")
	}

	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	updated = next.(*uiModel)
	if !testProcessListOpen(updated) || !testProcessListSurfaceActive(updated) {
		t.Fatalf("expected ctrl+t ignored while /ps overlay open, visible=%t surface=%t", testProcessListOpen(updated), testProcessListSurfaceActive(updated))
	}
	if updated.view.Mode() != tui.ModeOngoing {
		t.Fatalf("expected ctrl+t to keep ongoing transcript mode while /ps overlay open, got %q", updated.view.Mode())
	}
	if cmd != nil {
		t.Fatal("expected no transcript toggle command for ctrl+t while /ps overlay is open")
	}
}

func TestSlashCommandPickerHidesInArgumentMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	updated := next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new")})
	updated = next.(*uiModel)
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeySpace})
	updated = next.(*uiModel)

	lines := updated.layout().renderActivePicker(80)
	if len(lines) != 0 {
		t.Fatalf("expected hidden picker in argument mode, got %d lines", len(lines))
	}
}

func TestSlashCommandArrowKeysNavigatePickerAndReplaceInput(t *testing.T) {
	m := newProjectedStaticUIModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	updated := next.(*uiModel)

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.input != "/new" {
		t.Fatalf("expected first down to select /new, got %q", updated.input)
	}

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = next.(*uiModel)
	if updated.input != "/exit" {
		t.Fatalf("expected second down to select /exit, got %q", updated.input)
	}
}

func TestSlashCommandArrowKeysDoNotOverrideArgumentMode(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/new arg"
	m.inputCursor = -1

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := next.(*uiModel)
	if updated.input != "/new arg" {
		t.Fatalf("expected argument input unchanged, got %q", updated.input)
	}
	if updated.inputCursor != 0 {
		t.Fatalf("expected regular cursor navigation, got %d", updated.inputCursor)
	}
}

func TestSlashCommandTabAutocompletesSelectedCommandAndAddsSpace(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/ne"
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if updated.input != "/new " {
		t.Fatalf("expected tab to autocomplete /new with trailing space, got %q", updated.input)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected no queued messages after autocomplete, got %d", len(updated.queued))
	}
	if updated.isBusy() {
		t.Fatal("did not expect autocomplete to start submission")
	}
}

func TestSlashCommandEnterExecutesSelectedPartialMatch(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.input = "/ex"
	m.refreshSlashCommandFilterFromInputWithAuth(true)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected quit cmd for selected /exit partial match")
	}
	if updated.exitAction != UIActionExit {
		t.Fatalf("expected UIActionExit, got %q", updated.exitAction)
	}
	if updated.input != "" {
		t.Fatalf("expected input cleared after slash command execution, got %q", updated.input)
	}
}

func TestBusyTabQueuesSlashCommandAndFlushesAfterTurn(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.input = "/name queued title"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/name queued title" {
		t.Fatalf("expected queued slash command, got %+v", updated.queued)
	}
	if updated.sessionName != "" {
		t.Fatalf("did not expect queued slash command to execute immediately, got %q", updated.sessionName)
	}

	next, cmd := updated.Update(submitDoneMsg{})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected follow-up command from queued /name execution")
	}
	if updated.sessionName != "queued title" {
		t.Fatalf("expected queued /name to execute after turn, got %q", updated.sessionName)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued slash command drained, got %+v", updated.queued)
	}
}

func TestBusyQueuedSlashCommandDrainContinuesIntoQueuedPrompt(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.input = "/name queued title"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	updated.input = "follow up"

	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated = next.(*uiModel)
	if len(updated.queued) != 2 {
		t.Fatalf("expected two queued items, got %+v", updated.queued)
	}

	next, _ = updated.Update(submitDoneMsg{})
	updated = next.(*uiModel)
	if updated.sessionName != "queued title" {
		t.Fatalf("expected queued /name to execute before queued prompt, got %q", updated.sessionName)
	}
	if !updated.isBusy() {
		t.Fatal("expected queued prompt to auto-submit after queued slash command")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued items fully drained, got %+v", updated.queued)
	}
	plain := stripANSIAndTrimRight(updated.view.OngoingSnapshot())
	if !strings.Contains(plain, "follow up") {
		t.Fatalf("expected queued prompt in transcript, got %q", plain)
	}
}

func TestSubmitDoneWithRuntimeClientDoesNotRequestTranscriptCatchUpWithoutQueuedWork(t *testing.T) {
	client := &refreshingRuntimeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning

	next, cmd := m.Update(submitDoneMsg{message: "ignored by runtime-backed flow"})
	updated := next.(*uiModel)
	if cmd != nil {
		t.Fatalf("did not expect transcript sync command after ordinary runtime-backed submit completion, got %T", cmd())
	}
	if updated.activity != uiActivityIdle {
		t.Fatalf("expected idle activity after submit completion, got %v", updated.activity)
	}
	if client.calls != 0 {
		t.Fatalf("refresh call count = %d, want 0", client.calls)
	}
}

func TestSubmitDoneWithQueuedWorkWaitsForInFlightTranscriptCatchUp(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
		}},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("follow up")
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7

	next, cmd := m.Update(submitDoneMsg{message: "ignored by runtime-backed flow"})
	updated := next.(*uiModel)
	if cmd != nil {
		t.Fatalf("expected no new transcript sync command while refresh is already in flight, got %T", cmd())
	}
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued drain deferred until hydration completes")
	}
	if updated.isBusy() {
		t.Fatal("expected submit completion to leave runtime-backed UI idle while waiting for hydration")
	}
	if client.submitText != "" {
		t.Fatalf("did not expect queued follow-up to start before hydration settles, got %q", client.submitText)
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "follow up" {
		t.Fatalf("expected queued follow-up preserved until hydration finishes, got %+v", updated.queued)
	}

	next, applyCmd := updated.Update(runtimeTranscriptRefreshedMsg{token: 7, transcript: client.transcripts[0]})
	updated = next.(*uiModel)
	if !updated.runtimeTranscriptBusy {
		t.Fatal("expected dirty transcript sync to schedule a follow-up hydration before queued drain")
	}
	if updated.isBusy() {
		t.Fatal("did not expect queued follow-up submission before authoritative hydration settles")
	}
	if updated.pendingQueuedDrainAfterHydration == false {
		t.Fatal("expected deferred queued drain to remain armed until follow-up hydration completes")
	}
	if client.submitText != "" {
		t.Fatalf("did not expect queued follow-up submit before final hydration settles, got %q", client.submitText)
	}
	if applyCmd == nil {
		t.Fatal("expected follow-up hydration command after dirty in-flight refresh completes")
	}
	refreshAgain, ok := applyCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg from follow-up hydration command, got %T", applyCmd())
	}
	if refreshAgain.syncCause != runtimeTranscriptSyncCauseQueuedDrain {
		t.Fatalf("follow-up sync cause = %q, want %q", refreshAgain.syncCause, runtimeTranscriptSyncCauseQueuedDrain)
	}

	next, finalCmd := updated.Update(refreshAgain)
	updated = next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected queued follow-up submission to begin after hydration completes")
	}
	if updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected deferred queued drain flag cleared after hydration completion")
	}
	if updated.activeSubmit.text != "follow up" {
		t.Fatalf("expected queued follow-up to submit after hydration, got %q", updated.activeSubmit.text)
	}
	if got := stripANSIAndTrimRight(updated.view.OngoingSnapshot()); !strings.Contains(got, "final answer") {
		t.Fatalf("expected hydration to commit final answer before queued drain, got %q", got)
	}
	if finalCmd == nil {
		t.Fatal("expected follow-up command sequence after hydration")
	}
}

func TestStaleHydrateKeepsQueuedDrainReadyAfterCommittedGapUserFlush(t *testing.T) {
	client := &refreshingRuntimeClient{}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.pendingInjected = queuedUserMessagesForTest("steered message")
	m.input = "steered message"
	m.lockedInjectText = "steered message"
	m.lockedInjectID = "queue-test-0"
	m.setInputSubmitLocked(true)
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "user", Text: "seed"}}
	m.transcriptRevision = 6
	m.transcriptTotalEntries = 1
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries, Ongoing: "working"})
	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{Kind: clientui.EventAssistantDelta, StepID: "step-1", AssistantDelta: "working"}).cmd

	_ = m.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                         clientui.EventUserMessageFlushed,
		StepID:                       "step-1",
		CommittedTranscriptChanged:   true,
		TranscriptRevision:           7,
		UserMessage:                  "steered message",
		UserMessageBatchQueueItemIDs: []string{"queue-test-0"},
		TranscriptEntries:            []clientui.ChatEntry{{Role: "user", Text: "steered message"}},
	}).
		cmd
	if got := len(m.deferredCommittedTail); got != 1 {
		t.Fatalf("expected queued user flush to use deferred committed tail while assistant stream is live, got %d", got)
	}

	m.setRuntimeActivityBusyForTest(false)
	m.activity = uiActivityIdle
	m.queued = queuedInputsForTest("follow up")
	m.pendingQueuedDrainAfterHydration = true
	m.queuedDrainReadyAfterHydration = false
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7

	next, cmd := m.Update(runtimeTranscriptRefreshedMsg{
		token: 7,
		req:   clientui.TranscriptPageRequest{},
		transcript: clientui.TranscriptPage{
			SessionID: "session-1",
			Revision:  6,
			Entries:   []clientui.ChatEntry{{Role: "user", Text: "seed"}},
		},
	})
	updated := next.(*uiModel)
	if got := len(updated.deferredCommittedTail); got != 1 {
		t.Fatalf("expected stale hydrate + queued drain path to preserve deferred committed tail, got %d", got)
	}
	if updated.activeSubmit.text != "follow up" {
		t.Fatalf("expected queued drain to continue after stale hydrate rejection, got active=%q", updated.activeSubmit.text)
	}
	if !updated.isBusy() {
		t.Fatal("expected queued drain to start the next submission after stale hydrate rejection")
	}
	if cmd == nil {
		t.Fatal("expected queued follow-up command after stale hydrate rejection")
	}
}

func TestHydrationCompletionDoesNotRedrainQueuedTurnAfterManualDrainStarts(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
		}},
	}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil
	m.setRuntimeActivityBusyForTest(true)
	m.activity = uiActivityRunning
	m.queued = queuedInputsForTest("follow up")
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7

	next, _ := m.Update(submitDoneMsg{message: "ignored by runtime-backed flow"})
	updated := next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected queued drain deferred until hydration completes")
	}

	next, drainCmd := updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if !updated.isBusy() {
		t.Fatal("expected manual queued drain to start submission while hydration is still pending")
	}
	if updated.activeSubmit.text != "follow up" {
		t.Fatalf("expected manual drain to own queued follow-up, got %q", updated.activeSubmit.text)
	}
	if drainCmd == nil {
		t.Fatal("expected submit command from manual queued drain")
	}
	_ = collectCmdMessages(t, drainCmd)
	if client.submitText != "follow up" {
		t.Fatalf("expected submit after manual drain starts, got %q", client.submitText)
	}

	next, refreshCmd := updated.Update(runtimeTranscriptRefreshedMsg{token: 7, transcript: client.transcripts[0]})
	updated = next.(*uiModel)
	if !updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected deferred drain to remain armed until dirty follow-up hydration completes")
	}
	if refreshCmd == nil {
		t.Fatal("expected dirty follow-up hydration after in-flight refresh completes")
	}
	followRefresh, ok := refreshCmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected follow-up hydration message, got %T", refreshCmd())
	}

	next, finalCmd := updated.Update(followRefresh)
	updated = next.(*uiModel)
	if updated.pendingQueuedDrainAfterHydration {
		t.Fatal("expected final hydration completion to drop stale deferred drain once manual drain owns the queued turn")
	}
	msgs := collectCmdMessages(t, finalCmd)
	_ = msgs
}
