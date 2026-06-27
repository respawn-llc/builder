package app

import (
	"fmt"
	"testing"

	"core/shared/clientui"
)

func TestDetailTranscriptWindowMergeExpandsOverlappingPages(t *testing.T) {
	window := uiDetailTranscriptWindow{}
	replaceTestTranscriptPage(&window, 100, 3, 110)
	window.setKnownBounds(103, 110)
	window.merge(testTranscriptPage(103, 2, 110))

	if !window.loaded {
		t.Fatal("expected merged window to stay loaded")
	}
	if window.offset != 100 {
		t.Fatalf("offset = %d, want 100", window.offset)
	}
	if got := len(window.entries); got != 5 {
		t.Fatalf("entry count = %d, want 5", got)
	}
	if window.totalEntries != 110 {
		t.Fatalf("total entries = %d, want 110", window.totalEntries)
	}
	if window.entries[0].Text != "line 100" || window.entries[4].Text != "line 104" {
		t.Fatalf("unexpected merged entries: first=%q last=%q", window.entries[0].Text, window.entries[4].Text)
	}
}

func TestDetailTranscriptWindowKeepsSingleOversizeSegmentWhole(t *testing.T) {
	window := uiDetailTranscriptWindow{}
	page := testTranscriptPage(0, uiDetailTranscriptMaxEntries+200, uiDetailTranscriptMaxEntries+200)
	window.replace(page)

	if got := len(window.entries); got != uiDetailTranscriptMaxEntries+200 {
		t.Fatalf("single segment must be kept whole, got %d want %d", got, uiDetailTranscriptMaxEntries+200)
	}
	if window.offset != 0 {
		t.Fatalf("offset after single-segment replace = %d, want 0", window.offset)
	}
}

func TestDetailTranscriptWindowKeepsTwoSegmentsForSmoothSeam(t *testing.T) {
	segLen := uiDetailTranscriptMaxEntries/2 + 100
	window := uiDetailTranscriptWindow{}
	top := testTranscriptPage(0, segLen, segLen)
	top.HasMoreBelow = true
	top.NewerCursor = 111
	window.replace(top)

	newer := testTranscriptPage(segLen, segLen, 2*segLen)
	newer.OlderCursor = 111
	newer.HasMoreAbove = true
	newer.NewerCursor = 0
	window.appendCursorPage(newer)

	if got := len(window.segments); got != 2 {
		t.Fatalf("two adjacent segments must both stay resident for a smooth seam, got %d segments", got)
	}
	if got := len(window.entries); got != 2*segLen {
		t.Fatalf("two-segment window over the entry budget must be retained whole, got %d want %d", got, 2*segLen)
	}
}

func TestDetailTranscriptWindowAppendThirdSegmentDropsFarTop(t *testing.T) {
	segLen := uiDetailTranscriptMaxEntries/2 + 100
	window := uiDetailTranscriptWindow{}
	a := testTranscriptPage(0, segLen, segLen)
	a.HasMoreBelow = true
	a.NewerCursor = 111
	window.replace(a)

	b := testTranscriptPage(segLen, segLen, 2*segLen)
	b.OlderCursor = 111
	b.HasMoreAbove = true
	b.NewerCursor = 222
	b.HasMoreBelow = true
	window.appendCursorPage(b)

	c := testTranscriptPage(2*segLen, segLen, 3*segLen)
	c.OlderCursor = 222
	c.HasMoreAbove = true
	c.NewerCursor = 0
	c.HasMoreBelow = false
	window.appendCursorPage(c)

	if got := len(window.segments); got != 2 {
		t.Fatalf("third appended segment must trim back to two resident segments, got %d", got)
	}
	if got := len(window.entries); got != 2*segLen {
		t.Fatalf("trimmed window entries = %d, want %d (B+C)", got, 2*segLen)
	}
	if window.entries[0].Text != fmt.Sprintf("line %03d", segLen) {
		t.Fatalf("expected far top segment dropped, got first %q", window.entries[0].Text)
	}
	if !window.hasMoreAbove || window.olderCursor != 111 {
		t.Fatalf("after dropping top, expected hasMoreAbove cursor 111, got above=%t cursor=%d", window.hasMoreAbove, window.olderCursor)
	}
	if window.hasMoreBelow {
		t.Fatal("newest resident segment must report no more below")
	}
}

func TestDetailTranscriptWindowPageRequests(t *testing.T) {
	window := uiDetailTranscriptWindow{}
	page := testTranscriptPage(250, 250, 800)
	page.OlderCursor = 4096
	page.HasMoreAbove = true
	page.NewerCursor = 9001
	page.HasMoreBelow = true
	window.setKnownBounds(250, 800)
	window.replace(page)

	before, ok := window.pageBefore()
	if !ok {
		t.Fatal("expected pageBefore request")
	}
	if want := (clientui.TranscriptPageRequest{Cursor: 4096}); !pageRequestEqual(before, want) {
		t.Fatalf("pageBefore = %+v, want %+v", before, want)
	}

	after, ok := window.pageAfter()
	if !ok {
		t.Fatal("expected pageAfter request")
	}
	if want := (clientui.TranscriptPageRequest{NewerCursor: 9001}); !pageRequestEqual(after, want) {
		t.Fatalf("pageAfter = %+v, want %+v", after, want)
	}

	requested := window.requestedPageForDetailEntry()
	if want := (clientui.TranscriptPageRequest{}); !pageRequestEqual(requested, want) {
		t.Fatalf("requestedPageForDetailEntry = %+v, want %+v", requested, want)
	}
	window.reset()
	if requested := window.requestedPageForDetailEntry(); !pageRequestEqual(requested, (clientui.TranscriptPageRequest{})) {
		t.Fatalf("default requested page = %+v", requested)
	}
}

func TestDetailTranscriptWindowPrependCursorPageGrowsUpward(t *testing.T) {
	window := uiDetailTranscriptWindow{}
	tail := testTranscriptPage(250, 250, 500)
	tail.OlderCursor = 4096
	tail.HasMoreAbove = true
	window.replace(tail)

	older := testTranscriptPage(0, 250, 500)
	older.OlderCursor = 0
	older.HasMoreAbove = false
	window.prependCursorPage(older)

	if window.offset != 0 {
		t.Fatalf("offset after prepend = %d, want 0", window.offset)
	}
	if got := len(window.entries); got != 500 {
		t.Fatalf("entry count after prepend = %d, want 500", got)
	}
	if window.entries[0].Text != "line 000" || window.entries[499].Text != "line 499" {
		t.Fatalf("unexpected entries after prepend: first=%q last=%q", window.entries[0].Text, window.entries[499].Text)
	}
	if window.hasMoreAbove {
		t.Fatal("expected hasMoreAbove cleared after reaching oldest segment")
	}
	if _, ok := window.pageBefore(); ok {
		t.Fatal("expected no further pageBefore at top of transcript")
	}
}

func TestDetailTranscriptWindowPrependThirdSegmentDropsFarBottom(t *testing.T) {
	segLen := uiDetailTranscriptMaxEntries/2 + 100
	window := uiDetailTranscriptWindow{}
	c := testTranscriptPage(2*segLen, segLen, 3*segLen)
	c.OlderCursor = 222
	c.HasMoreAbove = true
	c.NewerCursor = 0
	window.replace(c)

	b := testTranscriptPage(segLen, segLen, 3*segLen)
	b.OlderCursor = 111
	b.HasMoreAbove = true
	b.NewerCursor = 222
	b.HasMoreBelow = true
	window.prependCursorPage(b)

	a := testTranscriptPage(0, segLen, 3*segLen)
	a.OlderCursor = 0
	a.HasMoreAbove = false
	a.NewerCursor = 111
	a.HasMoreBelow = true
	window.prependCursorPage(a)

	if got := len(window.segments); got != 2 {
		t.Fatalf("third prepended segment must trim back to two resident segments, got %d", got)
	}
	if got := len(window.entries); got != 2*segLen {
		t.Fatalf("trimmed window entries = %d, want %d (A+B)", got, 2*segLen)
	}
	if window.offset != 0 {
		t.Fatalf("offset after prepend = %d, want 0", window.offset)
	}
	if window.entries[0].Text != "line 000" {
		t.Fatalf("expected top of oldest prepended segment retained, got %q", window.entries[0].Text)
	}
	if window.hasMoreAbove || window.olderCursor != 0 {
		t.Fatalf("oldest resident segment must report no more above, got above=%t cursor=%d", window.hasMoreAbove, window.olderCursor)
	}
	if !window.hasMoreBelow || window.newerCursor != 222 {
		t.Fatalf("after dropping far bottom, expected hasMoreBelow cursor 222, got below=%t cursor=%d", window.hasMoreBelow, window.newerCursor)
	}
}

func TestDetailTranscriptWindowResetClearsState(t *testing.T) {
	window := uiDetailTranscriptWindow{}
	window.replace(testTranscriptPage(10, 5, 25))
	window.reset()

	if window.loaded {
		t.Fatal("expected reset window to be unloaded")
	}
	if window.sessionID != "" || window.offset != 0 || window.totalEntries != 0 {
		t.Fatalf("expected zeroed window metadata after reset, got %+v", window)
	}
	if len(window.entries) != 0 {
		t.Fatalf("expected no entries after reset, got %d", len(window.entries))
	}
	if window.lastRequest != (clientui.TranscriptPageRequest{}) {
		t.Fatalf("expected zero lastRequest after reset, got %+v", window.lastRequest)
	}
}

func replaceTestTranscriptPage(window *uiDetailTranscriptWindow, offset, count, total int) {
	window.setKnownBounds(offset, total)
	window.replace(testTranscriptPage(offset, count, total))
}

func testTranscriptPage(offset, count, total int) clientui.TranscriptPage {
	page := clientui.TranscriptPage{
		SessionID:    "session-1",
		HasMoreAbove: offset > 0,
		HasMoreBelow: offset+count < total,
	}
	for i := 0; i < count; i++ {
		index := offset + i
		page.Entries = append(page.Entries, clientui.ChatEntry{Role: "assistant", Text: fmt.Sprintf("line %03d", index)})
	}
	return page
}
