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
		activity = clientui.RuntimeActivity{
			State:              clientui.RuntimeActivityUnavailable,
			Reviewer:           clientui.ReviewerActivityInactive,
			DiagnosticRecovery: true,
		}
	}
	return clientui.RuntimeMainView{
		Version:  version,
		Status:   status,
		Session:  sessionView,
		Activity: activity,
	}, nil
}

func StatusFromRuntime(engine *runtime.Engine) (clientui.RuntimeStatus, error) {
	if engine == nil {
		return clientui.RuntimeStatus{}, nil
	}
	goalAvailability, err := engine.GoalAvailability()
	if err != nil {
		return clientui.RuntimeStatus{}, err
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
		Goal:            GoalFromSessionState(engine.Goal(), goalAvailability, engine.GoalLoopSuspended()),
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

func GoalFromSessionState(goal *session.GoalState, availability session.GoalAvailability, suspended bool) *clientui.RuntimeGoal {
	projected := GoalAvailabilityFromSession(availability)
	return &clientui.RuntimeGoal{Goal: GoalCoreFromSessionState(goal), Availability: &projected, Suspended: suspended}
}

func GoalCoreFromSessionState(goal *session.GoalState) *clientui.Goal {
	if goal == nil {
		return nil
	}
	return &clientui.Goal{ID: strings.TrimSpace(goal.ID), Objective: goal.Objective, Status: clientui.RuntimeGoalStatus(goal.Status), CreatedAt: goal.CreatedAt, UpdatedAt: goal.UpdatedAt}
}

func GoalAvailabilityFromSession(availability session.GoalAvailability) clientui.GoalAvailability {
	if availability == session.GoalAvailable {
		return clientui.GoalAvailabilityAvailable
	}
	return clientui.GoalAvailabilityAgentCapabilityMissing
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
	copyMeta.PatchPresentation = patchformat.ClonePresentation(meta.PatchPresentation)
	return &copyMeta
}
