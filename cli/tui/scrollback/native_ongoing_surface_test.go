package scrollback

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestNativeOngoingSurfaceStreamsThroughSourceBackedStablePromotionAndLiveTail(t *testing.T) {
	var out bytes.Buffer
	surface := NewOngoingScrollbackBufferImpl(context.Background(), 6, 4, &out, nil)
	defer surface.close()

	if err := surface.RenderLive(NativeLiveAreaFrame{Lines: []string{"input"}}); err != nil {
		t.Fatalf("render live returned error: %v", err)
	}
	if err := surface.StreamMarkdownAssistantContent("hello"); err != nil {
		t.Fatalf("stream partial returned error: %v", err)
	}
	if got := out.String(); got != nativeLiveAreaRenderSequence(4, nativeLiveAreaFrame("input")) {
		t.Fatalf("partial stream wrote stable bytes, output = %q", got)
	}
	if got, want := surface.AssistantStreamTailLines(), []string{"hello"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tail lines = %q, want %q", got, want)
	}

	if err := surface.StreamMarkdownAssistantContent(" world\nnext\n"); err != nil {
		t.Fatalf("stream wrap returned error: %v", err)
	}
	if got, want := surface.AssistantStreamTailLines(), []string{"next"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tail lines after completed wrapped line = %q, want %q", got, want)
	}
	if got := out.String(); !strings.Contains(got, "hello "+terminalLineBreak) {
		t.Fatalf("wrapped stable row was not promoted through scrollback output: %q", got)
	}
	if got := out.String(); !strings.Contains(got, "world"+terminalLineBreak) {
		t.Fatalf("wrapped stable tail row was not promoted through scrollback output: %q", got)
	}

	if err := surface.FinishAssistantStreaming(); err != nil {
		t.Fatalf("finish returned error: %v", err)
	}
	if got := surface.AssistantStreamTailLines(); len(got) != 0 {
		t.Fatalf("tail after finish = %q, want empty", got)
	}
	if got := out.String(); !strings.Contains(got, "next"+terminalLineBreak) {
		t.Fatalf("final tail was not promoted through scrollback output: %q", got)
	}
}
