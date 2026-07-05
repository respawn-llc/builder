package runtimeview

import (
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/transcript"
)

const runtimeNoopFinalToken = "NO_OP"

func MainViewFromRuntimeActivity(engine *runtime.Engine, version clientui.ReadModelVersion, activity clientui.RuntimeActivity) clientui.RuntimeMainView {
	if engine == nil {
		return clientui.RuntimeMainView{}
	}
	sessionView := SessionViewFromRuntime(engine)
	if err := activity.Validate(); err != nil {
		activity = clientui.MustRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{DiagnosticRecovery: true})
	}
	return clientui.RuntimeMainView{
		Version:             version,
		Status:              StatusFromRuntime(engine),
		Session:             sessionView,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	}
}

func RuntimeMainViewFromActivity(activity clientui.RuntimeActivity, status clientui.RuntimeStatus, sessionView clientui.RuntimeSessionView) clientui.RuntimeMainView {
	version := runtimeactivity.NextReadModelVersion(sessionView.SessionID)
	if err := activity.Validate(); err != nil {
		activity = clientui.MustRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{DiagnosticRecovery: true})
	}
	return clientui.RuntimeMainView{
		Version:             version,
		Status:              status,
		Session:             sessionView,
		Activity:            activity,
		InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(version),
	}
}

func StatusFromRuntime(engine *runtime.Engine) clientui.RuntimeStatus {
	return clientui.RuntimeStatus(TranscriptSessionStatusFromRuntime(engine))
}

func TranscriptSessionStatusFromRuntime(engine *runtime.Engine) clientui.TranscriptSessionStatus {
	if engine == nil {
		return clientui.TranscriptSessionStatus{}
	}
	usage := engine.ContextUsage()
	status := clientui.TranscriptSessionStatus{
		ReviewerFrequency:                 engine.ReviewerFrequency(),
		ReviewerEnabled:                   engine.ReviewerEnabled(),
		AutoCompactionEnabled:             engine.AutoCompactionEnabled(),
		QuestionsEnabled:                  engine.QuestionsEnabled(),
		FastModeAvailable:                 engine.FastModeAvailable(),
		FastModeEnabled:                   engine.FastModeEnabled(),
		ConversationFreshness:             ConversationFreshnessFromSession(engine.ConversationFreshness()),
		ParentSessionID:                   engine.ParentSessionID(),
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

func EventFromRuntime(evt runtime.Event) clientui.Event {
	view := clientui.Event{
		Kind:                         clientui.EventKind(evt.Kind),
		StepID:                       evt.StepID,
		CommittedTranscriptChanged:   evt.CommittedTranscriptChanged,
		Error:                        evt.Error,
		AssistantDelta:               evt.AssistantDelta,
		AssistantDeltaPhase:          clientui.MessagePhase(evt.AssistantDeltaPhase),
		UserMessage:                  evt.UserMessage,
		UserMessageBatch:             append([]string(nil), evt.UserMessageBatch...),
		UserMessageBatchQueueItemIDs: append([]string(nil), evt.UserMessageBatchQueueItemIDs...),
	}
	if evt.ReasoningDelta != nil {
		view.ReasoningDelta = &clientui.ReasoningDelta{
			Key:  evt.ReasoningDelta.Key,
			Role: evt.ReasoningDelta.Role,
			Text: evt.ReasoningDelta.Text,
		}
	}
	if evt.Compaction != nil {
		view.Compaction = &clientui.CompactionStatus{
			Mode:  evt.Compaction.Mode,
			Count: evt.Compaction.Count,
			Error: evt.Compaction.Error,
		}
	}
	if evt.CacheWarning != nil {
		view.CacheWarning = copyCacheWarningView(evt.CacheWarning)
	}
	view.CacheWarningVisibility = clientui.EntryVisibility(evt.CacheWarningVisibility)
	if evt.RunState != nil {
		state := runtimeRunStateToClient(*evt.RunState)
		view.RunState = &state
	}
	if evt.ContextUsage != nil {
		view.ContextUsage = &clientui.RuntimeContextUsage{
			UsedTokens:            evt.ContextUsage.UsedTokens,
			WindowTokens:          evt.ContextUsage.WindowTokens,
			CacheHitPercent:       evt.ContextUsage.CacheHitPercent,
			HasCacheHitPercentage: evt.ContextUsage.HasCacheHitPercentage,
		}
	}
	if evt.GoalStatus != nil {
		view.GoalStatus = goalStatusUpdateFromRuntime(evt.GoalStatus)
	}
	if evt.Background != nil {
		view.Background = &clientui.BackgroundShellEvent{
			Type:              evt.Background.Type,
			ID:                evt.Background.ID,
			State:             evt.Background.State,
			Command:           evt.Background.Command,
			Workdir:           evt.Background.Workdir,
			LogPath:           evt.Background.LogPath,
			NoticeText:        evt.Background.NoticeText,
			CompactText:       evt.Background.CompactText,
			Preview:           evt.Background.Preview,
			Removed:           evt.Background.Removed,
			UserRequestedKill: evt.Background.UserRequestedKill,
			NoticeSuppressed:  evt.Background.NoticeSuppressed,
		}
		if evt.Background.ExitCode != nil {
			exitCode := *evt.Background.ExitCode
			view.Background.ExitCode = &exitCode
		}
	}
	if evt.QueuedUserMessageStatus != nil {
		view.QueuedUserMessageStatus = &clientui.QueuedUserMessageStatusEvent{
			SessionID:       evt.QueuedUserMessageStatus.SessionID,
			QueueItemID:     evt.QueuedUserMessageStatus.QueueItemID,
			ClientRequestID: evt.QueuedUserMessageStatus.ClientRequestID,
			Status:          clientui.QueuedUserMessageStatus(evt.QueuedUserMessageStatus.Status),
			FailureReason:   clientui.QueuedUserMessageFailureReason(evt.QueuedUserMessageStatus.FailureReason),
			RestoreText:     evt.QueuedUserMessageStatus.RestoreText,
		}
	}
	return view
}

func runtimeRunStateToClient(state runtime.RunState) clientui.RunState {
	activeKind := clientui.RuntimeActivityActiveKind("")
	if state.ActiveKind.Valid() {
		activeKind = ClientActiveKindFromRuntime(state.ActiveKind)
	}
	return clientui.RunState{
		Lifecycle: clientui.MustRunLifecycle(
			clientui.RunLifecyclePhase(state.Lifecycle.Phase),
			clientui.RunMode(state.Lifecycle.Mode),
		),
		RunID:      state.RunID,
		ActiveKind: activeKind,
		Status:     clientui.RunStatus(state.Status),
		StartedAt:  state.StartedAt,
		FinishedAt: state.FinishedAt,
	}
}

func goalStatusUpdateFromRuntime(update *runtime.GoalStatusUpdate) *clientui.RuntimeGoalStatusUpdate {
	if update == nil {
		return nil
	}
	if update.Cleared {
		return &clientui.RuntimeGoalStatusUpdate{Cleared: true}
	}
	return &clientui.RuntimeGoalStatusUpdate{
		ID:        strings.TrimSpace(update.State.ID),
		Objective: update.State.Objective,
		Status:    clientui.RuntimeGoalStatus(strings.TrimSpace(string(update.State.Status))),
	}
}

func copyCacheWarningView(in *transcript.CacheWarning) *transcript.CacheWarning {
	if in == nil {
		return nil
	}
	copyWarning := *in
	return &copyWarning
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

func cloneToolCallMeta(meta *transcript.ToolCallMeta) *clientui.ToolCallMeta {
	if meta == nil {
		return nil
	}
	copyMeta := &clientui.ToolCallMeta{
		ToolName:               meta.ToolName,
		Presentation:           clientui.ToolPresentationKind(meta.Presentation),
		RenderBehavior:         clientui.ToolCallRenderBehavior(meta.RenderBehavior),
		IsShell:                meta.IsShell,
		UserInitiated:          meta.UserInitiated,
		Command:                meta.Command,
		CompactText:            meta.CompactText,
		InlineMeta:             meta.InlineMeta,
		TimeoutLabel:           meta.TimeoutLabel,
		PatchSummary:           meta.PatchSummary,
		PatchDetail:            meta.PatchDetail,
		Question:               meta.Question,
		RecommendedOptionIndex: meta.RecommendedOptionIndex,
		OmitSuccessfulResult:   meta.OmitSuccessfulResult,
		RawOutputRequested:     meta.RawOutputRequested,
		OutputTruncated:        meta.OutputTruncated,
	}
	if len(meta.Suggestions) > 0 {
		copyMeta.Suggestions = append([]string(nil), meta.Suggestions...)
	}
	if meta.RenderHint != nil {
		copyMeta.RenderHint = &clientui.ToolRenderHint{
			Kind:         clientui.ToolRenderKind(meta.RenderHint.Kind),
			Path:         meta.RenderHint.Path,
			ResultOnly:   meta.RenderHint.ResultOnly,
			ShellDialect: clientui.ToolShellDialect(meta.RenderHint.ShellDialect),
		}
	}
	if meta.PatchRender != nil {
		copyMeta.PatchRender = clientui.CloneRenderedPatch(meta.PatchRender)
	}
	return copyMeta
}
