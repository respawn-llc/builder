package tui

import (
	"reflect"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"

	xansi "github.com/charmbracelet/x/ansi"
)

func defaultProjectionViewState() TranscriptProjectionViewState {
	return TranscriptProjectionViewState{ViewportWidth: 80, ViewportLines: 20, Theme: "dark"}
}

func TestCommittedOngoingProjectionRenderAppendDeltaFromAppendedEntry(t *testing.T) {
	m := NewModel(WithPreviewLines(20))
	m = updateModel(t, m, AppendTranscriptMsg{Role: "assistant", Text: "seed"})
	previous := m.CommittedOngoingProjection()

	m = updateModel(t, m, AppendTranscriptMsg{Role: "assistant", Text: "tail"})
	current := m.CommittedOngoingProjection()
	delta, ok := current.RenderAppendDeltaFrom(previous, TranscriptDivider)
	if !ok {
		t.Fatal("expected append-only committed projection delta")
	}
	if !strings.Contains(delta, "tail") {
		t.Fatalf("expected delta to include appended tail, got %q", delta)
	}
	if strings.Contains(delta, "seed") {
		t.Fatalf("expected delta to exclude already committed prefix, got %q", delta)
	}
}

func TestCommittedOngoingProjectorCachesByRevisionAndWidth(t *testing.T) {
	var projector CommittedOngoingProjector
	entries := []TranscriptEntry{{Role: "assistant", Text: "seed"}}
	key := CommittedOngoingProjectionKey{Revision: 7, Width: 80, Theme: "dark", EntryCount: len(entries)}

	initial := projector.Project(entries, key)
	entries[0].Text = "changed"
	sameKey := projector.Project(entries, key)
	if rendered := sameKey.Render(TranscriptDivider); !strings.Contains(rendered, "seed") || strings.Contains(rendered, "changed") {
		t.Fatalf("expected unchanged revision/width to reuse cached projection, got %q", rendered)
	}

	key.Revision = 8
	updated := projector.Project(entries, key)
	if rendered := updated.Render(TranscriptDivider); !strings.Contains(rendered, "changed") || strings.Contains(rendered, "seed") {
		t.Fatalf("expected advanced revision to rebuild projection, got %q", rendered)
	}
	if initial.Render(TranscriptDivider) == updated.Render(TranscriptDivider) {
		t.Fatalf("expected projection to change after revision advance")
	}
}

func TestCommittedOngoingProjectorDoesNotCacheWithoutRevision(t *testing.T) {
	var projector CommittedOngoingProjector
	entries := []TranscriptEntry{{Role: "assistant", Text: "seed"}}
	key := CommittedOngoingProjectionKey{Width: 80, Theme: "dark", EntryCount: len(entries)}

	_ = projector.Project(entries, key)
	entries[0].Text = "changed"
	updated := projector.Project(entries, key)
	if rendered := updated.Render(TranscriptDivider); !strings.Contains(rendered, "changed") || strings.Contains(rendered, "seed") {
		t.Fatalf("expected revisionless projection to rebuild, got %q", rendered)
	}
}

func TestCommittedOngoingProjectorPreservesBaseOffset(t *testing.T) {
	var projector CommittedOngoingProjector
	entries := []TranscriptEntry{{Role: "user", Text: "prompt"}, {Role: "assistant", Text: "answer"}}
	projection := projector.Project(entries, CommittedOngoingProjectionKey{
		Revision:   3,
		Width:      80,
		BaseOffset: 42,
		EntryCount: len(entries),
	})

	if len(projection.Blocks) != 2 {
		t.Fatalf("expected two projection blocks, got %#v", projection.Blocks)
	}
	if projection.Blocks[0].EntryIndex != 42 || projection.Blocks[1].EntryIndex != 43 {
		t.Fatalf("expected absolute entry indices from base offset, got %#v", projection.Blocks)
	}
}

func TestCommittedOngoingProjectionRenderAppendDeltaFromAssistantCommentaryContinuation(t *testing.T) {
	m := NewModel(WithPreviewLines(20))
	m = updateModel(t, m, AppendTranscriptMsg{Role: "assistant", Text: "Decision: keep", Phase: clientui.MessagePhaseCommentary})
	previous := m.CommittedOngoingProjection()

	m = updateModel(t, m, AppendTranscriptMsg{Role: "assistant", Text: "going"})
	current := m.CommittedOngoingProjection()
	delta, ok := current.RenderAppendDeltaFrom(previous, TranscriptDivider)
	if !ok {
		t.Fatal("expected append-only committed projection delta")
	}
	if strings.Contains(delta, TranscriptDivider) {
		t.Fatalf("expected assistant commentary continuation delta without divider, got %q", delta)
	}
	if !strings.Contains(delta, "going") {
		t.Fatalf("expected delta to include appended assistant continuation, got %q", delta)
	}
}

func TestRenderAssistantMarkdownProjectionMatchesCommittedAssistantEntry(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		phase clientui.MessagePhase
		width int
	}{
		{
			name:  "prose",
			text:  "Long **markdown** prose wraps with the same assistant prefix and styling.",
			phase: clientui.MessagePhaseFinal,
			width: 32,
		},
		{
			name:  "table",
			text:  "| Name | Value |\n| --- | --- |\n| alpha | beta |\n",
			phase: clientui.MessagePhaseFinal,
			width: 48,
		},
		{
			name:  "commentary",
			text:  "Decision: keep `commentary` visually aligned with final answers.",
			phase: clientui.MessagePhaseCommentary,
			width: 40,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			entries := []TranscriptEntry{{
				Role:  TranscriptRoleAssistant,
				Text:  tt.text,
				Phase: tt.phase,
			}}
			committed := ProjectCommittedOngoingTranscript(entries, "dark", tt.width).Lines(TranscriptDivider)
			streamed := RenderAssistantMarkdownProjection(tt.text, "dark", tt.width)

			if !reflect.DeepEqual(streamed, committed) {
				t.Fatalf("streaming projection mismatch\nstreamed=%#v\ncommitted=%#v", streamed, committed)
			}
		})
	}
}

func TestCommittedOngoingProjectionCommitFrontierWaitsForToolResult(t *testing.T) {
	m := NewModel(WithPreviewLines(20))
	m = updateModel(t, m, AppendTranscriptMsg{Role: "user", Text: "prompt"})
	base := m.CommittedOngoingProjection()

	m = updateModel(t, m, AppendTranscriptMsg{
		Role:       "tool_call",
		Text:       "pwd",
		ToolCallID: "call_1",
		ToolCall:   &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"},
	})
	pending := m.CommittedOngoingProjection()
	if rendered := pending.Render(TranscriptDivider); strings.Contains(rendered, "pwd") {
		t.Fatalf("expected unresolved tool call to stay out of committed projection, got %q", rendered)
	}

	m = updateModel(t, m, AppendTranscriptMsg{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call_1"})
	current := m.CommittedOngoingProjection()
	delta, ok := current.RenderAppendDeltaFrom(base, TranscriptDivider)
	if !ok {
		t.Fatal("expected tool completion to extend committed projection")
	}
	if !strings.Contains(delta, "pwd") {
		t.Fatalf("expected committed delta to include finalized tool call, got %q", delta)
	}
	if strings.Contains(delta, "prompt") {
		t.Fatalf("expected committed delta to exclude previously emitted prompt, got %q", delta)
	}
}

func TestPendingOngoingSnapshotLinesUsePerEntrySpinnerCallback(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "tool_call", Text: "echo alpha", ToolCallID: "call_alpha", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "echo alpha"}},
		{Role: "tool_call", Text: "echo beta", ToolCallID: "call_beta", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "echo beta"}},
	}
	seen := make([]string, 0, len(entries))

	lines := RenderPendingOngoingSnapshotLinesWithSpinnerFrames(entries, "dark", 80, func(entry TranscriptEntry, entryIndex int) string {
		seen = append(seen, entry.ToolCallID)
		if entryIndex == 0 {
			return "A"
		}
		return "B"
	})
	rendered := xansi.Strip(TranscriptProjection{Blocks: []TranscriptProjectionBlock{{
		DividerGroup: "tool",
		Lines: func() []string {
			out := make([]string, 0, len(lines))
			for _, line := range lines {
				out = append(out, line.Text)
			}
			return out
		}(),
	}}}.Render(TranscriptDivider))

	if !containsInOrder(rendered, "A echo alpha", "B echo beta") {
		t.Fatalf("expected per-entry pending spinner frames, got %q", rendered)
	}
	if len(seen) != 2 || seen[0] != "call_alpha" || seen[1] != "call_beta" {
		t.Fatalf("spinner callback entries = %+v, want call_alpha/call_beta", seen)
	}
}

func TestRenderAppendDeltaFromIgnoresHiddenSourceIndexShifts(t *testing.T) {
	previous := TranscriptProjection{Blocks: []TranscriptProjectionBlock{{
		Role:         "user",
		DividerGroup: "user",
		EntryIndex:   0,
		EntryEnd:     0,
		Lines:        []string{"❯ trigger"},
	}}}
	current := TranscriptProjection{Blocks: []TranscriptProjectionBlock{
		{
			Role:         "user",
			DividerGroup: "user",
			EntryIndex:   3,
			EntryEnd:     3,
			Lines:        []string{"❯ trigger"},
		},
		{
			Role:         "assistant",
			DividerGroup: "assistant",
			EntryIndex:   4,
			EntryEnd:     4,
			Lines:        []string{"❮ FINAL-CONTENT"},
		},
	}}

	delta, ok := current.RenderAppendDeltaFrom(previous, TranscriptDivider)
	if !ok {
		t.Fatal("expected append delta to survive hidden source index shifts")
	}
	if !strings.Contains(delta, "FINAL-CONTENT") {
		t.Fatalf("expected delta to include appended assistant content, got %q", delta)
	}
	if strings.Contains(delta, "trigger") {
		t.Fatalf("expected delta to exclude already rendered user content, got %q", delta)
	}
}

func TestTranscriptProjectionSharedPrefixBlockCountStopsAtFirstDivergence(t *testing.T) {
	previous := TranscriptProjection{Blocks: []TranscriptProjectionBlock{
		{Role: "user", DividerGroup: "user", Lines: []string{"❯ prompt"}},
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ before"}},
		{Role: "user", DividerGroup: "user", Lines: []string{"❯ later"}},
	}}
	current := TranscriptProjection{Blocks: []TranscriptProjectionBlock{
		{Role: "user", DividerGroup: "user", Lines: []string{"❯ prompt"}},
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ after"}},
		{Role: "user", DividerGroup: "user", Lines: []string{"❯ later"}},
	}}

	if got := current.SharedPrefixBlockCount(previous); got != 1 {
		t.Fatalf("expected shared prefix to stop before divergent assistant block, got %d", got)
	}
}

func TestTranscriptProjectionSharedPrefixBlockCountUsesShorterProjectionLength(t *testing.T) {
	previous := TranscriptProjection{Blocks: []TranscriptProjectionBlock{
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ one"}},
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ two"}},
	}}
	current := TranscriptProjection{Blocks: []TranscriptProjectionBlock{
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ one"}},
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ two"}},
		{Role: "assistant", DividerGroup: "assistant", Lines: []string{"❮ three"}},
	}}

	if got := current.SharedPrefixBlockCount(previous); got != 2 {
		t.Fatalf("expected shared prefix to include all shorter matching blocks, got %d", got)
	}
}

func TestCommittedOngoingEntriesDoNotTruncateAfterEmptyToolResult(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "tool_call", Text: "apply patch", ToolCallID: "call_patch", ToolCall: &transcript.ToolCallMeta{ToolName: "patch"}},
		{Role: "tool_result_ok", Text: "", ToolCallID: "call_patch"},
		{Role: "assistant", Text: "continued after empty result"},
	}

	committed := CommittedOngoingEntries(entries)
	if len(committed) != 4 {
		t.Fatalf("expected empty tool result marker preserved through committed frontier, got %#v", committed)
	}
	if committed[2].Role != "tool_result_ok" || committed[2].ToolCallID != "call_patch" {
		t.Fatalf("expected committed entries to keep empty tool result as structural status marker, got %#v", committed)
	}
	if committed[3].Role != "assistant" || committed[3].Text != "continued after empty result" {
		t.Fatalf("expected committed entries to include content after empty tool result, got %#v", committed)
	}

	pending := PendingOngoingEntries(entries)
	if len(pending) != 0 {
		t.Fatalf("expected no pending entries after empty tool result resolution, got %#v", pending)
	}
}

func TestCommittedOngoingProjectionPreservesSuccessStateForEmptyToolResult(t *testing.T) {
	m := NewModel(WithTheme("dark"), WithPreviewLines(20))
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 20, Width: 80})
	entries := []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "tool_call", Text: "apply patch", ToolCallID: "call_patch", ToolCall: &transcript.ToolCallMeta{ToolName: "patch", Command: "apply patch"}},
		{Role: "tool_result_ok", Text: "", ToolCallID: "call_patch"},
		{Role: "assistant", Text: "continued after empty result"},
	}

	projection := m.CommittedOngoingProjectionForEntries(entries)
	if len(projection.Blocks) < 3 {
		t.Fatalf("expected patch success block plus assistant tail, got %#v", projection.Blocks)
	}
	if got := projection.Blocks[1].Role; got != "tool_patch_success" {
		t.Fatalf("expected patch block to resolve to tool_patch_success after empty result, got %q (%#v)", got, projection.Blocks)
	}
	if !strings.Contains(strings.Join(projection.Blocks[1].Lines, "\n"), "apply patch") {
		t.Fatalf("expected patch success block to retain tool call text, got %#v", projection.Blocks[1])
	}
	if got := projection.Blocks[2].Role; got != "assistant" {
		t.Fatalf("expected assistant tail after patch success block, got %#v", projection.Blocks)
	}
}

func TestCommittedOngoingPrefixEndExcludesToolCallWhenMatchingResultIsTransient(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "tool_call", Text: "pwd", ToolCallID: "call_1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
		{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call_1", Transient: true},
	}

	if got := committedOngoingPrefixEnd(entries); got != 1 {
		t.Fatalf("committedOngoingPrefixEnd = %d, want 1", got)
	}
	committed := CommittedOngoingEntries(entries)
	if len(committed) != 1 || committed[0].Role != "user" {
		t.Fatalf("expected committed entries to exclude unresolved tool pair, got %#v", committed)
	}
}

func TestCommittedOngoingPrefixEndKeepsResolvedToolPairBeforeLaterTransientEntry(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "tool_call", Text: "pwd", ToolCallID: "call_1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
		{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call_1"},
		{Role: "assistant", Text: "streaming", Transient: true},
	}

	if got := committedOngoingPrefixEnd(entries); got != 3 {
		t.Fatalf("committedOngoingPrefixEnd = %d, want 3", got)
	}
	committed := CommittedOngoingEntries(entries)
	if len(committed) != 3 {
		t.Fatalf("expected committed entries to keep resolved tool pair, got %#v", committed)
	}
	if committed[1].Role != "tool_call" || committed[2].Role != "tool_result_ok" {
		t.Fatalf("expected committed tool pair before transient tail, got %#v", committed)
	}
}

func TestCommittedOngoingProjectionPreservesWebSearchSuccessState(t *testing.T) {
	m := NewModel(WithTheme("dark"), WithPreviewLines(20))
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 20, Width: 80})
	entries := []TranscriptEntry{
		{Role: "tool_call", Text: `web search: "latest golang release"`, ToolCallID: "call_web", ToolCall: &transcript.ToolCallMeta{ToolName: "web_search", Command: `web search: "latest golang release"`, CompactText: `web search: "latest golang release"`}},
		{Role: "tool_result_ok", Text: `{"type":"web_search_call","status":"completed"}`, ToolCallID: "call_web"},
	}

	projection := m.CommittedOngoingProjectionForEntries(entries)
	if len(projection.Blocks) != 1 {
		t.Fatalf("expected a single merged web search success block, got %#v", projection.Blocks)
	}
	if got := projection.Blocks[0].Role; got != "tool_web_search_success" {
		t.Fatalf("expected web search block to resolve to tool_web_search_success, got %q (%#v)", got, projection.Blocks)
	}
	if !strings.Contains(strings.Join(projection.Blocks[0].Lines, "\n"), `web search: "latest golang release"`) {
		t.Fatalf("expected web search success block to retain tool call text, got %#v", projection.Blocks[0])
	}
}

func TestProjectTranscriptViewsDerivesOngoingAndDetailFromSameCanonicalEntries(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "assistant", Text: "answer"},
	}

	projection := ProjectTranscriptViews(TranscriptProjectionInput{
		BaseOffset: 10,
		Entries:    entries,
	}, defaultProjectionViewState())

	if len(projection.Ongoing.Blocks) != 2 {
		t.Fatalf("expected ongoing projection from canonical entries, got %#v", projection.Ongoing.Blocks)
	}
	if len(projection.Detail.Blocks) != 2 {
		t.Fatalf("expected detail projection from canonical entries, got %#v", projection.Detail.Blocks)
	}
	if projection.Ongoing.Blocks[0].EntryIndex != 10 || projection.Detail.Blocks[0].EntryIndex != 10 {
		t.Fatalf("expected projections to preserve shared base offset, ongoing=%#v detail=%#v", projection.Ongoing.Blocks, projection.Detail.Blocks)
	}
	if projection.Ongoing.Blocks[1].EntryIndex != projection.Detail.Blocks[1].EntryIndex {
		t.Fatalf("expected ongoing/detail entry identity parity, ongoing=%#v detail=%#v", projection.Ongoing.Blocks, projection.Detail.Blocks)
	}
}

func TestDetailProjectionEmptySeparatorsRemainContentLines(t *testing.T) {
	projection := ProjectTranscriptViews(TranscriptProjectionInput{
		Entries: []TranscriptEntry{
			{Role: "user", Text: "prompt"},
			{Role: "assistant", Text: "answer"},
		},
	}, defaultProjectionViewState())

	if len(projection.DetailLines) < 3 {
		t.Fatalf("expected detail projection to include blank separator, got %#v", projection.DetailLines)
	}
	separator := projection.DetailLines[1]
	if separator.Text != "" {
		t.Fatalf("expected empty detail separator text, got %q in %#v", separator.Text, projection.DetailLines)
	}
	if separator.Kind != VisibleLineContent {
		t.Fatalf("expected empty detail separator to remain content, got %v", separator.Kind)
	}
}

func TestCommittedOngoingProjectionCacheTracksSelectedUserState(t *testing.T) {
	m := NewModel(WithTheme("dark"), WithPreviewLines(20))
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 20, Width: 80})
	m = updateModel(t, m, SetConversationMsg{Entries: []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "assistant", Text: "answer"},
	}})
	unselected := m.OngoingSnapshot()

	m = updateModel(t, m, SetSelectedTranscriptEntryMsg{EntryIndex: 0, Active: true})
	selected := m.OngoingSnapshot()

	if selected == unselected {
		t.Fatal("expected selected user entry to invalidate cached ongoing projection styling")
	}
	if !strings.Contains(selected, "48;2;") {
		t.Fatalf("expected selected ongoing snapshot to include selection background, got %q", selected)
	}
}

func TestProjectTranscriptViewsMapsSelectionEntryToDetailLines(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "second line one\nsecond line two"},
		{Role: "user", Text: "third"},
	}

	state := defaultProjectionViewState()
	state.DetailSelectedEntry = 21
	state.DetailSelectedActive = true
	state.SelectedEntry = 21
	state.SelectedEntryIsActive = true
	projection := ProjectTranscriptViews(TranscriptProjectionInput{
		BaseOffset: 20,
		Entries:    entries,
	}, state)

	lineRange, ok := projection.DetailEntryLineRanges[21]
	if !ok {
		t.Fatalf("expected detail range for selected entry, got %#v", projection.DetailEntryLineRanges)
	}
	if lineRange.Start < 0 || lineRange.End < lineRange.Start {
		t.Fatalf("invalid selected entry range: %#v", lineRange)
	}
	for idx := lineRange.Start; idx <= lineRange.End; idx++ {
		if projection.DetailLineOwners[idx] != 21 {
			t.Fatalf("expected selected range line %d to be owned by entry 21, owners=%#v", idx, projection.DetailLineOwners)
		}
	}
}

func TestProjectTranscriptViewsDoesNotMutateInputModel(t *testing.T) {
	m := NewModel(WithTheme("dark"), WithPreviewLines(20), WithCompactDetail())
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 20, Width: 80})
	m = updateModel(t, m, SetConversationMsg{Entries: []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "assistant", Text: "answer"},
	}})
	m.selectedTranscriptEntry = 0
	m.selectedTranscriptActive = true

	before := m
	_ = ProjectTranscriptViews(m.TranscriptProjectionInput(), m.TranscriptProjectionViewState())

	if before.detailBottomAnchor != m.detailBottomAnchor ||
		before.detailBottomOffset != m.detailBottomOffset {
		t.Fatalf("projection mutated model cache state before=%#v after=%#v", before, m)
	}
}

func TestModelViewDoesNotMutateCanonicalProjectionState(t *testing.T) {
	m := NewModel(WithTheme("dark"), WithPreviewLines(5), WithCompactDetail())
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 5, Width: 80})
	m = updateModel(t, m, SetConversationMsg{Entries: []TranscriptEntry{
		{Role: "user", Text: "prompt"},
		{Role: "assistant", Text: "answer"},
	}})
	m = updateModel(t, m, ToggleModeMsg{})

	beforeInput := m.TranscriptProjectionInput()
	beforeState := m.TranscriptProjectionViewState()
	beforeScroll := m.DetailScroll()
	beforeOngoingScroll := m.OngoingScroll()

	_ = m.View()
	_ = m.VisibleLineKinds()

	afterInput := m.TranscriptProjectionInput()
	afterState := m.TranscriptProjectionViewState()
	if !reflect.DeepEqual(beforeInput, afterInput) ||
		!reflect.DeepEqual(beforeState, afterState) ||
		beforeScroll != m.DetailScroll() ||
		beforeOngoingScroll != m.OngoingScroll() {
		t.Fatalf("rendering mutated canonical/view state before input=%+v state=%+v after input=%+v state=%+v", beforeInput, beforeState, afterInput, afterState)
	}
}

func TestModelDoesNotStoreLegacyRenderCacheGraph(t *testing.T) {
	forbidden := map[string]struct{}{
		"transcript":                {},
		"transcriptBaseOffset":      {},
		"transcriptTotalEntries":    {},
		"transcriptRevision":        {},
		"transcriptEntriesRevision": {},
		"ongoing":                   {},
		"streamingReasoning":        {},
		"ongoingSnapshot":           {},
		"ongoingLineCache":          {},
		"ongoingLineKinds":          {},
		"ongoingBaseLines":          {},
		"ongoingBaseLineKinds":      {},
		"ongoingBaseLastGroup":      {},
		"ongoingStreamingLines":     {},
		"ongoingStreamingKinds":     {},
		"ongoingStreamingDivider":   {},
		"ongoingBaseDirty":          {},
		"ongoingDirty":              {},
		"detailSnapshot":            {},
		"detailLines":               {},
		"detailLineKinds":           {},
		"detailLineEntryIndices":    {},
		"detailEntryLineRanges":     {},
		"detailBlockLineRanges":     {},
		"detailEntryRangeOffset":    {},
		"detailBlocks":              {},
		"detailBlockLines":          {},
		"detailTotalLineCount":      {},
		"detailMetricsResolved":     {},
		"detailDirty":               {},
		"detailStale":               {},
		"detailRebuildCount":        {},
	}
	modelType := reflect.TypeOf(Model{})
	if _, ok := modelType.FieldByName("transcriptInput"); !ok {
		t.Fatal("Model must store transcript data through one canonical transcriptInput field")
	}
	for idx := 0; idx < modelType.NumField(); idx++ {
		field := modelType.Field(idx)
		if _, ok := forbidden[field.Name]; ok {
			t.Fatalf("Model still stores legacy render cache field %q", field.Name)
		}
	}
}

func TestProjectTranscriptViewsExpansionChangesDetailProjectionOnly(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "tool_call", Text: "printf long", ToolCallID: "call_1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "printf long"}},
		{Role: "tool_result_ok", Text: strings.Repeat("line\n", 8), ToolCallID: "call_1"},
	}
	input := TranscriptProjectionInput{Entries: entries}
	collapsed := ProjectTranscriptViews(input, TranscriptProjectionViewState{ViewportWidth: 80, ViewportLines: 20, Theme: "dark", CompactDetail: true})
	expanded := ProjectTranscriptViews(input, TranscriptProjectionViewState{
		ViewportWidth:         80,
		ViewportLines:         20,
		Theme:                 "dark",
		CompactDetail:         true,
		DetailExpandedEntries: map[int]struct{}{0: {}},
	})

	if len(expanded.DetailLines) <= len(collapsed.DetailLines) {
		t.Fatalf("expected expanded detail projection to add visible detail lines, collapsed=%d expanded=%d", len(collapsed.DetailLines), len(expanded.DetailLines))
	}
	if collapsed.Ongoing.Render(TranscriptDivider) != expanded.Ongoing.Render(TranscriptDivider) {
		t.Fatalf("expected detail expansion to leave ongoing projection unchanged")
	}
}

func TestProjectTranscriptViewsIncludesStreamingReasoningInDetailProjection(t *testing.T) {
	projection := ProjectTranscriptViews(TranscriptProjectionInput{
		Entries: []TranscriptEntry{{Role: "assistant", Text: "answer"}},
		StreamingReasoning: []StreamingReasoningEntry{{
			Key:  "r1",
			Role: TranscriptRoleReasoning,
			Text: "thinking now",
		}},
	}, defaultProjectionViewState())

	detail := xansi.Strip(projection.Detail.Render(detailItemSeparator))
	if !strings.Contains(detail, "thinking now") {
		t.Fatalf("expected detail projection to include streaming reasoning, got %q", detail)
	}
	ongoing := xansi.Strip(projection.Ongoing.Render(TranscriptDivider))
	if strings.Contains(ongoing, "thinking now") {
		t.Fatalf("expected ongoing projection to exclude streaming reasoning, got %q", ongoing)
	}
}

func TestProjectTranscriptViewsToolCallRenderingUsesSameEntryMapping(t *testing.T) {
	entries := []TranscriptEntry{
		{Role: "tool_call", Text: "pwd", ToolCallID: "call_1", ToolCall: &transcript.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
		{Role: "tool_result_ok", Text: "/tmp/project", ToolCallID: "call_1"},
	}

	projection := ProjectTranscriptViews(TranscriptProjectionInput{BaseOffset: 30, Entries: entries}, defaultProjectionViewState())

	if len(projection.Ongoing.Blocks) != 1 || projection.Ongoing.Blocks[0].EntryIndex != 30 || projection.Ongoing.Blocks[0].EntryEnd != 31 {
		t.Fatalf("expected ongoing tool projection to merge call/result entry range, got %#v", projection.Ongoing.Blocks)
	}
	if len(projection.Detail.Blocks) != 1 || projection.Detail.Blocks[0].EntryIndex != 30 || projection.Detail.Blocks[0].EntryEnd != 31 {
		t.Fatalf("expected detail tool projection to merge call/result entry range, got %#v", projection.Detail.Blocks)
	}
}

func TestProjectTranscriptViewsViewportResizeChangesProjectionWrapping(t *testing.T) {
	input := TranscriptProjectionInput{Entries: []TranscriptEntry{{Role: "assistant", Text: strings.Repeat("x", 90)}}}
	wide := ProjectTranscriptViews(input, TranscriptProjectionViewState{ViewportWidth: 120, ViewportLines: 20, Theme: "dark"})
	narrow := ProjectTranscriptViews(input, TranscriptProjectionViewState{ViewportWidth: 40, ViewportLines: 20, Theme: "dark"})

	if len(narrow.OngoingLines) <= len(wide.OngoingLines) {
		t.Fatalf("expected narrower viewport to increase wrapped ongoing line count, wide=%d narrow=%d", len(wide.OngoingLines), len(narrow.OngoingLines))
	}
	if len(narrow.DetailLines) <= len(wide.DetailLines) {
		t.Fatalf("expected narrower viewport to increase wrapped detail line count, wide=%d narrow=%d", len(wide.DetailLines), len(narrow.DetailLines))
	}
}

func TestProjectTranscriptViewsLargeTranscriptKeepsLastEntryRange(t *testing.T) {
	entries := benchmarkDetailEntries(500)
	baseOffset := 1000
	lastEntry := baseOffset + len(entries) - 1

	projection := ProjectTranscriptViews(TranscriptProjectionInput{BaseOffset: baseOffset, Entries: entries}, TranscriptProjectionViewState{ViewportWidth: 100, ViewportLines: 30, Theme: "dark"})

	if len(projection.Detail.Blocks) == 0 || len(projection.Ongoing.Blocks) == 0 {
		t.Fatalf("expected large transcript projections to contain blocks")
	}
	lineRange, ok := projection.DetailEntryLineRanges[lastEntry]
	if !ok {
		t.Fatalf("expected detail range for last entry %d", lastEntry)
	}
	if lineRange.End < lineRange.Start || lineRange.Start < 0 {
		t.Fatalf("expected valid last entry line range, got %#v", lineRange)
	}
}

func TestTranscriptViewProjectionDetailViewportPreservesScrollAndOwners(t *testing.T) {
	projection := ProjectTranscriptViews(TranscriptProjectionInput{Entries: []TranscriptEntry{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "second"},
		{Role: "user", Text: "third"},
	}}, defaultProjectionViewState())

	viewport := projection.DetailViewport(ProjectionViewportState{ViewportLines: 2, Scroll: 2})
	if viewport.Scroll != 2 {
		t.Fatalf("expected viewport scroll to stay at requested position, got %d", viewport.Scroll)
	}
	if len(viewport.Lines) != 2 || len(viewport.Owners) != 2 {
		t.Fatalf("expected two viewport lines with owners, got lines=%#v owners=%#v", viewport.Lines, viewport.Owners)
	}
	if viewport.Owners[0] < 0 {
		t.Fatalf("expected first visible line to retain an entry owner, owners=%#v", viewport.Owners)
	}
}

func TestTranscriptViewProjectionDetailViewportBottomAnchorUsesOffset(t *testing.T) {
	projection := ProjectTranscriptViews(TranscriptProjectionInput{Entries: benchmarkDetailEntries(4)}, TranscriptProjectionViewState{ViewportWidth: 80, ViewportLines: 3, Theme: "dark"})

	viewport := projection.DetailViewport(ProjectionViewportState{ViewportLines: 3, BottomAnchor: true, BottomOffset: 2})
	if viewport.Scroll != viewport.MaxScroll-2 {
		t.Fatalf("expected bottom offset to anchor viewport above bottom, scroll=%d max=%d", viewport.Scroll, viewport.MaxScroll)
	}
	if viewport.BottomOffset != 2 {
		t.Fatalf("expected bottom offset to be preserved, got %d", viewport.BottomOffset)
	}
}

func TestTranscriptViewProjectorCachesByRevisionAndViewState(t *testing.T) {
	var projector TranscriptViewProjector
	input := TranscriptProjectionInput{
		Revision: 1,
		Entries:  []TranscriptEntry{{Role: "assistant", Text: "seed " + strings.Repeat("x", 90)}},
	}
	state := TranscriptProjectionViewState{ViewportWidth: 80, ViewportLines: 20, Theme: "dark"}

	_ = projector.project(input, state, true)
	input.Entries[0].Text = "changed " + strings.Repeat("x", 90)
	cached := projector.project(input, state, true)
	if rendered := cached.Ongoing.Render(TranscriptDivider); !strings.Contains(rendered, "seed") || strings.Contains(rendered, "changed") {
		t.Fatalf("expected same revision/view state to reuse cached projection, got %q", rendered)
	}

	input.Revision = 2
	updated := projector.project(input, state, true)
	if rendered := updated.Ongoing.Render(TranscriptDivider); !strings.Contains(rendered, "changed") || strings.Contains(rendered, "seed") {
		t.Fatalf("expected advanced revision to rebuild projection, got %q", rendered)
	}

	state.ViewportWidth = 40
	resized := projector.project(input, state, true)
	if resized.Ongoing.Render(TranscriptDivider) == updated.Ongoing.Render(TranscriptDivider) {
		t.Fatalf("expected viewport width change to rebuild projection")
	}
}

func TestTranscriptViewProjectorReturnsImmutableProjectionClone(t *testing.T) {
	var projector TranscriptViewProjector
	input := TranscriptProjectionInput{
		Revision: 1,
		Entries:  []TranscriptEntry{{Role: "assistant", Text: "seed"}},
	}
	state := TranscriptProjectionViewState{ViewportWidth: 80, ViewportLines: 20, Theme: "dark"}

	first := projector.project(input, state, true)
	first.Ongoing.Blocks[0].Lines[0] = "mutated"
	first.OngoingLines[0].Text = "mutated"
	first.OngoingLineOwners[0] = 999
	first.OngoingEntryLineRanges[0] = lineRange{Start: 999, End: 999}

	second := projector.project(input, state, true)
	rendered := second.Ongoing.Render(TranscriptDivider)
	if strings.Contains(rendered, "mutated") || !strings.Contains(rendered, "seed") {
		t.Fatalf("expected cached projection to be immutable to caller mutation, got %q", rendered)
	}
	if second.OngoingLines[0].Text == "mutated" {
		t.Fatalf("expected projection lines to be cloned")
	}
	if second.OngoingLineOwners[0] == 999 {
		t.Fatalf("expected line owners to be cloned")
	}
	if second.OngoingEntryLineRanges[0].Start == 999 {
		t.Fatalf("expected entry ranges to be cloned")
	}
}

func TestModelTranscriptProjectionInputRevisionAdvancesOnCanonicalInputChanges(t *testing.T) {
	m := NewModel(WithPreviewLines(20))
	initial := m.TranscriptProjectionInput().Revision

	m = updateModel(t, m, AppendTranscriptMsg{Role: "user", Text: "prompt"})
	afterTranscript := m.TranscriptProjectionInput()
	if afterTranscript.Revision <= initial || afterTranscript.EntriesRevision <= 0 {
		t.Fatalf("expected transcript append to advance projection revisions, got %+v initial=%d", afterTranscript, initial)
	}

	m = updateModel(t, m, StreamAssistantMsg{Delta: "stream"})
	afterStreaming := m.TranscriptProjectionInput()
	if afterStreaming.Revision <= afterTranscript.Revision {
		t.Fatalf("expected streaming assistant to advance projection revision, before=%d after=%d", afterTranscript.Revision, afterStreaming.Revision)
	}
	if afterStreaming.EntriesRevision != afterTranscript.EntriesRevision {
		t.Fatalf("expected streaming assistant to keep committed entries revision stable, before=%d after=%d", afterTranscript.EntriesRevision, afterStreaming.EntriesRevision)
	}

	m = updateModel(t, m, UpsertStreamingReasoningMsg{Key: "r1", Role: "reasoning", Text: "thinking"})
	afterReasoning := m.TranscriptProjectionInput().Revision
	if afterReasoning <= afterStreaming.Revision {
		t.Fatalf("expected streaming reasoning to advance projection revision, before=%d after=%d", afterStreaming.Revision, afterReasoning)
	}
}

func TestModelTranscriptProjectionInputRevisionDoesNotAdvanceOnViewStateOnlyChanges(t *testing.T) {
	m := NewModel(WithPreviewLines(20))
	m = updateModel(t, m, SetConversationMsg{Entries: []TranscriptEntry{{Role: "user", Text: "prompt"}}})
	before := m.TranscriptProjectionInput().Revision

	m = updateModel(t, m, SetSelectedTranscriptEntryMsg{EntryIndex: 0, Active: true, RefreshDetailSnapshot: true})
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 10, Width: 100})

	after := m.TranscriptProjectionInput().Revision
	if after != before {
		t.Fatalf("expected view state changes to keep canonical projection revision stable, before=%d after=%d", before, after)
	}
}
