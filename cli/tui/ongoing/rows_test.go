package ongoing

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
	"github.com/charmbracelet/x/ansi"
)

func TestApplyTerminalMessageAppendsHydrationRowsInServerOrderWithBlankGroupSeparators(t *testing.T) {
	output := renderTerminalMessageForTest(t, hydrationMessage(
		userRow("first user"),
		userRow("second user"),
		assistantRow("assistant answer"),
		toolRow("tool result"),
		noticeRow("notice"),
	), testFrame())
	rows := immutableAppendedRows(parseTerminalOps(output))
	// Two consecutive user rows form one group: no separator between them.
	// Each group transition (user->assistant, assistant->tool, tool->notice)
	// emits exactly one blank line immediately before the new group.
	wantStructure := []rowKind{
		{content: "❯ first user"},
		{content: "❯ second user"},
		{separator: true},
		{content: "❮ assistant answer"},
		{separator: true},
		{content: "• tool result"},
		{separator: true},
		{content: "ℹ notice"},
	}
	assertRowStructure(t, rows, wantStructure)
	if output == ansi.Strip(output) {
		t.Fatalf("immutable append text = %q, want styled ANSI output", output)
	}
}

func TestApplyTerminalMessageDoesNotEmitSeparatorForConsecutiveSameGroup(t *testing.T) {
	var out bytes.Buffer
	surface := NewSurface(&out)
	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("first")), testFrame()); err != nil {
		t.Fatalf("apply first row: %v", err)
	}
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(userRow("second")), testFrame()); err != nil {
		t.Fatalf("apply second row: %v", err)
	}
	rows := immutableAppendedRows(parseTerminalOps(out.String()))
	// Same-group append (user->user): no separator before the second row.
	assertRowStructure(t, rows, []rowKind{{content: "❯ second"}})
	out.Reset()

	if _, err := surface.ApplyTerminalMessage(committedMessage(assistantRow("answer")), testFrame()); err != nil {
		t.Fatalf("apply assistant row: %v", err)
	}
	rows = immutableAppendedRows(parseTerminalOps(out.String()))
	// Group transition (user->assistant): one blank line before the assistant row.
	assertRowStructure(t, rows, []rowKind{{separator: true}, {content: "❮ answer"}})
}

func TestBackgroundShellCompletionStaysFlushWithToolActivity(t *testing.T) {
	output := renderTerminalMessageForTest(t, hydrationMessage(
		toolRow("backgrounded command"),
		backgroundNoticeRow("background complete"),
		noticeRow("ordinary notice"),
	), testFrame())
	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(output)), []rowKind{
		{content: "• backgrounded command"},
		{content: "ℹ background complete"},
		{separator: true},
		{content: "ℹ ordinary notice"},
	})
}

func TestCommittedAssistantFinalWithoutStreamRendersFullAnswer(t *testing.T) {
	row := assistantRow("first final line\nsecond final line")
	row.Visibility = clientui.EntryVisibilityOngoingCollapsed

	output := renderTerminalMessageForTest(t, committedMessage(row), testFrame())
	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(output)), []rowKind{
		{content: "❮ first final line second final line"},
	})
}

func TestCommittedUserParagraphAppendsOneLogicalLineAtNarrowWidth(t *testing.T) {
	output := renderTerminalMessageForTest(
		t,
		committedMessage(userRow("alpha beta gamma delta")),
		FrameInput{Size: Size{Width: 10, Height: 6}},
	)
	assertRowStructure(t, immutableAppendedRows(parseTerminalOps(output)), []rowKind{
		{content: "❯ alpha beta gamma delta"},
	})
}

func TestCommittedAssistantFinalFallbackUsesFrameTheme(t *testing.T) {
	frame := testFrame()
	frame.Theme = "light"

	expected := renderTerminalMessageForTest(t, committedMessage(assistantRow("themed answer")), frame)
	streamID := runtimeids.NewAssistantStreamID()
	fallbackRow := assistantRow("themed answer")
	fallbackRow.Assistant.StreamID = &streamID
	actual := renderTerminalMessageForTest(t, committedMessage(fallbackRow), frame)
	if actual != expected {
		t.Fatalf("fallback committed assistant row did not preserve the frame theme")
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
	output := renderTerminalMessageForTest(t, hydrationMessage(
		userRow("user\x1b[2J\nnext\tline\rafter"),
		assistantRow("assistant\x1b]0;spoof\a **answer**"),
		toolRow("tool\x1b[3;1H result"),
		noticeRow("notice\x07 value"),
	), testFrame())
	rows := immutableAppendedRows(parseTerminalOps(output))
	wantStructure := []rowKind{
		{content: "❯ user[2J next lineafter"},
		{separator: true},
		{content: "❮ assistant]0;spoof answer"},
		{separator: true},
		{content: "• tool[3;1H result"},
		{separator: true},
		{content: "ℹ notice value"},
	}
	assertRowStructure(t, rows, wantStructure)
}

func TestCommittedRowsFilterNonOngoingVisibility(t *testing.T) {
	output := renderTerminalMessageForTest(t, hydrationMessage(
		visibleRow(userRow("ongoing"), clientui.EntryVisibilityOngoing),
		visibleRow(userRow("collapsed ongoing"), clientui.EntryVisibilityOngoingCollapsed),
		visibleRow(userRow("detail only"), clientui.EntryVisibilityDetail),
		visibleRow(userRow("hidden"), clientui.EntryVisibilityHidden),
	), testFrame())
	assertVisibleTextOps(t, parseTerminalOps(output), []string{
		"❯ ongoing",
		"❯ collapsed ongoing",
	})
}

func TestCommittedRowsRejectUnresolvedVisibility(t *testing.T) {
	surface := NewSurface(discardWriter{})
	row := userRow("invalid")
	row.Visibility = clientui.EntryVisibilityAuto

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("unresolved committed row visibility did not panic")
		}
	}()
	_, _ = surface.ApplyTerminalMessage(committedMessage(row), testFrame())
}

// Ongoing native scrollback uses compact transcript text. Detail expansion owns
// full transcript text.
func TestOngoingRendersOngoingCollapsedRowsAsCompactSingleLine(t *testing.T) {
	multiLine := "first line\nsecond line\nthird line"
	cases := []struct {
		name       string
		visibility clientui.EntryVisibility
	}{
		{name: "O", visibility: clientui.EntryVisibilityOngoing},
		{name: "OC", visibility: clientui.EntryVisibilityOngoingCollapsed},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			row := clientui.TranscriptCommittedRow{
				Kind:       clientui.TranscriptRowAssistant,
				Visibility: tt.visibility,
				Integrity:  transcript.RowIntegrityValid,
				Assistant: &clientui.TranscriptAssistantRow{
					StepID: assistantFinalizationStepID(),
					Text:   multiLine,
					Phase:  transcript.AssistantPhaseCommentary,
				},
			}
			output := renderTerminalMessageForTest(t, committedMessage(row), FrameInput{Size: Size{Width: 80, Height: 24}})
			rows := visibleTextRows(parseTerminalOps(output))
			if len(rows) != 1 {
				t.Fatalf("compact rows = %v, want one logical row", rows)
			}
			if strings.Contains(rows[0], "second line") || strings.Contains(rows[0], "third line") {
				t.Fatalf("compact form leaked non-first body line: %q", rows[0])
			}
		})
	}
}

func TestAnsweredQuestionRendersFullQuestionAndSelectedAnswer(t *testing.T) {
	condensedText := "Recursive scan\nUser also said:\ninclude tests"
	row := clientui.TranscriptCommittedRow{
		Kind:       clientui.TranscriptRowTool,
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Tool: &clientui.TranscriptToolRow{
			StepID:        assistantFinalizationStepID(),
			ToolCallID:    "5c8ff40e-35fd-48db-9b42-bc42fb7a741d",
			ToolName:      "ask_question",
			Text:          "User chose option #2. They also said: include tests",
			CondensedText: &condensedText,
			Presentation: &transcript.ToolCallMeta{
				ToolName:       "ask_question",
				Presentation:   transcript.ToolPresentationAskQuestion,
				RenderBehavior: transcript.ToolCallRenderBehaviorAskQuestion,
				Command:        "Choose scope?\nKeep generated files in scope?",
				CompactText:    "Choose scope?\nKeep generated files in scope?",
				Question:       "Choose scope?\nKeep generated files in scope?",
				Suggestions:    []string{"flat scan", "Recursive scan"},
			},
		},
	}

	output := renderTerminalMessageForTest(t, committedMessage(row), FrameInput{Size: Size{Width: 80, Height: 24}})
	assertVisibleTextOps(t, parseTerminalOps(output), []string{
		"? Choose scope?",
		"│ Keep generated files in scope?",
		"│ Recursive scan",
		"│ User also said:",
		"└ include tests",
	})
}

func TestHydrationRendersFinalAssistantFullText(t *testing.T) {
	condensedText := "compact answer"
	row := assistantRow("first line\nsecond line\nthird line")
	row.Assistant.CondensedText = &condensedText
	output := renderTerminalMessageForTest(
		t,
		hydrationMessage(row),
		FrameInput{Size: Size{Width: 80, Height: 24}},
	)
	assertVisibleTextOps(t, parseTerminalOps(output), []string{
		"❮ first line second line third line",
	})
}

func TestHydrationRendersFullUserPrompt(t *testing.T) {
	condensedText := "/review"
	row := userRow("/review\nInspect every changed file.\nReport architectural regressions.")
	row.User.CondensedText = &condensedText
	output := renderTerminalMessageForTest(
		t,
		hydrationMessage(row),
		FrameInput{Size: Size{Width: 80, Height: 24}},
	)
	assertVisibleTextOps(t, parseTerminalOps(output), []string{
		"❯ /review Inspect every changed file. Report architectural regressions.",
	})
}

func committedMessage(row clientui.TranscriptCommittedRow) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind:    clientui.TranscriptMessageCommittedRow,
		Payload: clientui.TranscriptPayload{CommittedRow: &row},
	}
}

func hydrationMessage(rows ...clientui.TranscriptCommittedRow) clientui.TranscriptMessage {
	return clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Payload: clientui.TranscriptPayload{Hydration: &clientui.TranscriptHydration{
			CommittedRows: rows,
		}},
	}
}

func renderTerminalMessageForTest(t *testing.T, message clientui.TranscriptMessage, frame FrameInput) string {
	t.Helper()
	var out bytes.Buffer
	if _, err := NewSurface(&out).ApplyTerminalMessage(message, frame); err != nil {
		t.Fatalf("apply terminal message: %v", err)
	}
	return out.String()
}

func userRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowUser,
		User: &clientui.TranscriptUserRow{
			StepID: assistantFinalizationStepID(),
			Text:   text,
		},
	}
}

func visibleRow(row clientui.TranscriptCommittedRow, visibility clientui.EntryVisibility) clientui.TranscriptCommittedRow {
	row.Visibility = visibility
	return row
}

func assistantRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			StepID: assistantFinalizationStepID(),
			Text:   text,
			Phase:  transcript.AssistantPhaseFinal,
		},
	}
}

func toolRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			StepID:     assistantFinalizationStepID(),
			ToolCallID: "71219421-994c-4eea-8280-fdc345770f22",
			ToolName:   "tool",
			Text:       text,
		},
	}
}

func noticeRow(text string) clientui.TranscriptCommittedRow {
	legacyText := text
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:     clientui.TranscriptNoticeLegacyUntypedNotice,
			Severity:   clientui.TranscriptNoticeInfo,
			LegacyText: &legacyText,
		},
	}
}

func backgroundNoticeRow(text string) clientui.TranscriptCommittedRow {
	messageType := clientui.TranscriptMessageBackgroundNotice
	compactLabel := text
	return clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:       clientui.TranscriptNoticeLegacyUntypedNotice,
			Severity:     clientui.TranscriptNoticeInfo,
			MessageType:  &messageType,
			CompactLabel: &compactLabel,
		},
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
	terminalOpOSC  terminalOpKind = "osc"
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
		if raw[i] == '\x1b' && i+1 < len(raw) && raw[i+1] == ']' {
			end := i + 2
			for end < len(raw) {
				if raw[end] == '\a' {
					end++
					break
				}
				if raw[end] == '\x1b' && end+1 < len(raw) && raw[end+1] == '\\' {
					end += 2
					break
				}
				end++
			}
			ops = append(ops, terminalOp{kind: terminalOpOSC, value: raw[i:end]})
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
		case terminalOpOSC:
			continue
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

// rowKind describes one immutable terminal row by structural role: either a
// blank group separator or a content row carrying the visible text.
type rowKind struct {
	separator bool
	content   string
}

func assertRowStructure(t *testing.T, rows []string, want []rowKind) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("visible rows = %#v, want %d rows of structure %#v", rows, len(want), want)
	}
	for idx, wantRow := range want {
		got := rows[idx]
		if wantRow.separator {
			if got != "" {
				t.Fatalf("row %d = %q, want a blank group separator", idx, got)
			}
			continue
		}
		if got == "" {
			t.Fatalf("row %d is blank, want content %q", idx, wantRow.content)
		}
		if got != wantRow.content {
			t.Fatalf("row %d = %q, want content %q", idx, got, wantRow.content)
		}
	}
}

func immutableAppendedRows(ops []terminalOp) []string {
	var candidate []string
	rows := make([]string, 0)
	current := ""
	started := false
	finishBlock := func() {
		if !started {
			return
		}
		rows = append(rows, strings.TrimRight(current, " "))
		for _, row := range rows {
			if row != "" {
				candidate = append([]string(nil), rows...)
				break
			}
		}
		rows = rows[:0]
		current = ""
		started = false
	}
	for _, op := range ops {
		switch op.kind {
		case terminalOpCRLF:
			if started {
				rows = append(rows, strings.TrimRight(current, " "))
				current = ""
			} else {
				started = true
			}
		case terminalOpCSI:
			if started && op.value == resetScrollRegionAndOriginMode()[:3] {
				finishBlock()
			}
		case terminalOpOSC:
			continue
		case terminalOpText:
			if started {
				current += ansi.Strip(op.value)
			}
		}
	}
	finishBlock()
	return candidate
}
