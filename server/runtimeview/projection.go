package runtimeview

import (
	"strings"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

const runtimeNoopFinalToken = "NO_OP"

func MainViewFromRuntime(engine *runtime.Engine) clientui.RuntimeMainView {
	if engine == nil {
		return clientui.RuntimeMainView{}
	}
	sessionView := SessionViewFromRuntime(engine)
	return clientui.RuntimeMainView{
		Status:    StatusFromRuntime(engine),
		Session:   sessionView,
		ActiveRun: RunViewFromRuntime(sessionView.SessionID, engine.ActiveRun()),
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
		Transcript: clientui.TranscriptMetadata{
			Revision:            engine.TranscriptRevision(),
			CommittedEntryCount: engine.CommittedTranscriptEntryCount(),
		},
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
		TranscriptRevision:           evt.TranscriptRevision,
		CommittedEntryCount:          evt.CommittedEntryCount,
		CommittedEntryStart:          evt.CommittedEntryStart,
		CommittedEntryStartSet:       evt.CommittedEntryStartSet,
		Error:                        evt.Error,
		AssistantDelta:               evt.AssistantDelta,
		AssistantDeltaPhase:          clientui.MessagePhase(evt.AssistantDeltaPhase),
		UserMessage:                  evt.UserMessage,
		UserMessageBatch:             append([]string(nil), evt.UserMessageBatch...),
		UserMessageBatchQueueItemIDs: append([]string(nil), evt.UserMessageBatchQueueItemIDs...),
		TranscriptEntries:            chatEntriesFromRuntime(runtime.TranscriptEntriesFromEvent(evt)),
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
		view.RunState = &clientui.RunState{
			Lifecycle: clientui.MustRunLifecycle(
				clientui.RunLifecyclePhase(evt.RunState.Lifecycle.Phase),
				clientui.RunMode(evt.RunState.Lifecycle.Mode),
			),
			RunID:      evt.RunState.RunID,
			Status:     clientui.RunStatus(evt.RunState.Status),
			StartedAt:  evt.RunState.StartedAt,
			FinishedAt: evt.RunState.FinishedAt,
		}
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

func chatEntriesFromRuntime(entries []runtime.ChatEntry) []clientui.ChatEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]clientui.ChatEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, clientui.ChatEntry{
			Visibility:        clientui.EntryVisibility(entry.Visibility),
			RollbackTargetID:  entry.RollbackTargetID,
			Role:              entry.Role,
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             string(entry.Phase),
			MessageType:       string(entry.MessageType),
			SourcePath:        entry.SourcePath,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCallID:        entry.ToolCallID,
			NoticeID:          entry.NoticeID,
			ToolCall:          cloneToolCallMeta(entry.ToolCall),
		})
	}
	return out
}

func RunViewFromRuntime(sessionID string, snapshot *runtime.RunSnapshot) *clientui.RunView {
	if snapshot == nil {
		return nil
	}
	mode := clientui.RunModeTurn
	if snapshot.GoalLoop {
		mode = clientui.RunModeGoalLoop
	}
	return &clientui.RunView{
		RunID:      snapshot.RunID,
		SessionID:  sessionID,
		StepID:     snapshot.StepID,
		Status:     clientui.RunStatus(snapshot.Status),
		Lifecycle:  clientui.MustRunLifecycle(clientui.RunLifecycleRunning, mode),
		StartedAt:  snapshot.StartedAt,
		FinishedAt: snapshot.FinishedAt,
	}
}

func RunViewFromSessionRecord(sessionID string, record *session.RunRecord) *clientui.RunView {
	if record == nil {
		return nil
	}
	lifecycle := clientui.MustRunLifecycle(clientui.RunLifecycleFinished, clientui.RunModeTurn)
	if record.Status == session.RunStatusRunning {
		lifecycle = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)
	}
	return &clientui.RunView{
		RunID:      record.RunID,
		SessionID:  sessionID,
		StepID:     record.StepID,
		Status:     clientui.RunStatus(record.Status),
		Lifecycle:  lifecycle,
		StartedAt:  record.StartedAt,
		FinishedAt: record.FinishedAt,
	}
}

func ChatSnapshotFromRuntime(snapshot runtime.ChatSnapshot) clientui.ChatSnapshot {
	entries := make([]clientui.ChatEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if isSuppressedNoopAssistantEntry(entry) {
			continue
		}
		entries = append(entries, clientui.ChatEntry{
			Visibility:        clientui.EntryVisibility(entry.Visibility),
			RollbackTargetID:  entry.RollbackTargetID,
			Role:              entry.Role,
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             string(entry.Phase),
			MessageType:       string(entry.MessageType),
			SourcePath:        entry.SourcePath,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCallID:        entry.ToolCallID,
			NoticeID:          entry.NoticeID,
			ToolCall:          cloneToolCallMeta(entry.ToolCall),
		})
	}
	streaming := snapshot.Streaming
	if strings.TrimSpace(streaming) == runtimeNoopFinalToken {
		streaming = ""
	}
	return clientui.ChatSnapshot{
		Entries:        entries,
		Streaming:      streaming,
		StreamingError: snapshot.StreamingError,
	}
}

func isSuppressedNoopAssistantEntry(entry runtime.ChatEntry) bool {
	return strings.TrimSpace(entry.Role) == "assistant" && entry.Phase == llm.MessagePhaseFinal && strings.TrimSpace(entry.Text) == runtimeNoopFinalToken
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
		copyMeta.PatchRender = cloneRenderedPatch(meta.PatchRender)
	}
	return copyMeta
}

func cloneRenderedPatch(in *patchformat.RenderedPatch) *patchformat.RenderedPatch {
	if in == nil {
		return nil
	}
	out := &patchformat.RenderedPatch{}
	if len(in.Files) > 0 {
		out.Files = make([]patchformat.RenderedFile, 0, len(in.Files))
		for _, file := range in.Files {
			copyFile := file
			if len(file.Diff) > 0 {
				copyFile.Diff = append([]string(nil), file.Diff...)
			}
			out.Files = append(out.Files, copyFile)
		}
	}
	if len(in.SummaryLines) > 0 {
		out.SummaryLines = append([]patchformat.RenderedLine(nil), in.SummaryLines...)
	}
	if len(in.DetailLines) > 0 {
		out.DetailLines = append([]patchformat.RenderedLine(nil), in.DetailLines...)
	}
	return out
}
