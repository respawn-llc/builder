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
	return clientui.TranscriptHydration{
		CommittedRows:   transcriptRowsFromFacts(runtimeSnapshot.CommittedRows),
		ActiveAssistant: transcriptAssistantStream(runtimeSnapshot),
	}
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

func TranscriptMessagesFromRuntimeEvent(evt runtime.Event) []clientui.TranscriptMessage {
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
		return []clientui.TranscriptMessage{transcriptMessage(
			clientui.TranscriptMessageAssistantDelta,
			clientui.TranscriptPayload{AssistantDelta: &delta},
		)}
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
		return []clientui.TranscriptMessage{transcriptMessage(
			clientui.TranscriptMessageAssistantStreamAbort,
			clientui.TranscriptPayload{AssistantStreamAbort: &abort},
		)}
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
		return []clientui.TranscriptMessage{transcriptMessage(
			clientui.TranscriptMessageReasoningUpdate,
			clientui.TranscriptPayload{ReasoningUpdate: &update},
		)}
	case runtime.EventReasoningDeltaReset:
		reset := clientui.TranscriptReasoningReset{StepID: mustTranscriptStepID(evt.StepID, "reasoning reset")}
		return []clientui.TranscriptMessage{transcriptMessage(
			clientui.TranscriptMessageReasoningReset,
			clientui.TranscriptPayload{ReasoningReset: &reset},
		)}
	case runtime.EventToolCallStarted:
		return transcriptToolStartMessages(runtime.TranscriptToolStartFactsFromEvent(evt))
	case runtime.EventToolCallAborted:
		return transcriptToolAbortMessages(evt)
	case runtime.EventQueuedUserMessageStatus:
		return transcriptQueuedMessageStateMessages(evt)
	case runtime.EventRunStateChanged:
		return transcriptStepStateMessages(evt)
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

func transcriptFeedStateMessages(evt runtime.Event) []clientui.TranscriptMessage {
	out := make([]clientui.TranscriptMessage, 0, 4)
	if evt.Compaction != nil {
		status := transcriptCompactionStatus(evt)
		out = append(out, transcriptMessage(
			clientui.TranscriptMessageCompactionStatus,
			clientui.TranscriptPayload{CompactionStatus: &status},
		))
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
		out = append(out, transcriptMessage(
			clientui.TranscriptMessageContextUsage,
			clientui.TranscriptPayload{ContextUsage: &usage},
		))
	}
	if evt.GoalStatus != nil {
		goal := transcriptGoalStatus(*evt.GoalStatus)
		out = append(out, transcriptMessage(
			clientui.TranscriptMessageGoalStatus,
			clientui.TranscriptPayload{GoalStatus: &goal},
		))
	}
	if evt.Background != nil {
		background := transcriptBackgroundActivity(*evt.Background)
		out = append(out, transcriptMessage(
			clientui.TranscriptMessageBackgroundActivity,
			clientui.TranscriptPayload{BackgroundActivity: &background},
		))
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

func TranscriptSessionIdentityFromRuntime(engine *runtime.Engine) clientui.TranscriptSessionIdentity {
	if engine == nil {
		return clientui.TranscriptSessionIdentity{}
	}
	return clientui.TranscriptSessionIdentity{
		SessionID:             mustTranscriptSessionID(engine.SessionID(), "runtime session identity"),
		SessionName:           textutil.OptionalTrimmedString(engine.SessionName()),
		ConversationFreshness: ConversationFreshnessFromSession(engine.ConversationFreshness()),
	}
}

func transcriptCommittedRowMessages(evt runtime.Event) []clientui.TranscriptMessage {
	rowFacts := runtime.TranscriptCommittedRowFactsFromEvent(evt)
	if len(rowFacts) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptMessage, 0, len(rowFacts))
	for _, fact := range rowFacts {
		row := transcriptRowFromFact(fact)
		out = append(out, transcriptMessage(
			clientui.TranscriptMessageCommittedRow,
			clientui.TranscriptPayload{CommittedRow: &row},
		))
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

func transcriptToolStartMessages(starts []runtime.TranscriptLiveToolStart) []clientui.TranscriptMessage {
	projected := transcriptToolStartsFromRuntime(starts)
	if len(projected) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptMessage, 0, len(projected))
	for index := range projected {
		start := projected[index]
		out = append(out, transcriptMessage(
			clientui.TranscriptMessageToolStart,
			clientui.TranscriptPayload{ToolStart: &start},
		))
	}
	return out
}

func transcriptToolAbortMessages(evt runtime.Event) []clientui.TranscriptMessage {
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
	return []clientui.TranscriptMessage{transcriptMessage(
		clientui.TranscriptMessageToolAbort,
		clientui.TranscriptPayload{ToolAbort: &abort},
	)}
}

func transcriptQueuedMessageStateMessages(evt runtime.Event) []clientui.TranscriptMessage {
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
	return []clientui.TranscriptMessage{transcriptMessage(
		clientui.TranscriptMessageQueuedMessageState,
		clientui.TranscriptPayload{QueuedMessageState: &state},
	)}
}

func transcriptUserMessageFlushedMessages(evt runtime.Event) []clientui.TranscriptMessage {
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
	return []clientui.TranscriptMessage{transcriptMessage(
		clientui.TranscriptMessageUserMessageFlushed,
		clientui.TranscriptPayload{UserMessageFlushed: &flushed},
	)}
}

func transcriptStepStateMessages(evt runtime.Event) []clientui.TranscriptMessage {
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
	return []clientui.TranscriptMessage{transcriptMessage(
		clientui.TranscriptMessageStepState,
		clientui.TranscriptPayload{StepState: &state},
	)}
}

func transcriptReviewerStateMessages(evt runtime.Event) []clientui.TranscriptMessage {
	state := clientui.TranscriptReviewerState{StepID: mustTranscriptStepID(evt.StepID, "reviewer state")}
	switch evt.Kind {
	case runtime.EventReviewerStarted:
		state.State = clientui.ReviewerStateRunning
	case runtime.EventReviewerCompleted:
		state.State = clientui.ReviewerStateCompleted
	default:
		panic(fmt.Sprintf("runtime event %q is not a reviewer lifecycle event", evt.Kind))
	}
	return []clientui.TranscriptMessage{transcriptMessage(
		clientui.TranscriptMessageReviewerState,
		clientui.TranscriptPayload{ReviewerState: &state},
	)}
}

func transcriptOperationalDiagnosticMessages(evt runtime.Event) []clientui.TranscriptMessage {
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
	return []clientui.TranscriptMessage{transcriptMessage(
		clientui.TranscriptMessageOperationalDiagnostic,
		clientui.TranscriptPayload{OperationalDiagnostic: &diagnostic},
	)}
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
			LostInputTokens: fact.CacheWarning.LostInputTokens,
			Visibility:      fact.CacheWarning.Visibility,
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

func transcriptMessage(kind clientui.TranscriptMessageKind, payload clientui.TranscriptPayload) clientui.TranscriptMessage {
	message := clientui.TranscriptMessage{Kind: kind, Payload: payload}
	if err := message.ValidatePayload(); err != nil {
		panic(fmt.Sprintf("project invalid runtime transcript message kind %q: %v", kind, err))
	}
	return message
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
