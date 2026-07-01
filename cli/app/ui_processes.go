package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	uiProcessInlineMaxChars   = 12_000
	uiProcessOperationTimeout = 5 * time.Second
)

func (m *uiModel) applyProcessEntries(entries []clientui.BackgroundProcess) {
	selectedID := ""
	if m.processList.selection >= 0 && m.processList.selection < len(m.processList.entries) {
		selectedID = m.processList.entries[m.processList.selection].ID
	}
	m.processList.entries = append([]clientui.BackgroundProcess(nil), entries...)
	m.processList.errorText = ""
	m.processList.loading = false
	if len(m.processList.entries) == 0 {
		m.processList.selection = 0
		return
	}
	if selectedID != "" {
		for idx, entry := range m.processList.entries {
			if entry.ID == selectedID {
				m.processList.selection = idx
				return
			}
		}
	}
	if m.processList.selection < 0 {
		m.processList.selection = 0
	}
	if m.processList.selection >= len(m.processList.entries) {
		m.processList.selection = len(m.processList.entries) - 1
	}
}

func (m *uiModel) applyBackgroundProcessEventToCache(evt *clientui.BackgroundShellEvent) {
	if m == nil || evt == nil {
		return
	}
	id := strings.TrimSpace(evt.ID)
	if id == "" {
		return
	}
	state := strings.TrimSpace(evt.State)
	remove := strings.EqualFold(strings.TrimSpace(evt.Type), "removed") || strings.EqualFold(state, "removed")
	if remove {
		for idx, entry := range m.processList.entries {
			if entry.ID == id {
				m.processList.entries = append(m.processList.entries[:idx], m.processList.entries[idx+1:]...)
				if m.processList.selection >= len(m.processList.entries) {
					m.processList.selection = max(0, len(m.processList.entries)-1)
				}
				return
			}
		}
		return
	}
	upsert := clientui.BackgroundProcess{
		ID:              id,
		State:           state,
		Command:         strings.TrimSpace(evt.Command),
		Workdir:         strings.TrimSpace(evt.Workdir),
		LogPath:         strings.TrimSpace(evt.LogPath),
		RecentOutput:    evt.Preview,
		OutputAvailable: strings.TrimSpace(evt.Preview) != "",
		ExitCode:        evt.ExitCode,
		Running:         backgroundProcessEventRunning(evt),
		Backgrounded:    true,
	}
	for idx, existing := range m.processList.entries {
		if existing.ID != id {
			continue
		}
		m.processList.entries[idx] = mergeBackgroundProcessCacheEntry(existing, upsert)
		return
	}
	m.processList.entries = append([]clientui.BackgroundProcess{upsert}, m.processList.entries...)
}

func mergeBackgroundProcessCacheEntry(existing, update clientui.BackgroundProcess) clientui.BackgroundProcess {
	next := existing
	if update.State != "" {
		next.State = update.State
		next.Running = update.Running
	}
	if update.Command != "" {
		next.Command = update.Command
	}
	if update.Workdir != "" {
		next.Workdir = update.Workdir
	}
	if update.LogPath != "" {
		next.LogPath = update.LogPath
	}
	if update.RecentOutput != "" {
		next.RecentOutput = update.RecentOutput
		next.OutputAvailable = update.OutputAvailable
	}
	if update.ExitCode != nil {
		next.ExitCode = update.ExitCode
	}
	next.Backgrounded = next.Backgrounded || update.Backgrounded
	return next
}

func backgroundProcessEventRunning(evt *clientui.BackgroundShellEvent) bool {
	if evt == nil {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(evt.State))
	switch state {
	case "starting", "running":
		return true
	case "completed", "failed", "canceled", "cancelled", "done", "exited", "killed", "removed":
		return false
	}
	eventType := strings.ToLower(strings.TrimSpace(evt.Type))
	return eventType == "started" || eventType == "running" || eventType == "backgrounded"
}

func (m *uiModel) openProcessList() {
	m.processList.open = true
	m.processList.surfaceGeneration++
	m.setInputMode(uiInputModeProcessList)
}

func (m *uiModel) closeProcessList() {
	m.processList.open = false
	m.processList.surfaceGeneration++
	m.restorePrimaryInputMode()
}

func (m *uiModel) moveProcessSelection(delta int) {
	if len(m.processList.entries) == 0 {
		m.processList.selection = 0
		return
	}
	m.processList.selection += delta
	if m.processList.selection < 0 {
		m.processList.selection = 0
	}
	if m.processList.selection >= len(m.processList.entries) {
		m.processList.selection = len(m.processList.entries) - 1
	}
}

func (m *uiModel) moveProcessSelectionPage(deltaPages int) {
	rowsPerPage := m.processListRowsPerPage()
	m.moveProcessSelection(deltaPages * rowsPerPage)
}

func (m *uiModel) processListRowsPerPage() int {
	available := m.termHeight - 1 - processListHeaderLines - processListFooterLines // status line + header + footer
	if available < processListEntryLines {
		return 1
	}
	rows := available / processListEntryLines
	if rows < 1 {
		return 1
	}
	return rows
}

func (m *uiModel) selectFirstProcess() {
	if len(m.processList.entries) == 0 {
		m.processList.selection = 0
		return
	}
	m.processList.selection = 0
}

func (m *uiModel) selectLastProcess() {
	if len(m.processList.entries) == 0 {
		m.processList.selection = 0
		return
	}
	m.processList.selection = len(m.processList.entries) - 1
}

func (m *uiModel) selectedProcess() (clientui.BackgroundProcess, bool) {
	if len(m.processList.entries) == 0 || m.processList.selection < 0 || m.processList.selection >= len(m.processList.entries) {
		return clientui.BackgroundProcess{}, false
	}
	return m.processList.entries[m.processList.selection], true
}

func (m *uiModel) nextProcessActionToken() uint64 {
	m.processList.actionToken++
	return m.processList.actionToken
}

func (m *uiModel) requestProcessListRefresh() tea.Cmd {
	if m == nil || !m.processList.open {
		return nil
	}
	if m.processClient == nil {
		m.processList.entries = nil
		m.processList.selection = 0
		m.processList.loading = false
		m.processList.errorText = "background process client is unavailable"
		return nil
	}
	if m.processList.refreshInFlight {
		m.processList.refreshDirty = true
		return nil
	}
	m.processList.refreshToken++
	token := m.processList.refreshToken
	m.processList.refreshInFlight = true
	m.processList.refreshDirty = false
	m.processList.loading = true
	client := m.processClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), uiProcessOperationTimeout)
		defer cancel()
		entries, err := client.ListProcesses(ctx)
		return processListRefreshDoneMsg{token: token, entries: entries, err: err}
	}
}

func (m *uiModel) processActionCmd(action, id, logPath string, inputDraftToken uint64) tea.Cmd {
	if m == nil || m.processClient == nil {
		return nil
	}
	if m.processList.actionInFlight {
		return nil
	}
	token := m.nextProcessActionToken()
	surfaceGeneration := m.processList.surfaceGeneration
	client := m.processClient
	action = strings.ToLower(strings.TrimSpace(action))
	id = strings.TrimSpace(id)
	logPath = strings.TrimSpace(logPath)
	m.processList.actionInFlight = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), uiProcessOperationTimeout)
		defer cancel()
		msg := processActionDoneMsg{
			token:             token,
			surfaceGeneration: surfaceGeneration,
			inputDraftToken:   inputDraftToken,
			action:            action,
			id:                id,
			logPath:           logPath,
		}
		switch action {
		case "kill":
			msg.err = client.KillProcess(ctx, id)
		case "inline":
			msg.output, msg.logPath, msg.err = client.InlineOutput(ctx, id, uiProcessInlineMaxChars)
		case "logs":
			if msg.logPath == "" {
				entries, err := client.ListProcesses(ctx)
				if err != nil {
					msg.err = err
					return msg
				}
				msg.logPath, msg.err = processLogPath(entries, id)
			}
			if msg.err == nil {
				msg.editorCmd, msg.err = openProcessLogPath(msg.logPath)
			}
		default:
			msg.err = fmt.Errorf("unknown /ps action %q", action)
		}
		return msg
	}
}

func (m *uiModel) processListHasRunningEntries() bool {
	if m == nil || !m.processList.open {
		return false
	}
	for _, entry := range m.processList.entries {
		if entry.Running || strings.TrimSpace(entry.State) == "starting" || strings.TrimSpace(entry.State) == "running" {
			return true
		}
	}
	return false
}

func (m *uiModel) applyProcessActionDone(msg processActionDoneMsg) tea.Cmd {
	if m == nil || msg.token != m.processList.actionToken {
		return nil
	}
	m.processList.actionInFlight = false
	if msg.err != nil {
		statusCmd := m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		c := uiInputController{model: m}
		switch msg.action {
		case "kill", "logs":
			return tea.Batch(statusCmd, c.resumeQueuedInputsAfterIdleRuntime())
		default:
			return tea.Batch(statusCmd, c.resumeQueuedInputsAfterIdleRuntime())
		}
	}
	c := uiInputController{model: m}
	switch msg.action {
	case "kill":
		status := fmt.Sprintf("sent terminate signal to %s", msg.id)
		var refreshCmd tea.Cmd
		if m.processList.open && msg.surfaceGeneration == m.processList.surfaceGeneration {
			refreshCmd = m.requestProcessListRefresh()
		}
		return tea.Batch(m.sendTransientStatusWithNoticeID(status, uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, ""), refreshCmd, c.resumeQueuedInputsAfterIdleRuntime())
	case "inline":
		if msg.inputDraftToken != 0 && msg.inputDraftToken != m.mainInputDraftToken {
			return c.resumeQueuedInputsAfterIdleRuntime()
		}
		preview := strings.TrimSpace(msg.output)
		if preview == "" {
			preview = "<no output yet>"
		}
		releaseCmd := c.releaseLockedInjectedInput(true)
		m.appendProcessOutputToInput(msg.id, preview)
		if m.processList.open && msg.surfaceGeneration == m.processList.surfaceGeneration {
			return tea.Batch(releaseCmd, c.stopProcessListFlowCmd(), c.model.sendTransientStatusWithNoticeID("Pasted shell transcript", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return tea.Batch(releaseCmd, c.model.sendTransientStatusWithNoticeID("Pasted shell transcript", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, ""))
	case "logs":
		statusCmd := c.model.sendTransientStatusWithNoticeID("Opened logs", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, "")
		if msg.editorCmd != nil {
			execCmd := tea.ExecProcess(msg.editorCmd, func(runErr error) tea.Msg {
				return openProcessLogsDoneMsg{err: runErr}
			})
			if m.processList.open && msg.surfaceGeneration == m.processList.surfaceGeneration {
				return tea.Batch(c.stopProcessListFlowCmd(), statusCmd, execCmd, c.resumeQueuedInputsAfterIdleRuntime())
			}
			return tea.Batch(statusCmd, execCmd, c.resumeQueuedInputsAfterIdleRuntime())
		}
		if m.processList.open && msg.surfaceGeneration == m.processList.surfaceGeneration {
			return tea.Batch(c.stopProcessListFlowCmd(), statusCmd, c.resumeQueuedInputsAfterIdleRuntime())
		}
		return tea.Batch(statusCmd, c.resumeQueuedInputsAfterIdleRuntime())
	default:
		return nil
	}
}

func (c uiInputController) handleProcessListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	switch strings.ToLower(msg.String()) {
	case "ctrl+c":
		m.exitAction = UIActionExit
		if overlayCmd := m.restoreTranscriptSurface(); overlayCmd != nil {
			m.closeProcessList()
			return m, tea.Sequence(overlayCmd, tea.Quit)
		}
		return m, tea.Quit
	case "esc", "q":
		return m, c.stopProcessListFlowCmd()
	case "up":
		m.moveProcessSelection(-1)
		return m, nil
	case "down":
		m.moveProcessSelection(1)
		return m, nil
	case "pgup":
		m.moveProcessSelectionPage(-1)
		return m, nil
	case "pgdown":
		m.moveProcessSelectionPage(1)
		return m, nil
	case "home":
		m.selectFirstProcess()
		return m, nil
	case "end":
		m.selectLastProcess()
		return m, nil
	case "r":
		return m, tea.Batch(m.requestProcessListRefresh(), c.model.sendTransientStatusWithNoticeID("refreshing processes", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, ""))
	case "enter":
		return c.runProcessListAction("inline")
	case "k":
		return c.runProcessListAction("kill")
	case "i":
		return c.runProcessListAction("inline")
	case "o":
		return c.runProcessListAction("logs")
	default:
		return m, nil
	}
}

func (c uiInputController) runProcessListAction(action string) (tea.Model, tea.Cmd) {
	m := c.model
	selected, ok := m.selectedProcess()
	if !ok {
		return m, c.model.sendTransientStatusWithNoticeID("no background process selected", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	return c.runProcessAction(action, selected.ID)
}

func (c uiInputController) runProcessAction(action, id string) (tea.Model, tea.Cmd) {
	m := c.model
	if m.processClient == nil {
		return m, c.model.sendTransientStatusWithNoticeID("background process client is unavailable", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	if m.processList.actionInFlight {
		return m, c.model.sendTransientStatusWithNoticeID("process action already in flight", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	id = strings.TrimSpace(id)
	if id == "" {
		return m, c.startProcessListFlowCmd()
	}
	switch action {
	case "kill":
		return m, m.processActionCmd(action, id, "", 0)
	case "inline":
		return m, m.processActionCmd(action, id, "", m.mainInputDraftToken)
	case "logs":
		path := ""
		for _, entry := range m.processList.entries {
			if entry.ID == id {
				path = entry.LogPath
				break
			}
		}
		return m, m.processActionCmd(action, id, path, 0)
	default:
		return m, c.model.sendTransientStatusWithNoticeID(fmt.Sprintf("unknown /ps action %q", action), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
}

func (m *uiModel) appendProcessOutputToInput(id, output string) {
	payload := fmt.Sprintf("Output of bg shell %s:\n%s\n", id, output)
	if strings.TrimSpace(m.input) == "" {
		m.replaceMainInput(payload, -1)
		return
	}
	m.moveCursorEnd()
	prefix := "\n"
	if strings.HasSuffix(m.input, "\n") {
		prefix = ""
	}
	m.insertInputRunes([]rune(prefix + payload))
}

func processLogPath(entries []clientui.BackgroundProcess, id string) (string, error) {
	for _, entry := range entries {
		if entry.ID == id {
			if strings.TrimSpace(entry.LogPath) == "" {
				return "", fmt.Errorf("process %s has no log file", id)
			}
			return entry.LogPath, nil
		}
	}
	return "", fmt.Errorf("unknown session_id %s", id)
}

var openDefault = func(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return fmt.Errorf("open is not supported on %s", runtime.GOOS)
	}
	return cmd.Start()
}

func editorCommand(path string) (*exec.Cmd, error) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return nil, fmt.Errorf("open logs failed and EDITOR/VISUAL is not set")
	}
	shellPath := strings.TrimSpace(os.Getenv("SHELL"))
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	cmd := exec.Command(shellPath, "-lc", `eval "$KENT_EDITOR \"$1\""`, "kent-editor", path)
	cmd.Env = append(os.Environ(), "KENT_EDITOR="+editor)
	return cmd, nil
}

func openProcessLogPath(path string) (*exec.Cmd, error) {
	if err := openDefault(path); err == nil {
		return nil, nil
	}
	return editorCommand(path)
}
