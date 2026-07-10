package analyzer_test

import (
	"reflect"
	"testing"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
)

func TestStreamAndCaptureReplayShareTerminalInterpretation(t *testing.T) {
	t.Parallel()

	dimensions := pty.MustDimensions(3, 8)
	beforeResize := []byte("\x1b[?1049hhello\x1b[2J")
	afterResize := []byte("\x1b[?25hworld")

	stream, err := analyzer.NewStream(dimensions)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	for _, fragment := range [][]byte{beforeResize[:3], beforeResize[3:]} {
		if err := stream.FeedChunk(pty.NewChunk(0, 0, fragment)); err != nil {
			t.Fatalf("Feed: %v", err)
		}
	}
	if err := stream.Resize(pty.MustDimensions(4, 8)); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := stream.FeedChunk(pty.NewChunk(1, 0, afterResize)); err != nil {
		t.Fatalf("Feed after resize: %v", err)
	}
	live, err := stream.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	assembler, err := analyzer.NewCaptureAssembler(dimensions)
	if err != nil {
		t.Fatalf("NewCaptureAssembler: %v", err)
	}
	if err := assembler.Append(beforeResize); err != nil {
		t.Fatalf("Append before resize: %v", err)
	}
	if err := assembler.Resize(pty.MustDimensions(4, 8)); err != nil {
		t.Fatalf("Capture resize: %v", err)
	}
	if err := assembler.Append(afterResize); err != nil {
		t.Fatalf("Append after resize: %v", err)
	}
	capture, err := assembler.Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	replayed, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if !reflect.DeepEqual(live.Screen, replayed.Screen) {
		t.Fatalf("screen differs: live=%#v replay=%#v", live.Screen, replayed.Screen)
	}
	if !reflect.DeepEqual(live.PrivateModeChanges, replayed.PrivateModeChanges) {
		t.Fatalf("private modes differ: live=%#v replay=%#v", live.PrivateModeChanges, replayed.PrivateModeChanges)
	}
	if got, want := operationView(live), operationView(replayed); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations differ: live=%#v replay=%#v", got, want)
	}
}

func TestSnapshotDoesNotMutatePendingWriteBatch(t *testing.T) {
	t.Parallel()

	stream, err := analyzer.NewStream(pty.MustDimensions(2, 8))
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if err := stream.Feed([]byte("ab")); err != nil {
		t.Fatalf("Feed first fragment: %v", err)
	}
	before, err := stream.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot before: %v", err)
	}
	if err := stream.Feed([]byte("cd")); err != nil {
		t.Fatalf("Feed second fragment: %v", err)
	}
	after, err := stream.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after: %v", err)
	}
	finished, err := stream.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if got := operationView(before); len(got) != 1 || got[0].Write != "ab" {
		t.Fatalf("before snapshot operations = %#v, want write ab", got)
	}
	if got, want := operationView(after), operationView(finished); !reflect.DeepEqual(got, want) {
		t.Fatalf("post-feed snapshot differs from finish: got=%#v want=%#v", got, want)
	}
	if len(finished.Operations) != 1 || finished.Operations[0].Write == nil || finished.Operations[0].Write.Text() != "abcd" {
		t.Fatalf("finished operations = %#v, want one write abcd", finished.Operations)
	}
}

func TestLiveSnapshotsAreInvariantToReadFragmentation(t *testing.T) {
	t.Parallel()

	payload := []byte("\x1b[?1049hhello\x1b[2J\x1b[?25hworld")
	analyze := func(fragments [][]byte) pty.Analysis {
		t.Helper()
		stream, err := analyzer.NewStream(pty.MustDimensions(3, 8))
		if err != nil {
			t.Fatalf("NewStream: %v", err)
		}
		for _, fragment := range fragments {
			if err := stream.Feed(fragment); err != nil {
				t.Fatalf("Feed %q: %v", fragment, err)
			}
		}
		snapshot, err := stream.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		return snapshot
	}

	coalesced := analyze([][]byte{payload})
	oneByte := make([][]byte, 0, len(payload))
	for _, value := range payload {
		oneByte = append(oneByte, []byte{value})
	}
	if got := analyze(oneByte); !equivalentAnalysis(got, coalesced) {
		t.Fatalf("one-byte snapshot differs: got=%#v want=%#v", operationView(got), operationView(coalesced))
	}
	if got := analyze([][]byte{payload[:1], payload[1:5], payload[5:14], payload[14:23], payload[23:]}); !equivalentAnalysis(got, coalesced) {
		t.Fatalf("randomized snapshot differs: got=%#v want=%#v", operationView(got), operationView(coalesced))
	}
}

func equivalentAnalysis(left, right pty.Analysis) bool {
	return reflect.DeepEqual(left.Screen, right.Screen) &&
		reflect.DeepEqual(left.PrivateModeChanges, right.PrivateModeChanges) &&
		reflect.DeepEqual(left.PhaseEvents, right.PhaseEvents) &&
		reflect.DeepEqual(operationView(left), operationView(right))
}

type operationViewEntry struct {
	Kind        pty.OperationKind
	ByteRange   pty.ByteRange
	Region      pty.Region
	Write       string
	PrivateMode *pty.PrivateModeChange
}

func operationView(analysis pty.Analysis) []operationViewEntry {
	result := make([]operationViewEntry, 0, len(analysis.Operations))
	for _, operation := range analysis.Operations {
		for _, record := range pty.OperationRecords(operation) {
			entry := operationViewEntry{
				Kind:        record.Kind,
				ByteRange:   record.ByteRange,
				Region:      record.Region,
				PrivateMode: record.PrivateMode,
			}
			if record.Write != nil {
				entry.Write = record.Write.Text()
			}
			result = append(result, entry)
		}
	}
	return result
}
