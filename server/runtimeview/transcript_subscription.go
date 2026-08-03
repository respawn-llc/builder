package runtimeview

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TranscriptHydrationFromSnapshot(runtimeSnapshot runtime.TranscriptHydrationSnapshot) clientui.TranscriptHydration {
	hydration := clientui.TranscriptHydration{
		CommittedRows:   transcriptRowsFromFacts(runtimeSnapshot.CommittedRows),
		ActiveAssistant: transcriptAssistantStream(runtimeSnapshot),
	}
	hydration.ActiveReasoning = transcriptReasoningStateFromRuntime(runtimeSnapshot.ActiveReasoning)
	hydration.InFlightTools = transcriptToolStartsFromRuntime(runtimeSnapshot.InFlightTools)
	hydration.QueuedMessages = transcriptQueuedMessagesFromRuntime(runtimeSnapshot.QueuedMessages)
	hydration.ActiveReviewer = transcriptReviewerStateFromRuntime(runtimeSnapshot.ActiveReviewer)
	hydration.ActiveCompaction = transcriptCompactionStateFromRuntime(runtimeSnapshot.ActiveCompaction)
	hydration.ContextUsage = transcriptContextUsageFromRuntime(runtimeSnapshot.ContextUsage)
	hydration.GoalStatus = transcriptGoalStatusFromRuntime(runtimeSnapshot.Goal, runtimeSnapshot.GoalSuspended)
	return hydration
}

func transcriptReasoningStateFromRuntime(state *runtime.TranscriptReasoningState) *clientui.TranscriptReasoningUpdate {
	if state == nil {
		return nil
	}
	update := &clientui.TranscriptReasoningUpdate{
		StepID: mustTranscriptStepID(state.StepID, "hydrated reasoning"),
		Key:    strings.TrimSpace(state.Key),
		Text:   state.Text,
	}
	if state.CurrentStatus != nil {
		update.CurrentStatus = &clientui.ReasoningStatus{Text: state.CurrentStatus.Text}
	}
	return update
}

func transcriptQueuedMessagesFromRuntime(messages []runtime.QueuedUserMessage) []clientui.TranscriptQueuedMessageState {
	if len(messages) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptQueuedMessageState, 0, len(messages))
	for index, message := range messages {
		out = append(out, clientui.TranscriptQueuedMessageState{
			ClientRequestID: mustTranscriptClientRequestID(message.ClientRequestID, fmt.Sprintf("hydrated queued message %d", index)),
			QueueItemID:     mustTranscriptQueueItemID(message.ID, fmt.Sprintf("hydrated queued message %d", index)),
			Status:          clientui.QueuedUserMessageAccepted,
			Text:            textutil.Value(message.Text),
		})
	}
	return out
}

func transcriptReviewerStateFromRuntime(state *runtime.TranscriptReviewerState) *clientui.TranscriptReviewerState {
	if state == nil {
		return nil
	}
	return &clientui.TranscriptReviewerState{
		StepID: mustTranscriptStepID(state.StepID, "hydrated reviewer"),
		State:  clientui.ReviewerStateRunning,
	}
}

func transcriptCompactionStateFromRuntime(state *runtime.TranscriptCompactionState) *clientui.TranscriptCompactionStatus {
	if state == nil {
		return nil
	}
	return &clientui.TranscriptCompactionStatus{
		StepID: mustTranscriptStepID(state.StepID, "hydrated compaction"),
		State:  clientui.CompactionStarted,
		Mode:   strings.TrimSpace(state.Mode),
		Count:  state.Count,
	}
}

func transcriptContextUsageFromRuntime(usage *runtime.ContextUsage) *clientui.TranscriptContextUsage {
	if usage == nil {
		return nil
	}
	projected := &clientui.TranscriptContextUsage{
		UsedTokens:   usage.UsedTokens,
		WindowTokens: usage.WindowTokens,
	}
	if usage.HasCacheHitPercentage {
		cacheHitPercent := usage.CacheHitPercent
		projected.CacheHitPercent = &cacheHitPercent
	}
	return projected
}

func transcriptGoalStatusFromRuntime(goal *session.GoalState, suspended bool) *clientui.TranscriptGoalStatus {
	projected := GoalFromSessionState(goal, suspended)
	if projected == nil {
		return nil
	}
	return &clientui.TranscriptGoalStatus{Goal: &clientui.TranscriptGoal{
		ID:        projected.ID,
		Objective: projected.Objective,
		Status:    projected.Status,
		Suspended: projected.Suspended,
	}}
}

func transcriptToolStartsFromRuntime(starts []runtime.TranscriptLiveToolStart) []clientui.TranscriptToolStart {
	if len(starts) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptToolStart, 0, len(starts))
	for index, start := range starts {
		out = append(out, clientui.TranscriptToolStart{
			StepID:       mustTranscriptStepID(start.StepID, fmt.Sprintf("in-flight tool %d", index)),
			ToolCallID:   clientui.ToolCallID(strings.TrimSpace(start.ToolCallID)),
			ToolName:     strings.TrimSpace(start.ToolName),
			Presentation: cloneToolCallMeta(start.Presentation),
		})
	}
	return out
}

func TranscriptMessagesFromRuntimeEvent(evt runtime.Event) []clientui.TranscriptEvent {
	switch evt.Kind {
	case runtime.EventAssistantDelta:
		if evt.AssistantDelta == "" {
			return nil
		}
		delta := clientui.TranscriptAssistantDelta{
			StepID:   mustTranscriptStepID(evt.StepID, "assistant delta"),
			StreamID: mustTranscriptAssistantStreamID(evt.AssistantTranscriptStreamID, "assistant delta"),
			Delta:    evt.AssistantDelta,
			Phase:    transcript.ClassifyAssistantPhase(string(evt.AssistantDeltaPhase)),
		}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(delta)}
	case runtime.EventAssistantDeltaReset:
		reason := strings.TrimSpace(evt.AssistantStreamAbortReason)
		if reason == "" || evt.AssistantTranscriptStreamID == nil {
			return nil
		}
		abort := clientui.TranscriptAssistantStreamAbort{
			StepID:   mustTranscriptStepID(evt.StepID, "assistant stream abort"),
			StreamID: mustTranscriptAssistantStreamID(evt.AssistantTranscriptStreamID, "assistant stream abort"),
			Reason:   transcriptAssistantAbortReason(reason),
		}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(abort)}
	case runtime.EventReasoningDelta:
		if evt.ReasoningDelta == nil {
			return nil
		}
		update := clientui.TranscriptReasoningUpdate{
			StepID: mustTranscriptStepID(evt.StepID, "reasoning update"),
			Key:    strings.TrimSpace(evt.ReasoningDelta.Key),
			Text:   evt.ReasoningDelta.Text,
		}
		if evt.ReasoningDelta.CurrentStatus != nil {
			update.CurrentStatus = &clientui.ReasoningStatus{Text: evt.ReasoningDelta.CurrentStatus.Text}
		}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(update)}
	case runtime.EventReasoningDeltaReset:
		reset := clientui.TranscriptReasoningReset{StepID: mustTranscriptStepID(evt.StepID, "reasoning reset")}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(reset)}
	case runtime.EventToolCallStarted:
		return transcriptToolStartMessages(runtime.TranscriptToolStartFactsFromEvent(evt))
	case runtime.EventToolCallAborted:
		return transcriptToolAbortMessages(evt)
	case runtime.EventQueuedUserMessageStatus:
		return transcriptQueuedMessageStateMessages(evt)
	case runtime.EventRunStateChanged:
		return transcriptStepStateMessages(evt)
	case runtime.EventLiveRunFinished:
		return transcriptLiveRunFinishedMessages(evt)
	case runtime.EventReviewerStarted, runtime.EventReviewerCompleted:
		return transcriptReviewerStateMessages(evt)
	case runtime.EventSleepGuardFailed, runtime.EventPromptHistoryPersistFailed:
		return transcriptOperationalDiagnosticMessages(evt)
	case runtime.EventUserMessageFlushed:
		messages := transcriptFeedStateMessages(evt)
		messages = append(messages, transcriptUserMessageFlushedMessages(evt)...)
		messages = append(messages, transcriptCommittedRowMessages(evt)...)
		return messages
	default:
		messages := transcriptFeedStateMessages(evt)
		messages = append(messages, transcriptCommittedRowMessages(evt)...)
		return messages
	}
}

func transcriptLiveRunFinishedMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if evt.LiveRunResult == nil {
		return nil
	}
	result := evt.LiveRunResult
	projected := clientui.TranscriptLiveRunResult{
		Status:        clientui.LiveRunStatus(result.Status),
		ResultKind:    clientui.LiveRunResultKind(result.ResultKind),
		NoFinalReason: clientui.LiveRunNoFinalReason(result.NoFinalReason),
		WorkPerformed: result.WorkPerformed,
		StartedAt:     result.StartedAt,
		FinishedAt:    result.FinishedAt,
	}
	if result.ResultKind == runtime.LiveRunResultAssistantFinalAnswer {
		if result.AssistantMessage.Content != nil {
			projected.FinalAnswer = textutil.Pointer(result.AssistantMessage.Content)
		} else {
			projected.ResultKind = clientui.LiveRunResultNoFinalAnswer
		}
	}
	if result.Status == runtime.RunStatusFailed && result.Error != nil {
		failure := result.Error.Error()
		projected.Failure = &failure
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(projected)}
}

func transcriptFeedStateMessages(evt runtime.Event) []clientui.TranscriptEvent {
	out := make([]clientui.TranscriptEvent, 0, 4)
	if evt.Compaction != nil {
		status := transcriptCompactionStatus(evt)
		out = append(out, clientui.NewTranscriptEvent(status))
	}
	if evt.ContextUsage != nil {
		usage := clientui.TranscriptContextUsage{
			UsedTokens:   evt.ContextUsage.UsedTokens,
			WindowTokens: evt.ContextUsage.WindowTokens,
		}
		if evt.ContextUsage.HasCacheHitPercentage {
			cacheHitPercent := evt.ContextUsage.CacheHitPercent
			usage.CacheHitPercent = &cacheHitPercent
		}
		out = append(out, clientui.NewTranscriptEvent(usage))
	}
	if evt.GoalStatus != nil {
		goal := transcriptGoalStatus(*evt.GoalStatus)
		out = append(out, clientui.NewTranscriptEvent(goal))
	}
	if evt.Background != nil {
		background := transcriptBackgroundActivity(*evt.Background)
		out = append(out, clientui.NewTranscriptEvent(background))
	}
	return out
}

func transcriptCompactionStatus(evt runtime.Event) clientui.TranscriptCompactionStatus {
	status := clientui.TranscriptCompactionStatus{
		StepID: mustTranscriptStepID(evt.StepID, "compaction status"),
		Mode:   strings.TrimSpace(evt.Compaction.Mode),
		Count:  evt.Compaction.Count,
	}
	switch evt.Kind {
	case runtime.EventCompactionStarted:
		status.State = clientui.CompactionStarted
	case runtime.EventCompactionCompleted:
		status.State = clientui.CompactionCompleted
	case runtime.EventCompactionFailed:
		status.State = clientui.CompactionFailed
		status.Diagnostic = &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode("compaction_failed"),
			Detail: strings.TrimSpace(evt.Compaction.Error),
		}
	default:
		panic(fmt.Sprintf("runtime event %q carries compaction facts outside the compaction lifecycle", evt.Kind))
	}
	return status
}

func transcriptGoalStatus(update runtime.GoalStatusUpdate) clientui.TranscriptGoalStatus {
	if update.Cleared {
		return clientui.TranscriptGoalStatus{}
	}
	return clientui.TranscriptGoalStatus{Goal: &clientui.TranscriptGoal{
		ID:        strings.TrimSpace(update.State.ID),
		Objective: update.State.Objective,
		Status:    clientui.RuntimeGoalStatus(strings.TrimSpace(string(update.State.Status))),
	}}
}

func transcriptBackgroundActivity(evt runtime.BackgroundShellEvent) clientui.TranscriptBackgroundActivity {
	background := clientui.TranscriptBackgroundActivity{
		ActivityID:        mustTranscriptBackgroundActivityID(evt.ActivityID.String(), "background activity"),
		ProcessID:         clientui.ProcessID(strings.TrimSpace(evt.ID)),
		OwnerRunID:        mustTranscriptRunID(evt.OwnerRunID, "background activity owner"),
		OwnerStepID:       mustTranscriptStepID(evt.OwnerStepID, "background activity owner"),
		Command:           evt.Command,
		Workdir:           evt.Workdir,
		LogPath:           textutil.OptionalTrimmedString(evt.LogPath),
		Preview:           textutil.OptionalTrimmedString(evt.Preview),
		ExitCode:          textutil.Pointer(evt.ExitCode),
		UserRequestedKill: evt.UserRequestedKill,
		NoticeSuppressed:  evt.NoticeSuppressed,
	}
	switch evt.Type {
	case runtime.BackgroundShellEventBackgrounded:
		background.Lifecycle = clientui.BackgroundLifecycleBackgrounded
	case runtime.BackgroundShellEventCompleted:
		background.Lifecycle = clientui.BackgroundLifecycleCompleted
	case runtime.BackgroundShellEventKilled:
		background.Lifecycle = clientui.BackgroundLifecycleKilled
	default:
		panic(fmt.Sprintf("runtime background activity has unknown lifecycle %q: process_id=%q activity_id=%q", evt.Type, evt.ID, evt.ActivityID))
	}
	return background
}

func TranscriptSessionIdentityFromRuntime(
	engine *runtime.Engine,
) (clientui.TranscriptSessionIdentity, error) {
	if engine == nil {
		return clientui.TranscriptSessionIdentity{}, nil
	}
	freshness, err := engine.ConversationFreshness()
	if err != nil {
		return clientui.TranscriptSessionIdentity{}, err
	}
	return clientui.TranscriptSessionIdentity{
		SessionID:             mustTranscriptSessionID(engine.SessionID(), "runtime session identity"),
		SessionName:           textutil.OptionalTrimmedString(engine.SessionName()),
		ConversationFreshness: ConversationFreshnessFromSession(freshness),
	}, nil
}

func transcriptCommittedRowMessages(evt runtime.Event) []clientui.TranscriptEvent {
	rowFacts := runtime.TranscriptCommittedRowFactsFromEvent(evt)
	if len(rowFacts) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptEvent, 0, len(rowFacts))
	for _, fact := range rowFacts {
		row := transcriptRowFromFact(fact)
		out = append(out, clientui.NewTranscriptEvent(row))
	}
	return out
}

func transcriptRowsFromFacts(facts []runtime.TranscriptCommittedRowFact) []clientui.TranscriptCommittedRow {
	if len(facts) == 0 {
		return []clientui.TranscriptCommittedRow{}
	}
	rows := make([]clientui.TranscriptCommittedRow, 0, len(facts))
	for _, fact := range facts {
		rows = append(rows, transcriptRowFromFact(fact))
	}
	return rows
}

func transcriptAssistantStream(snapshot runtime.TranscriptHydrationSnapshot) *clientui.TranscriptAssistantStream {
	text := snapshot.ActiveAssistantText
	if text == "" && snapshot.ActiveAssistantMetadata == nil && snapshot.ActiveAssistantStreamID == nil {
		return nil
	}
	if text == "" || snapshot.ActiveAssistantMetadata == nil || snapshot.ActiveAssistantStreamID == nil {
		panic(fmt.Sprintf(
			"runtime transcript hydration has partial assistant stream identity: text_present=%t metadata_present=%t stream_id_present=%t",
			text != "",
			snapshot.ActiveAssistantMetadata != nil,
			snapshot.ActiveAssistantStreamID != nil,
		))
	}
	return &clientui.TranscriptAssistantStream{
		StepID:   mustTranscriptStepID(snapshot.ActiveAssistantMetadata.StepID, "hydrated assistant stream"),
		StreamID: mustTranscriptAssistantStreamID(snapshot.ActiveAssistantStreamID, "hydrated assistant stream"),
		Text:     text,
		Phase:    transcript.ClassifyAssistantPhase(string(snapshot.ActiveAssistantPhase)),
	}
}

func transcriptToolStartMessages(starts []runtime.TranscriptLiveToolStart) []clientui.TranscriptEvent {
	projected := transcriptToolStartsFromRuntime(starts)
	if len(projected) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptEvent, 0, len(projected))
	for index := range projected {
		start := projected[index]
		out = append(out, clientui.NewTranscriptEvent(start))
	}
	return out
}

func transcriptToolAbortMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if evt.ToolCall == nil {
		panic("runtime tool abort is missing its tool call identity")
	}
	reason := clientui.ToolAbortReason(strings.TrimSpace(evt.ToolAbortReason))
	if reason == "" || reason == "interrupted" {
		reason = clientui.ToolAbortCanceled
	}
	abort := clientui.TranscriptToolAbort{
		StepID:     mustTranscriptStepID(evt.StepID, "tool abort"),
		ToolCallID: clientui.ToolCallID(strings.TrimSpace(evt.ToolCall.ID)),
		Reason:     reason,
	}
	if reason == clientui.ToolAbortFailed {
		abort.Diagnostic = &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode("tool_failed"),
			Detail: strings.TrimSpace(evt.Error),
		}
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(abort)}
}

func transcriptQueuedMessageStateMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if evt.QueuedUserMessageStatus == nil {
		return nil
	}
	status := evt.QueuedUserMessageStatus
	state := clientui.TranscriptQueuedMessageState{
		ClientRequestID: mustTranscriptClientRequestID(status.ClientRequestID, "queued-message state"),
		QueueItemID:     mustTranscriptQueueItemID(status.QueueItemID, "queued-message state"),
		Status:          clientui.QueuedUserMessageStatus(status.Status),
	}
	switch status.Status {
	case runtime.QueuedUserMessageAccepted:
		state.Text = textutil.OptionalTrimmedString(status.RestoreText)
	case runtime.QueuedUserMessageFailed:
		reason := clientui.QueuedUserMessageFailureReason(status.FailureReason)
		state.FailureReason = &reason
		state.Text = textutil.OptionalTrimmedString(status.RestoreText)
	case runtime.QueuedUserMessageSubmitted, runtime.QueuedUserMessageDiscarded:
	default:
		panic(fmt.Sprintf("runtime queued-message event has unknown status %q", status.Status))
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(state)}
}

func transcriptUserMessageFlushedMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if len(evt.UserMessageBatchQueuedItems) == 0 {
		return nil
	}
	operations := make([]clientui.RuntimeOperationRef, 0, len(evt.UserMessageBatchQueuedItems))
	for index, item := range evt.UserMessageBatchQueuedItems {
		queueItemID := mustTranscriptQueueItemID(item.QueueItemID, fmt.Sprintf("flushed queued message %d", index))
		operations = append(operations, clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindQueuedMessage,
			ClientRequestID: mustTranscriptClientRequestID(item.ClientRequestID, fmt.Sprintf("flushed queued message %d", index)),
			QueueItemID:     &queueItemID,
		})
	}
	flushed := clientui.TranscriptUserMessageFlushed{
		StepID:     mustTranscriptStepID(evt.StepID, "user-message flush"),
		Operations: operations,
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(flushed)}
}

func transcriptStepStateMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if evt.RunState == nil || evt.RunState.Lifecycle.Phase == runtime.RunLifecycleIdle {
		return nil
	}
	state := clientui.TranscriptStepState{
		RunID:      mustTranscriptRunID(evt.RunState.RunID, "step state"),
		StepID:     mustTranscriptStepID(evt.StepID, "step state"),
		ActiveKind: ClientActiveKindFromRuntime(evt.RunState.ActiveKind),
		Status:     clientui.RunStatus(evt.RunState.Status),
	}
	switch evt.RunState.Lifecycle.Phase {
	case runtime.RunLifecycleRunning:
		state.Lifecycle = clientui.StepLifecycleStarted
	case runtime.RunLifecycleFinished:
		state.Lifecycle = clientui.StepLifecycleFinished
	default:
		panic(fmt.Sprintf("runtime run state has unknown lifecycle phase %q", evt.RunState.Lifecycle.Phase))
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(state)}
}

func transcriptReviewerStateMessages(evt runtime.Event) []clientui.TranscriptEvent {
	state := clientui.TranscriptReviewerState{StepID: mustTranscriptStepID(evt.StepID, "reviewer state")}
	switch evt.Kind {
	case runtime.EventReviewerStarted:
		state.State = clientui.ReviewerStateRunning
	case runtime.EventReviewerCompleted:
		state.State = clientui.ReviewerStateCompleted
	default:
		panic(fmt.Sprintf("runtime event %q is not a reviewer lifecycle event", evt.Kind))
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(state)}
}

func transcriptOperationalDiagnosticMessages(evt runtime.Event) []clientui.TranscriptEvent {
	diagnostic := clientui.TranscriptOperationalDiagnostic{Detail: strings.TrimSpace(evt.Error)}
	if strings.TrimSpace(evt.StepID) != "" {
		stepID := mustTranscriptStepID(evt.StepID, "operational diagnostic")
		diagnostic.StepID = &stepID
	}
	switch evt.Kind {
	case runtime.EventSleepGuardFailed:
		diagnostic.Code = clientui.OperationalDiagnosticSleepGuardFailed
	case runtime.EventPromptHistoryPersistFailed:
		diagnostic.Code = clientui.OperationalDiagnosticPromptHistoryPersistFailed
	default:
		panic(fmt.Sprintf("runtime event %q is not an operational diagnostic", evt.Kind))
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(diagnostic)}
}

func transcriptRowFromFact(fact runtime.TranscriptCommittedRowFact) clientui.TranscriptCommittedRow {
	row := clientui.TranscriptCommittedRow{
		Visibility: fact.Visibility,
		Integrity:  fact.Integrity,
	}
	switch fact.Kind {
	case runtime.TranscriptCommittedRowFactUser:
		if fact.User == nil {
			panic("runtime transcript user row fact is missing its user payload")
		}
		row.Kind = clientui.TranscriptRowUser
		row.User = &clientui.TranscriptUserRow{
			StepID:           mustTranscriptStepID(fact.StepID, "committed user row"),
			Text:             fact.User.Text,
			CondensedText:    optionalNonBlankString(fact.User.CondensedText),
			RollbackTargetID: textutil.Pointer(fact.User.RollbackTargetID),
		}
	case runtime.TranscriptCommittedRowFactAssistant:
		if fact.Assistant == nil {
			panic("runtime transcript assistant row fact is missing its assistant payload")
		}
		row.Kind = clientui.TranscriptRowAssistant
		row.Assistant = &clientui.TranscriptAssistantRow{
			StepID:        mustTranscriptStepID(fact.StepID, "committed assistant row"),
			Text:          fact.Assistant.Text,
			CondensedText: optionalNonBlankString(fact.Assistant.CondensedText),
			Phase:         transcript.ClassifyAssistantPhase(string(fact.Assistant.Phase)),
		}
		if fact.Assistant.StreamID != nil {
			streamID := mustTranscriptAssistantStreamID(fact.Assistant.StreamID, "committed assistant row")
			row.Assistant.StreamID = &streamID
		}
	case runtime.TranscriptCommittedRowFactTool:
		if fact.Tool == nil {
			panic("runtime transcript tool row fact is missing its tool payload")
		}
		row.Kind = clientui.TranscriptRowTool
		row.Tool = &clientui.TranscriptToolRow{
			StepID:        mustTranscriptStepID(fact.StepID, "committed tool row"),
			ToolCallID:    clientui.ToolCallID(strings.TrimSpace(fact.Tool.ToolCallID)),
			ToolName:      strings.TrimSpace(fact.Tool.ToolName),
			Text:          fact.Tool.Text,
			IsError:       fact.Tool.IsError,
			ResultSummary: optionalNonBlankString(fact.Tool.ResultSummary),
			CondensedText: optionalNonBlankString(fact.Tool.CondensedText),
			Presentation:  cloneToolCallMeta(fact.Tool.Presentation),
		}
	case runtime.TranscriptCommittedRowFactNotice:
		row.Kind = clientui.TranscriptRowNotice
		row.Notice = transcriptNoticeFromFact(fact.StepID, fact.Notice)
	default:
		panic(fmt.Sprintf("runtime transcript row fact has unknown kind %q", fact.Kind))
	}
	return row
}

func transcriptNoticeFromFact(stepID string, fact *runtime.TranscriptNoticeRowFact) *clientui.TranscriptNoticeRow {
	if fact == nil {
		panic("runtime transcript notice row fact is missing its notice payload")
	}
	notice := &clientui.TranscriptNoticeRow{
		Reason:        clientui.TranscriptNoticeReason(strings.TrimSpace(fact.Reason)),
		Severity:      clientui.TranscriptNoticeSeverity(strings.TrimSpace(fact.Severity)),
		LegacyText:    optionalStringPointer(fact.LegacyText),
		SourcePath:    textutil.OptionalTrimmedString(fact.SourcePath),
		Worktree:      transcriptWorktreeContext(fact.MessageType, fact.WorktreeContext),
		CondensedText: optionalNonBlankString(fact.CondensedText),
		CompactLabel:  optionalNonBlankString(fact.CompactLabel),
	}
	if strings.TrimSpace(stepID) != "" {
		parsed := mustTranscriptStepID(stepID, "committed notice row")
		notice.StepID = &parsed
	}
	if messageType := strings.TrimSpace(string(fact.MessageType)); messageType != "" {
		typed := clientui.TranscriptMessageType(messageType)
		notice.MessageType = &typed
	}
	if fact.NoticeID != nil {
		value := clientui.NoticeID(strings.TrimSpace(*fact.NoticeID))
		notice.NoticeID = &value
	}
	if fact.CacheWarning != nil {
		notice.CacheWarning = &clientui.TranscriptCacheWarning{
			Scope:           strings.TrimSpace(fact.CacheWarning.Scope),
			Reason:          strings.TrimSpace(fact.CacheWarning.Reason),
			LostInputTokens: textutil.Pointer(fact.CacheWarning.LostInputTokens),
			Visibility:      fact.CacheWarning.Visibility,
		}
	}
	if fact.Compaction != nil {
		notice.Compaction = &clientui.TranscriptCompactionNotice{
			Count:  textutil.Pointer(fact.Compaction.Count),
			Detail: optionalStringPointer(fact.Compaction.Detail),
		}
	}
	diagnosticCode := strings.TrimSpace(fact.DiagnosticCode)
	diagnosticDetail := fact.DiagnosticDetail
	if diagnosticCode != "" || strings.TrimSpace(diagnosticDetail) != "" {
		if diagnosticCode == "" || strings.TrimSpace(diagnosticDetail) == "" {
			panic(fmt.Sprintf(
				"runtime transcript notice has partial diagnostic facts: code=%q detail_present=%t reason=%q",
				diagnosticCode,
				diagnosticDetail != "",
				fact.Reason,
			))
		}
		notice.Diagnostic = &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode(diagnosticCode),
			Detail: diagnosticDetail,
		}
	}
	activityID := strings.TrimSpace(fact.BackgroundActivityID)
	processID := strings.TrimSpace(fact.BackgroundProcessID)
	if activityID != "" || processID != "" {
		if activityID == "" || processID == "" {
			panic(fmt.Sprintf(
				"runtime transcript background notice has partial identity: activity_id=%q process_id=%q",
				activityID,
				processID,
			))
		}
		notice.Background = &clientui.TranscriptBackgroundNoticeIdentity{
			ActivityID: mustTranscriptBackgroundActivityID(activityID, "committed background notice"),
			ProcessID:  clientui.ProcessID(processID),
			ExitCode:   textutil.Pointer(fact.BackgroundExitCode),
		}
	}
	return notice
}

func transcriptWorktreeContext(messageType llm.MessageType, context *session.WorktreeContext) *clientui.TranscriptWorktreeContext {
	if context == nil {
		return nil
	}
	mode := session.WorktreeReminderMode("")
	switch messageType {
	case llm.MessageTypeWorktreeMode:
		mode = session.WorktreeReminderModeEnter
	case llm.MessageTypeWorktreeModeExit:
		mode = session.WorktreeReminderModeExit
	default:
		panic(fmt.Sprintf("worktree transcript context has non-worktree message type %q", messageType))
	}
	state, err := session.NormalizeWorktreeReminderState(session.WorktreeReminderState{
		Mode:            mode,
		WorktreeContext: *session.CloneWorktreeContext(context),
	})
	if err != nil {
		panic(fmt.Sprintf("project worktree transcript context: message_type=%q context=%+v: %v", messageType, context, err))
	}
	return &clientui.TranscriptWorktreeContext{
		Branch:        textutil.Pointer(state.Branch),
		WorktreePath:  strings.TrimSpace(state.WorktreePath),
		WorkspaceRoot: strings.TrimSpace(state.WorkspaceRoot),
		EffectiveCwd:  strings.TrimSpace(state.EffectiveCwd),
	}
}

func transcriptAssistantAbortReason(reason string) clientui.AssistantStreamAbortReason {
	switch strings.TrimSpace(reason) {
	case string(runtime.AssistantStreamAbortSuperseded):
		return clientui.AssistantStreamAbortSuperseded
	case "interrupted", "canceled":
		return clientui.AssistantStreamAbortInterrupted
	case "failed":
		return clientui.AssistantStreamAbortFailed
	default:
		panic(fmt.Sprintf("runtime assistant stream abort has unknown reason %q", reason))
	}
}

func optionalNonBlankString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func optionalStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return optionalNonBlankString(*value)
}

func mustTranscriptSessionID(raw string, owner string) runtimeids.SessionID {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("%s has invalid session id %q: %v", owner, raw, err))
	}
	return id
}

func mustTranscriptRunID(raw string, owner string) runtimeids.RunID {
	id, err := runtimeids.ParseRunID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("%s has invalid run id %q: %v", owner, raw, err))
	}
	return id
}

func mustTranscriptStepID(raw string, owner string) runtimeids.StepID {
	id, err := runtimeids.ParseStepID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("%s has invalid step id %q: %v", owner, raw, err))
	}
	return id
}

func mustTranscriptAssistantStreamID(raw *uuid.UUID, owner string) runtimeids.AssistantStreamID {
	if raw == nil {
		panic(fmt.Sprintf("%s is missing its assistant stream id", owner))
	}
	id, err := runtimeids.ParseAssistantStreamID(raw.String())
	if err != nil {
		panic(fmt.Sprintf("%s has invalid assistant stream id %q: %v", owner, raw.String(), err))
	}
	return id
}

func mustTranscriptClientRequestID(raw string, owner string) runtimeids.RuntimeClientRequestID {
	id, err := runtimeids.ParseRuntimeClientRequestID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("%s has invalid client request id %q: %v", owner, raw, err))
	}
	return id
}

func mustTranscriptQueueItemID(raw string, owner string) runtimeids.QueueItemID {
	id, err := runtimeids.ParseQueueItemID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("%s has invalid queue item id %q: %v", owner, raw, err))
	}
	return id
}

func mustTranscriptBackgroundActivityID(raw string, owner string) runtimeids.BackgroundActivityID {
	id, err := runtimeids.ParseBackgroundActivityID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("%s has invalid background activity id %q: %v", owner, raw, err))
	}
	return id
}
