package runtimeview

import (
	"context"
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

const (
	projectionWorkspaceID  = "10000000-0000-4000-8000-000000000001"
	projectionRunID        = "10000000-0000-4000-8000-000000000002"
	projectionStepID       = "10000000-0000-4000-8000-000000000003"
	projectionParentID     = "10000000-0000-4000-8000-000000000006"
	projectionWorkflowTask = "10000000-0000-4000-8000-000000000008"
	projectionWorkflowID   = "10000000-0000-4000-8000-000000000009"
)

type projectionFastClient struct{}

func (projectionFastClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (projectionFastClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}

type projectionUnavailableFastClient struct{}

func (projectionUnavailableFastClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (projectionUnavailableFastClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "azure-openai", SupportsResponsesAPI: true}, nil
}

type projectionBlockingClient struct {
	started chan struct{}
	release chan struct{}
}

func newProjectionBlockingClient() *projectionBlockingClient {
	return &projectionBlockingClient{started: make(chan struct{}), release: make(chan struct{})}
}

func (c *projectionBlockingClient) Generate(ctx context.Context, _ llm.Request) (llm.Response, error) {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-c.release:
		return llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}}, nil
	}
}

func (c *projectionBlockingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true}, nil
}

type projectionPreciseClient struct {
	inputTokens int
}

func (c projectionPreciseClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{InputTokens: 900, OutputTokens: 100, WindowTokens: 400_000},
	}, nil
}

func (c projectionPreciseClient) CountRequestInputTokens(context.Context, llm.Request) (int, error) {
	return c.inputTokens, nil
}

func (c projectionPreciseClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsRequestInputTokenCount: true, IsOpenAIFirstParty: true}, nil
}

func newRuntimeViewStore(t *testing.T) *session.Store {
	t.Helper()
	dir := t.TempDir()
	store := newRuntimeViewSession(t, dir, projectionWorkspaceID, dir)
	return store
}

func newRuntimeViewEngine(t *testing.T, store *session.Store, client llm.Client, cfg ...runtime.Config) *runtime.Engine {
	t.Helper()
	engineConfig := runtime.Config{Model: "gpt-5"}
	if len(cfg) > 0 {
		engineConfig = cfg[0]
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	engine, err := runtime.New(store, eventLog, client, tools.NewRegistry(), engineConfig)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestActivityFromRuntimeSnapshotCopiesRuntimeOwnedActiveKinds(t *testing.T) {
	tests := []struct {
		name string
		kind runtime.ActiveKind
		want clientui.RuntimeActivityActiveKind
	}{
		{name: "user turn", kind: runtime.ActiveKindUserTurn, want: clientui.RuntimeActivityActiveKindUserTurn},
		{name: "workflow turn", kind: runtime.ActiveKindWorkflowTurn, want: clientui.RuntimeActivityActiveKindWorkflowTurn},
		{name: "goal loop", kind: runtime.ActiveKindGoalLoop, want: clientui.RuntimeActivityActiveKindGoalLoop},
		{name: "compaction", kind: runtime.ActiveKindCompaction, want: clientui.RuntimeActivityActiveKindCompaction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := ActivityFromRuntimeSnapshot(&runtime.RunSnapshot{
				RunID:      projectionRunID,
				StepID:     projectionStepID,
				Status:     runtime.RunStatusRunning,
				ActiveKind: tt.kind,
			}, true)
			if activity.State != clientui.RuntimeActivityRunning || activity.ActiveStep == nil || activity.ActiveStep.ActiveKind != tt.want {
				t.Fatalf("activity = %+v, want running %q", activity, tt.want)
			}
			if !activity.QueueAccepting {
				t.Fatalf("queue accepting was not preserved in activity: %+v", activity)
			}
		})
	}
}

func TestStatusFromRuntimeIncludesSuspendedGoal(t *testing.T) {
	client := newProjectionBlockingClient()
	engine := newRuntimeViewEngine(t, newRuntimeViewStore(t), client, runtime.Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship feature", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("start goal loop: %v", err)
	}
	<-client.started
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	close(client.release)

	status, err := StatusFromRuntime(engine)
	if err != nil {
		t.Fatalf("project runtime status: %v", err)
	}
	if status.Goal == nil || !status.Goal.Suspended {
		t.Fatalf("goal status = %+v, want suspended goal", status.Goal)
	}
}

func TestTranscriptSessionStatusDoesNotAdvertiseUnavailableFastMode(t *testing.T) {
	eng := newRuntimeViewEngine(
		t,
		newRuntimeViewStore(t),
		projectionUnavailableFastClient{},
		runtime.Config{
			Model:          "gpt-5",
			FastModeState:  runtime.NewFastModeState(true),
			ThinkingLevel:  "medium",
			CompactionMode: "auto",
		},
	)

	status, err := TranscriptSessionStatusFromRuntime(eng)
	if err != nil {
		t.Fatalf("TranscriptSessionStatusFromRuntime: %v", err)
	}
	if status.FastModeAvailable {
		t.Fatal("expected transcript status to report fast mode unavailable")
	}
	if status.FastModeEnabled {
		t.Fatal("expected transcript status to disable unavailable fast mode")
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("transcript session status: %v", err)
	}
}

func TestMainViewFromRuntimeBundlesStatusAndSession(t *testing.T) {
	dir := t.TempDir()
	store, parentSessionID := newRuntimeViewParentAgentChild(t, dir, projectionWorkspaceID, dir)
	if err := store.SetName("Session Name"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	role := "worker"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	finalAnswer := "final answer"
	finalPhase := session.MessagePhaseFinal
	stepID := projectionStepID
	if _, _, err := eventLog.AppendRecord(
		&stepID,
		session.MessageRecord{
			Role:    session.MessageRoleAssistant,
			Content: &finalAnswer,
			Phase:   &finalPhase,
		},
	); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	if err := eng.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	if changed, err := eng.SetFastModeEnabled(true); err != nil {
		t.Fatalf("enable fast mode: %v", err)
	} else if !changed {
		t.Fatal("expected fast mode enable to report changed=true")
	}
	if changed, enabled, err := eng.SetAutoCompactionEnabled(false); err != nil {
		t.Fatalf("disable auto-compaction: %v", err)
	} else if !changed || enabled {
		t.Fatalf("expected auto-compaction disabled, changed=%v enabled=%v", changed, enabled)
	}

	view := mainViewFromRuntimeForTest(t, eng)
	if view.Session.SessionID != store.Meta().SessionID || view.Session.SessionName != "Session Name" {
		t.Fatalf("unexpected session hydration: %+v", view.Session)
	}
	if view.Session.AgentRole == nil || *view.Session.AgentRole != role {
		t.Fatalf("session agent role = %v, want %q", view.Session.AgentRole, role)
	}
	if view.Status.ParentAgentSessionID == nil || view.Status.ParentAgentSessionID.String() != parentSessionID ||
		view.Status.NavigationTargetSessionID == nil || view.Status.NavigationTargetSessionID.String() != parentSessionID ||
		view.Status.LastCommittedAssistantFinalAnswer == nil ||
		*view.Status.LastCommittedAssistantFinalAnswer != "final answer" {
		t.Fatalf("unexpected status hydration: %+v", view.Status)
	}
	if view.Status.ThinkingLevel != "high" || !view.Status.FastModeEnabled || view.Status.AutoCompactionEnabled {
		t.Fatalf("unexpected runtime flags: %+v", view.Status)
	}
	if view.Status.ContextUsage.WindowTokens != 400_000 {
		t.Fatalf("context window tokens = %d, want 400000", view.Status.ContextUsage.WindowTokens)
	}
	if view.Activity.ActiveForControl() {
		t.Fatalf("expected idle activity in idle main view, got %+v", view.Activity)
	}
}

func TestSessionViewFromRuntimeOmitsDefaultAgentRole(t *testing.T) {
	eng := newRuntimeViewEngine(t, newRuntimeViewStore(t), projectionFastClient{})

	view, err := SessionViewFromRuntime(eng)
	if err != nil {
		t.Fatalf("project runtime session: %v", err)
	}
	if view.AgentRole != nil {
		t.Fatalf("session agent role = %v, want nil for default agent", view.AgentRole)
	}
}

func mainViewFromRuntimeForTest(t *testing.T, eng *runtime.Engine) clientui.RuntimeMainView {
	t.Helper()
	version := clientui.ReadModelVersion{Epoch: "runtimeview-test", Generation: 1, Sequence: 1}
	activity := clientui.RuntimeActivity{
		State:          clientui.RuntimeActivityRegisteredIdle,
		Reviewer:       clientui.ReviewerActivityInactive,
		QueueAccepting: true,
	}
	view, err := MainViewFromRuntimeActivity(eng, version, activity)
	if err != nil {
		t.Fatalf("project runtime main view: %v", err)
	}
	return view
}

func TestMainViewFromWorkflowRuntimeIncludesWorkflowStatus(t *testing.T) {
	store := newRuntimeViewStore(t)
	eng := newRuntimeViewEngine(t, store, projectionFastClient{}, runtime.Config{
		Model: "gpt-5",
	})
	publication, err := eng.PrepareCurrentNodeExecutionPublication(
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID: runtimeids.NewExecutionScopeID(),
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode: workflow.CurrentNodeReference{
					TaskID: workflow.TaskID(projectionWorkflowTask),
					NodeID: workflow.NodeID("10000000-0000-4000-8000-000000000007"),
				},
				WorkflowID: testsetup.WorkflowID(t, "runtimeview-projection"),
			},
		},
	)
	if err != nil {
		t.Fatalf("PrepareCurrentNodeExecutionPublication: %v", err)
	}
	if err := publication.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	binding := publication.Commit()
	t.Cleanup(func() { _ = binding.Close() })
	view := mainViewFromRuntimeForTest(t, eng)
	if view.Status.WorkflowSession == nil {
		t.Fatalf("workflow status = %+v, want active workflow session", view.Status)
	}
	if view.Status.WorkflowSession.TaskID != projectionWorkflowTask || view.Status.WorkflowSession.WorkflowID != testsetup.WorkflowID(t, "runtimeview-projection") {
		t.Fatalf("workflow session = %+v, want task/workflow ids", view.Status.WorkflowSession)
	}
}

func TestStatusFromRuntimeUsesFreshPreciseCurrentTokens(t *testing.T) {
	eng := newRuntimeViewEngine(t, newRuntimeViewStore(t), projectionPreciseClient{inputTokens: 180}, runtime.Config{
		Model:                         "gpt-5",
		ContextWindowTokens:           400_000,
		AutoCompactTokenLimit:         1_000,
		PreSubmitCompactionLeadTokens: 100,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "prompt"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if _, err := eng.ShouldCompactBeforeUserMessage(context.Background(), "follow-up"); err != nil {
		t.Fatalf("warm exact count: %v", err)
	}
	view, err := StatusFromRuntime(eng)
	if err != nil {
		t.Fatalf("project runtime status: %v", err)
	}
	if view.ContextUsage.UsedTokens != 180 {
		t.Fatalf("projected used tokens=%d, want exact 180", view.ContextUsage.UsedTokens)
	}
}
