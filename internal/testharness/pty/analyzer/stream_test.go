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
		if err := stream.Feed(fragment); err != nil {
			t.Fatalf("Feed: %v", err)
		}
	}
	if err := stream.Resize(pty.MustDimensions(4, 8)); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := stream.Feed(afterResize); err != nil {
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

type operationViewEntry struct {
	Kind        pty.OperationKind
	ByteRange   pty.ByteRange
	Region      pty.Region
	Write       string
	PrivateMode *pty.PrivateModeChange
}

func operationView(analysis pty.Analysis) []operationViewEntry {
	result := make([]operationViewEntry, len(analysis.Operations))
	for index, operation := range analysis.Operations {
		entry := operationViewEntry{
			Kind:        operation.Kind,
			ByteRange:   operation.ByteRange,
			Region:      operation.Region,
			PrivateMode: operation.PrivateMode,
		}
		if operation.Write != nil {
			entry.Write = operation.Write.Text()
		}
		result[index] = entry
	}
	return result
}
