package runtimeview

import (
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/transcript"
	"core/shared/transcript/patchformat"
)

func MainViewFromRuntimeActivity(
	engine *runtime.Engine,
	version clientui.ReadModelVersion,
	activity clientui.RuntimeActivity,
) (clientui.RuntimeMainView, error) {
	if engine == nil {
		return clientui.RuntimeMainView{}, nil
	}
	sessionView, err := SessionViewFromRuntime(engine)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	status, err := StatusFromRuntime(engine)
	if err != nil {
		return clientui.RuntimeMainView{}, err
	}
	if err := activity.Validate(); err != nil {
		activity = clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable, DiagnosticRecovery: true}
	}
	return clientui.RuntimeMainView{
		Version:             version,
		Status:              status,
		Session:             sessionView,
		Activity:            activity,
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
	}, nil
}

func RuntimeMainViewFromActivity(activity clientui.RuntimeActivity, status clientui.RuntimeStatus, sessionView clientui.RuntimeSessionView) clientui.RuntimeMainView {
	version := runtimeactivity.NextReadModelVersion(sessionView.SessionID)
	if err := activity.Validate(); err != nil {
		activity = clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable, DiagnosticRecovery: true}
	}
	return clientui.RuntimeMainView{
		Version:             version,
		Status:              status,
		Session:             sessionView,
		Activity:            activity,
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
	}
}

func StatusFromRuntime(engine *runtime.Engine) (clientui.RuntimeStatus, error) {
	if engine == nil {
		return clientui.RuntimeStatus{}, nil
	}
	freshness, err := engine.ConversationFreshness()
	if err != nil {
		return clientui.RuntimeStatus{}, err
	}
	usage := engine.ContextUsage()
	fastModeAvailable := engine.FastModeAvailable()
	status := clientui.RuntimeStatus{
		ReviewerFrequency:                 engine.ReviewerFrequency(),
		ReviewerEnabled:                   engine.ReviewerEnabled(),
		AutoCompactionEnabled:             engine.AutoCompactionEnabled(),
		QuestionsEnabled:                  engine.QuestionsEnabled(),
		FastModeAvailable:                 fastModeAvailable,
		FastModeEnabled:                   engine.FastModeEnabled(),
		ConversationFreshness:             ConversationFreshnessFromSession(freshness),
		PreviousSessionID:                 engine.PreviousSessionID(),
		ParentAgentSessionID:              engine.ParentAgentSessionID(),
		NavigationTargetSessionID:         engine.NavigationTargetSessionID(),
		LastCommittedAssistantFinalAnswer: engine.LastCommittedAssistantFinalAnswer(),
		ThinkingLevel:                     engine.ThinkingLevel(),
		CompactionMode:                    engine.CompactionMode(),
		ContextUsage: clientui.RuntimeContextUsage{
			UsedTokens:            usage.UsedTokens,
			WindowTokens:          usage.WindowTokens,
			CacheHitPercent:       usage.CacheHitPercent,
			HasCacheHitPercentage: usage.HasCacheHitPercentage,
		},
		CompactionCount: engine.CompactionCount(),
		Goal:            GoalFromSessionState(engine.Goal(), engine.GoalLoopSuspended()),
	}
	if workflowState, err := engine.WorkflowSessionState(); err != nil {
		return clientui.RuntimeStatus{}, err
	} else if workflowState != nil && !engine.WorkflowTerminalState().Completed {
		status.WorkflowSession = &clientui.WorkflowSessionStatus{
			TaskID:     string(workflowState.TaskID),
			WorkflowID: workflowState.WorkflowID,
		}
	}
	return status, nil
}

func TranscriptSessionStatusFromRuntime(engine *runtime.Engine) (clientui.TranscriptSessionStatus, error) {
	if engine == nil {
		return clientui.TranscriptSessionStatus{}, nil
	}
	fastModeAvailable := engine.FastModeAvailable()
	status := clientui.TranscriptSessionStatus{
		ReviewerFrequency:         engine.ReviewerFrequency(),
		ReviewerEnabled:           engine.ReviewerEnabled(),
		AutoCompactionEnabled:     engine.AutoCompactionEnabled(),
		QuestionsEnabled:          engine.QuestionsEnabled(),
		FastModeAvailable:         fastModeAvailable,
		FastModeEnabled:           engine.FastModeEnabled(),
		ThinkingLevel:             engine.ThinkingLevel(),
		CompactionMode:            engine.CompactionMode(),
		CompactionCount:           engine.CompactionCount(),
		PreviousSessionID:         engine.PreviousSessionID(),
		ParentAgentSessionID:      engine.ParentAgentSessionID(),
		NavigationTargetSessionID: engine.NavigationTargetSessionID(),
	}
	workflowState, err := engine.WorkflowSessionState()
	if err != nil {
		return clientui.TranscriptSessionStatus{}, err
	}
	if workflowState != nil && !engine.WorkflowTerminalState().Completed {
		status.Workflow = &clientui.TranscriptWorkflowSession{
			TaskID:     string(workflowState.TaskID),
			WorkflowID: workflowState.WorkflowID,
		}
	}
	return status, nil
}

func GoalFromSessionState(goal *session.GoalState, suspended bool) *clientui.RuntimeGoal {
	if goal == nil {
		return nil
	}
	return &clientui.RuntimeGoal{
		ID:        strings.TrimSpace(goal.ID),
		Objective: goal.Objective,
		Status:    clientui.RuntimeGoalStatus(strings.TrimSpace(string(goal.Status))),
		Suspended: suspended,
	}
}

func SessionViewFromRuntime(engine *runtime.Engine) (clientui.RuntimeSessionView, error) {
	if engine == nil {
		return clientui.RuntimeSessionView{}, nil
	}
	freshness, err := engine.ConversationFreshness()
	if err != nil {
		return clientui.RuntimeSessionView{}, err
	}
	return clientui.RuntimeSessionView{
		SessionID:             engine.SessionID(),
		SessionName:           engine.SessionName(),
		AgentRole:             engine.ContinuationAgentRole(),
		ConversationFreshness: ConversationFreshnessFromSession(freshness),
	}, nil
}

func ConversationFreshnessFromSession(freshness session.ConversationFreshness) clientui.ConversationFreshness {
	if freshness.IsFresh() {
		return clientui.ConversationFreshnessFresh
	}
	return clientui.ConversationFreshnessEstablished
}

func ActivityFromRuntimeSnapshot(snapshot *runtime.RunSnapshot, queueAccepting bool) clientui.RuntimeActivity {
	var active *runtimeactivity.ActiveStepSnapshot
	if snapshot != nil {
		active = runtimeactivity.ActiveStepFromRuntimeSnapshot(snapshot)
	}
	activity, err := runtimeactivity.ResolveRuntimeActivity(runtimeactivity.ResolverSnapshot{
		Registry: runtimeactivity.RegistrySnapshot{Registered: true, QueueAccepting: queueAccepting},
		Active:   active,
	})
	if err != nil {
		panic(err)
	}
	return activity
}

func ClientActiveKindFromRuntime(kind runtime.ActiveKind) clientui.RuntimeActivityActiveKind {
	return runtimeactivity.MustClientActiveKindFromRuntime(kind)
}

func cloneToolCallMeta(meta *transcript.ToolCallMeta) *transcript.ToolCallMeta {
	if meta == nil {
		return nil
	}
	copyMeta := *meta
	copyMeta.Suggestions = append([]string(nil), meta.Suggestions...)
	if meta.RenderHint != nil {
		renderHint := *meta.RenderHint
		copyMeta.RenderHint = &renderHint
	}
	if meta.PatchRender != nil {
		copyMeta.PatchRender = patchformat.Clone(meta.PatchRender)
	}
	return &copyMeta
}
