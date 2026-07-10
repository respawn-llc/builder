package ongoing

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
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

	rows := visibleTextRows(parseTerminalOps(out.String()))
	// Two consecutive user rows form one group: no divider between them.
	// Each group transition (user->assistant, assistant->tool, tool->notice)
	// emits exactly one plain-rule divider line immediately before the new group.
	wantStructure := []rowKind{
		{content: "❯ first user", divider: false},
		{content: "❯ second user", divider: false},
		{divider: true},
		{content: "❮ assistant answer", divider: false},
		{divider: true},
		{content: "• tool result", divider: false},
		{divider: true},
		{content: "ℹ notice", divider: false},
	}
	assertRowStructure(t, rows, wantStructure)
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
	rows := visibleTextRows(parseTerminalOps(out.String()))
	// Same-group append (user->user): no divider before the second row.
	assertRowStructure(t, rows, []rowKind{{content: "❯ second", divider: false}})
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(assistantRow("answer")), testFrame()); err != nil {
		t.Fatalf("apply assistant row: %v", err)
	}
	rows = visibleTextRows(parseTerminalOps(out.String()))
	// Group transition (user->assistant): one divider before the assistant row.
	assertRowStructure(t, rows, []rowKind{{divider: true}, {content: "❮ answer", divider: false}})
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

	rows := visibleTextRows(parseTerminalOps(out.String()))
	wantStructure := []rowKind{
		{content: "❯ user[2J…", divider: false},
		{divider: true},
		{content: "❮ assistant]0;spoof answer", divider: false},
		{divider: true},
		{content: "• tool[3;1H result", divider: false},
		{divider: true},
		{content: "ℹ notice value", divider: false},
	}
	assertRowStructure(t, rows, wantStructure)
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

// Ongoing native scrollback uses compact transcript text. Detail expansion owns
// full transcript text.
func TestOngoingRendersOngoingCollapsedRowsAsCompactSingleLine(t *testing.T) {
	multiLine := "first line\nsecond line\nthird line"
	cases := []struct {
		name              string
		visibility        clientui.EntryVisibility
		wantContentRows   int
		wantCompactMarker bool
	}{
		{name: "O shows compact single line", visibility: clientui.EntryVisibilityOngoing, wantContentRows: 1, wantCompactMarker: true},
		{name: "OC shows compact single line", visibility: clientui.EntryVisibilityOngoingCollapsed, wantContentRows: 1, wantCompactMarker: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			surface := NewSurface(&out)
			row := clientui.TranscriptCommittedRow{
				Kind:       clientui.TranscriptRowAssistant,
				Visibility: tt.visibility,
				Assistant:  &clientui.TranscriptAssistantRow{Text: multiLine},
			}
			if _, err := surface.ApplyTerminalMessage(committedMessage(row), FrameInput{Size: Size{Width: 80, Height: 24}}); err != nil {
				t.Fatalf("apply: %v", err)
			}
			rows := visibleTextRows(parseTerminalOps(out.String()))
			contentRows := 0
			for _, r := range rows {
				if isDividerRule(r) {
					continue
				}
				contentRows++
			}
			if contentRows != tt.wantContentRows {
				t.Fatalf("content rows = %d, want %d (rows=%v)", contentRows, tt.wantContentRows, rows)
			}
			if tt.wantCompactMarker {
				// OC compact form shows only the first non-empty line, never "second line".
				for _, r := range rows {
					if strings.Contains(r, "second line") || strings.Contains(r, "third line") {
						t.Fatalf("OC compact form leaked non-first body line: %q (rows=%v)", r, rows)
					}
				}
			}
		})
	}
}

func TestHydrationRendersFinalAssistantFullText(t *testing.T) {
	for _, phase := range []transcript.AssistantPhase{transcript.AssistantPhaseFinal, transcript.AssistantPhaseLegacyFinal} {
		t.Run(string(phase), func(t *testing.T) {
			var out bytes.Buffer
			surface := NewSurface(&out)
			row := clientui.TranscriptCommittedRow{
				Kind:       clientui.TranscriptRowAssistant,
				Visibility: clientui.EntryVisibilityOngoing,
				Assistant: &clientui.TranscriptAssistantRow{
					Text:          "first line\nsecond line\nthird line",
					CondensedText: "compact answer",
					Phase:         phase,
				},
			}

			if _, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
				Kind:      clientui.TranscriptMessageHydration,
				Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{row}},
			}, FrameInput{Size: Size{Width: 80, Height: 24}}); err != nil {
				t.Fatalf("apply hydration: %v", err)
			}

			assertVisibleTextOps(t, parseTerminalOps(out.String()), []string{
				"❮ first line",
				"  second line",
				"  third line",
			})
		})
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

func visibleRow(row clientui.TranscriptCommittedRow, visibility clientui.EntryVisibility) clientui.TranscriptCommittedRow {
	row.Visibility = visibility
	return row
}

func assistantRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{
		Text:  text,
		Phase: transcript.AssistantPhaseFinal,
	}}
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

func visibleTextRows(ops []terminalOp) []string {
	got := make([]string, 0)
	current := ""
	flush := func() {
		if row := strings.TrimRight(current, " "); row != "" {
			got = append(got, row)
		}
		current = ""
	}
	for _, op := range ops {
		switch op.kind {
		case terminalOpCRLF:
			flush()
		case terminalOpCSI:
			if len(op.value) > 0 {
				final := op.value[len(op.value)-1]
				if final == 'H' || final == 'f' {
					flush()
				}
			}
		case terminalOpText:
			current += ansi.Strip(op.value)
		default:
			continue
		}
	}
	flush()
	return got
}

func assertVisibleTextOps(t *testing.T, ops []terminalOp, want []string) {
	t.Helper()
	got := visibleTextRows(ops)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible terminal text ops = %#v, want %#v", got, want)
	}
}

// isDividerRule reports whether a visible row is a plain horizontal rule made
// only of the box-drawing "─" rune (the divider line shape), with no label text.
func isDividerRule(row string) bool {
	if row == "" {
		return false
	}
	for _, r := range row {
		if r != '─' && r != '…' {
			return false
		}
	}
	return true
}

// rowKind describes one visible terminal row by structural role: either a plain
// divider rule, or a content row carrying the visible text. Tests assert
// structure (divider placement and content presence) rather than literal text,
// so divider content/style changes do not break them.
type rowKind struct {
	divider bool
	content string
}

func assertRowStructure(t *testing.T, rows []string, want []rowKind) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("visible rows = %#v, want %d rows of structure %#v", rows, len(want), want)
	}
	for idx, wantRow := range want {
		got := rows[idx]
		if wantRow.divider {
			if !isDividerRule(got) {
				t.Fatalf("row %d = %q, want a plain divider rule", idx, got)
			}
			continue
		}
		if isDividerRule(got) {
			t.Fatalf("row %d = %q, want content %q (not a divider)", idx, got, wantRow.content)
		}
		if got != wantRow.content {
			t.Fatalf("row %d = %q, want content %q", idx, got, wantRow.content)
		}
	}
}
