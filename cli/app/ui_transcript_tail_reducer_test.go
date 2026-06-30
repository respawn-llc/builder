package app

import (
	"testing"

	"core/cli/tui"
	"core/shared/clientui"
)

func TestReduceDeferredCommittedTailDeferDerivesRangeAndPendingBatch(t *testing.T) {
	reduction := reduceDeferredCommittedTailDefer(deferredCommittedTailState{
		committedEntries: []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		baseOffset:       5,
		revision:         6,
		totalEntries:     6,
	}, clientui.Event{
		Kind:               clientui.EventUserMessageFlushed,
		TranscriptRevision: 7,
		UserMessageBatch:   []string{" first ", "second"},
		TranscriptEntries:  []clientui.ChatEntry{{Role: "user", Text: "first"}, {Role: "user", Text: "second"}},
	})

	if !reduction.shouldDefer {
		t.Fatal("expected defer reduction")
	}
	if reduction.tail.rangeStart != 6 || reduction.tail.rangeEnd != 8 {
		t.Fatalf("range = [%d,%d], want [6,8]", reduction.tail.rangeStart, reduction.tail.rangeEnd)
	}
	if reduction.revisionAfter != 7 || reduction.totalEntriesAfter != 8 {
		t.Fatalf("after = revision:%d total:%d, want revision:7 total:8", reduction.revisionAfter, reduction.totalEntriesAfter)
	}
	if got := reduction.tail.pending; len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("pending = %+v, want trimmed batch", got)
	}
}

func TestReduceDeferredCommittedTailDeferChainsFallbackRangeAfterPendingTails(t *testing.T) {
	reduction := reduceDeferredCommittedTailDefer(deferredCommittedTailState{
		committedEntries: []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		baseOffset:       0,
		revision:         6,
		totalEntries:     2,
		tails: []deferredProjectedTranscriptTail{
			{rangeStart: 1, rangeEnd: 2, revision: 7, entries: []clientui.ChatEntry{{Role: "user", Text: "queued one"}}},
		},
	}, clientui.Event{
		Kind:               clientui.EventUserMessageFlushed,
		TranscriptRevision: 8,
		TranscriptEntries:  []clientui.ChatEntry{{Role: "user", Text: "queued two"}},
	})

	if !reduction.shouldDefer {
		t.Fatal("expected defer reduction")
	}
	if reduction.tail.rangeStart != 2 || reduction.tail.rangeEnd != 3 {
		t.Fatalf("range = [%d,%d], want [2,3]", reduction.tail.rangeStart, reduction.tail.rangeEnd)
	}
	if reduction.totalEntriesAfter != 3 {
		t.Fatalf("totalEntriesAfter = %d, want 3", reduction.totalEntriesAfter)
	}
}

func TestReduceDeferredCommittedTailMergeConsumesContiguousChain(t *testing.T) {
	state := deferredCommittedTailState{
		committedEntries: []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		baseOffset:       0,
		tails: []deferredProjectedTranscriptTail{
			{rangeStart: 1, rangeEnd: 2, revision: 7, entries: []clientui.ChatEntry{{Role: "user", Text: "queued one"}}},
			{rangeStart: 2, rangeEnd: 3, revision: 8, entries: []clientui.ChatEntry{{Role: "user", Text: "queued two"}}},
			{rangeStart: 4, rangeEnd: 5, revision: 9, entries: []clientui.ChatEntry{{Role: "user", Text: "gap"}}},
		},
	}
	reduction := reduceDeferredCommittedTailMerge(state, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "answer"}},
	})

	if !reduction.merged {
		t.Fatalf("expected merge, got %+v", reduction)
	}
	if reduction.consumedTails != 2 || len(reduction.remaining) != 1 {
		t.Fatalf("consumed/remaining = %d/%d, want 2/1", reduction.consumedTails, len(reduction.remaining))
	}
	if reduction.event.CommittedEntryStart != 1 || !reduction.event.CommittedEntryStartSet {
		t.Fatalf("merged start = %d set:%t, want 1 true", reduction.event.CommittedEntryStart, reduction.event.CommittedEntryStartSet)
	}
	text := make([]string, 0, len(reduction.event.TranscriptEntries))
	for _, entry := range reduction.event.TranscriptEntries {
		text = append(text, entry.Text)
	}
	if len(text) != 3 || text[0] != "queued one" || text[1] != "queued two" || text[2] != "answer" {
		t.Fatalf("merged text = %+v", text)
	}
}

func TestReduceDeferredCommittedTailMergeConsumesCoveredPostEventTail(t *testing.T) {
	state := deferredCommittedTailState{
		committedEntries: []tui.TranscriptEntry{
			{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true},
			{Role: tui.TranscriptRoleAssistant, Text: "previous", Committed: true},
			{Role: tui.TranscriptRoleUser, Text: "run task", Committed: true},
			{Role: tui.TranscriptRoleAssistant, Text: "draft", Committed: true},
		},
		baseOffset: 0,
		tails: []deferredProjectedTranscriptTail{
			{rangeStart: 5, rangeEnd: 6, revision: 8, entries: []clientui.ChatEntry{{Role: "user", Text: "queued follow-up"}}},
		},
	}
	reduction := reduceDeferredCommittedTailMerge(state, clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryCount:        6,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final answer"}},
	})

	if !reduction.merged {
		t.Fatalf("expected post-event tail merge, got %+v", reduction)
	}
	if reduction.consumedTails != 1 || len(reduction.remaining) != 0 {
		t.Fatalf("consumed/remaining = %d/%d, want 1/0", reduction.consumedTails, len(reduction.remaining))
	}
	if reduction.event.CommittedEntryStart != 4 || !reduction.event.CommittedEntryStartSet {
		t.Fatalf("merged start = %d set:%t, want 4 true", reduction.event.CommittedEntryStart, reduction.event.CommittedEntryStartSet)
	}
	text := make([]string, 0, len(reduction.event.TranscriptEntries))
	for _, entry := range reduction.event.TranscriptEntries {
		text = append(text, entry.Text)
	}
	if len(text) != 2 || text[0] != "final answer" || text[1] != "queued follow-up" {
		t.Fatalf("merged text = %+v", text)
	}
}

func TestReduceDeferredCommittedTailMergeRejectsChainGap(t *testing.T) {
	evt := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		CommittedTranscriptChanged: true,
		CommittedEntryStart:        2,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "answer"}},
	}
	reduction := reduceDeferredCommittedTailMerge(deferredCommittedTailState{
		committedEntries: []tui.TranscriptEntry{{Role: tui.TranscriptRoleAssistant, Text: "seed", Committed: true}},
		baseOffset:       0,
		tails:            []deferredProjectedTranscriptTail{{rangeStart: 3, rangeEnd: 4, revision: 7, entries: []clientui.ChatEntry{{Role: "user", Text: "gap"}}}},
	}, evt)

	if reduction.merged {
		t.Fatalf("expected no merge, got %+v", reduction)
	}
	if len(reduction.remaining) != 1 {
		t.Fatalf("remaining tails = %d, want 1", len(reduction.remaining))
	}
	if len(reduction.event.TranscriptEntries) != 1 || reduction.event.TranscriptEntries[0].Text != "answer" {
		t.Fatalf("event changed on no-merge: %+v", reduction.event.TranscriptEntries)
	}
}

func TestDeferredCommittedTailFinalizerFlushRequiresMatchingKnownStepID(t *testing.T) {
	state := deferredCommittedTailState{
		committedEntries: []tui.TranscriptEntry{{Role: tui.TranscriptRoleUser, Text: "prompt", Committed: true}},
		baseOffset:       0,
		revision:         1,
		totalEntries:     2,
		tails: []deferredProjectedTranscriptTail{{
			rangeStart: 1,
			rangeEnd:   2,
			revision:   2,
			stepID:     "step-b",
			entries:    []clientui.ChatEntry{{Role: "assistant", Text: "Done"}},
		}},
	}

	if _, _, ok := deferredCommittedTailFinalizerFlushEvent(state, "Done", "step-a"); ok {
		t.Fatal("mismatched known deferred finalizer step was flushed by text alone")
	}
	flush, remaining, ok := deferredCommittedTailFinalizerFlushEvent(state, "Done", "step-b")
	if !ok {
		t.Fatal("matching known deferred finalizer step did not flush")
	}
	if flush.StepID != "step-b" || len(flush.TranscriptEntries) != 1 {
		t.Fatalf("flush event = %+v, want matching step finalizer entry", flush)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining tails = %d, want none", len(remaining))
	}
}
