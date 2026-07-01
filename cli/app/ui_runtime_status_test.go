package app

import (
	"reflect"
	"strings"
	"testing"

	"core/cli/tui"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
)

func TestRuntimeStatusUsesLocalFallbackWhenRuntimeClientMissing(t *testing.T) {
	m := newProjectedStaticUIModel(
		WithUIThinkingLevel("high"),
		WithUIFastModeAvailable(true),
		WithUIFastModeEnabled(true),
		WithUIConversationFreshness(clientui.ConversationFreshnessEstablished),
	)
	m.reviewerMode = "edits"
	m.reviewerEnabled = true
	m.autoCompactionEnabled = true
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "done", Phase: llm.MessagePhaseFinal}}

	status := m.runtimeStatus()
	if status.ReviewerFrequency != "edits" {
		t.Fatalf("reviewer frequency = %q, want edits", status.ReviewerFrequency)
	}
	if !status.ReviewerEnabled {
		t.Fatal("expected reviewer enabled in local fallback status")
	}
	if !status.AutoCompactionEnabled {
		t.Fatal("expected auto-compaction enabled in local fallback status")
	}
	if !status.FastModeAvailable || !status.FastModeEnabled {
		t.Fatalf("expected fast mode flags in local fallback status, got available=%v enabled=%v", status.FastModeAvailable, status.FastModeEnabled)
	}
	if status.ConversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", status.ConversationFreshness)
	}
	if status.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", status.ThinkingLevel)
	}
	if status.ParentSessionID != "" {
		t.Fatalf("expected empty parent session id in local fallback status, got %+v", status)
	}
	if status.LastCommittedAssistantFinalAnswer != "done" {
		t.Fatalf("last committed assistant answer = %q, want done", status.LastCommittedAssistantFinalAnswer)
	}
}

func TestCurrentConversationFreshnessAcceptsCachedFreshness(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		ConversationFreshness: clientui.ConversationFreshnessFresh,
	}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.conversationFreshness = clientui.ConversationFreshnessEstablished

	if got := m.currentConversationFreshness(); got != clientui.ConversationFreshnessFresh {
		t.Fatalf("conversation freshness = %v, want fresh", got)
	}
	if m.conversationFreshness != clientui.ConversationFreshnessFresh {
		t.Fatalf("cached freshness did not update model state: %v", m.conversationFreshness)
	}
}

func TestCurrentConversationFreshnessDoesNotDowngradeLocalTurnFromCachedFresh(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		ConversationFreshness: clientui.ConversationFreshnessFresh,
	}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	m.conversationFreshness = clientui.ConversationFreshnessEstablished
	m.localConversationTurn = true

	if got := m.currentConversationFreshness(); got != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", got)
	}
	if m.conversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("cached stale freshness downgraded local turn state: %v", m.conversationFreshness)
	}
}

func TestRuntimeBackedLocalEntryAppendWaitsForCommittedServerEcho(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), closedAskEvents())
	m.startupCmds = nil

	_ = m.appendLocalEntryWithNoticeID("developer_feedback", "local feedback", "")
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("did not expect local transcript entry before committed server echo, got %+v", m.transcriptEntries)
	}
	if committed := committedTranscriptEntriesForApp(m.transcriptEntries); len(committed) != 0 {
		t.Fatalf("runtime-backed append advanced committed transcript entries before server echo: %+v", committed)
	}
}

func TestStaticLocalEntryAppendShowsStatusOnly(t *testing.T) {
	m := newProjectedStaticUIModel()

	cmd := m.appendLocalEntryWithNoticeID("developer_feedback", "local feedback", "notice-1")
	if len(m.transcriptEntries) != 0 {
		t.Fatalf("static append without runtime must not create transcript entries: %+v", m.transcriptEntries)
	}
	if committed := committedTranscriptEntriesForApp(m.transcriptEntries); len(committed) != 0 {
		t.Fatalf("static append without runtime advanced committed transcript entries: %+v", committed)
	}
	if cmd == nil {
		t.Fatal("expected status clear timer command")
	}
	if m.transientStatus != "local feedback" || m.transientStatusNoticeID != "notice-1" {
		t.Fatalf("expected status-only local feedback, got status=%q notice=%q", m.transientStatus, m.transientStatusNoticeID)
	}
}

func TestRuntimeStatusLineGoalLabelUsesPresentGoalOnly(t *testing.T) {
	for _, goalStatus := range []clientui.RuntimeGoalStatus{clientui.RuntimeGoalStatusActive, clientui.RuntimeGoalStatusPaused} {
		client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
			Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: goalStatus},
		}}
		m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())

		status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
		if !strings.HasPrefix(status, statusStateCircleGlyph+" goal ") {
			t.Fatalf("expected present goal %s to render goal label, got %q", goalStatus, status)
		}
	}
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete},
	}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
	status := stripANSIAndTrimRight(m.layout().renderStatusLine(120, uiThemeStyles("dark")))
	if strings.HasPrefix(status, statusStateCircleGlyph+" goal ") {
		t.Fatalf("did not expect complete goal to render goal label, got %q", status)
	}
}

func TestRuntimeStatusLineShowsIdleGoalDotForIdleActiveGoal(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
	}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	status := stripANSIAndTrimRight(uiViewLayout{model: m}.renderStatusLine(100, uiThemeStyles(m.theme)))

	if !strings.HasPrefix(status, statusStateCircleGlyph+" goal ") {
		t.Fatalf("expected idle active goal to render a dot with goal label, got %q", status)
	}
}

func TestRuntimeStatusLineGoalLabelOverridesInterruptedActivity(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
	}}
	m := newSizedProjectedClosedUIModel(client, 100, 20)
	m.activity = uiActivityInterrupted
	status := stripANSIAndTrimRight(uiViewLayout{model: m}.renderStatusLine(100, uiThemeStyles(m.theme)))

	if !strings.HasPrefix(status, statusStateCircleGlyph+" goal ") {
		t.Fatalf("expected active goal label after interrupt, got %q", status)
	}
	if !strings.Contains(status, "interrupted") {
		t.Fatalf("expected interrupted status after interrupt, got %q", status)
	}
}

func TestRuntimeStatusLineShapeUsesOnlyActiveWork(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(*uiModel)
		spinning bool
	}{
		{
			name: "idle active goal",
			prepare: func(m *uiModel) {
				m.engine = &runtimeControlFakeClient{status: clientui.RuntimeStatus{
					Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
				}}
			},
		},
		{
			name: "paused goal",
			prepare: func(m *uiModel) {
				m.engine = &runtimeControlFakeClient{status: clientui.RuntimeStatus{
					Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
				}}
			},
		},
		{
			name: "interrupted active goal",
			prepare: func(m *uiModel) {
				m.activity = uiActivityInterrupted
				m.engine = &runtimeControlFakeClient{status: clientui.RuntimeStatus{
					Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
				}}
			},
		},
		{
			name:     "agent turn",
			prepare:  func(m *uiModel) { m.setBusy(true) },
			spinning: true,
		},
		{
			name: "waiting on question",
			prepare: func(m *uiModel) {
				m.setBusy(true)
				m.activity = uiActivityQuestion
			},
		},
		{
			name:     "compaction",
			prepare:  func(m *uiModel) { m.setCompacting(true) },
			spinning: true,
		},
		{
			name:     "supervisor",
			prepare:  func(m *uiModel) { m.setReviewerRunning(true) },
			spinning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newProjectedStaticUIModel()
			if tt.prepare != nil {
				tt.prepare(m)
			}

			if got := m.statusLineSpinning(); got != tt.spinning {
				t.Fatalf("spinning = %t, want %t", got, tt.spinning)
			}
			status := stripANSIAndTrimRight(uiViewLayout{model: m}.renderStatusLine(100, uiThemeStyles(m.theme)))
			hasSpinner := !strings.HasPrefix(status, statusStateCircleGlyph+" ")
			if hasSpinner != tt.spinning {
				t.Fatalf("rendered spinning = %t, want %t, status=%q", hasSpinner, tt.spinning, status)
			}
		})
	}
}

func TestRuntimeStatusPhasePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		goal    *clientui.RuntimeGoal
		prepare func(*uiModel)
		want    statusLinePhase
	}{
		{
			name: "nil goal",
			want: statusLinePhasePrimary,
		},
		{
			name: "last event error",
			prepare: func(m *uiModel) {
				m.activity = uiActivityError
			},
			want: statusLinePhaseError,
		},
		{
			name: "goal active overrides error",
			goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			prepare: func(m *uiModel) {
				m.activity = uiActivityError
			},
			want: statusLinePhasePrimary,
		},
		{
			name: "paused goal",
			goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
			want: statusLinePhasePrimary,
		},
		{
			name: "active goal",
			goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			want: statusLinePhasePrimary,
		},
		{
			name:    "supervisor overrides active goal",
			goal:    &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			prepare: func(m *uiModel) { m.setReviewerRunning(true) },
			want:    statusLinePhaseSuccess,
		},
		{
			name:    "compaction overrides active goal",
			goal:    &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			prepare: func(m *uiModel) { m.setCompacting(true) },
			want:    statusLinePhaseSecondary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{Goal: tt.goal}}
			m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
			if tt.prepare != nil {
				tt.prepare(m)
			}

			if got := m.statusLinePhase(); got != tt.want {
				t.Fatalf("phase = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRuntimeStatusLabelPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		goal    *clientui.RuntimeGoal
		prepare func(*uiModel)
		want    string
	}{
		{name: "idle", want: ""},
		{
			name: "last event error",
			prepare: func(m *uiModel) {
				m.activity = uiActivityError
			},
			want: "error",
		},
		{
			name: "active goal overrides error",
			goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			prepare: func(m *uiModel) {
				m.activity = uiActivityError
			},
			want: "goal",
		},
		{
			name: "paused goal",
			goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused},
			want: "goal",
		},
		{
			name: "complete goal omitted",
			goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete},
			want: "",
		},
		{
			name:    "review overrides active goal",
			goal:    &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			prepare: func(m *uiModel) { m.setReviewerRunning(true) },
			want:    "review",
		},
		{
			name:    "compaction overrides active goal",
			goal:    &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
			prepare: func(m *uiModel) { m.setCompacting(true) },
			want:    "compacting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{Goal: tt.goal}}
			m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents())
			if tt.prepare != nil {
				tt.prepare(m)
			}

			if got := m.statusLineLabel(); got != tt.want {
				t.Fatalf("label = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRuntimeStatusLineReopenActiveGoalFromStartupMainViewUsesIdleDot(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status: clientui.RuntimeStatus{
			Goal: &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive},
		},
	}}
	runtimeClient := newTestSessionRuntimeClient(reads, &reconnectRetryRuntimeControlClient{})

	m := newProjectedTestUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents(), WithUISessionID("session-1"))

	if got := m.statusLinePhase(); got != statusLinePhasePrimary {
		t.Fatalf("phase = %v, want primary active goal from startup main view", got)
	}
	if m.statusLineSpinning() {
		t.Fatal("reopened active goal without active run must not spin")
	}
	status := stripANSIAndTrimRight(uiViewLayout{model: m}.renderStatusLine(100, uiThemeStyles(m.theme)))
	if !strings.HasPrefix(status, statusStateCircleGlyph+" goal ") {
		t.Fatalf("expected reopened active goal to render idle dot with goal label, got %q", status)
	}
}

func TestStatusLineRenderDoesNotRefreshMainViewWhenCacheMissing(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status: clientui.RuntimeStatus{
			ContextUsage: clientui.RuntimeContextUsage{UsedTokens: 100, WindowTokens: 1_000},
		},
	}}
	client := newTestSessionRuntimeClient(reads, &reconnectRetryRuntimeControlClient{})
	m := newSizedProjectedClosedUIModel(client, 120, 20, WithUISessionID("session-1"))
	clearSessionRuntimeClientMainViewCache(client)
	reads.count.Store(0)

	_ = uiViewLayout{model: m}.renderStatusLine(120, uiThemeStyles(m.theme))

	if got := reads.count.Load(); got != 0 {
		t.Fatalf("status-line render performed %d synchronous main-view reads, want 0", got)
	}
}

func TestViewRenderDoesNotRefreshMainViewWhenCacheMissing(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status: clientui.RuntimeStatus{
			ContextUsage: clientui.RuntimeContextUsage{UsedTokens: 100, WindowTokens: 1_000},
		},
	}}
	client := newTestSessionRuntimeClient(reads, &reconnectRetryRuntimeControlClient{})
	m := newSizedProjectedClosedUIModel(client, 120, 20, WithUISessionID("session-1"))
	clearSessionRuntimeClientMainViewCache(client)
	reads.count.Store(0)

	_ = m.View()

	if got := reads.count.Load(); got != 0 {
		t.Fatalf("view render performed %d synchronous main-view reads, want 0", got)
	}
}

func TestSlashCommandPickerRenderDoesNotRefreshMainViewWhenCacheMissing(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		Status:  clientui.RuntimeStatus{ParentSessionID: "parent-1"},
	}}
	client := newTestSessionRuntimeClient(reads, &reconnectRetryRuntimeControlClient{})
	m := newSizedProjectedClosedUIModel(client, 120, 20, WithUISessionID("session-1"))
	m.input = "/ba"
	m.refreshSlashCommandFilterFromInputWithAuth(true)
	clearSessionRuntimeClientMainViewCache(client)
	reads.count.Store(0)

	_ = m.View()

	if got := reads.count.Load(); got != 0 {
		t.Fatalf("slash-command render performed %d synchronous main-view reads, want 0", got)
	}
}

func clearSessionRuntimeClientMainViewCache(client *sessionRuntimeClient) {
	client.mu.Lock()
	client.hasMainView = false
	client.mainView = clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: client.sessionID}}
	client.mu.Unlock()
}

func TestRuntimeStatusLocalFallbackSkipsTrailingDeveloperFeedback(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "done", Phase: llm.MessagePhaseFinal},
		{Role: "developer_feedback", Text: "phase mismatch"},
	}

	status := m.runtimeStatus()
	if status.LastCommittedAssistantFinalAnswer != "done" {
		t.Fatalf("last committed assistant answer = %q, want done", status.LastCommittedAssistantFinalAnswer)
	}
}

func TestRuntimeStatusLocalFallbackSkipsTrailingDeveloperErrorFeedback(t *testing.T) {
	m := newProjectedStaticUIModel()
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "done", Phase: llm.MessagePhaseFinal},
		{Role: tui.TranscriptRoleDeveloperErrorFeedback, Text: "server disconnected"},
	}

	status := m.runtimeStatus()
	if status.LastCommittedAssistantFinalAnswer != "done" {
		t.Fatalf("last committed assistant answer = %q, want done", status.LastCommittedAssistantFinalAnswer)
	}
}

func TestRuntimeStatusUsesLoopbackRuntimeSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Create(dir, "ws", dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetParentSessionID("parent-123"); err != nil {
		t.Fatalf("set parent session id: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "final answer", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng, err := runtime.New(store, statusLineFastClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	if err := eng.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	if changed, err := eng.SetFastModeEnabled(true); err != nil {
		t.Fatalf("enable fast mode: %v", err)
	} else if !changed {
		t.Fatal("expected fast mode enable to report changed=true")
	}
	if changed, enabled := eng.SetAutoCompactionEnabled(false); !changed || enabled {
		t.Fatalf("expected auto-compaction disabled, changed=%v enabled=%v", changed, enabled)
	}

	m := newProjectedEngineUIModel(eng)
	status := m.runtimeStatus()
	if status.ReviewerFrequency != "off" || status.ReviewerEnabled {
		t.Fatalf("unexpected reviewer status: %+v", status)
	}
	if status.AutoCompactionEnabled {
		t.Fatal("expected auto-compaction disabled in runtime status")
	}
	if !status.FastModeAvailable || !status.FastModeEnabled {
		t.Fatalf("expected fast mode enabled in runtime status, got available=%v enabled=%v", status.FastModeAvailable, status.FastModeEnabled)
	}
	if status.ConversationFreshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", status.ConversationFreshness)
	}
	if status.ParentSessionID != "parent-123" {
		t.Fatalf("parent session id = %q, want parent-123", status.ParentSessionID)
	}
	if status.LastCommittedAssistantFinalAnswer != "final answer" {
		t.Fatalf("last committed assistant answer = %q, want final answer", status.LastCommittedAssistantFinalAnswer)
	}
	if status.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", status.ThinkingLevel)
	}
	if status.CompactionMode != "native" {
		t.Fatalf("compaction mode = %q, want native", status.CompactionMode)
	}
	if status.ContextUsage.WindowTokens != 400_000 {
		t.Fatalf("context window tokens = %d, want 400000", status.ContextUsage.WindowTokens)
	}
	if status.CompactionCount != 0 {
		t.Fatalf("compaction count = %d, want 0", status.CompactionCount)
	}
}

func TestRuntimeMainViewActiveRunSeedsBusyGoalState(t *testing.T) {
	client := &runtimeControlFakeClient{mainView: clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionID: "session-1"},
		ActiveRun: &clientui.RunView{
			RunID:     "run-1",
			SessionID: "session-1",
			StepID:    "step-1",
			Status:    clientui.RunStatusRunning,
			Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeGoalLoop),
		},
	}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUISessionID("session-1"))
	if !m.isBusy() || !m.isGoalRun() || m.activity != uiActivityRunning {
		t.Fatalf("startup run state = busy:%t goal:%t activity:%v, want active goal run", m.isBusy(), m.isGoalRun(), m.activity)
	}
}

func TestRuntimeStatusUsesLiveContextUsageFromRuntimeEvents(t *testing.T) {
	client := &runtimeControlFakeClient{status: clientui.RuntimeStatus{
		ContextUsage: clientui.RuntimeContextUsage{UsedTokens: 100, WindowTokens: 1_000},
	}, sessionView: clientui.RuntimeSessionView{SessionID: "session-1"}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUISessionID("session-1"))
	if got := m.runtimeStatus().ContextUsage.UsedTokens; got != 100 {
		t.Fatalf("initial context used tokens = %d, want 100", got)
	}

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventModelResponse,
		ContextUsage: &clientui.RuntimeContextUsage{
			UsedTokens:      420,
			WindowTokens:    1_000,
			CacheHitPercent: 25,
		},
	}})
	updated := next.(*uiModel)
	usage := updated.runtimeStatus().ContextUsage
	if usage.UsedTokens != 420 || usage.WindowTokens != 1_000 || usage.CacheHitPercent != 25 {
		t.Fatalf("live context usage not applied: %+v", usage)
	}
}

func TestRuntimeGoalStatusEventUpdatesCachedGoal(t *testing.T) {
	runtimeClient := newTestSessionRuntimeClientWithControls(&reconnectRetryRuntimeControlClient{})
	runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}})
	m := newProjectedTestUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents(), WithUISessionID("session-1"))
	m.activity = uiActivityRunning
	m.setBusy(true)

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusActive,
		},
	}})
	updated := next.(*uiModel)
	assertCachedRuntimeGoal(t, runtimeClient, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusActive})
	if updated.isGoalRun() {
		t.Fatalf("goal status update must not synthesize goal-loop lifecycle")
	}

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusPaused,
		},
	}})
	updated = next.(*uiModel)
	assertCachedRuntimeGoal(t, runtimeClient, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusPaused})

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{
			ID:        "goal-1",
			Objective: "ship feature",
			Status:    clientui.RuntimeGoalStatusComplete,
		},
	}})
	updated = next.(*uiModel)
	assertCachedRuntimeGoal(t, runtimeClient, &clientui.RuntimeGoal{ID: "goal-1", Objective: "ship feature", Status: clientui.RuntimeGoalStatusComplete})

	next, _ = updated.Update(runtimeEventMsg{event: clientui.Event{
		Kind:       clientui.EventGoalStatusUpdated,
		GoalStatus: &clientui.RuntimeGoalStatusUpdate{Cleared: true},
	}})
	updated = next.(*uiModel)
	assertCachedRuntimeGoal(t, runtimeClient, nil)
}

func assertCachedRuntimeGoal(t *testing.T, runtimeClient *sessionRuntimeClient, want *clientui.RuntimeGoal) {
	t.Helper()
	view, ok := runtimeClient.CachedMainView()
	if !ok {
		t.Fatal("expected cached main view")
	}
	if !reflect.DeepEqual(view.Status.Goal, want) {
		t.Fatalf("cached goal = %+v, want %+v", view.Status.Goal, want)
	}
}

func TestRuntimeStatusUsesLiveContextUsageFromNonModelResponseEvents(t *testing.T) {
	client := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: "session-1"}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUISessionID("session-1"))

	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventToolCallCompleted,
		ContextUsage: &clientui.RuntimeContextUsage{
			UsedTokens:   520,
			WindowTokens: 1_000,
		},
	}})
	updated := next.(*uiModel)
	usage := updated.runtimeStatus().ContextUsage
	if usage.UsedTokens != 520 || usage.WindowTokens != 1_000 {
		t.Fatalf("tool event context usage not applied: %+v", usage)
	}
}

func TestRuntimeStatusDoesNotLeakLiveContextUsageAcrossSessions(t *testing.T) {
	client := &runtimeControlFakeClient{sessionView: clientui.RuntimeSessionView{SessionID: "session-1"}}
	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), closedAskEvents(), WithUISessionID("session-1"))
	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{
		Kind: clientui.EventModelResponse,
		ContextUsage: &clientui.RuntimeContextUsage{
			UsedTokens:   420,
			WindowTokens: 1_000,
		},
	}})
	updated := next.(*uiModel)
	if got := updated.runtimeStatus().ContextUsage.UsedTokens; got != 420 {
		t.Fatalf("session-1 context used tokens = %d, want 420", got)
	}

	updated.sessionID = "session-2"
	client.sessionView.SessionID = "session-2"
	client.status.ContextUsage = clientui.RuntimeContextUsage{}
	usage := updated.runtimeStatus().ContextUsage
	if usage.WindowTokens != 0 || usage.UsedTokens != 0 {
		t.Fatalf("expected zero context usage for fresh session, got %+v", usage)
	}
}
