package ongoing

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"core/shared/clientui"
	"github.com/charmbracelet/x/ansi"
)

func TestApplyTerminalMessageAppendsHydrationRowsInServerOrderWithGroupDividers(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{
			userRow("first user"),
			userRow("second user"),
			assistantRow("assistant answer"),
			toolRow("tool result"),
			noticeRow("notice"),
		}},
	}, testFrame())
	if err != nil {
		t.Fatalf("apply hydration: %v", err)
	}

	raw := out.String()
	if !strings.Contains(raw, "\x1b[1;5r\x1b[5;1H") || !strings.Contains(raw, "\x1b[r\x1b[?6l\x1b[?25l") {
		t.Fatalf("immutable append bytes = %q, want scroll-region transaction", raw)
	}
	stripped := ansi.Strip(raw)
	for _, want := range []string{"❯ first user", "❯ second user", "assistant", "❮ assistant answer", "tool", "• tool result", "notice", "ℹ notice"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("immutable append text = %q, want %q", stripped, want)
		}
	}
	if !strings.Contains(raw, "\x1b[") {
		t.Fatalf("immutable append text = %q, want styled ANSI output", raw)
	}
}

func TestApplyTerminalMessageDoesNotEmitDividerForConsecutiveSameGroup(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("first")), testFrame()); err != nil {
		t.Fatalf("apply first row: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("second")), testFrame()); err != nil {
		t.Fatalf("apply second row: %v", err)
	}
	stripped := ansi.Strip(out.String())
	if strings.Contains(stripped, "user") || !strings.Contains(stripped, "❯ second") {
		t.Fatalf("same-group append bytes = %q, want styled second user without divider", out.String())
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(assistantRow("answer")), testFrame()); err != nil {
		t.Fatalf("apply assistant row: %v", err)
	}
	if got := ansi.Strip(out.String()); !strings.Contains(got, "assistant") || !strings.Contains(got, "❮ answer") {
		t.Fatalf("group-change append bytes = %q, want assistant divider and answer", out.String())
	}
}

func TestSurfaceDoesNotRetainCommittedRowContentAfterAppend(t *testing.T) {
	surface := NewSurface(discardWriter{})
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("not retained")), testFrame()); err != nil {
		t.Fatalf("apply row: %v", err)
	}

	typ := reflect.TypeOf(*surface)
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.Name == "writer" {
			continue
		}
		switch field.Type.Kind() {
		case reflect.String, reflect.Slice, reflect.Map:
			t.Fatalf("surface field %s retains forbidden emitted-output-shaped state of type %s", field.Name, field.Type)
		}
	}
}

func TestCommittedRowsNeutralizeTranscriptSourcedControlBytes(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{
			userRow("user\x1b[2J\nnext\tline\rafter"),
			assistantRow("assistant\x1b]0;spoof\a **answer**"),
			toolRow("tool\x1b[3;1H result"),
			noticeRow("notice\x07 value"),
		}},
	}, testFrame())
	if err != nil {
		t.Fatalf("apply malicious hydration: %v", err)
	}

	raw := out.String()
	for _, forbidden := range []string{"\x1b[2J", "\x1b]0;spoof", "\x1b[3;1H", "\a"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("committed output contains transcript control %q in %q", forbidden, raw)
		}
	}
	stripped := ansi.Strip(raw)
	for _, want := range []string{"user[2J", "assistant]0;spoof **answer**", "tool[3;1H result", "notice value"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("sanitized output = %q, want visible text %q", stripped, want)
		}
	}
}

func committedMessage(row clientui.TranscriptCommittedRow) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind:         clientui.TranscriptMessageCommittedRow,
		CommittedRow: &row,
	}
}

func userRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: text}}
}

func assistantRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: text}}
}

func toolRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{Text: text}}
}

func noticeRow(text string) clientui.TranscriptCommittedRow {
	legacyText := text
	return clientui.TranscriptCommittedRow{
		Kind:   clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{Data: clientui.TranscriptNoticeData{LegacyText: &legacyText}},
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

func testFrame() FrameInput {
	return FrameInput{Size: Size{Width: 40, Height: 5}}
}
