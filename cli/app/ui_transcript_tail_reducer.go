package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
)

type deferredCommittedTailState struct {
	tails            []deferredProjectedTranscriptTail
	committedEntries []tui.TranscriptEntry
	baseOffset       int
	revision         int64
	totalEntries     int
}

type deferredCommittedTailSnapshot struct {
	tails            []deferredProjectedTranscriptTail
	committedEntries []tui.TranscriptEntry
	baseOffset       int
	revision         int64
	totalEntries     int
}

type deferredCommittedTailDeferReduction struct {
	shouldDefer       bool
	tail              deferredProjectedTranscriptTail
	revisionAfter     int64
	totalEntriesAfter int
}

type deferredCommittedTailMergeReduction struct {
	merged        bool
	event         clientui.Event
	remaining     []deferredProjectedTranscriptTail
	consumedTails int
	mergedStart   int
	mergedCount   int
}

func newDeferredCommittedTailState(snapshot deferredCommittedTailSnapshot) deferredCommittedTailState {
	return deferredCommittedTailState{
		tails:            append([]deferredProjectedTranscriptTail(nil), snapshot.tails...),
		committedEntries: append([]tui.TranscriptEntry(nil), snapshot.committedEntries...),
		baseOffset:       snapshot.baseOffset,
		revision:         snapshot.revision,
		totalEntries:     snapshot.totalEntries,
	}
}

func reduceDeferredCommittedTailDefer(state deferredCommittedTailState, evt clientui.Event) deferredCommittedTailDeferReduction {
	if len(evt.TranscriptEntries) == 0 {
		return deferredCommittedTailDeferReduction{}
	}
	start, end, ok := projectedTranscriptEventRange(evt, len(evt.TranscriptEntries))
	if !ok {
		start = state.baseOffset + len(state.committedEntries)
		for _, tail := range state.tails {
			if tail.rangeStart != start {
				break
			}
			start = tail.rangeEnd
		}
		end = start + len(evt.TranscriptEntries)
	}
	pendingBatch := deferredPendingInjectedBatchFromEvent(evt)
	revisionAfter := state.revision
	if evt.TranscriptRevision > revisionAfter {
		revisionAfter = evt.TranscriptRevision
	}
	totalEntriesAfter := state.totalEntries
	if end > totalEntriesAfter {
		totalEntriesAfter = end
	}
	if evt.CommittedEntryCount > totalEntriesAfter {
		totalEntriesAfter = evt.CommittedEntryCount
	}
	return deferredCommittedTailDeferReduction{
		shouldDefer: true,
		tail: deferredProjectedTranscriptTail{
			rangeStart: start,
			rangeEnd:   end,
			revision:   evt.TranscriptRevision,
			entries:    cloneChatEntries(evt.TranscriptEntries),
			pending:    pendingBatch,
		},
		revisionAfter:     revisionAfter,
		totalEntriesAfter: totalEntriesAfter,
	}
}

func reduceDeferredCommittedTailMerge(state deferredCommittedTailState, evt clientui.Event) deferredCommittedTailMergeReduction {
	if len(state.tails) == 0 || len(evt.TranscriptEntries) == 0 || !evt.CommittedTranscriptChanged {
		return deferredCommittedTailMergeReduction{event: evt, remaining: append([]deferredProjectedTranscriptTail(nil), state.tails...)}
	}
	eventStart, eventEnd, ok := projectedTranscriptEventRange(evt, len(evt.TranscriptEntries))
	if !ok {
		return deferredCommittedTailMergeReduction{event: evt, remaining: append([]deferredProjectedTranscriptTail(nil), state.tails...)}
	}
	currentEnd := state.baseOffset + len(state.committedEntries)
	mergedEntries := make([]clientui.ChatEntry, 0, len(evt.TranscriptEntries)+len(state.tails))
	mergedStart := currentEnd
	used := 0
	chainEnd := currentEnd
	for _, deferred := range state.tails {
		if deferred.rangeStart != chainEnd || deferred.rangeEnd > eventStart {
			break
		}
		mergedEntries = append(mergedEntries, cloneChatEntries(deferred.entries)...)
		chainEnd = deferred.rangeEnd
		used++
	}
	if eventStart != chainEnd {
		return deferredCommittedTailMergeReduction{event: evt, remaining: append([]deferredProjectedTranscriptTail(nil), state.tails...)}
	}
	mergedEntries = append(mergedEntries, cloneChatEntries(evt.TranscriptEntries)...)
	chainEnd = eventEnd
	for _, deferred := range state.tails[used:] {
		if deferred.rangeStart != chainEnd || evt.CommittedEntryCount < deferred.rangeEnd {
			break
		}
		mergedEntries = append(mergedEntries, cloneChatEntries(deferred.entries)...)
		chainEnd = deferred.rangeEnd
		used++
	}
	if used == 0 {
		return deferredCommittedTailMergeReduction{event: evt, remaining: append([]deferredProjectedTranscriptTail(nil), state.tails...)}
	}
	evt.TranscriptEntries = mergedEntries
	evt.CommittedEntryStart = mergedStart
	evt.CommittedEntryStartSet = true
	return deferredCommittedTailMergeReduction{
		merged:        true,
		event:         evt,
		remaining:     append([]deferredProjectedTranscriptTail(nil), state.tails[used:]...),
		consumedTails: used,
		mergedStart:   mergedStart,
		mergedCount:   len(mergedEntries),
	}
}

func deferredPendingInjectedBatchFromEvent(evt clientui.Event) []string {
	if evt.Kind != clientui.EventUserMessageFlushed {
		return nil
	}
	batch := append([]string(nil), evt.UserMessageBatch...)
	if len(batch) == 0 {
		trimmed := strings.TrimSpace(evt.UserMessage)
		if trimmed == "" {
			return nil
		}
		batch = []string{trimmed}
	}
	for idx := range batch {
		batch[idx] = strings.TrimSpace(batch[idx])
	}
	return batch
}
