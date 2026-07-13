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
		"\x1b[5;1H\x1b]133;A;redraw=1\x1b\\" +
		"gamma" +
		"\x1b[?25l"
	if got := out.String(); got != want {
		t.Fatalf("terminal transaction bytes = %q, want %q", got, want)
	}
}

func TestRenderReturnsSynchronousWriterError(t *testing.T) {
	wantErr := errors.New("terminal closed")
	surface := NewSurface(failingWriter{err: wantErr})

	_, err := surface.Render(FrameInput{Size: Size{Width: 10, Height: 5}, Sections: []FrameSection{{Lines: []string{"alpha"}}}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("render error = %v, want %v", err, wantErr)
	}
}

func TestRenderPanicsForInvalidGeometry(t *testing.T) {
	surface := NewSurface(&bytes.Buffer{})

	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid geometry panic")
		}
	}()

	_, _ = surface.Render(FrameInput{Size: Size{Width: 0, Height: 5}})
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
