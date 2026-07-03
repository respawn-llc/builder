package runtime

import (
	"encoding/json"
	"errors"
	"sort"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type steeringPriority int

const (
	steeringPriorityRuntimeContext steeringPriority = iota
	steeringPriorityUser
	steeringPriorityNormal
	steeringPriorityRuntimeEvent
)

type steeringIntent struct {
	priority steeringPriority
	items    []steeringItem
}

type steeringItem struct {
	message            *steeringMessage
	committedAssistant *steeringCommittedAssistantMessage
	localEntry         *steeringLocalEntry
	historyReplace     *steeringHistoryReplacement
	toolCompletion     *tools.Result
	queuedFlush        *steeringQueuedUserMessageFlush
	event              *Event
	streaming          *steeringStreamingOutput
	cacheWarning       *steeringCacheWarning
	cacheObservation   *steeringCacheObservation
}

type steeringMessage struct {
	message     llm.Message
	eventPolicy steeringMessageEventPolicy
	persist     bool
}

type steeringLocalEntry struct {
	entry storedLocalEntry
}

type steeringCommittedAssistantMessage struct {
	message llm.Message
}

type steeringHistoryReplacement struct {
	payload          historyReplacementPayload
	projectedEntries []ChatEntry
	workflowRunID    string
}

type steeringStreamingOutput struct {
	assistantDelta *llm.AssistantDelta
	reasoningDelta *llm.ReasoningSummaryDelta
	clearState     bool
	resetEvents    bool
	abortReason    AssistantStreamAbortReason
}

type steeringCacheWarning struct {
	warning    transcript.CacheWarning
	visibility transcript.EntryVisibility
	emit       bool
}

type steeringCacheObservation struct {
	events     []session.EventInput
	warning    transcript.CacheWarning
	visibility transcript.EntryVisibility
	emit       bool
}

type steeringQueuedUserMessageFlush struct {
	text       string
	batch      []string
	queueItems []QueuedUserMessage
}

type steeringMessageEventPolicy uint8

const (
	steeringMessageEventDefault steeringMessageEventPolicy = iota
	steeringMessageEventNone
)

func steerMessagesWithPersistenceIntent(priority steeringPriority, eventPolicy steeringMessageEventPolicy, persist bool, messages []llm.Message) steeringIntent {
	items := make([]steeringItem, 0, len(messages))
	for _, message := range messages {
		msg := message
		items = append(items, steeringItem{message: &steeringMessage{
			message:     msg,
			eventPolicy: eventPolicy,
			persist:     persist,
		}})
	}
	return steeringIntent{priority: priority, items: items}
}

func steerLocalEntryIntent(entry storedLocalEntry) steeringIntent {
	copyEntry := entry
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{localEntry: &steeringLocalEntry{
			entry: copyEntry,
		}}},
	}
}

func steerHistoryReplacementIntent(engine string, mode compactionMode, workflowRunID string, compactionNumber int, pendingHandoffFutureMessage string, lastCommittedAssistantFinalAnswer string, items []llm.ResponseItem) steeringIntent {
	preparedItems := llm.PrepareOpenAIInputItems(items)
	payload := historyReplacementPayload{
		Engine:                            normalizeHistoryReplacementEngine(engine),
		Mode:                              string(mode),
		WorkflowRunID:                     workflowRunID,
		CompactionNumber:                  compactionNumber,
		PendingHandoffFutureMessage:       pendingHandoffFutureMessage,
		LastCommittedAssistantFinalAnswer: lastCommittedAssistantFinalAnswer,
		Items:                             llm.CloneResponseItems(preparedItems),
	}
	return steeringIntent{
		priority: steeringPriorityNormal,
		items: []steeringItem{{historyReplace: &steeringHistoryReplacement{
			payload:          payload,
			projectedEntries: transcriptEntriesFromHistoryReplacement(payload.Items),
			workflowRunID:    workflowRunID,
		}}},
	}
}

func steerToolCompletionIntent(result tools.Result) steeringIntent {
	copyResult := cloneToolResult(result)
	return steeringIntent{
		priority: steeringPriorityNormal,
		items:    []steeringItem{{toolCompletion: &copyResult}},
	}
}

func steerQueuedUserMessageFlushIntent(text string, batch []string, queueItems []QueuedUserMessage) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityUser,
		items: []steeringItem{{queuedFlush: &steeringQueuedUserMessageFlush{
			text:       text,
			batch:      append([]string(nil), batch...),
			queueItems: append([]QueuedUserMessage(nil), queueItems...),
		}}},
	}
}

func steerEventIntent(evt Event) steeringIntent {
	copyEvent := evt
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{event: &copyEvent}},
	}
}

func steerCommittedAssistantMessageIntent(msg llm.Message) steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{committedAssistant: &steeringCommittedAssistantMessage{
			message: msg,
		}}},
	}
}

func steerAssistantDeltaIntent(delta llm.AssistantDelta) steeringIntent {
	copyDelta := delta
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{assistantDelta: &copyDelta}}},
	}
}

func steerReasoningDeltaIntent(delta llm.ReasoningSummaryDelta) steeringIntent {
	copyDelta := delta
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{reasoningDelta: &copyDelta}}},
	}
}

func steerClearStreamingStateIntent() steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{clearState: true, resetEvents: true, abortReason: AssistantStreamAbortSuperseded}}},
	}
}

func steerClearStreamingStateStoreIntent() steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{clearState: true}}},
	}
}

func steerStreamingResetEventsIntent() steeringIntent {
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items:    []steeringItem{{streaming: &steeringStreamingOutput{resetEvents: true}}},
	}
}

func steerCacheWarningIntent(warning transcript.CacheWarning, visibility transcript.EntryVisibility, emit bool) steeringIntent {
	copyWarning := warning
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{cacheWarning: &steeringCacheWarning{
			warning:    copyWarning,
			visibility: transcript.NormalizeEntryVisibility(visibility),
			emit:       emit,
		}}},
	}
}

func steerCacheObservationIntent(events []session.EventInput, warning transcript.CacheWarning, visibility transcript.EntryVisibility, emit bool) steeringIntent {
	copyEvents := make([]session.EventInput, len(events))
	copy(copyEvents, events)
	copyWarning := warning
	return steeringIntent{
		priority: steeringPriorityRuntimeEvent,
		items: []steeringItem{{cacheObservation: &steeringCacheObservation{
			events:     copyEvents,
			warning:    copyWarning,
			visibility: transcript.NormalizeEntryVisibility(visibility),
			emit:       emit,
		}}},
	}
}

func (e *Engine) steer(stepID string, intents ...steeringIntent) error {
	if e.closed.Load() {
		return ErrEngineClosed
	}
	ordered := make([]steeringIntent, 0, len(intents))
	for _, intent := range intents {
		if len(intent.items) == 0 {
			continue
		}
		ordered = append(ordered, intent)
	}
	if len(ordered) == 0 {
		return nil
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].priority < ordered[j].priority
	})
	for _, intent := range ordered {
		for _, item := range intent.items {
			if err := e.applySteeringItem(stepID, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) applySteeringItem(stepID string, item steeringItem) error {
	if item.message != nil {
		return e.appendMessageRaw(stepID, item.message.message, item.message.eventPolicy, item.message.persist)
	}
	if item.committedAssistant != nil {
		return e.emitCommittedAssistantMessageRaw(stepID, *item.committedAssistant)
	}
	if item.localEntry != nil {
		return e.appendPersistedLocalEntryRecordRaw(stepID, item.localEntry.entry)
	}
	if item.historyReplace != nil {
		return e.replaceHistoryRaw(stepID, *item.historyReplace)
	}
	if item.toolCompletion != nil {
		if err := e.persistToolCompletionRaw(stepID, *item.toolCompletion); err != nil {
			return err
		}
		result := cloneToolResult(*item.toolCompletion)
		e.transcriptRuntimeState().CompleteLiveTool(result.CallID)
		e.emitRaw(Event{Kind: EventToolCallCompleted, StepID: stepID, ToolResult: &result, CommittedTranscriptChanged: true})
		return nil
	}
	if item.queuedFlush != nil {
		return e.appendQueuedUserMessageFlush(stepID, item.queuedFlush.text, item.queuedFlush.batch, item.queuedFlush.queueItems)
	}
	if item.event != nil {
		evt := *item.event
		if evt.StepID == "" {
			evt.StepID = stepID
		}
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil {
			if err := e.transcriptRuntimeState().RecordLiveToolStart(*evt.ToolCall); err != nil {
				return err
			}
		}
		e.emitRaw(evt)
	}
	if item.cacheWarning != nil {
		warning := item.cacheWarning.warning
		visibility := transcript.NormalizeEntryVisibility(item.cacheWarning.visibility)
		_, committed, appendErr := e.store.AppendEvent(stepID, sessionEventCacheWarning, warning)
		if appendErr != nil && !committed {
			return appendErr
		}
		newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility)
		if item.cacheWarning.emit {
			e.emitRaw(Event{Kind: EventCacheWarning, StepID: stepID, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true})
		}
		return appendErr
	}
	if item.cacheObservation != nil {
		observation := item.cacheObservation
		if e.beforePersistCacheObservation != nil {
			if err := e.beforePersistCacheObservation(observation.events); err != nil {
				return err
			}
		}
		if _, err := e.store.AppendTurnAtomic(stepID, observation.events); err != nil {
			return err
		}
		warning := observation.warning
		visibility := transcript.NormalizeEntryVisibility(observation.visibility)
		newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendCommittedEntryWithVisibility(cacheWarningTranscriptRole, transcript.CacheWarningText(warning), visibility)
		if observation.emit {
			e.emitRaw(Event{Kind: EventCacheWarning, StepID: stepID, CacheWarning: copyCacheWarning(&warning), CacheWarningVisibility: visibility, CommittedTranscriptChanged: true})
		}
		return nil
	}
	if item.streaming != nil {
		if item.streaming.assistantDelta != nil {
			delta := *item.streaming.assistantDelta
			if delta.Text == "" {
				return nil
			}
			metadata, streamID := newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).AppendStreamingDelta(stepID, delta.Text)
			e.emitRaw(Event{Kind: EventAssistantDelta, StepID: stepID, AssistantDelta: delta.Text, AssistantDeltaPhase: delta.Phase, AssistantStreamMetadata: metadata, AssistantTranscriptStreamID: streamID})
			return nil
		}
		if item.streaming.reasoningDelta != nil {
			delta := *item.streaming.reasoningDelta
			e.emitRaw(Event{Kind: EventReasoningDelta, StepID: stepID, ReasoningDelta: &delta})
			return nil
		}
		var clearedMetadata *AssistantStreamMetadata
		var clearedStreamID *uuid.UUID
		if item.streaming.clearState {
			clearedMetadata, clearedStreamID = e.clearStreamingAssistantStateRaw()
		}
		if item.streaming.resetEvents {
			e.emitStreamingAssistantResetEventsRaw(stepID, clearedMetadata, clearedStreamID, item.streaming.abortReason)
		}
		return nil
	}
	return nil
}

func (e *Engine) replaceHistoryRaw(stepID string, replacement steeringHistoryReplacement) error {
	reminderIssued := false
	preparedItems := llm.CloneResponseItems(replacement.payload.Items)
	// Compaction reinjects base meta into the same replacement payload, so a
	// non-empty replacement active list is born already carrying it. Mirror the
	// restore-time length signal here rather than scanning the items.
	e.baseMetaInjected = len(preparedItems) > 0
	_, committed, appendErr := e.store.AppendEvent(stepID, "history_replaced", replacement.payload)
	if appendErr != nil && !committed {
		return appendErr
	}
	// The committed event is the single durable record of this compaction's
	// provenance; mirror it into runtime state so an in-process gate sees it
	// without re-reading the transcript, matching what restore reconstructs.
	e.compactionRuntimeState().SetLastWorkflowRunID(replacement.workflowRunID)
	e.resetCurrentPreciseInputTracking()
	e.resetLocalDiagnostics()
	newTranscriptPersistenceCoordinator(e.transcriptRuntimeState()).ReplaceHistory(preparedItems)
	e.compactionRuntimeState().SetSoonReminderIssued(false)
	e.emitProjectedHistoryReplacementEntriesRaw(stepID, replacement.projectedEntries)
	e.emitRaw(Event{Kind: EventConversationUpdated, StepID: stepID})
	return errors.Join(
		appendErr,
		e.store.SetCompactionSoonReminderIssued(reminderIssued),
		e.store.SetUsageState(nil),
	)
}

func (e *Engine) emitProjectedHistoryReplacementEntriesRaw(stepID string, entries []ChatEntry) {
	if e == nil || len(entries) == 0 {
		return
	}
	for _, entry := range entries {
		copyEntry := clonePersistedChatEntry(entry)
		e.emitRaw(Event{
			Kind:                       EventLocalEntryAdded,
			StepID:                     stepID,
			LocalEntry:                 &copyEntry,
			CommittedTranscriptChanged: true,
		})
	}
}

func cloneToolResult(result tools.Result) tools.Result {
	copyResult := result
	copyResult.Output = append(json.RawMessage(nil), result.Output...)
	copyResult.Presentation = clonePersistedToolCallMeta(result.Presentation)
	return copyResult
}
