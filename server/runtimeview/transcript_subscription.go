package runtimeview

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	shelltool "core/server/tools/shell"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TranscriptHydrationFromSnapshot(runtimeSnapshot runtime.TranscriptHydrationSnapshot) clientui.TranscriptHydration {
	hydration, err := TranscriptHydrationFromSnapshotChecked(runtimeSnapshot)
	if err != nil {
		panic(err)
	}
	return hydration
}

func TranscriptHydrationFromSnapshotChecked(runtimeSnapshot runtime.TranscriptHydrationSnapshot) (clientui.TranscriptHydration, error) {
	rows, err := transcriptRowsFromFactsChecked(runtimeSnapshot.CommittedRows)
	if err != nil {
		return clientui.TranscriptHydration{}, err
	}
	hydration := clientui.TranscriptHydration{
		CommittedRows:   rows,
		ActiveAssistant: transcriptAssistantStream(runtimeSnapshot),
	}
	hydration.ActiveThinkingStatus = transcriptThinkingStatusFromRuntime(runtimeSnapshot.ActiveThinkingStatus)
	hydration.ActiveReasoningTraces = transcriptReasoningTracesFromRuntime(runtimeSnapshot.ActiveReasoningTraces)
	hydration.InFlightTools = transcriptToolStartsFromRuntime(runtimeSnapshot.InFlightTools)
	hydration.QueuedMessages, err = transcriptQueuedMessagesFromRuntime(runtimeSnapshot.QueuedMessages)
	if err != nil {
		return clientui.TranscriptHydration{}, err
	}
	hydration.ActiveCompaction = transcriptCompactionStateFromRuntime(runtimeSnapshot.ActiveCompaction)
	hydration.ContextUsage = transcriptContextUsageFromRuntime(runtimeSnapshot.ContextUsage)
	hydration.GoalStatus = transcriptGoalStatusFromRuntime(runtimeSnapshot.Goal, runtimeSnapshot.GoalSuspended)
	return hydration, nil
}

func transcriptThinkingStatusFromRuntime(state *runtime.TranscriptThinkingStatusState) *clientui.TranscriptThinkingStatusUpdate {
	if state == nil {
		return nil
	}
	return &clientui.TranscriptThinkingStatusUpdate{
		StepID: mustTranscriptStepID(state.StepID, "hydrated thinking status"),
		Text:   state.Text,
	}
}

func transcriptReasoningTracesFromRuntime(states []runtime.TranscriptReasoningTraceState) []clientui.TranscriptReasoningTraceUpdate {
	if len(states) == 0 {
		return nil
	}
	out := make([]clientui.TranscriptReasoningTraceUpdate, 0, len(states))
	for index, state := range states {
		presentation := runtime.ProjectReasoningTrace(state.Text)
		identity := transcriptReasoningTraceIdentityProjection(&state.Identity, fmt.Sprintf("hydrated reasoning trace %d", index))
		out = append(out, clientui.TranscriptReasoningTraceUpdate{
			StepID:      mustTranscriptStepID(state.StepID, fmt.Sprintf("hydrated reasoning trace %d", index)),
			Identity:    identity,
			CompactText: presentation.CompactText,
			Text:        presentation.Text,
		})
	}
	return out
}

func transcriptQueuedMessagesFromRuntime(messages []runtime.QueuedUserMessage) ([]clientui.TranscriptQueuedMessageState, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	out := make([]clientui.TranscriptQueuedMessageState, 0, len(messages))
	for index, message := range messages {
		text, err := message.DisplayText()
		if err != nil {
			return nil, fmt.Errorf("queued message %d: %w", index, err)
		}
		out = append(out, clientui.TranscriptQueuedMessageState{
			QueueItemID: mustTranscriptQueueItemID(message.ID, fmt.Sprintf("hydrated queued message %d", index)),
			Status:      clientui.QueuedUserMessageAccepted,
			Text:        textutil.Value(text),
		})
	}
	return out, nil
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
	if goal == nil {
		return nil
	}
	return &clientui.TranscriptGoalStatus{Goal: &clientui.TranscriptGoal{
		Goal:      GoalCoreFromSessionState(goal),
		Suspended: suspended,
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
	return transcriptMessagesFromRuntimeEvent(evt)
}

func TranscriptMessagesFromRuntimeEventChecked(evt runtime.Event) ([]clientui.TranscriptEvent, error) {
	for index, fact := range runtime.TranscriptCommittedRowFactsFromEvent(evt) {
		if err := fact.Locator.Validate(); err != nil {
			return nil, fmt.Errorf("runtime committed row fact %d from event %q lacks valid provenance: %w", index, evt.Kind, err)
		}
	}
	return transcriptMessagesFromRuntimeEvent(evt), nil
}

func transcriptMessagesFromRuntimeEvent(evt runtime.Event) []clientui.TranscriptEvent {
	switch evt.Kind {
	case runtime.EventAssistantDelta:
		if evt.AssistantDelta == "" {
			return nil
		}
		delta := clientui.TranscriptAssistantDelta{
			StepID:   mustRuntimeTranscriptStepID(evt.StepID, "assistant delta"),
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
			StepID:   mustRuntimeTranscriptStepID(evt.StepID, "assistant stream abort"),
			StreamID: mustTranscriptAssistantStreamID(evt.AssistantTranscriptStreamID, "assistant stream abort"),
			Reason:   transcriptAssistantAbortReason(reason),
		}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(abort)}
	case runtime.EventReasoningDelta:
		if evt.ReasoningDelta == nil {
			return nil
		}
		presentation := runtime.ProjectReasoningTrace(evt.ReasoningDelta.Text)
		if evt.ReasoningTraceIdentity == nil && strings.TrimSpace(evt.ReasoningDelta.Text) == "" {
			if evt.ReasoningDelta.CurrentStatus != nil {
				status := clientui.TranscriptThinkingStatusUpdate{
					StepID: mustRuntimeTranscriptStepID(evt.StepID, "thinking status update"),
					Text:   evt.ReasoningDelta.CurrentStatus.Text,
				}
				return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(status)}
			}
			return nil
		}
		traceIdentity := transcriptReasoningTraceIdentityProjection(evt.ReasoningTraceIdentity, "reasoning delta")
		update := clientui.TranscriptReasoningTraceUpdate{
			StepID:      mustRuntimeTranscriptStepID(evt.StepID, "reasoning trace update"),
			Identity:    traceIdentity,
			CompactText: presentation.CompactText,
			Text:        presentation.Text,
		}
		if evt.ReasoningDelta.CurrentStatus != nil {
			status := clientui.TranscriptThinkingStatusUpdate{
				StepID: mustRuntimeTranscriptStepID(evt.StepID, "thinking status update"),
				Text:   evt.ReasoningDelta.CurrentStatus.Text,
			}
			return []clientui.TranscriptEvent{
				clientui.NewTranscriptEvent(status),
				clientui.NewTranscriptEvent(update),
			}
		}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(update)}
	case runtime.EventReasoningDeltaReset:
		reset := clientui.TranscriptReasoningTraceReset{StepID: mustRuntimeTranscriptStepID(evt.StepID, "reasoning trace reset")}
		return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(reset)}
	case runtime.EventToolCallStarted:
		return transcriptToolStartMessages(runtime.TranscriptToolStartFactsFromEvent(evt))
	case runtime.EventToolCallAborted:
		return transcriptToolAbortMessages(evt)
	case runtime.EventQueuedUserMessageStatus:
		return transcriptQueuedMessageStateMessages(evt)
	case runtime.EventHumanInputInterrupted:
		return transcriptHumanInputInterruptedMessages(evt)
	case runtime.EventRunStateChanged:
		return transcriptStepStateMessages(evt)
	case runtime.EventLiveRunFinished:
		return transcriptLiveRunFinishedMessages(evt)
	case runtime.EventSleepGuardFailed,
		runtime.EventPromptHistoryPersistFailed,
		runtime.EventContextFactsPersistFailed,
		runtime.EventInFlightClearFailed:
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
		StepID: mustRuntimeTranscriptStepID(evt.StepID, "compaction status"),
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
		Goal: GoalCoreFromSessionState(&update.State),
	}}
}

type transcriptBackgroundActivityFacts struct {
	activityID        runtimeids.BackgroundActivityID
	processID         clientui.ProcessID
	ownerRunID        runtimeids.RunID
	ownerStepID       runtimeids.StepID
	lifecycle         clientui.BackgroundLifecycle
	command           string
	workdir           string
	logPath           *string
	preview           *string
	exitCode          *int
	userRequestedKill bool
	noticeSuppressed  bool
}

func transcriptBackgroundActivityFromFacts(facts transcriptBackgroundActivityFacts) clientui.TranscriptBackgroundActivity {
	return clientui.TranscriptBackgroundActivity{
		ActivityID:        facts.activityID,
		ProcessID:         facts.processID,
		OwnerRunID:        facts.ownerRunID,
		OwnerStepID:       facts.ownerStepID,
		Lifecycle:         facts.lifecycle,
		Command:           facts.command,
		Workdir:           facts.workdir,
		LogPath:           facts.logPath,
		Preview:           facts.preview,
		ExitCode:          facts.exitCode,
		UserRequestedKill: facts.userRequestedKill,
		NoticeSuppressed:  facts.noticeSuppressed,
	}
}

func transcriptBackgroundActivity(evt runtime.BackgroundShellEvent) clientui.TranscriptBackgroundActivity {
	lifecycle := clientui.BackgroundLifecycle("")
	switch evt.Type {
	case runtime.BackgroundShellEventBackgrounded:
		lifecycle = clientui.BackgroundLifecycleBackgrounded
	case runtime.BackgroundShellEventCompleted:
		lifecycle = clientui.BackgroundLifecycleCompleted
	case runtime.BackgroundShellEventKilled:
		lifecycle = clientui.BackgroundLifecycleKilled
	default:
		panic(fmt.Sprintf("runtime background activity has unknown lifecycle %q: process_id=%q activity_id=%q", evt.Type, evt.ID, evt.ActivityID))
	}
	return transcriptBackgroundActivityFromFacts(transcriptBackgroundActivityFacts{
		activityID:        mustTranscriptBackgroundActivityID(evt.ActivityID.String(), "background activity"),
		processID:         clientui.ProcessID(strings.TrimSpace(evt.ID)),
		ownerRunID:        mustTranscriptRunID(evt.OwnerRunID, "background activity owner"),
		ownerStepID:       mustTranscriptStepID(evt.OwnerStepID, "background activity owner"),
		lifecycle:         lifecycle,
		command:           evt.Command,
		workdir:           evt.Workdir,
		logPath:           textutil.OptionalTrimmedString(evt.LogPath),
		preview:           textutil.OptionalTrimmedString(evt.Preview),
		exitCode:          textutil.Pointer(evt.ExitCode),
		userRequestedKill: evt.UserRequestedKill,
		noticeSuppressed:  evt.NoticeSuppressed,
	})
}

func TranscriptBackgroundActivitiesFromProcessSnapshots(
	sessionID string,
	snapshots []shelltool.Snapshot,
) ([]clientui.TranscriptBackgroundActivity, error) {
	sessionID = strings.TrimSpace(sessionID)
	out := make([]clientui.TranscriptBackgroundActivity, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.Running || !snapshot.Backgrounded || strings.TrimSpace(snapshot.OwnerSessionID) != sessionID {
			continue
		}
		activityID, err := runtimeids.ParseBackgroundActivityID(snapshot.ActivityID.String())
		if err != nil {
			return nil, fmt.Errorf("background process %q activity id: %w", snapshot.ID, err)
		}
		runID, err := runtimeids.ParseRunID(snapshot.OwnerRunID)
		if err != nil {
			return nil, fmt.Errorf("background process %q owner run id: %w", snapshot.ID, err)
		}
		stepID, err := runtimeids.ParseStepID(snapshot.OwnerStepID)
		if err != nil {
			return nil, fmt.Errorf("background process %q owner step id: %w", snapshot.ID, err)
		}
		out = append(out, transcriptBackgroundActivityFromFacts(transcriptBackgroundActivityFacts{
			activityID:  activityID,
			processID:   clientui.ProcessID(strings.TrimSpace(snapshot.ID)),
			ownerRunID:  runID,
			ownerStepID: stepID,
			lifecycle:   clientui.BackgroundLifecycleBackgrounded,
			command:     snapshot.Command,
			workdir:     snapshot.Workdir,
			logPath:     textutil.OptionalTrimmedString(snapshot.LogPath),
		}))
	}
	return out, nil
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
		if err := fact.Locator.Validate(); err != nil {
			panic(fmt.Sprintf("runtime committed row lacks valid provenance: event_kind=%q fact=%+v error=%v", evt.Kind, fact, err))
		}
		row := transcriptRowFromFact(fact)
		out = append(out, clientui.NewTranscriptEvent(row))
	}
	return out
}

func transcriptRowsFromFactsChecked(facts []runtime.TranscriptCommittedRowFact) ([]clientui.TranscriptCommittedRow, error) {
	if len(facts) == 0 {
		return []clientui.TranscriptCommittedRow{}, nil
	}
	rows := make([]clientui.TranscriptCommittedRow, 0, len(facts))
	for index, fact := range facts {
		if err := fact.Locator.Validate(); err != nil {
			return nil, fmt.Errorf(
				"runtime hydrated committed row lacks valid provenance: fact_index=%d kind=%q provenance=%+v notice=%+v error=%v",
				index,
				fact.Kind,
				fact.Provenance,
				transcriptNoticeFactDiagnostic(fact.Notice),
				err,
			)
		}
		rows = append(rows, transcriptRowFromFact(fact))
	}
	return rows, nil
}

func transcriptNoticeFactDiagnostic(notice *runtime.TranscriptNoticeRowFact) any {
	if notice == nil {
		return nil
	}
	return *notice
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
		StepID:     mustRuntimeTranscriptStepID(evt.StepID, "tool abort"),
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
		QueueItemID: mustTranscriptQueueItemID(status.QueueItemID, "queued-message state"),
		Status:      clientui.QueuedUserMessageStatus(status.Status),
	}
	switch status.Status {
	case runtime.QueuedUserMessageAccepted:
		state.Text = textutil.OptionalTrimmedString(status.Text)
	case runtime.QueuedUserMessageFailed:
		reason := clientui.QueuedUserMessageFailureReason(status.FailureReason)
		state.FailureReason = &reason
		state.Text = textutil.OptionalTrimmedString(status.Text)
	case runtime.QueuedUserMessageSubmitted, runtime.QueuedUserMessageDiscarded:
	default:
		panic(fmt.Sprintf("runtime queued-message event has unknown status %q", status.Status))
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(state)}
}

func transcriptHumanInputInterruptedMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if evt.HumanInputInterrupted == nil || len(evt.HumanInputInterrupted.Items) == 0 {
		return nil
	}
	items := make([]clientui.TranscriptInterruptedHumanInputItem, 0, len(evt.HumanInputInterrupted.Items))
	for index, item := range evt.HumanInputInterrupted.Items {
		items = append(items, clientui.TranscriptInterruptedHumanInputItem{
			QueueItemID: mustTranscriptQueueItemID(item.QueueItemID, fmt.Sprintf("interrupted human input %d", index)),
			Text:        item.Text,
		})
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(
		clientui.TranscriptHumanInputInterrupted{Items: items},
	)}
}

func transcriptUserMessageFlushedMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if len(evt.UserMessageBatchQueuedItems) == 0 {
		return nil
	}
	for index, item := range evt.UserMessageBatchQueuedItems {
		mustTranscriptQueueItemID(item.QueueItemID, fmt.Sprintf("flushed queued message %d", index))
	}
	flushed := clientui.TranscriptUserMessageFlushed{
		StepID: optionalRuntimeTranscriptStepID(evt.StepID, "user-message flush"),
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(flushed)}
}

func transcriptStepStateMessages(evt runtime.Event) []clientui.TranscriptEvent {
	if evt.RunState == nil || evt.RunState.Lifecycle.Phase == runtime.RunLifecycleIdle {
		return nil
	}
	state := clientui.TranscriptStepState{
		RunID:      mustTranscriptRunID(evt.RunState.RunID, "step state"),
		StepID:     mustRuntimeTranscriptStepID(evt.StepID, "step state"),
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

func transcriptOperationalDiagnosticMessages(evt runtime.Event) []clientui.TranscriptEvent {
	diagnostic := clientui.TranscriptOperationalDiagnostic{Detail: strings.TrimSpace(evt.Error)}
	if evt.StepID != nil {
		stepID := mustRuntimeTranscriptStepID(evt.StepID, "operational diagnostic")
		diagnostic.StepID = &stepID
	}
	switch evt.Kind {
	case runtime.EventSleepGuardFailed:
		diagnostic.Code = clientui.OperationalDiagnosticSleepGuardFailed
	case runtime.EventPromptHistoryPersistFailed:
		diagnostic.Code = clientui.OperationalDiagnosticPromptHistoryPersistFailed
	case runtime.EventContextFactsPersistFailed:
		diagnostic.Code = clientui.OperationalDiagnosticContextFactsPersistFailed
	case runtime.EventInFlightClearFailed:
		diagnostic.Code = clientui.OperationalDiagnosticInFlightClearFailed
	default:
		panic(fmt.Sprintf("runtime event %q is not an operational diagnostic", evt.Kind))
	}
	return []clientui.TranscriptEvent{clientui.NewTranscriptEvent(diagnostic)}
}

func transcriptRowFromFact(fact runtime.TranscriptCommittedRowFact) clientui.TranscriptCommittedRow {
	row := clientui.TranscriptCommittedRow{
		Visibility: fact.Visibility,
		Integrity:  fact.Integrity,
		Locator:    fact.Locator,
	}
	switch fact.Kind {
	case runtime.TranscriptCommittedRowFactUser:
		if fact.User == nil {
			panic("runtime transcript user row fact is missing its user payload")
		}
		row.Kind = clientui.TranscriptRowUser
		row.User = &clientui.TranscriptUserRow{
			StepID:           optionalRuntimeTranscriptStepID(fact.StepID, "committed user row"),
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
			StepID:        mustRuntimeTranscriptStepID(fact.StepID, "committed assistant row"),
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
			StepID:        optionalRuntimeTranscriptStepID(fact.StepID, "committed tool row"),
			ToolCallID:    clientui.ToolCallID(strings.TrimSpace(fact.Tool.ToolCallID)),
			ToolName:      strings.TrimSpace(fact.Tool.ToolName),
			Text:          fact.Tool.Text,
			IsError:       fact.Tool.IsError,
			ResultSummary: optionalNonBlankString(fact.Tool.ResultSummary),
			CondensedText: optionalNonBlankString(fact.Tool.CondensedText),
			Presentation:  cloneToolCallMeta(fact.Tool.Presentation),
		}
	case runtime.TranscriptCommittedRowFactReasoningTrace:
		if fact.ReasoningTrace == nil {
			panic("runtime transcript reasoning trace row fact is missing its reasoning trace payload")
		}
		row.Kind = clientui.TranscriptRowReasoningTrace
		row.ReasoningTrace = &clientui.TranscriptReasoningTraceRow{
			StepID:              mustRuntimeTranscriptStepID(fact.StepID, "committed reasoning trace row"),
			CompactText:         fact.ReasoningTrace.CompactText,
			Text:                fact.ReasoningTrace.Text,
			DurationMs:          textutil.Pointer(fact.ReasoningTrace.DurationMs),
			ProvisionalIdentity: transcriptReasoningTraceIdentityFromRuntime(fact.ReasoningTrace.ProvisionalIdentity),
		}
	case runtime.TranscriptCommittedRowFactNotice:
		row.Kind = clientui.TranscriptRowNotice
		row.Notice = transcriptNoticeFromFact(fact.StepID, fact.Notice)
	case runtime.TranscriptCommittedRowFactReviewerFeedback:
		if fact.ReviewerFeedback == nil {
			panic("runtime transcript Reviewer feedback row fact is missing its payload")
		}
		row.Kind = clientui.TranscriptRowReviewerFeedback
		row.ReviewerFeedback = &clientui.TranscriptReviewerFeedbackRow{
			ID:              fact.ReviewerFeedback.ID,
			StepID:          mustRuntimeTranscriptStepID(fact.StepID, "committed Reviewer feedback row"),
			Suggestions:     append([]string(nil), fact.ReviewerFeedback.Suggestions...),
			SuggestionCount: fact.ReviewerFeedback.SuggestionCount,
		}
	case runtime.TranscriptCommittedRowFactReviewerError:
		if fact.ReviewerError == nil {
			panic("runtime transcript Reviewer error row fact is missing its payload")
		}
		row.Kind = clientui.TranscriptRowReviewerError
		row.ReviewerError = &clientui.TranscriptReviewerErrorRow{
			ID:     fact.ReviewerError.ID,
			StepID: mustRuntimeTranscriptStepID(fact.StepID, "committed Reviewer error row"),
			Detail: fact.ReviewerError.Detail,
		}
	default:
		panic(fmt.Sprintf("runtime transcript row fact has unknown kind %q", fact.Kind))
	}
	return row
}

func transcriptReasoningTraceIdentityFromRuntime(identity *runtime.TranscriptReasoningTraceIdentity) *clientui.TranscriptReasoningTraceIdentity {
	if identity == nil {
		return nil
	}
	projected := transcriptReasoningTraceIdentityProjection(identity, "runtime reasoning trace identity")
	return &projected
}

func transcriptReasoningTraceIdentityProjection(identity *runtime.TranscriptReasoningTraceIdentity, context string) clientui.TranscriptReasoningTraceIdentity {
	if identity == nil {
		panic(fmt.Sprintf("%s has no public identity", context))
	}
	projected := clientui.TranscriptReasoningTraceIdentity{}
	switch {
	case identity.Provider != nil:
		projected.Provider = &clientui.TranscriptProviderReasoningTraceIdentity{
			ItemID:       identity.Provider.ItemID,
			SummaryIndex: textutil.Pointer(identity.Provider.PartIndex),
		}
	case identity.Kent != nil:
		projected.Kent = identity.Kent
	default:
		panic(fmt.Sprintf("%s has no public identity", context))
	}
	return projected
}

func transcriptNoticeFromFact(stepID *string, fact *runtime.TranscriptNoticeRowFact) *clientui.TranscriptNoticeRow {
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
	if stepID != nil {
		parsed := mustRuntimeTranscriptStepID(stepID, "committed notice row")
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
	if fact.ToolOutputRepair != nil {
		notice.ToolOutputRepair = textutil.Pointer(fact.ToolOutputRepair)
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

func mustRuntimeTranscriptStepID(raw *string, owner string) runtimeids.StepID {
	if raw == nil {
		panic(fmt.Sprintf("%s is missing its step id", owner))
	}
	return mustTranscriptStepID(*raw, owner)
}

func optionalRuntimeTranscriptStepID(raw *string, owner string) *runtimeids.StepID {
	if raw == nil {
		return nil
	}
	stepID := mustTranscriptStepID(*raw, owner)
	return &stepID
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
