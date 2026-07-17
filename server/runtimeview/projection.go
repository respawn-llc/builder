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

func MainViewFromRuntimeActivity(engine *runtime.Engine, version clientui.ReadModelVersion, activity clientui.RuntimeActivity) clientui.RuntimeMainView {
	if engine == nil {
		return clientui.RuntimeMainView{}
	}
	sessionView := SessionViewFromRuntime(engine)
	if err := activity.Validate(); err != nil {
		activity = clientui.RuntimeActivity{State: clientui.RuntimeActivityUnavailable, DiagnosticRecovery: true}
	}
	return clientui.RuntimeMainView{
		Version:             version,
		Status:              StatusFromRuntime(engine),
		Session:             sessionView,
		Activity:            activity,
		InputReconciliation: clientui.RuntimeInputReconciliationSnapshot{},
	}
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

func StatusFromRuntime(engine *runtime.Engine) clientui.RuntimeStatus {
	if engine == nil {
		return clientui.RuntimeStatus{}
	}
	usage := engine.ContextUsage()
	status := clientui.RuntimeStatus{
		ReviewerFrequency:                 engine.ReviewerFrequency(),
		ReviewerEnabled:                   engine.ReviewerEnabled(),
		AutoCompactionEnabled:             engine.AutoCompactionEnabled(),
		QuestionsEnabled:                  engine.QuestionsEnabled(),
		FastModeAvailable:                 engine.FastModeAvailable(),
		FastModeEnabled:                   engine.FastModeEnabled(),
		ConversationFreshness:             ConversationFreshnessFromSession(engine.ConversationFreshness()),
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
	if workflowState := engine.WorkflowSessionState(); workflowState.RunID != "" {
		status.WorkflowActive = engine.WorkflowRunConfigured() && !engine.WorkflowTerminalState().Completed
		status.WorkflowSession = &clientui.WorkflowSessionStatus{
			RunID:      workflowState.RunID,
			TaskID:     workflowState.TaskID,
			WorkflowID: workflowState.WorkflowID,
		}
	}
	return status
}

func TranscriptSessionStatusFromRuntime(engine *runtime.Engine) clientui.TranscriptSessionStatus {
	if engine == nil {
		return clientui.TranscriptSessionStatus{}
	}
	status := clientui.TranscriptSessionStatus{
		ReviewerFrequency:         engine.ReviewerFrequency(),
		ReviewerEnabled:           engine.ReviewerEnabled(),
		AutoCompactionEnabled:     engine.AutoCompactionEnabled(),
		QuestionsEnabled:          engine.QuestionsEnabled(),
		FastModeAvailable:         engine.FastModeAvailable(),
		FastModeEnabled:           engine.FastModeEnabled(),
		ThinkingLevel:             engine.ThinkingLevel(),
		CompactionMode:            engine.CompactionMode(),
		PreviousSessionID:         engine.PreviousSessionID(),
		ParentAgentSessionID:      engine.ParentAgentSessionID(),
		NavigationTargetSessionID: engine.NavigationTargetSessionID(),
	}
	if workflowState := engine.WorkflowSessionState(); workflowState.RunID != "" {
		status.Workflow = &clientui.TranscriptWorkflowSession{
			Active:     engine.WorkflowRunConfigured() && !engine.WorkflowTerminalState().Completed,
			RunID:      workflowState.RunID,
			TaskID:     workflowState.TaskID,
			WorkflowID: workflowState.WorkflowID,
		}
	}
	return status
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

func SessionViewFromRuntime(engine *runtime.Engine) clientui.RuntimeSessionView {
	if engine == nil {
		return clientui.RuntimeSessionView{}
	}
	return clientui.RuntimeSessionView{
		SessionID:             engine.SessionID(),
		SessionName:           engine.SessionName(),
		ConversationFreshness: ConversationFreshnessFromSession(engine.ConversationFreshness()),
	}
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
