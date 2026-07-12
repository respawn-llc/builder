package ongoing

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestWidthChangeBeforeImmutableScrollbackRepaintsOnly(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}, Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{Sections: []FrameSection{{Kind: FrameSectionStatus, Lines: []string{"ready"}}}})
	if err != nil {
		t.Fatalf("width resize before immutable: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want noop", result.Action)
	}
	if got := out.String(); got == "" {
		t.Fatal("width resize before immutable did not repaint mutable band")
	}
}

func TestWidthChangeAfterCommittedRowRepaintsWithoutRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("immutable")), FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("append committed row: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{})
	if err != nil {
		t.Fatalf("width resize after immutable: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want repaint-only noop", result.Action)
	}
	if got := out.String(); got == "" {
		t.Fatal("width resize after immutable did not repaint mutable band")
	}
}

func TestWidthChangeAfterAssistantPromotionRepaintsWithoutRehydration(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.Render(FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("render initial frame: %v", err)
	}
	if _, err := surface.ApplyTerminalMessage(assistantDeltaMessage(uuid.New(), "stable\n\n"), FrameInput{Size: Size{Width: 20, Height: 3}}); err != nil {
		t.Fatalf("promote assistant row: %v", err)
	}
	out.Reset()

	result, err := surface.Resize(Size{Width: 30, Height: 3}, FrameInput{})
	if err != nil {
		t.Fatalf("width resize after assistant promotion: %v", err)
	}
	if result.Action != ResultNoop {
		t.Fatalf("resize action = %q, want repaint-only noop", result.Action)
	}
}
