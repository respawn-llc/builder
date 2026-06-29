package tui

import (
	"sort"
	"strings"

	sharedtheme "core/shared/theme"

	xansi "github.com/charmbracelet/x/ansi"
)

type TranscriptProjection struct {
	Blocks []TranscriptProjectionBlock
}

// CommittedOngoingProjectionKey identifies the render-affecting inputs for the
// committed ongoing transcript projection. Revision must advance when transcript
// content changes; revisionless keys intentionally skip projection caching.
type CommittedOngoingProjectionKey struct {
	Revision              int64
	Width                 int
	Theme                 string
	BaseOffset            int
	EntryCount            int
	CompactDetail         bool
	SelectedEntry         int
	SelectedEntryIsActive bool
}

// CommittedOngoingProjector reuses the ongoing transcript renderer and caches
// committed projections by transcript revision and terminal width.
type CommittedOngoingProjector struct {
	key           CommittedOngoingProjectionKey
	projection    TranscriptProjection
	projectionSet bool
	renderer      Model
	rendererSet   bool
	rendererTheme string
	rendererWidth int
}

type TranscriptProjectionInput struct {
	Revision           int64
	EntriesRevision    int64
	BaseOffset         int
	TotalEntries       int
	Entries            []TranscriptEntry
	Ongoing            string
	StreamingReasoning []StreamingReasoningEntry
}

type TranscriptProjectionViewState struct {
	ViewportWidth         int
	ViewportLines         int
	Theme                 string
	CompactDetail         bool
	SelectedEntry         int
	SelectedEntryIsActive bool
	DetailSelectedEntry   int
	DetailSelectedActive  bool
	DetailExpandedEntries map[int]struct{}
}

type TranscriptViewProjectionKey struct {
	Revision              int64
	BaseOffset            int
	TotalEntries          int
	EntryCount            int
	ViewportWidth         int
	ViewportLines         int
	Theme                 string
	CompactDetail         bool
	SelectedEntry         int
	SelectedEntryIsActive bool
	DetailSelectedEntry   int
	DetailSelectedActive  bool
	ExpandedEntries       []int
}

type TranscriptViewProjector struct {
	key                  TranscriptViewProjectionKey
	projection           TranscriptViewProjection
	projectionSet        bool
	detailKey            TranscriptViewProjectionKey
	detailProjection     TranscriptProjection
	detailSet            bool
	detailViewKey        TranscriptViewProjectionKey
	detailViewProjection TranscriptViewProjection
	detailViewSet        bool
	ongoingKey           CommittedOngoingProjectionKey
	ongoingLines         []TranscriptProjectionLine
	ongoingGroup         string
	ongoingSet           bool
	streamingKey         ongoingStreamingProjectionKey
	streamingLines       []TranscriptProjectionLine
	streamingSet         bool
	detailStreamingKey   ongoingStreamingProjectionKey
	detailStreamingLines []string
	detailStreamingSet   bool
}

type ongoingStreamingProjectionKey struct {
	Text  string
	Theme string
	Width int
}

type TranscriptViewProjection struct {
	InputRevision          int64
	Ongoing                TranscriptProjection
	Detail                 TranscriptProjection
	OngoingLines           []TranscriptProjectionLine
	DetailLines            []TranscriptProjectionLine
	OngoingLineOwners      []int
	DetailLineOwners       []int
	OngoingEntryLineRanges map[int]lineRange
	DetailEntryLineRanges  map[int]lineRange
	DetailSelectableBlocks map[int]int
}

type ProjectionViewportState struct {
	ViewportLines int
	Scroll        int
	BottomAnchor  bool
	BottomOffset  int
}

type ProjectionViewport struct {
	Lines        []string
	Kinds        []VisibleLineKind
	Owners       []int
	TotalLines   int
	MaxScroll    int
	Scroll       int
	BottomOffset int
}

func (p TranscriptViewProjection) Clone() TranscriptViewProjection {
	return TranscriptViewProjection{
		InputRevision:          p.InputRevision,
		Ongoing:                p.Ongoing.Clone(),
		Detail:                 p.Detail.Clone(),
		OngoingLines:           append([]TranscriptProjectionLine(nil), p.OngoingLines...),
		DetailLines:            append([]TranscriptProjectionLine(nil), p.DetailLines...),
		OngoingLineOwners:      append([]int(nil), p.OngoingLineOwners...),
		DetailLineOwners:       append([]int(nil), p.DetailLineOwners...),
		OngoingEntryLineRanges: cloneLineRangeMap(p.OngoingEntryLineRanges),
		DetailEntryLineRanges:  cloneLineRangeMap(p.DetailEntryLineRanges),
		DetailSelectableBlocks: cloneIntMap(p.DetailSelectableBlocks),
	}
}

func (p TranscriptViewProjection) DetailViewport(state ProjectionViewportState) ProjectionViewport {
	return projectionViewportFromLines(p.DetailLines, p.DetailLineOwners, state)
}

func projectionViewportFromLines(lines []TranscriptProjectionLine, owners []int, state ProjectionViewportState) ProjectionViewport {
	viewportLines := state.ViewportLines
	if viewportLines <= 0 {
		return ProjectionViewport{}
	}
	if len(lines) == 0 {
		lines = []TranscriptProjectionLine{{Kind: VisibleLineContent, Text: ""}}
		owners = []int{-1}
	}
	total := len(lines)
	maxScroll := max(0, total-viewportLines)
	scroll := clamp(state.Scroll, 0, maxScroll)
	bottomOffset := state.BottomOffset
	if bottomOffset < 0 {
		bottomOffset = 0
	}
	if bottomOffset > maxScroll {
		bottomOffset = maxScroll
	}
	if state.BottomAnchor {
		scroll = maxScroll - bottomOffset
	}
	end := scroll + viewportLines
	if end > total {
		end = total
	}
	outLines := make([]string, 0, viewportLines)
	outKinds := make([]VisibleLineKind, 0, viewportLines)
	outOwners := make([]int, 0, viewportLines)
	for idx := scroll; idx < end; idx++ {
		line := lines[idx]
		outLines = append(outLines, line.Text)
		outKinds = append(outKinds, line.Kind)
		if idx < len(owners) {
			outOwners = append(outOwners, owners[idx])
		} else {
			outOwners = append(outOwners, -1)
		}
	}
	return ProjectionViewport{
		Lines:        outLines,
		Kinds:        outKinds,
		Owners:       outOwners,
		TotalLines:   total,
		MaxScroll:    maxScroll,
		Scroll:       scroll,
		BottomOffset: bottomOffset,
	}
}

type TranscriptProjectionLine struct {
	Kind VisibleLineKind
	Text string
}

type TranscriptProjectionBlock struct {
	Role         RenderIntent
	DividerGroup string
	EntryIndex   int
	EntryEnd     int
	Selectable   bool
	Expanded     bool
	Expandable   bool
	Lines        []string
}

func (p TranscriptProjection) Empty() bool {
	return len(p.Blocks) == 0
}

func (p TranscriptProjection) Clone() TranscriptProjection {
	if len(p.Blocks) == 0 {
		return TranscriptProjection{}
	}
	blocks := make([]TranscriptProjectionBlock, 0, len(p.Blocks))
	for _, block := range p.Blocks {
		blocks = append(blocks, TranscriptProjectionBlock{
			Role:         block.Role,
			DividerGroup: block.DividerGroup,
			EntryIndex:   block.EntryIndex,
			EntryEnd:     block.EntryEnd,
			Selectable:   block.Selectable,
			Expanded:     block.Expanded,
			Expandable:   block.Expandable,
			Lines:        append([]string(nil), block.Lines...),
		})
	}
	return TranscriptProjection{Blocks: blocks}
}

func (p TranscriptProjection) Lines(dividerText string) []TranscriptProjectionLine {
	if len(p.Blocks) == 0 {
		return nil
	}
	lines := make([]TranscriptProjectionLine, 0, len(p.Blocks)*2)
	for idx, block := range p.Blocks {
		if idx > 0 && p.Blocks[idx-1].DividerGroup != block.DividerGroup {
			kind := VisibleLineDivider
			if dividerText == "" {
				kind = VisibleLineContent
			}
			lines = append(lines, TranscriptProjectionLine{Kind: kind, Text: dividerText})
		}
		for _, line := range block.Lines {
			lines = append(lines, TranscriptProjectionLine{Kind: VisibleLineContent, Text: line})
		}
	}
	return lines
}

func (p TranscriptProjection) LineOwners() []int {
	if len(p.Blocks) == 0 {
		return nil
	}
	owners := make([]int, 0, len(p.Blocks)*2)
	for idx, block := range p.Blocks {
		if idx > 0 && p.Blocks[idx-1].DividerGroup != block.DividerGroup {
			owners = append(owners, -1)
		}
		for range block.Lines {
			owners = append(owners, block.EntryIndex)
		}
	}
	return owners
}

func (p TranscriptProjection) EntryLineRanges() map[int]lineRange {
	if len(p.Blocks) == 0 {
		return nil
	}
	ranges := make(map[int]lineRange)
	lineOffset := 0
	for idx, block := range p.Blocks {
		if idx > 0 && p.Blocks[idx-1].DividerGroup != block.DividerGroup {
			lineOffset++
		}
		if block.EntryIndex >= 0 && len(block.Lines) > 0 {
			start := lineOffset
			end := lineOffset + len(block.Lines) - 1
			if existing, ok := ranges[block.EntryIndex]; ok {
				ranges[block.EntryIndex] = lineRange{Start: existing.Start, End: end}
			} else {
				ranges[block.EntryIndex] = lineRange{Start: start, End: end}
			}
		}
		lineOffset += len(block.Lines)
	}
	return ranges
}

func (p TranscriptProjection) SelectableBlockIndexes() map[int]int {
	if len(p.Blocks) == 0 {
		return nil
	}
	indexes := make(map[int]int, len(p.Blocks))
	for idx, block := range p.Blocks {
		if !block.Selectable || block.EntryIndex < 0 {
			continue
		}
		if _, ok := indexes[block.EntryIndex]; !ok {
			indexes[block.EntryIndex] = idx
		}
	}
	return indexes
}

func (p TranscriptProjection) Render(divider string) string {
	lines := p.Lines(divider)
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Text)
	}
	return strings.Join(out, "\n")
}

func (p TranscriptProjection) RenderWithBlockSeparator(separator string) string {
	if len(p.Blocks) == 0 {
		return ""
	}
	lines := make([]string, 0, len(p.Blocks)*2)
	for idx, block := range p.Blocks {
		if idx > 0 {
			lines = append(lines, separator)
		}
		lines = append(lines, block.Lines...)
	}
	return strings.Join(lines, "\n")
}

func (p TranscriptProjection) RenderAppendDeltaFrom(previous TranscriptProjection, divider string) (string, bool) {
	if len(previous.Blocks) == 0 {
		return p.Render(divider), true
	}
	if len(previous.Blocks) > len(p.Blocks) {
		return "", false
	}
	for idx, prior := range previous.Blocks {
		if !prior.equal(p.Blocks[idx]) {
			return "", false
		}
	}
	if len(previous.Blocks) == len(p.Blocks) {
		return "", true
	}
	return p.renderFromBlock(len(previous.Blocks), divider), true
}

func (p TranscriptProjection) RenderNativeStableAppendDeltaFrom(previous TranscriptProjection, divider string) (string, bool) {
	if len(previous.Blocks) == 0 {
		return p.Render(divider), true
	}
	if len(previous.Blocks) > len(p.Blocks) {
		return "", false
	}
	for idx, prior := range previous.Blocks {
		if !prior.nativeStableEqual(p.Blocks[idx]) {
			return "", false
		}
	}
	if len(previous.Blocks) == len(p.Blocks) {
		return "", true
	}
	return p.renderFromBlock(len(previous.Blocks), divider), true
}

func (p TranscriptProjection) SharedPrefixBlockCount(other TranscriptProjection) int {
	limit := min(len(p.Blocks), len(other.Blocks))
	for idx := 0; idx < limit; idx++ {
		if !p.Blocks[idx].equal(other.Blocks[idx]) {
			return idx
		}
	}
	return limit
}

func (p TranscriptProjection) SharedNativeStablePrefixBlockCount(other TranscriptProjection) int {
	limit := min(len(p.Blocks), len(other.Blocks))
	for idx := 0; idx < limit; idx++ {
		if !p.Blocks[idx].nativeStableEqual(other.Blocks[idx]) {
			return idx
		}
	}
	return limit
}

func (p TranscriptProjection) SharedNativeStableSuffixPrefixBlockCount(previous TranscriptProjection) int {
	limit := min(len(p.Blocks), len(previous.Blocks))
	for overlap := limit; overlap > 0; overlap-- {
		start := len(previous.Blocks) - overlap
		matches := true
		for idx := 0; idx < overlap; idx++ {
			if !p.Blocks[idx].nativeStableEqual(previous.Blocks[start+idx]) {
				matches = false
				break
			}
		}
		if matches {
			return overlap
		}
	}
	return 0
}

func (p TranscriptProjection) SharedSuffixPrefixBlockCount(previous TranscriptProjection) int {
	limit := min(len(p.Blocks), len(previous.Blocks))
	for overlap := limit; overlap > 0; overlap-- {
		start := len(previous.Blocks) - overlap
		matches := true
		for idx := 0; idx < overlap; idx++ {
			if !p.Blocks[idx].equal(previous.Blocks[start+idx]) {
				matches = false
				break
			}
		}
		if matches {
			return overlap
		}
	}
	return 0
}

func (p TranscriptProjection) LinesFromBlock(start int, dividerText string) []TranscriptProjectionLine {
	if start < 0 {
		start = 0
	}
	if start >= len(p.Blocks) {
		return nil
	}
	lines := make([]TranscriptProjectionLine, 0, (len(p.Blocks)-start)*2)
	for idx := start; idx < len(p.Blocks); idx++ {
		if idx > 0 && p.Blocks[idx-1].DividerGroup != p.Blocks[idx].DividerGroup {
			lines = append(lines, TranscriptProjectionLine{Kind: VisibleLineDivider, Text: dividerText})
		}
		for _, line := range p.Blocks[idx].Lines {
			lines = append(lines, TranscriptProjectionLine{Kind: VisibleLineContent, Text: line})
		}
	}
	return lines
}

func (p TranscriptProjection) renderFromBlock(start int, divider string) string {
	if start < 0 {
		start = 0
	}
	if start >= len(p.Blocks) {
		return ""
	}
	lines := make([]string, 0, (len(p.Blocks)-start)*2)
	for idx := start; idx < len(p.Blocks); idx++ {
		if idx > 0 && p.Blocks[idx-1].DividerGroup != p.Blocks[idx].DividerGroup {
			lines = append(lines, divider)
		}
		lines = append(lines, p.Blocks[idx].Lines...)
	}
	return strings.Join(lines, "\n")
}

func (b TranscriptProjectionBlock) equal(other TranscriptProjectionBlock) bool {
	if b.Role != other.Role || b.DividerGroup != other.DividerGroup || len(b.Lines) != len(other.Lines) {
		return false
	}
	if b.Selectable != other.Selectable || b.Expanded != other.Expanded || b.Expandable != other.Expandable {
		return false
	}
	for idx := range b.Lines {
		if b.Lines[idx] != other.Lines[idx] {
			return false
		}
	}
	return true
}

func (b TranscriptProjectionBlock) nativeStableEqual(other TranscriptProjectionBlock) bool {
	if b.Role != other.Role || b.DividerGroup != other.DividerGroup || len(b.Lines) != len(other.Lines) {
		return false
	}
	for idx := range b.Lines {
		if b.Lines[idx] != other.Lines[idx] {
			return false
		}
	}
	return true
}

func (m Model) CommittedOngoingProjection() TranscriptProjection {
	return m.CommittedOngoingProjectionForEntries(m.transcriptInput.Entries)
}

func (m Model) CommittedOngoingProjectionForEntries(entries []TranscriptEntry) TranscriptProjection {
	target := m.transcriptInput.Entries
	if len(entries) > 0 {
		target = entries
	}
	return projectCommittedOngoingTranscriptWithRenderer(m, target)
}

func (m Model) TranscriptProjectionInput() TranscriptProjectionInput {
	input := m.transcriptProjectionInput()
	input.Entries = cloneTranscriptEntries(input.Entries)
	input.StreamingReasoning = append([]StreamingReasoningEntry(nil), input.StreamingReasoning...)
	return input
}

func (m Model) transcriptProjectionInput() TranscriptProjectionInput {
	return TranscriptProjectionInput{
		Revision:           m.transcriptInput.Revision,
		EntriesRevision:    m.transcriptInput.EntriesRevision,
		BaseOffset:         m.transcriptInput.BaseOffset,
		TotalEntries:       m.TranscriptTotalEntries(),
		Entries:            m.transcriptInput.Entries,
		Ongoing:            m.transcriptInput.Ongoing,
		StreamingReasoning: m.transcriptInput.StreamingReasoning,
	}
}

func (m Model) TranscriptProjectionViewState() TranscriptProjectionViewState {
	return TranscriptProjectionViewState{
		ViewportWidth:         m.viewportWidth,
		ViewportLines:         m.viewportLines,
		Theme:                 m.theme,
		CompactDetail:         m.compactDetail,
		SelectedEntry:         m.selectedTranscriptEntry,
		SelectedEntryIsActive: m.selectedTranscriptActive,
		DetailSelectedEntry:   m.detailSelectedEntry,
		DetailSelectedActive:  m.detailSelectedActive,
		DetailExpandedEntries: cloneExpandedEntrySet(m.detailExpandedEntries),
	}
}

func ProjectTranscriptViews(input TranscriptProjectionInput, state TranscriptProjectionViewState) TranscriptViewProjection {
	renderer := transcriptProjectionRenderer(state.Theme, state.ViewportWidth, input.BaseOffset)
	if state.ViewportLines > 0 {
		renderer.viewportLines = state.ViewportLines
	}
	renderer.compactDetail = state.CompactDetail
	renderer.selectedTranscriptEntry = state.SelectedEntry
	renderer.selectedTranscriptActive = state.SelectedEntryIsActive
	renderer.detailSelectedEntry = state.DetailSelectedEntry
	renderer.detailSelectedActive = state.DetailSelectedActive
	renderer.detailExpandedEntries = cloneExpandedEntrySet(state.DetailExpandedEntries)
	renderer.transcriptInput.Entries = cloneTranscriptEntries(input.Entries)
	renderer.transcriptInput.BaseOffset = max(0, input.BaseOffset)
	renderer.transcriptInput.TotalEntries = input.TotalEntries
	if renderer.transcriptInput.TotalEntries < renderer.transcriptInput.BaseOffset+len(renderer.transcriptInput.Entries) {
		renderer.transcriptInput.TotalEntries = renderer.transcriptInput.BaseOffset + len(renderer.transcriptInput.Entries)
	}
	renderer.transcriptInput.Ongoing = input.Ongoing
	renderer.transcriptInput.StreamingReasoning = append([]StreamingReasoningEntry(nil), input.StreamingReasoning...)

	ongoing := projectionFromOngoingBlocks(renderer.buildTranscriptBlocks(transcriptBlockOptions{
		mode:             transcriptBlockModeOngoing,
		includeStreaming: true,
		applySelection:   true,
	}))
	detail := renderer.DetailProjection(true, true)
	return TranscriptViewProjection{
		InputRevision:          input.Revision,
		Ongoing:                ongoing,
		Detail:                 detail,
		OngoingLines:           ongoing.Lines(TranscriptDivider),
		DetailLines:            detail.Lines(detailItemSeparator),
		OngoingLineOwners:      ongoing.LineOwners(),
		DetailLineOwners:       detail.LineOwners(),
		OngoingEntryLineRanges: ongoing.EntryLineRanges(),
		DetailEntryLineRanges:  detail.EntryLineRanges(),
		DetailSelectableBlocks: detail.SelectableBlockIndexes(),
	}
}

func (p *TranscriptViewProjector) CommittedOngoingLines(input TranscriptProjectionInput, state TranscriptProjectionViewState) ([]TranscriptProjectionLine, string) {
	key := CommittedOngoingProjectionKey{
		Revision:              input.EntriesRevision,
		Width:                 state.ViewportWidth,
		Theme:                 state.Theme,
		BaseOffset:            input.BaseOffset,
		EntryCount:            len(input.Entries),
		CompactDetail:         state.CompactDetail,
		SelectedEntry:         state.SelectedEntry,
		SelectedEntryIsActive: state.SelectedEntryIsActive,
	}
	key = normalizeCommittedOngoingProjectionKey(key, len(input.Entries))
	if key.Revision > 0 && p != nil && p.ongoingSet && p.ongoingKey == key {
		return p.ongoingLines, p.ongoingGroup
	}
	renderer := transcriptProjectionRenderer(key.Theme, key.Width, key.BaseOffset)
	renderer.compactDetail = key.CompactDetail
	renderer.selectedTranscriptEntry = key.SelectedEntry
	renderer.selectedTranscriptActive = key.SelectedEntryIsActive
	projection := projectCommittedOngoingTranscriptWithRenderer(renderer, input.Entries)
	lines := projection.Lines(TranscriptDivider)
	lastGroup := ""
	if blockCount := len(projection.Blocks); blockCount > 0 {
		lastGroup = projection.Blocks[blockCount-1].DividerGroup
	}
	if key.Revision > 0 && p != nil {
		p.ongoingKey = key
		p.ongoingLines = lines
		p.ongoingGroup = lastGroup
		p.ongoingSet = true
	}
	return lines, lastGroup
}

func (p *TranscriptViewProjector) StreamingOngoingLines(text string, state TranscriptProjectionViewState) []TranscriptProjectionLine {
	if text == "" {
		return nil
	}
	key := ongoingStreamingProjectionKey{
		Text:  text,
		Theme: sharedtheme.Resolve(state.Theme),
		Width: state.ViewportWidth,
	}
	if key.Width <= 0 {
		key.Width = 120
	}
	if p != nil && p.streamingSet && p.streamingKey == key {
		return p.streamingLines
	}
	lines := RenderAssistantMarkdownStreamingProjection(text, key.Theme, key.Width)
	if p != nil {
		p.streamingKey = key
		p.streamingLines = lines
		p.streamingSet = true
	}
	return lines
}

func RenderAssistantMarkdownStreamingProjection(text string, theme string, width int) []TranscriptProjectionLine {
	if text == "" {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		return renderAssistantWhitespaceStreamingProjection(text, width)
	}
	return RenderAssistantMarkdownProjection(text, theme, width)
}

func RenderAssistantMarkdownProjection(text string, theme string, width int) []TranscriptProjectionLine {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	renderer := transcriptProjectionRenderer(theme, width, 0)
	flattened := renderer.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, text, false, nil, "")
	lines := make([]TranscriptProjectionLine, 0, len(flattened))
	for _, line := range flattened {
		lines = append(lines, TranscriptProjectionLine{Kind: VisibleLineContent, Text: line})
	}
	return lines
}

func renderAssistantWhitespaceStreamingProjection(text string, width int) []TranscriptProjectionLine {
	if width < 1 {
		width = 120
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	parts := strings.Split(normalized, "\n")
	if strings.HasSuffix(normalized, "\n") && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	lines := make([]TranscriptProjectionLine, 0, len(parts))
	for _, part := range parts {
		for _, row := range wrapAssistantWhitespaceLine(part, width) {
			lines = append(lines, TranscriptProjectionLine{Kind: VisibleLineContent, Text: row})
		}
	}
	return lines
}

func wrapAssistantWhitespaceLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	rows := []string{}
	remaining := line
	for xansi.StringWidth(remaining) > width {
		visualWidth := xansi.StringWidth(remaining)
		rows = append(rows, xansi.Cut(remaining, 0, width))
		remaining = xansi.Cut(remaining, width, visualWidth)
	}
	return append(rows, remaining)
}

func (p *TranscriptViewProjector) StreamingDetailAssistantLines(text string, state TranscriptProjectionViewState) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	key := ongoingStreamingProjectionKey{
		Text:  text,
		Theme: sharedtheme.Resolve(state.Theme),
		Width: state.ViewportWidth,
	}
	if key.Width <= 0 {
		key.Width = 120
	}
	if p != nil && p.detailStreamingSet && p.detailStreamingKey == key {
		return p.detailStreamingLines
	}
	renderer := transcriptProjectionRenderer(key.Theme, key.Width, 0)
	lines := renderer.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, text, false, nil, "")
	if p != nil {
		p.detailStreamingKey = key
		p.detailStreamingLines = lines
		p.detailStreamingSet = true
	}
	return lines
}

func (p *TranscriptViewProjector) ProjectDetailShared(input TranscriptProjectionInput, state TranscriptProjectionViewState) TranscriptViewProjection {
	key := NewTranscriptViewProjectionKey(input, state)
	if key.Revision > 0 && p != nil && p.detailViewSet && transcriptViewProjectionKeysEqual(p.detailViewKey, key) {
		return p.detailViewProjection
	}
	detail := p.detailProjectionFor(input, state)
	projection := transcriptViewProjectionForDetail(input.Revision, detail)
	if key.Revision > 0 && p != nil {
		p.detailViewKey = key
		p.detailViewProjection = projection
		p.detailViewSet = true
	}
	return projection
}

func (p *TranscriptViewProjector) detailProjectionFor(input TranscriptProjectionInput, state TranscriptProjectionViewState) TranscriptProjection {
	keyInput := input
	keyInput.Revision = input.EntriesRevision
	keyInput.Ongoing = ""
	keyInput.StreamingReasoning = nil
	key := NewTranscriptViewProjectionKey(keyInput, state)
	cacheable := key.Revision > 0
	var detail TranscriptProjection
	if cacheable && p != nil && p.detailSet && transcriptViewProjectionKeysEqual(p.detailKey, key) {
		detail = p.detailProjection
	} else {
		renderer := transcriptProjectionRenderer(state.Theme, state.ViewportWidth, input.BaseOffset)
		if state.ViewportLines > 0 {
			renderer.viewportLines = state.ViewportLines
		}
		renderer.compactDetail = state.CompactDetail
		renderer.selectedTranscriptEntry = state.SelectedEntry
		renderer.selectedTranscriptActive = state.SelectedEntryIsActive
		renderer.detailSelectedEntry = state.DetailSelectedEntry
		renderer.detailSelectedActive = state.DetailSelectedActive
		renderer.detailExpandedEntries = cloneExpandedEntrySet(state.DetailExpandedEntries)
		renderer.transcriptInput.Entries = cloneTranscriptEntries(input.Entries)
		renderer.transcriptInput.BaseOffset = max(0, input.BaseOffset)
		renderer.transcriptInput.TotalEntries = input.TotalEntries
		if renderer.transcriptInput.TotalEntries < renderer.transcriptInput.BaseOffset+len(renderer.transcriptInput.Entries) {
			renderer.transcriptInput.TotalEntries = renderer.transcriptInput.BaseOffset + len(renderer.transcriptInput.Entries)
		}
		detail = renderer.DetailProjection(false, true)
		if cacheable && p != nil {
			p.detailKey = key
			p.detailProjection = detail
			p.detailSet = true
		}
	}
	return appendDetailStreamingProjection(detail, input, state, p)
}

func transcriptViewProjectionForDetail(revision int64, detail TranscriptProjection) TranscriptViewProjection {
	return TranscriptViewProjection{
		InputRevision:          revision,
		Detail:                 detail,
		DetailLines:            detail.Lines(detailItemSeparator),
		DetailLineOwners:       detail.LineOwners(),
		DetailEntryLineRanges:  detail.EntryLineRanges(),
		DetailSelectableBlocks: detail.SelectableBlockIndexes(),
	}
}

func appendDetailStreamingProjection(base TranscriptProjection, input TranscriptProjectionInput, state TranscriptProjectionViewState, projector *TranscriptViewProjector) TranscriptProjection {
	blocks := append([]TranscriptProjectionBlock(nil), base.Blocks...)
	if reasoning := detailStreamingReasoningBlock(input, state); len(reasoning.Lines) > 0 {
		blocks = append(blocks, reasoning)
	}
	if strings.TrimSpace(input.Ongoing) != "" {
		var lines []string
		if projector != nil {
			lines = projector.StreamingDetailAssistantLines(input.Ongoing, state)
		} else {
			renderer := transcriptProjectionRenderer(state.Theme, state.ViewportWidth, input.BaseOffset)
			lines = renderer.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, input.Ongoing, false, nil, "")
		}
		if len(lines) > 0 {
			blocks = append(blocks, TranscriptProjectionBlock{
				Role:         RenderIntentAssistant,
				DividerGroup: ongoingDividerGroup(RenderIntentAssistant),
				EntryIndex:   -1,
				EntryEnd:     -1,
				Lines:        lines,
			})
		}
	}
	return TranscriptProjection{Blocks: blocks}
}

func detailStreamingReasoningBlock(input TranscriptProjectionInput, state TranscriptProjectionViewState) TranscriptProjectionBlock {
	if len(input.StreamingReasoning) == 0 {
		return TranscriptProjectionBlock{}
	}
	parts := make([]string, 0, len(input.StreamingReasoning))
	for _, entry := range input.StreamingReasoning {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return TranscriptProjectionBlock{}
	}
	renderer := transcriptProjectionRenderer(state.Theme, state.ViewportWidth, input.BaseOffset)
	lines := renderer.flattenEntryWithMetaAndSymbol(RenderIntentReasoning, strings.Join(parts, "\n"), false, nil, "")
	return TranscriptProjectionBlock{
		Role:         RenderIntentReasoning,
		DividerGroup: ongoingDividerGroup(RenderIntentReasoning),
		EntryIndex:   -1,
		EntryEnd:     -1,
		Lines:        lines,
	}
}

func (p *TranscriptViewProjector) project(input TranscriptProjectionInput, state TranscriptProjectionViewState, cloneResult bool) TranscriptViewProjection {
	key := NewTranscriptViewProjectionKey(input, state)
	if input.Revision > 0 && p != nil && p.projectionSet && transcriptViewProjectionKeysEqual(p.key, key) {
		if !cloneResult {
			return p.projection
		}
		return p.projection.Clone()
	}
	projection := ProjectTranscriptViews(input, state)
	if input.Revision > 0 && p != nil {
		p.key = key
		p.projection = projection
		p.projectionSet = true
	}
	if cloneResult {
		return projection.Clone()
	}
	return projection
}

func NewTranscriptViewProjectionKey(input TranscriptProjectionInput, state TranscriptProjectionViewState) TranscriptViewProjectionKey {
	width := state.ViewportWidth
	if width <= 0 {
		width = 120
	}
	lines := state.ViewportLines
	if lines <= 0 {
		lines = DefaultPreviewLines
	}
	return TranscriptViewProjectionKey{
		Revision:              input.Revision,
		BaseOffset:            max(0, input.BaseOffset),
		TotalEntries:          input.TotalEntries,
		EntryCount:            len(input.Entries),
		ViewportWidth:         width,
		ViewportLines:         lines,
		Theme:                 sharedtheme.Resolve(state.Theme),
		CompactDetail:         state.CompactDetail,
		SelectedEntry:         state.SelectedEntry,
		SelectedEntryIsActive: state.SelectedEntryIsActive,
		DetailSelectedEntry:   state.DetailSelectedEntry,
		DetailSelectedActive:  state.DetailSelectedActive,
		ExpandedEntries:       sortedExpandedEntries(state.DetailExpandedEntries),
	}
}

func transcriptViewProjectionKeysEqual(left TranscriptViewProjectionKey, right TranscriptViewProjectionKey) bool {
	if left.Revision != right.Revision ||
		left.BaseOffset != right.BaseOffset ||
		left.TotalEntries != right.TotalEntries ||
		left.EntryCount != right.EntryCount ||
		left.ViewportWidth != right.ViewportWidth ||
		left.ViewportLines != right.ViewportLines ||
		left.Theme != right.Theme ||
		left.CompactDetail != right.CompactDetail ||
		left.SelectedEntry != right.SelectedEntry ||
		left.SelectedEntryIsActive != right.SelectedEntryIsActive ||
		left.DetailSelectedEntry != right.DetailSelectedEntry ||
		left.DetailSelectedActive != right.DetailSelectedActive ||
		len(left.ExpandedEntries) != len(right.ExpandedEntries) {
		return false
	}
	for idx := range left.ExpandedEntries {
		if left.ExpandedEntries[idx] != right.ExpandedEntries[idx] {
			return false
		}
	}
	return true
}

// ProjectCommittedOngoingTranscript renders committed ongoing transcript entries
// without requiring callers to construct a throwaway tui.Model.
func ProjectCommittedOngoingTranscript(entries []TranscriptEntry, theme string, width int) TranscriptProjection {
	var projector CommittedOngoingProjector
	return projector.Project(entries, CommittedOngoingProjectionKey{
		Theme:      theme,
		Width:      width,
		EntryCount: len(entries),
	})
}

// Project returns the committed ongoing projection for entries, reusing a cached
// projection when the key still matches.
func (p *CommittedOngoingProjector) Project(entries []TranscriptEntry, key CommittedOngoingProjectionKey) TranscriptProjection {
	key = normalizeCommittedOngoingProjectionKey(key, len(entries))
	cacheable := key.Revision > 0
	if cacheable && p != nil && p.projectionSet && p.key == key {
		return p.projection.Clone()
	}
	renderer := transcriptProjectionRenderer(key.Theme, key.Width, key.BaseOffset)
	if p != nil {
		renderer = p.rendererFor(key.Theme, key.Width, key.BaseOffset)
	}
	projection := projectCommittedOngoingTranscriptWithRenderer(renderer, entries)
	if cacheable && p != nil {
		p.key = key
		p.projection = projection.Clone()
		p.projectionSet = true
	}
	return projection
}

func normalizeCommittedOngoingProjectionKey(key CommittedOngoingProjectionKey, entryCount int) CommittedOngoingProjectionKey {
	key.Theme = sharedtheme.Resolve(key.Theme)
	if key.Width <= 0 {
		key.Width = 120
	}
	if key.EntryCount != entryCount {
		key.EntryCount = entryCount
	}
	return key
}

func (p *CommittedOngoingProjector) rendererFor(theme string, width int, baseOffset int) Model {
	theme = sharedtheme.Resolve(theme)
	if width <= 0 {
		width = 120
	}
	if !p.rendererSet || p.rendererTheme != theme || p.rendererWidth != width {
		p.renderer = transcriptProjectionRenderer(theme, width, baseOffset)
		p.rendererSet = true
		p.rendererTheme = theme
		p.rendererWidth = width
	}
	p.renderer.transcriptInput.BaseOffset = baseOffset
	return p.renderer
}

func transcriptProjectionRenderer(theme string, width int, baseOffset int) Model {
	model := NewModel(WithTheme(theme))
	model.viewportWidth = width
	model.transcriptInput.BaseOffset = baseOffset
	return model
}

func projectCommittedOngoingTranscriptWithRenderer(renderer Model, entries []TranscriptEntry) TranscriptProjection {
	committed := CommittedOngoingEntries(entries)
	if len(committed) == 0 {
		return TranscriptProjection{}
	}
	renderer.transcriptInput.Entries = append([]TranscriptEntry(nil), committed...)
	renderer.transcriptInput.Ongoing = ""
	renderer.transcriptInput.StreamingReasoning = nil
	return projectionFromOngoingBlocks(renderer.buildTranscriptBlocks(transcriptBlockOptions{
		mode:             transcriptBlockModeOngoing,
		includeStreaming: false,
		applySelection:   true,
	}))
}

func cloneTranscriptEntries(entries []TranscriptEntry) []TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]TranscriptEntry, len(entries))
	for idx, entry := range entries {
		out[idx] = entry
		out[idx].ToolCall = cloneToolCallMeta(entry.ToolCall)
	}
	return out
}

func cloneExpandedEntrySet(entries map[int]struct{}) map[int]struct{} {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[int]struct{}, len(entries))
	for entry := range entries {
		out[entry] = struct{}{}
	}
	return out
}

func cloneLineRangeMap(ranges map[int]lineRange) map[int]lineRange {
	if len(ranges) == 0 {
		return nil
	}
	out := make(map[int]lineRange, len(ranges))
	for entry, lineRange := range ranges {
		out[entry] = lineRange
	}
	return out
}

func cloneIntMap(in map[int]int) map[int]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[int]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sortedExpandedEntries(entries map[int]struct{}) []int {
	if len(entries) == 0 {
		return nil
	}
	out := make([]int, 0, len(entries))
	for entry := range entries {
		out = append(out, entry)
	}
	sort.Ints(out)
	return out
}

func (m Model) DetailProjection(includeStreaming bool, applySelection bool) TranscriptProjection {
	m.mode = ModeDetail
	return m.projectionFromDetailBlockSpecs(m.buildDetailBlockSpecs(includeStreaming), applySelection)
}

func projectionFromOngoingBlocks(blocks []ongoingBlock) TranscriptProjection {
	projection := TranscriptProjection{Blocks: make([]TranscriptProjectionBlock, 0, len(blocks))}
	for _, block := range blocks {
		projection.Blocks = append(projection.Blocks, TranscriptProjectionBlock{
			Role:         block.role,
			DividerGroup: ongoingDividerGroup(block.role),
			EntryIndex:   block.entryIndex,
			EntryEnd:     block.entryEnd,
			Lines:        append([]string(nil), block.lines...),
		})
	}
	return projection
}

func (m Model) projectionFromDetailBlockSpecs(specs []detailBlockSpec, applySelection bool) TranscriptProjection {
	projection := TranscriptProjection{Blocks: make([]TranscriptProjectionBlock, 0, len(specs))}
	for _, spec := range specs {
		symbolOverride := ""
		if m.compactDetail && m.mode == ModeDetail && spec.selectable && spec.expandable {
			symbolOverride = m.detailExpansionSymbolOverrideForEntry(spec.role, spec.entryIndex, spec.expanded)
		}
		lines := spec.render(m, symbolOverride)
		if applySelection {
			lines = m.maybeSelectedUserBlock(spec.entryIndex, spec.role, lines)
		}
		projection.Blocks = append(projection.Blocks, TranscriptProjectionBlock{
			Role:         spec.role,
			DividerGroup: ongoingDividerGroup(spec.role),
			EntryIndex:   spec.entryIndex,
			EntryEnd:     spec.entryEnd,
			Selectable:   spec.selectable,
			Expanded:     spec.expanded,
			Expandable:   spec.expandable,
			Lines:        append([]string(nil), lines...),
		})
	}
	return projection
}

func CommittedOngoingEntries(entries []TranscriptEntry) []TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	prefixEnd := committedOngoingPrefixEnd(entries)
	if prefixEnd <= 0 {
		return nil
	}
	return nonEmptyTranscriptEntries(entries[:prefixEnd])
}

func PendingOngoingEntries(entries []TranscriptEntry) []TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	prefixEnd := committedOngoingPrefixEnd(entries)
	if prefixEnd >= len(entries) {
		return nil
	}
	return nonEmptyTranscriptEntries(entries[prefixEnd:])
}

func PendingToolEntries(entries []TranscriptEntry) []TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	start := committedOngoingPrefixEnd(entries)
	if start >= len(entries) {
		return nil
	}
	tail := entries[start:]
	include := make(map[int]struct{})
	consumedResults := make(map[int]struct{})
	resultIndex := buildToolResultIndex(tail)
	for idx, entry := range tail {
		if TranscriptRoleFromWire(string(entry.Role)) != TranscriptRoleToolCall {
			continue
		}
		text := entry.Text
		if strings.TrimSpace(entry.CondensedText) != "" {
			text = entry.CondensedText
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		include[idx] = struct{}{}
		resultIdx := resultIndex.findMatchingToolResultIndex(tail, idx, consumedResults)
		if resultIdx < 0 {
			continue
		}
		include[resultIdx] = struct{}{}
		consumedResults[resultIdx] = struct{}{}
	}
	pending := make([]TranscriptEntry, 0, len(include))
	for idx, entry := range tail {
		if _, ok := include[idx]; !ok {
			continue
		}
		pending = append(pending, entry)
	}
	return pending
}

func nonEmptyTranscriptEntries(entries []TranscriptEntry) []TranscriptEntry {
	filtered := make([]TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if TranscriptRoleFromWire(string(entry.Role)).IsToolResult() &&
			strings.TrimSpace(entry.Text) == "" &&
			strings.TrimSpace(entry.CondensedText) == "" {
			// Successful patch/edit calls intentionally emit an empty tool_result
			// body. Preserve the entry as a structural status marker so merged
			// tool blocks can still resolve to their final success/error role.
			filtered = append(filtered, entry)
			continue
		}
		if strings.TrimSpace(entry.Text) == "" && strings.TrimSpace(entry.CondensedText) == "" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func committedOngoingPrefixEnd(entries []TranscriptEntry) int {
	consumedResults := make(map[int]struct{})
	resultIndex := buildToolResultIndex(entries)
	for idx, entry := range entries {
		if entry.Transient {
			return committedOngoingPrefixEndBefore(entries, idx, resultIndex)
		}
		if TranscriptRoleFromWire(string(entry.Role)) != TranscriptRoleToolCall {
			continue
		}
		text := entry.Text
		if strings.TrimSpace(entry.CondensedText) != "" {
			text = entry.CondensedText
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		resultIdx := resultIndex.findMatchingToolResultIndex(entries, idx, consumedResults)
		if resultIdx < 0 || entries[resultIdx].Transient {
			return idx
		}
		consumedResults[resultIdx] = struct{}{}
	}
	return len(entries)
}

func committedOngoingPrefixEndBefore(entries []TranscriptEntry, boundary int, resultIndex toolResultIndex) int {
	consumedResults := make(map[int]struct{})
	for idx := boundary - 1; idx >= 0; idx-- {
		entry := entries[idx]
		if TranscriptRoleFromWire(string(entry.Role)) != TranscriptRoleToolCall {
			continue
		}
		text := entry.Text
		if strings.TrimSpace(entry.CondensedText) != "" {
			text = entry.CondensedText
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		resultIdx := resultIndex.findMatchingToolResultIndex(entries, idx, consumedResults)
		if resultIdx < 0 || resultIdx >= boundary || entries[resultIdx].Transient {
			return idx
		}
		consumedResults[resultIdx] = struct{}{}
	}
	return boundary
}
