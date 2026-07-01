package app

import (
	"strings"
	"testing"

	"core/cli/app/commands"
	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultRegistryBusyContract(t *testing.T) {
	r := commands.NewDefaultRegistry()
	want := map[string]bool{
		"exit":           false,
		"login":          false,
		"new":            false,
		"resume":         false,
		"logout":         false,
		"compact":        false,
		"name":           true,
		"thinking":       true,
		"fast":           true,
		"supervisor":     true,
		"autocompaction": true,
		"questions":      true,
		"status":         true,
		"goal":           true,
		"ps":             true,
		"worktree":       false,
		"copy":           true,
		"back":           false,
		"review":         false,
		"init":           false,
	}

	for _, command := range r.Commands() {
		wantBusy, ok := want[command.Name]
		if !ok {
			t.Fatalf("unexpected built-in command in registry: %q", command.Name)
		}
		if command.RunWhileBusy != wantBusy {
			t.Fatalf("command %q RunWhileBusy=%t, want %t", command.Name, command.RunWhileBusy, wantBusy)
		}
		delete(want, command.Name)
	}

	if len(want) != 0 {
		t.Fatalf("missing built-in commands from registry: %+v", want)
	}
}

func TestBusyEnterCommandBehavior(t *testing.T) {
	tests := []struct {
		name                string
		input               string
		setup               func(*uiModel)
		wantInput           string
		wantSessionName     string
		wantThinkingLevel   string
		wantFastModeEnabled bool
		wantStatusMode      bool
		wantProcessMode     bool
		wantGoalMode        bool
		wantStatusContains  string
		wantStatusOmits     string
	}{
		{
			name:            "name executes immediately while busy",
			input:           "/name queued title",
			wantSessionName: "queued title",
		},
		{
			name:              "thinking executes immediately while busy",
			input:             "/thinking low",
			wantThinkingLevel: "low",
		},
		{
			name:           "status opens overlay while busy",
			input:          "/status",
			wantStatusMode: true,
		},
		{
			name:            "goal pause executes while busy without duplicate local status",
			input:           "/goal pause",
			wantStatusOmits: "Goal paused",
		},
		{
			name:  "goal set executes while busy",
			input: "/goal ship feature",
		},
		{
			name:            "goal dashboard opens while busy",
			input:           "/goal",
			wantGoalMode:    true,
			wantStatusOmits: "cannot show /goal while model is working",
		},
		{
			name:  "goal resume executes while busy",
			input: "/goal resume",
		},
		{
			name:            "ps opens overlay while busy",
			input:           "/ps",
			wantProcessMode: true,
		},
		{
			name:  "fast executes immediately while busy",
			input: "/fast on",
			setup: func(m *uiModel) {
				m.fastModeAvailable = true
			},
			wantFastModeEnabled: true,
		},
		{
			name:               "compact is blocked on enter while busy",
			input:              "/compact now",
			wantStatusContains: "cannot run /compact while model is working",
		},
		{
			name:               "review is blocked on enter while busy",
			input:              "/review cli/app",
			wantStatusContains: "cannot run /review while model is working",
		},
		{
			name:               "init is blocked on enter while busy",
			input:              "/init starter repo",
			wantStatusContains: "cannot run /init while model is working",
		},
		{
			name:               "worktree is blocked on enter while busy",
			input:              "/worktree list",
			wantStatusContains: "cannot run /worktree while model is working",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			m.setBusy(true)
			m.activity = uiActivityRunning
			m.input = tt.input
			if tt.setup != nil {
				tt.setup(m)
			}

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			updated := next.(*uiModel)
			if !updated.isBusy() {
				t.Fatal("expected model to remain busy")
			}
			if len(updated.queued) != 0 {
				t.Fatalf("expected no queued inputs, got %+v", updated.queued)
			}
			if len(updated.pendingInjected) != 0 {
				t.Fatalf("expected no pending injected inputs, got %+v", updated.pendingInjected)
			}
			if updated.input != tt.wantInput {
				t.Fatalf("input = %q, want %q", updated.input, tt.wantInput)
			}
			if updated.sessionName != tt.wantSessionName {
				t.Fatalf("session name = %q, want %q", updated.sessionName, tt.wantSessionName)
			}
			if updated.thinkingLevel != tt.wantThinkingLevel {
				t.Fatalf("thinking level = %q, want %q", updated.thinkingLevel, tt.wantThinkingLevel)
			}
			if updated.fastModeEnabled != tt.wantFastModeEnabled {
				t.Fatalf("fast mode enabled = %t, want %t", updated.fastModeEnabled, tt.wantFastModeEnabled)
			}
			if got := updated.inputMode() == uiInputModeStatus; got != tt.wantStatusMode {
				t.Fatalf("status overlay open=%t, want %t", got, tt.wantStatusMode)
			}
			if got := updated.inputMode() == uiInputModeProcessList; got != tt.wantProcessMode {
				t.Fatalf("process overlay open=%t, want %t", got, tt.wantProcessMode)
			}
			if got := updated.inputMode() == uiInputModeGoal; got != tt.wantGoalMode {
				t.Fatalf("goal overlay open=%t, want %t", got, tt.wantGoalMode)
			}
			if tt.wantStatusContains != "" {
				status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
				if !strings.Contains(status, tt.wantStatusContains) {
					t.Fatalf("expected status line to contain %q, got %q", tt.wantStatusContains, status)
				}
			}
			if tt.wantStatusOmits != "" {
				status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
				if strings.Contains(status, tt.wantStatusOmits) {
					t.Fatalf("did not expect status line to contain %q, got %q", tt.wantStatusOmits, status)
				}
			}
		})
	}
}

func TestBusyQueueSubmissionCommandBehavior(t *testing.T) {
	tests := []struct {
		name               string
		input              string
		setup              func(*uiModel)
		wantQueued         []string
		wantInput          string
		wantStatusContains string
	}{
		{
			name:       "compact queues even though enter blocks it",
			input:      "/compact now",
			wantQueued: []string{"/compact now"},
		},
		{
			name:       "review queues even though enter blocks it",
			input:      "/review cli/app",
			wantQueued: []string{"/review cli/app"},
		},
		{
			name:  "fast queues when available",
			input: "/fast on",
			setup: func(m *uiModel) {
				m.fastModeAvailable = true
			},
			wantQueued: []string{"/fast on"},
		},
		{
			name:       "goal resume queues while busy",
			input:      "/goal resume",
			wantQueued: []string{"/goal resume"},
		},
		{
			name:               "fast is rejected when unavailable",
			input:              "/fast on",
			wantInput:          "/fast on",
			wantStatusContains: "Fast mode is only available for OpenAI-based Responses providers",
		},
		{
			name:               "back is rejected without parent session",
			input:              "/back",
			wantInput:          "/back",
			wantStatusContains: "No parent session available",
		},
		{
			name:               "ps action is rejected without process client",
			input:              "/ps kill proc-1",
			wantInput:          "/ps kill proc-1",
			wantStatusContains: "background process client is unavailable",
		},
		{
			name:               "worktree is rejected while busy",
			input:              "/worktree list",
			wantInput:          "/worktree list",
			wantStatusContains: "cannot run /worktree while model is working",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			m.setBusy(true)
			m.activity = uiActivityRunning
			m.input = tt.input
			if tt.setup != nil {
				tt.setup(m)
			}

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			updated := next.(*uiModel)

			if tt.wantStatusContains != "" {
				if cmd == nil {
					t.Fatal("expected transient-status command for rejected queued command")
				}
				if len(updated.queued) != 0 {
					t.Fatalf("expected no queued inputs, got %+v", updated.queued)
				}
				if len(updated.pendingInjected) != 0 {
					t.Fatalf("expected no pending injected inputs, got %+v", updated.pendingInjected)
				}
				if updated.input != tt.wantInput {
					t.Fatalf("input = %q, want %q", updated.input, tt.wantInput)
				}
				status := stripANSIAndTrimRight(updated.layout().renderStatusLine(120, uiThemeStyles("dark")))
				if !strings.Contains(status, tt.wantStatusContains) {
					t.Fatalf("expected status line to contain %q, got %q", tt.wantStatusContains, status)
				}
				return
			}

			if cmd != nil {
				t.Fatal("did not expect immediate command execution for queued busy command")
			}
			if updated.input != tt.wantInput {
				t.Fatalf("input = %q, want %q", updated.input, tt.wantInput)
			}
			if len(updated.queued) != len(tt.wantQueued) {
				t.Fatalf("queued count = %d, want %d (%+v)", len(updated.queued), len(tt.wantQueued), updated.queued)
			}
			for i, want := range tt.wantQueued {
				if updated.queued[i].Text != want {
					t.Fatalf("queued[%d] = %q, want %q", i, updated.queued[i].Text, want)
				}
			}
			if len(updated.pendingInjected) != 0 {
				t.Fatalf("expected no pending injected inputs, got %+v", updated.pendingInjected)
			}
		})
	}
}

func TestBusyQueuedCompactStartsCompactionAfterTurnDrains(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/compact tighten summary"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/compact tighten summary" {
		t.Fatalf("expected queued compact command, got %+v", updated.queued)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected compaction command after queued compact drains")
	}
	if !updated.isBusy() {
		t.Fatal("expected compact drain to re-enter busy state")
	}
	if !updated.isCompacting() {
		t.Fatal("expected queued compact drain to enter compaction mode")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued compact drained, got %+v", updated.queued)
	}
}

func TestBusyQueuedCopyCopiesFinalAnswerAfterTurnDrains(t *testing.T) {
	copier := &stubClipboardTextCopier{}
	m := newProjectedStaticUIModel(WithUIClipboardTextCopier(copier))
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "copied from queue", Phase: llm.MessagePhaseFinal}}
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.input = "/copy"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if cmd != nil {
		t.Fatal("did not expect immediate execution for queued /copy")
	}
	if len(updated.queued) != 1 || updated.queued[0].Text != "/copy" {
		t.Fatalf("expected queued /copy command, got %+v", updated.queued)
	}

	next, cmd = updated.Update(submitDoneMsg{message: "done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected clipboard copy command after queued /copy drains")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued /copy drained, got %+v", updated.queued)
	}

	next, followCmd := updated.Update(cmd())
	updated = next.(*uiModel)
	if copier.calls != 1 {
		t.Fatalf("expected one clipboard copy, got %d", copier.calls)
	}
	if copier.text != "copied from queue" {
		t.Fatalf("copied text = %q, want %q", copier.text, "copied from queue")
	}
	if updated.transientStatus != "Copied final answer to clipboard" {
		t.Fatalf("unexpected transient status %q", updated.transientStatus)
	}
	if updated.transientStatusKind != uiStatusNoticeSuccess {
		t.Fatalf("expected success status kind, got %d", updated.transientStatusKind)
	}
	if followCmd == nil {
		t.Fatal("expected transient-status clear command after queued /copy success")
	}
}

func TestBusyQueuedFastAppliesToNextRuntimeRequestAfterTurnDrains(t *testing.T) {
	client := &requestCaptureFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "next done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	_, eng := newAppRuntimeEngine(t, client, runtime.Config{Model: "gpt-5.3-codex"})

	m := newProjectedEngineUIModel(eng)
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.promptHistoryDraft = "previous prompt"
	m.promptHistoryDraftCursor = -1
	m.input = "/fast on"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	updated := next.(*uiModel)
	if len(updated.queued) != 1 || updated.queued[0].Text != "/fast on" {
		t.Fatalf("expected queued /fast command, got %+v", updated.queued)
	}
	if updated.input != "" || updated.promptHistoryDraft != "" || updated.promptHistoryDraftCursor != -1 {
		t.Fatalf("expected queued /fast to discard prompt-history draft, input=%q draft=%q cursor=%d", updated.input, updated.promptHistoryDraft, updated.promptHistoryDraftCursor)
	}

	next, cmd := updated.Update(submitDoneMsg{message: "prior turn done"})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected queued /fast feedback command")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		next, _ = updated.Update(msg)
		updated = next.(*uiModel)
	}
	if !eng.FastModeEnabled() {
		t.Fatal("expected queued /fast to enable runtime fast mode")
	}
	if len(updated.queued) != 0 {
		t.Fatalf("expected queued /fast to drain, got %+v", updated.queued)
	}
	if updated.isBusy() {
		t.Fatal("did not expect queued /fast alone to start a new turn")
	}

	updated.input = "next prompt"
	next, cmd = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated = next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected submit command for next prompt")
	}

	for _, msg := range collectCmdMessages(t, cmd) {
		done, ok := msg.(submitDoneMsg)
		if !ok {
			continue
		}
		next, _ = updated.Update(done)
		updated = next.(*uiModel)
	}

	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(requests))
	}
	if !requests[0].FastMode {
		t.Fatal("expected next runtime request after queued /fast to use fast mode")
	}
}
