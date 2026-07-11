package session

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func matchEventKind(kind string) func(Event) bool {
	return func(evt Event) bool { return evt.Kind == kind }
}

func eventKinds(events []Event) []string {
	kinds := make([]string, 0, len(events))
	for _, evt := range events {
		kinds = append(kinds, evt.Kind)
	}
	return kinds
}

func TestReadEventsBackwardUntilReturnsActiveTail(t *testing.T) {
	store := newSessionTestStore(t)
	for _, e := range []struct{ kind, body string }{
		{"message", "a"},
		{"message", "b"},
		{"history_replaced", "hr"},
		{"message", "c"},
		{"message", "d"},
	} {
		if _, _, err := store.AppendEvent("step", e.kind, map[string]string{"v": e.body}); err != nil {
			t.Fatalf("append %s: %v", e.kind, err)
		}
	}

	events, err := store.ReadEventsBackwardUntil(matchEventKind("history_replaced"))
	if err != nil {
		t.Fatalf("read backward: %v", err)
	}
	if got, want := eventKinds(events), []string{"history_replaced", "message", "message"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active tail kinds = %v, want %v", got, want)
	}
}

func TestReadEventsBackwardUntilNoMatchReturnsAllEvents(t *testing.T) {
	store := newSessionTestStore(t)
	for i := 0; i < 3; i++ {
		if _, _, err := store.AppendEvent("step", "message", map[string]int{"v": i}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	events, err := store.ReadEventsBackwardUntil(matchEventKind("history_replaced"))
	if err != nil {
		t.Fatalf("read backward: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("no-match read returned %d events, want all 3", len(events))
	}
}

func TestReadSegmentBackwardPaginatesSegmentsViaCursor(t *testing.T) {
	const segments = 4
	const perSegment = 5
	var sb strings.Builder
	seq := 0
	for s := 0; s < segments; s++ {
		seq++
		if s > 0 {
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"history_replaced","payload":{"v":%d}}`+"\n", seq, seq))
		} else {
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"message","payload":{"v":%d}}`+"\n", seq, seq))
		}
		for i := 1; i < perSegment; i++ {
			seq++
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"message","payload":{"v":%d}}`+"\n", seq, seq))
		}
	}
	sb.WriteString(`{"seq":99,"kind":"message","pa`)
	total := segments * perSegment
	path := filepath.Join(t.TempDir(), eventsFile)
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write events file: %v", err)
	}

	for _, chunk := range []int64{1, 5, 13, 1 << 20} {
		var collected []Event
		pages := 0
		window, err := readNewestSegmentBackwardFile(path, chunk, matchEventKind("history_replaced"))
		if err != nil {
			t.Fatalf("chunk=%d: read newest segment: %v", chunk, err)
		}
		for {
			pages++
			if pages > segments+1 {
				t.Fatalf("chunk=%d: pagination did not terminate", chunk)
			}
			if len(window.Events) != perSegment {
				t.Fatalf("chunk=%d: segment returned %d events, want %d", chunk, len(window.Events), perSegment)
			}
			collected = append(append([]Event(nil), window.Events...), collected...)
			if window.ReachedStart {
				break
			}
			window, err = readSegmentBackwardFile(path, window.StartOffset, chunk, matchEventKind("history_replaced"))
			if err != nil {
				t.Fatalf("chunk=%d: read older segment: %v", chunk, err)
			}
		}
		if pages != segments {
			t.Fatalf("chunk=%d: paginated %d segments, want %d", chunk, pages, segments)
		}
		if len(collected) != total {
			t.Fatalf("chunk=%d: reconstructed %d events, want %d (torn tail must be dropped)", chunk, len(collected), total)
		}
		for i := range collected {
			if collected[i].Seq != int64(i+1) {
				t.Fatalf("chunk=%d: event %d seq = %d, want %d", chunk, i, collected[i].Seq, i+1)
			}
		}
	}
}

func TestReadSegmentForwardPaginatesSegmentsViaCursor(t *testing.T) {
	const segments = 4
	const perSegment = 5
	var sb strings.Builder
	seq := 0
	for s := 0; s < segments; s++ {
		seq++
		if s > 0 {
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"history_replaced","payload":{"v":%d}}`+"\n", seq, seq))
		} else {
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"message","payload":{"v":%d}}`+"\n", seq, seq))
		}
		for i := 1; i < perSegment; i++ {
			seq++
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"message","payload":{"v":%d}}`+"\n", seq, seq))
		}
	}
	sb.WriteString(`{"seq":99,"kind":"message","pa`)
	total := segments * perSegment
	path := filepath.Join(t.TempDir(), eventsFile)
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write events file: %v", err)
	}

	for _, chunk := range []int64{1, 5, 13, 1 << 20} {
		var collected []Event
		startOffset := int64(0)
		pages := 0
		for {
			pages++
			if pages > segments+1 {
				t.Fatalf("chunk=%d: forward pagination did not terminate", chunk)
			}
			window, err := readSegmentForwardFile(path, startOffset, chunk, matchEventKind("history_replaced"))
			if err != nil {
				t.Fatalf("chunk=%d: read forward: %v", chunk, err)
			}
			if len(window.Events) != perSegment {
				t.Fatalf("chunk=%d: forward segment returned %d events, want %d", chunk, len(window.Events), perSegment)
			}
			collected = append(collected, window.Events...)
			if window.ReachedEnd {
				break
			}
			startOffset = window.EndOffset
		}
		if pages != segments {
			t.Fatalf("chunk=%d: forward-paginated %d segments, want %d", chunk, pages, segments)
		}
		if len(collected) != total {
			t.Fatalf("chunk=%d: reconstructed %d events, want %d (torn tail must be dropped)", chunk, len(collected), total)
		}
		for i := range collected {
			if collected[i].Seq != int64(i+1) {
				t.Fatalf("chunk=%d: event %d seq = %d, want %d", chunk, i, collected[i].Seq, i+1)
			}
		}
	}
}

func TestReadSegmentForwardAndBackwardAgreeOnBoundaries(t *testing.T) {
	const segments = 4
	const perSegment = 5
	var sb strings.Builder
	seq := 0
	for s := 0; s < segments; s++ {
		seq++
		if s > 0 {
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"history_replaced","payload":{"v":%d}}`+"\n", seq, seq))
		} else {
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"message","payload":{"v":%d}}`+"\n", seq, seq))
		}
		for i := 1; i < perSegment; i++ {
			seq++
			sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":"message","payload":{"v":%d}}`+"\n", seq, seq))
		}
	}
	path := filepath.Join(t.TempDir(), eventsFile)
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write events file: %v", err)
	}

	tail, err := readNewestSegmentBackwardFile(path, 1<<20, matchEventKind("history_replaced"))
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if !tail.ReachedEnd {
		t.Fatalf("tail segment must report ReachedEnd")
	}
	older, err := readSegmentBackwardFile(path, tail.StartOffset, 1<<20, matchEventKind("history_replaced"))
	if err != nil {
		t.Fatalf("read older: %v", err)
	}
	if older.ReachedEnd {
		t.Fatalf("non-tail segment must not report ReachedEnd")
	}
	if older.EndOffset != tail.StartOffset {
		t.Fatalf("older segment end %d must equal tail start %d", older.EndOffset, tail.StartOffset)
	}
	forward, err := readSegmentForwardFile(path, older.EndOffset, 1<<20, matchEventKind("history_replaced"))
	if err != nil {
		t.Fatalf("read forward from older end: %v", err)
	}
	if len(forward.Events) != len(tail.Events) {
		t.Fatalf("forward-from-older-end returned %d events, want tail %d", len(forward.Events), len(tail.Events))
	}
	for i := range forward.Events {
		if forward.Events[i].Seq != tail.Events[i].Seq {
			t.Fatalf("forward event %d seq=%d, want tail seq=%d", i, forward.Events[i].Seq, tail.Events[i].Seq)
		}
	}
	if !forward.ReachedEnd {
		t.Fatalf("forward read into the newest segment must report ReachedEnd")
	}
}

func TestReadRecentEventsCrossesBoundariesAndReassemblesAcrossChunks(t *testing.T) {
	var sb strings.Builder
	const total = 30
	for seq := 1; seq <= total; seq++ {
		kind := "message"
		if seq%7 == 0 {
			kind = "history_replaced"
		}
		sb.WriteString(fmt.Sprintf(`{"seq":%d,"kind":%q,"payload":{"v":%d}}`+"\n", seq, kind, seq))
	}
	sb.WriteString(`{"seq":99,"kind":"message","pa`)
	path := filepath.Join(t.TempDir(), eventsFile)
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write events file: %v", err)
	}

	for _, chunk := range []int64{1, 5, 17, 1 << 20} {
		window, err := readRecentEventsBackwardFile(path, 0, 8, chunk)
		if err != nil {
			t.Fatalf("chunk=%d: read recent: %v", chunk, err)
		}
		if len(window.Events) != 8 {
			t.Fatalf("chunk=%d: recent window returned %d events, want 8 (torn tail dropped)", chunk, len(window.Events))
		}
		if window.ReachedStart {
			t.Fatalf("chunk=%d: bounded recent window must report more above", chunk)
		}
		for i := range window.Events {
			wantSeq := int64(total - 8 + 1 + i)
			if window.Events[i].Seq != wantSeq {
				t.Fatalf("chunk=%d: recent event %d seq = %d, want %d", chunk, i, window.Events[i].Seq, wantSeq)
			}
		}
	}

	all, err := readRecentEventsBackwardFile(path, 0, total+5, 7)
	if err != nil {
		t.Fatalf("read recent (all): %v", err)
	}
	if len(all.Events) != total || !all.ReachedStart {
		t.Fatalf("recent window over-large limit = %d events reachedStart=%v, want %d reachedStart=true", len(all.Events), all.ReachedStart, total)
	}
}

func TestReadSegmentBackwardReassemblesAcrossChunksAndToleratesTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	content := `{"seq":1,"kind":"message","payload":{"v":"a"}}
{"seq":2,"kind":"history_replaced","payload":{"v":"hr"}}
{"seq":3,"kind":"message","payload":{"v":"c"}}
{"seq":4,"kind":"message","payload":{"v":"d"}}
{"seq":5,"kind":"message","pa`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write events file: %v", err)
	}

	for _, chunk := range []int64{1, 3, 7, 64, 1 << 20} {
		window, err := readNewestSegmentBackwardFile(path, chunk, matchEventKind("history_replaced"))
		if err != nil {
			t.Fatalf("read backward (chunk=%d): %v", chunk, err)
		}
		if got, want := eventKinds(window.Events), []string{"history_replaced", "message", "message"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("chunk=%d active tail kinds = %v, want %v (torn trailing line must be dropped)", chunk, got, want)
		}
		if window.ReachedStart {
			t.Fatalf("chunk=%d: segment beginning at a history_replaced must report more above", chunk)
		}
	}
}
