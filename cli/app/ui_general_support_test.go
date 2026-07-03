package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/tui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type stubSessionViewClient struct {
	getSessionMainView func(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
}

func (s stubSessionViewClient) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	if s.getSessionMainView == nil {
		return serverapi.SessionMainViewResponse{}, errors.New("session view stub is required")
	}
	return s.getSessionMainView(ctx, req)
}

func updateUIModel(t *testing.T, m *uiModel, msg tea.Msg) *uiModel {
	t.Helper()
	next, _ := m.Update(msg)
	updated, ok := next.(*uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", next)
	}
	return updated
}

func collectCmdMessages(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	msgs := make([]tea.Msg, 0)
	var runMsg func(tea.Msg)
	var runCmd func(tea.Cmd)
	runCmd = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		runMsg(cmd())
	}
	runMsg = func(msg tea.Msg) {
		if msg == nil {
			return
		}
		msgs = append(msgs, msg)
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, nested := range batch {
				runCmd(nested)
			}
		}
	}
	runCmd(cmd)
	return msgs
}

type stubClipboardTextCopier struct {
	text  string
	err   error
	calls int
}

func (s *stubClipboardTextCopier) CopyText(_ context.Context, text string) error {
	s.calls++
	s.text = text
	return s.err
}

func withTrueColor(t *testing.T) {
	t.Helper()
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
}

func committedTranscriptEntriesForApp(entries []tui.TranscriptEntry) []tui.TranscriptEntry {
	out := make([]tui.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Committed {
			out = append(out, entry)
		}
	}
	return out
}

type stubProgressiveStatusCollector struct {
	base       uiStatusSnapshot
	authResult uiStatusAuthStageResult
	gitResult  uiStatusGitStageResult
	envResult  uiStatusEnvironmentStageResult
	gitCalls   int
}

func (s *stubProgressiveStatusCollector) Collect(_ context.Context, _ uiStatusRequest) (uiStatusSnapshot, error) {
	snapshot := s.base
	snapshot.Auth = s.authResult.Auth
	snapshot.Subscription = s.authResult.Subscription
	snapshot.Git = s.gitResult.Git
	snapshot.Skills = s.envResult.Skills
	snapshot.SkillTokenCounts = s.envResult.SkillTokenCounts
	snapshot.AgentsPaths = s.envResult.AgentsPaths
	snapshot.AgentTokenCounts = s.envResult.AgentTokenCounts
	snapshot.CollectorWarning = s.envResult.CollectorWarning
	return snapshot, nil
}

func (s *stubProgressiveStatusCollector) CollectBase(_ uiStatusRequest) uiStatusSnapshot {
	return s.base
}

func (s *stubProgressiveStatusCollector) CollectAuth(_ context.Context, _ uiStatusRequest, _ uiStatusSnapshot) uiStatusAuthStageResult {
	return s.authResult
}

func (s *stubProgressiveStatusCollector) CollectGit(_ context.Context, _ uiStatusRequest, _ uiStatusSnapshot) uiStatusGitStageResult {
	s.gitCalls++
	return s.gitResult
}

func (s *stubProgressiveStatusCollector) CollectEnvironment(_ context.Context, _ uiStatusRequest, _ uiStatusSnapshot) uiStatusEnvironmentStageResult {
	return s.envResult
}

type statusRequestOption func(*uiStatusRequest)

func newStatusRequestForTest(options ...statusRequestOption) uiStatusRequest {
	var req uiStatusRequest
	for _, option := range options {
		if option != nil {
			option(&req)
		}
	}
	return populateStatusRequestCacheKeys(req)
}

func withStatusWorkspaceRoot(root string) statusRequestOption {
	return func(req *uiStatusRequest) {
		req.WorkspaceRoot = root
	}
}

func startupCmdMessage[T tea.Msg](cmds []tea.Cmd) (T, bool) {
	var zero T
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		msg, ok := cmd().(T)
		if ok {
			return msg, true
		}
	}
	return zero, false
}
