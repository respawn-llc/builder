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

	ops := parseTerminalOps(out.String())
	assertTerminalPrefix(t, ops, []terminalOp{
		{kind: terminalOpCSI, value: "\x1b[r"},
		{kind: terminalOpCSI, value: "\x1b[?6l"},
		{kind: terminalOpCSI, value: "\x1b[1;5r"},
		{kind: terminalOpCSI, value: "\x1b[5;1H"},
	})
	assertVisibleTextOps(t, ops, []string{
		"❯ first user",
		"❯ second user",
		"────────────── assistant ───────────────",
		"❮ assistant answer",
		"───────────────── tool ─────────────────",
		"• tool result",
		"──────────────── notice ────────────────",
		"ℹ notice",
	})
	if out.String() == ansi.Strip(out.String()) {
		t.Fatalf("immutable append text = %q, want styled ANSI output", out.String())
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
	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"❯ second"})
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(assistantRow("answer")), testFrame()); err != nil {
		t.Fatalf("apply assistant row: %v", err)
	}
	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{"────────────── assistant ───────────────", "❮ answer"})
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

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{
		"❯ user[2J",
		"└ next lineafter",
		"────────────── assistant ───────────────",
		"❮ assistant]0;spoof **answer**",
		"───────────────── tool ─────────────────",
		"• tool[3;1H result",
		"──────────────── notice ────────────────",
		"ℹ notice value",
	})
}

func TestCommittedRowsFilterNonOngoingVisibility(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)

	_, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{
			visibleRow(userRow("ongoing"), clientui.EntryVisibilityOngoing),
			visibleRow(userRow("collapsed ongoing"), clientui.EntryVisibilityOngoingCollapsed),
			visibleRow(userRow("auto default"), clientui.EntryVisibilityAuto),
			visibleRow(userRow("detail only"), clientui.EntryVisibilityDetail),
			visibleRow(userRow("hidden"), clientui.EntryVisibilityHidden),
		}},
	}, testFrame())
	if err != nil {
		t.Fatalf("apply visibility hydration: %v", err)
	}

	assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{
		"❯ ongoing",
		"❯ collapsed ongoing",
		"❯ auto default",
	})
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

func visibleRow(row clientui.TranscriptCommittedRow, visibility clientui.EntryVisibility) clientui.TranscriptCommittedRow {
	row.Visibility = visibility
	return row
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

type terminalOpKind string

const (
	terminalOpCSI  terminalOpKind = "csi"
	terminalOpCRLF terminalOpKind = "crlf"
	terminalOpText terminalOpKind = "text"
)

type terminalOp struct {
	kind  terminalOpKind
	value string
}

func parseTerminalOps(raw string) []terminalOp {
	ops := make([]terminalOp, 0)
	for i := 0; i < len(raw); {
		if raw[i] == '\x1b' && i+1 < len(raw) && raw[i+1] == '[' {
			end := i + 2
			for end < len(raw) && (raw[end] < '@' || raw[end] > '~') {
				end++
			}
			if end < len(raw) {
				end++
			}
			ops = append(ops, terminalOp{kind: terminalOpCSI, value: raw[i:end]})
			i = end
			continue
		}
		if raw[i] == '\r' && i+1 < len(raw) && raw[i+1] == '\n' {
			ops = append(ops, terminalOp{kind: terminalOpCRLF, value: raw[i : i+2]})
			i += 2
			continue
		}
		end := i + 1
		for end < len(raw) {
			if raw[end] == '\x1b' || (raw[end] == '\r' && end+1 < len(raw) && raw[end+1] == '\n') {
				break
			}
			end++
		}
		ops = append(ops, terminalOp{kind: terminalOpText, value: raw[i:end]})
		i = end
	}
	return ops
}

func assertTerminalPrefix(t *testing.T, got []terminalOp, want []terminalOp) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("terminal ops = %#v, want prefix %#v", got, want)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("terminal op %d = %#v, want %#v in prefix %#v", idx, got[idx], want[idx], want)
		}
	}
}

func assertVisibleTextOps(t *testing.T, ops []terminalOp, want []string) {
	t.Helper()
	got := make([]string, 0)
	current := ""
	for _, op := range ops {
		switch op.kind {
		case terminalOpCRLF:
			if row := strings.TrimRight(current, " "); row != "" {
				got = append(got, row)
			}
			current = ""
		case terminalOpText:
			current += ansi.Strip(op.value)
		default:
			continue
		}
	}
	if row := strings.TrimRight(current, " "); row != "" {
		got = append(got, row)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible terminal text ops = %#v, want %#v", got, want)
	}
}
