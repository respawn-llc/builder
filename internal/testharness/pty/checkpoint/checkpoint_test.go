package checkpoint

import (
	"bytes"
	"testing"
)

func TestMarkerCodecRoundTripsTypedKinds(t *testing.T) {
	windowID, err := NewWindowID("30e702a9-86c4-4e7e-9f50-2f59e6f64f00")
	if err != nil {
		t.Fatalf("new window ID: %v", err)
	}
	encoded, err := Encode(Marker{
		Sequence: 7,
		Kind:     KindWindowStart,
		WindowID: &windowID,
	})
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	decoded, recognized, err := DecodeOSCData(encodedOSCData(t, encoded))
	if err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	if !recognized {
		t.Fatal("encoded marker was not recognized")
	}
	if decoded.Sequence != 7 || decoded.Kind != KindWindowStart {
		t.Fatalf("decoded marker = %#v", decoded)
	}
	if decoded.WindowID == nil || *decoded.WindowID != windowID {
		t.Fatalf("decoded window ID = %#v, want %s", decoded.WindowID, windowID)
	}
}

func TestKindDescriptorsRoundTripEveryCheckpointKind(t *testing.T) {
	if len(kindDescriptors) == 0 {
		t.Fatal("checkpoint kind descriptor table is empty")
	}
	for _, want := range kindDescriptors {
		byKind, ok := descriptorForKind(want.kind)
		if !ok || byKind != want {
			t.Fatalf("descriptor for kind %d = %#v, %t; want %#v", want.kind, byKind, ok, want)
		}
		byName, ok := descriptorForProtocolName(want.protocolName)
		if !ok || byName != want {
			t.Fatalf("descriptor for protocol name %q = %#v, %t; want %#v", want.protocolName, byName, ok, want)
		}
	}
}

func TestWriterQueuesCheckpointImmediatelyBeforeNextTerminalWrite(t *testing.T) {
	var out bytes.Buffer
	writer := NewWriter(&out)
	if err := writer.QueueBeforeNextWrite(KindScenarioStart, nil); err != nil {
		t.Fatalf("queue checkpoint: %v", err)
	}
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if err := writer.Emit(KindInputApplied, nil); err != nil {
		t.Fatalf("emit checkpoint: %v", err)
	}

	first, firstEnd := decodeMarkerAt(t, out.Bytes(), 0)
	if first.Sequence != 1 || first.Kind != KindScenarioStart {
		t.Fatalf("first checkpoint = %#v", first)
	}
	frameEnd := firstEnd + len("frame")
	if got := string(out.Bytes()[firstEnd:frameEnd]); got != "frame" {
		t.Fatalf("payload after queued checkpoint = %q, want frame", got)
	}
	second, secondEnd := decodeMarkerAt(t, out.Bytes(), frameEnd)
	if second.Sequence != 2 || second.Kind != KindInputApplied {
		t.Fatalf("second checkpoint = %#v", second)
	}
	if secondEnd != out.Len() {
		t.Fatalf("decoded bytes end = %d, output length = %d", secondEnd, out.Len())
	}
}

func TestWriterSequencesQueuedCheckpointWhenItIsActuallyWritten(t *testing.T) {
	var out bytes.Buffer
	writer := NewWriter(&out)
	if err := writer.QueueBeforeNextWrite(KindDetailInitialPageApplied, nil); err != nil {
		t.Fatalf("queue checkpoint: %v", err)
	}
	if err := writer.Emit(KindInputApplied, nil); err != nil {
		t.Fatalf("emit checkpoint: %v", err)
	}
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	first, firstEnd := decodeMarkerAt(t, out.Bytes(), 0)
	if first.Sequence != 1 || first.Kind != KindInputApplied {
		t.Fatalf("first checkpoint = %#v, want emitted input-applied checkpoint", first)
	}
	second, secondEnd := decodeMarkerAt(t, out.Bytes(), firstEnd)
	if second.Sequence != 2 || second.Kind != KindDetailInitialPageApplied {
		t.Fatalf("second checkpoint = %#v, want queued detail checkpoint", second)
	}
	if got := string(out.Bytes()[secondEnd:]); got != "frame" {
		t.Fatalf("payload after queued checkpoint = %q, want frame", got)
	}
}

func encodedOSCData(t *testing.T, encoded []byte) []byte {
	t.Helper()
	const prefix = "\x1b]"
	if len(encoded) < len(prefix)+1 || string(encoded[:len(prefix)]) != prefix || encoded[len(encoded)-1] != '\a' {
		t.Fatalf("encoded marker is not an OSC sequence: %q", encoded)
	}
	return encoded[len(prefix) : len(encoded)-1]
}

func decodeMarkerAt(t *testing.T, payload []byte, start int) (Marker, int) {
	t.Helper()
	endOffset := bytes.IndexByte(payload[start:], '\a')
	if endOffset < 0 {
		t.Fatalf("checkpoint terminator missing after byte %d", start)
	}
	end := start + endOffset + 1
	marker, recognized, err := DecodeOSCData(encodedOSCData(t, payload[start:end]))
	if err != nil {
		t.Fatalf("decode checkpoint at byte %d: %v", start, err)
	}
	if !recognized {
		t.Fatalf("checkpoint at byte %d was not recognized", start)
	}
	return marker, end
}
