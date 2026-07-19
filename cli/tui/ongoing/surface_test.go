package ongoing

import (
	"bytes"
	"errors"
	"testing"
)

func TestRenderErasesMutableBandResetsMarginsAndPlacesCursor(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.Render(FrameInput{
		Size: Size{Width: 10, Height: 5},
		Sections: []FrameSection{{
			Kind:  FrameSectionStatus,
			Lines: []string{"alpha", "beta"},
		}},
		Cursor: Cursor{Visible: true, Row: 5, Column: 3},
	})
	if err != nil {
		t.Fatalf("render frame: %v", err)
	}

	want := "\x1b[r\x1b[?6l" +
		"\x1b]133;C\x1b\\" +
		"\x1b[4;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[5;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[4;1H\x1b]133;A;redraw=1\x1b\\" +
		"alpha\x1b[5;1Hbeta" +
		"\x1b[5;3H\x1b[?25h"
	if got := out.String(); got != want {
		t.Fatalf("terminal transaction bytes = %q, want %q", got, want)
	}
}

func TestRenderErasesPreviousMutableBandRows(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 10, Height: 5}, Sections: []FrameSection{{Lines: []string{"alpha", "beta"}}}}); err != nil {
		t.Fatalf("render first frame: %v", err)
	}
	out.Reset()

	if _, err := surface.Render(FrameInput{Size: Size{Width: 10, Height: 5}, Sections: []FrameSection{{Lines: []string{"gamma"}}}}); err != nil {
		t.Fatalf("render second frame: %v", err)
	}

	want := "\x1b[r\x1b[?6l" +
		"\x1b]133;C\x1b\\" +
		"\x1b[4;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[5;1H\x1b]133;C\x1b\\\x1b[2K" +
		"\x1b[4;1H\x1b]133;A;redraw=1\x1b\\" +
		"\x1b[5;1Hgamma" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("terminal transaction bytes = %q, want %q", got, want)
	}
}

func TestInitialRenderUsesNoPreviousBandGeometry(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	frame := FrameInput{
		Size:     Size{Width: 10, Height: 5},
		Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}},
	}
	if surface.lastPaintedSize != nil || surface.retainedBandHeight != 0 {
		t.Fatal("fresh surface invented painted geometry")
	}

	if _, err := surface.Render(frame); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}

	if surface.lastPaintedSize == nil || *surface.lastPaintedSize != frame.Size {
		t.Fatalf("last painted size = %+v, want %+v", surface.lastPaintedSize, frame.Size)
	}
	if got, want := surface.retainedBandHeight, 1; got != want {
		t.Fatalf("retained height = %d, want %d", got, want)
	}
	ops := parseTerminalOps(out.String())
	for _, op := range ops {
		if op.kind == terminalOpCRLF {
			t.Fatalf("initial render scrolled blank terminal: ops=%+v", ops)
		}
	}
	assertCursorAddress(t, ops, frame.Size.Height, 1)
}

func TestRenderReturnsSynchronousWriterError(t *testing.T) {
	wantErr := errors.New("terminal closed")
	surface := NewSurface(failingWriter{err: wantErr})

	_, err := surface.Render(FrameInput{Size: Size{Width: 10, Height: 5}, Sections: []FrameSection{{Lines: []string{"alpha"}}}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("render error = %v, want %v", err, wantErr)
	}
	if surface.lastPaintedSize != nil || surface.retainedBandHeight != 0 {
		t.Fatalf(
			"failed initial render committed geometry: size=%+v retained=%d",
			surface.lastPaintedSize,
			surface.retainedBandHeight,
		)
	}
}

func TestRenderPanicsWithTypedDeveloperDiagnosticsForInvalidFrame(t *testing.T) {
	tests := []struct {
		name       string
		frame      FrameInput
		wantReason string
		wantFacts  map[string]int
	}{
		{
			name:       "geometry",
			frame:      FrameInput{Size: Size{Width: 0, Height: 5}},
			wantReason: "invalid terminal geometry",
			wantFacts:  map[string]int{"width": 0, "height": 5},
		},
		{
			name: "cursor",
			frame: FrameInput{
				Size:   Size{Width: 10, Height: 5},
				Cursor: Cursor{Visible: true, Row: 6, Column: 2},
			},
			wantReason: "invalid cursor",
			wantFacts:  map[string]int{"width": 10, "height": 5, "row": 6, "column": 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface := NewSurface(&bytes.Buffer{})
			defer func() {
				recovered := recover()
				err, ok := recovered.(DeveloperError)
				if !ok {
					t.Fatalf("panic = %T, want DeveloperError", recovered)
				}
				if err.Operation != "render" || err.Reason != test.wantReason || err.Stack == "" {
					t.Fatalf("developer error = %+v", err)
				}
				for name, want := range test.wantFacts {
					got, ok := err.Facts[name].(int)
					if !ok || got != want {
						t.Fatalf("fact %q = %#v, want %d", name, err.Facts[name], want)
					}
				}
			}()
			_, _ = surface.Render(test.frame)
		})
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
