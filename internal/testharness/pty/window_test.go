package pty_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"core/internal/testharness/pty"
)

func TestResolveOperationWindowAndClassifyAppends(t *testing.T) {
	t.Parallel()

	windowID := mustWindowID(t)
	capture, err := pty.NewCapture(
		pty.MustDimensions(3, 8),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte(
			marker(t, 1, "WindowStart", windowID)+
				"\x1b[3;1Hbottom"+
				marker(t, 2, "WindowEnd", windowID)+
				"\x1b[1;1Habove",
		))},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	windows, err := pty.ResolveOperationWindows(analysis)
	if err != nil {
		t.Fatalf("ResolveOperationWindows: %v", err)
	}
	window := windows[windowID]
	appends := pty.ClassifyAppends(analysis, window, 2)
	if len(appends) != 1 {
		t.Fatalf("append count = %d, want 1: %#v", len(appends), appends)
	}
	if appends[0].Operation.Write == nil || appends[0].Operation.Write.Text != "bottom" {
		t.Fatalf("append write payload = %#v, want text %q", appends[0].Operation.Write, "bottom")
	}
}

func TestPhaseMarkerRejectsInvalidWindowID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		windowID string
	}{
		{name: "empty", windowID: ""},
		{name: "v4", windowID: uuid.NewString()},
		{name: "malformed", windowID: "not-a-uuid"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := pty.Analyze(mustCapture(t, []byte(markerRaw(t, 1, "WindowStart", &tc.windowID)), pty.MustDimensions(2, 8)))
			if err == nil {
				t.Fatalf("Analyze succeeded for invalid window_id %q", tc.windowID)
			}
		})
	}
}

func TestResolveOperationWindowAcrossChunksAndResize(t *testing.T) {
	t.Parallel()

	windowID := mustWindowID(t)
	capture, err := pty.NewCaptureWithEvents(
		pty.MustDimensions(2, 8),
		[]pty.Chunk{
			pty.NewChunk(0, time.Millisecond, []byte(marker(t, 1, "WindowStart", windowID))),
			pty.NewChunk(1, 2*time.Millisecond, []byte("\x1b[3;1Hgrown")),
			pty.NewChunk(2, 3*time.Millisecond, []byte(marker(t, 2, "WindowEnd", windowID))),
		},
		[]pty.ResizeEvent{{Placement: pty.AfterChunk(0), At: 1500 * time.Microsecond, Dimensions: pty.MustDimensions(3, 8)}},
	)
	if err != nil {
		t.Fatalf("NewCaptureWithEvents: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	windows, err := pty.ResolveOperationWindows(analysis)
	if err != nil {
		t.Fatalf("ResolveOperationWindows: %v", err)
	}
	appends := pty.ClassifyAppends(analysis, windows[windowID], 2)
	if len(appends) != 1 {
		t.Fatalf("append count = %d, want 1: %#v", len(appends), appends)
	}
}

func TestClassifyAppendsDoesNotTreatEveryMutableBandWriteAsAppend(t *testing.T) {
	t.Parallel()

	analysis := pty.Analysis{
		Dimensions: pty.MustDimensions(5, 8),
		Operations: []pty.Operation{
			writeOperation(pty.Region{Top: 3, Bottom: 4, Left: 0, Right: 4}, "mid"),
			writeOperation(pty.Region{Top: 2, Bottom: 3, Left: 0, Right: 4}, "boundary"),
			writeOperation(pty.Region{Top: 4, Bottom: 5, Left: 0, Right: 4}, "bottom"),
		},
	}
	appends := pty.ClassifyAppends(analysis, pty.OperationWindow{Start: 0, End: len(analysis.Operations)}, 2)
	if len(appends) != 2 || appends[0].Operation.Write == nil || appends[0].Operation.Write.Text != "boundary" || appends[1].Operation.Write == nil || appends[1].Operation.Write.Text != "bottom" {
		t.Fatalf("appends = %#v, want boundary and bottom writes only", appends)
	}
}

func writeOperation(region pty.Region, text string) pty.Operation {
	payload := pty.MustWritePayload(text)
	return pty.Operation{Kind: pty.OperationWrite, Region: region, Write: &payload}
}

func mustCapture(t *testing.T, payload []byte, dimensions pty.Dimensions) pty.Capture {
	t.Helper()
	capture, err := pty.NewCapture(dimensions, []pty.Chunk{pty.NewChunk(0, time.Millisecond, payload)})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	return capture
}

func mustWindowID(t *testing.T) pty.WindowID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("NewV7: %v", err)
	}
	windowID, err := pty.NewWindowID(id.String())
	if err != nil {
		t.Fatalf("NewWindowID: %v", err)
	}
	return windowID
}

func marker(t *testing.T, seq int, phase string, windowID pty.WindowID) string {
	t.Helper()
	raw := windowID.String()
	return markerRaw(t, seq, phase, &raw)
}

func markerRaw(t *testing.T, seq int, phase string, windowID *string) string {
	t.Helper()
	payload := map[string]any{
		"version": 1,
		"seq":     seq,
		"phase":   phase,
	}
	if windowID != nil {
		payload["window_id"] = *windowID
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	return "\x1b]777;kent-pty-phase;" + base64.RawURLEncoding.EncodeToString(encoded) + "\a"
}
