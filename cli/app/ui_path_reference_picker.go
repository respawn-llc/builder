package app

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

func WithUIPathReferenceSearch(search uiPathReferenceSearch) UIOption {
	return func(m *uiModel) {
		if m.pathReferenceSearch != nil && m.pathReferenceSearch != search {
			m.pathReferenceSearch.Stop()
		}
		m.pathReferenceSearch = search
		if search != nil {
			m.pathReferenceEvents = search.Events()
			return
		}
		m.pathReferenceEvents = nil
	}
}

func detectPathReferenceQuery(input string, cursor int) uiPathReferenceQuery {
	runes := []rune(input)
	cursor = clampCursor(cursor, len(runes))
	if cursor < 2 {
		return uiPathReferenceQuery{}
	}
	end := cursor
	start := end
	for start > 0 && isPathReferenceQueryRune(runes[start-1]) {
		start--
	}
	if end-start < 2 || start == 0 {
		return uiPathReferenceQuery{}
	}
	if runes[start-1] != '@' {
		return uiPathReferenceQuery{}
	}
	if start-1 > 0 {
		prev := runes[start-2]
		if isPathReferenceQueryRune(prev) || prev == '@' {
			return uiPathReferenceQuery{}
		}
	}
	query := string(runes[start:end])
	if strings.TrimSpace(query) == "" {
		return uiPathReferenceQuery{}
	}
	return uiPathReferenceQuery{
		Active:          true,
		Start:           start - 1,
		End:             end,
		RawQuery:        query,
		NormalizedQuery: strings.TrimSpace(query),
	}
}

func isPathReferenceQueryRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '/', '.', '_', '-':
		return true
	default:
		return false
	}
}

func applyPathReferenceCompletion(input string, cursor int, query uiPathReferenceQuery, candidate uiPathReferenceCandidate) (string, int, bool) {
	if !query.Active || query.End < query.Start || strings.TrimSpace(candidate.Path) == "" {
		return input, cursor, false
	}
	runes := []rune(input)
	if query.Start < 0 || query.End > len(runes) || query.Start > len(runes) {
		return input, cursor, false
	}
	completion := "@" + candidate.Path
	if candidate.Directory && !strings.HasSuffix(completion, "/") {
		completion += "/"
	}
	inserted := []rune(completion)
	updated := make([]rune, 0, len(runes)-max(0, query.End-query.Start)+len(inserted))
	updated = append(updated, runes[:query.Start]...)
	updated = append(updated, inserted...)
	updated = append(updated, runes[query.End:]...)
	nextCursor := query.Start + len(inserted)
	return string(updated), nextCursor, true
}

func (m *uiModel) refreshAutocompleteFromInput() tea.Cmd {
	cmd := m.refreshSlashCommandFilterFromInputWithAuth(true)
	m.refreshPathReferenceFromInput()
	return cmd
}

func (m *uiModel) refreshAutocompleteStateFromInput() {
	m.refreshSlashCommandFilterFromInputWithAuth(false)
	m.refreshPathReferenceFromInput()
}

func (m *uiModel) refreshPathReferenceFromInput() {
	if m.pathReferenceSearch == nil {
		m.clearPathReferenceState()
		return
	}
	if !m.shouldTrackPathReferenceQuery() {
		m.clearPathReferenceState()
		return
	}
	query := detectPathReferenceQuery(m.input, clampCursor(m.inputCursor, len([]rune(m.input))))
	if !query.Active {
		m.clearPathReferenceState()
		return
	}
	workspaceRoot := strings.TrimSpace(m.statusConfig.WorkspaceRoot)
	if m.pathReference.tracked == query &&
		m.pathReference.draftToken == m.mainInputDraftToken &&
		m.pathReference.workspaceRoot == workspaceRoot &&
		m.pathReference.normalizedQuery == query.NormalizedQuery {
		return
	}
	m.pathReference.queryToken = nextNonZeroToken(m.pathReference.queryToken)
	m.pathReference.tracked = query
	m.pathReference.selection = 0
	m.pathReference.draftToken = m.mainInputDraftToken
	m.pathReference.workspaceRoot = workspaceRoot
	m.pathReference.normalizedQuery = query.NormalizedQuery
	m.pathReference.pending = true
	m.pathReference.loading = false
	m.pathReferenceSearch.Search(uiPathReferenceSearchRequest{
		WorkspaceRoot:   workspaceRoot,
		DraftToken:      m.mainInputDraftToken,
		QueryToken:      m.pathReference.queryToken,
		NormalizedQuery: query.NormalizedQuery,
	})
}

func (m *uiModel) shouldTrackPathReferenceQuery() bool {
	if m == nil {
		return false
	}
	if m.rollback.isSelecting() ||
		m.ask.hasCurrent() {
		return false
	}
	switch m.inputModeState().Mode {
	case uiInputModeMain, uiInputModeRollbackEdit:
	default:
		return false
	}
	if m.slashCommandPicker().visible {
		return false
	}
	return true
}

func (m *uiModel) clearPathReferenceState() {
	m.pathReference.tracked = uiPathReferenceQuery{}
	m.pathReference.matches = nil
	m.pathReference.selection = 0
	m.pathReference.draftToken = 0
	m.pathReference.workspaceRoot = ""
	m.pathReference.normalizedQuery = ""
	m.pathReference.pending = false
	m.pathReference.loading = false
	m.pathReference.corpusGeneration = 0
}

func (m *uiModel) pathReferencePicker() uiPickerPresentation {
	if !m.pathReference.tracked.Active {
		return uiPickerPresentation{}
	}
	if m.pathReference.loading {
		return uiPickerPresentation{
			kind:      uiPickerKindPathReference,
			visible:   true,
			rows:      []uiPickerRow{{primary: "Loading...", muted: true}},
			lineCount: 1,
		}
	}
	if len(m.pathReference.matches) == 0 {
		return uiPickerPresentation{}
	}
	selection := clampSlashPickerIndex(m.pathReference.selection, 0, len(m.pathReference.matches)-1)
	start := 0
	if len(m.pathReference.matches) > slashCommandPickerLines {
		start = selection - (slashCommandPickerLines / 2)
		maxStart := len(m.pathReference.matches) - slashCommandPickerLines
		if start < 0 {
			start = 0
		}
		if start > maxStart {
			start = maxStart
		}
	}
	rows := make([]uiPickerRow, 0, len(m.pathReference.matches))
	for _, match := range m.pathReference.matches {
		rows = append(rows, uiPickerRow{primary: sanitizePathReferenceDisplayText(match.Path), selectable: true})
	}
	lineCount := len(m.pathReference.matches)
	if lineCount > slashCommandPickerLines {
		lineCount = slashCommandPickerLines
	}
	return uiPickerPresentation{
		kind:      uiPickerKindPathReference,
		visible:   true,
		rows:      rows,
		selection: selection,
		start:     start,
		lineCount: lineCount,
	}
}

func (m *uiModel) navigatePathReferencePicker(delta int) bool {
	state := m.pathReferencePicker()
	if !state.visible || len(m.pathReference.matches) == 0 || m.pathReference.loading {
		return false
	}
	m.pathReference.selection = clampSlashPickerIndex(m.pathReference.selection+delta, 0, len(m.pathReference.matches)-1)
	return true
}

func (m *uiModel) acceptPathReferenceSelection() bool {
	state := m.pathReferencePicker()
	if !state.visible || len(m.pathReference.matches) == 0 || m.pathReference.loading || m.pathReference.pending {
		return false
	}
	selection := clampSlashPickerIndex(m.pathReference.selection, 0, len(m.pathReference.matches)-1)
	updated, nextCursor, ok := applyPathReferenceCompletion(m.input, m.inputCursor, m.pathReference.tracked, m.pathReference.matches[selection])
	if !ok {
		return false
	}
	m.replaceMainInput(updated, nextCursor)
	return true
}

func (m *uiModel) activePickerPresentation() uiPickerPresentation {
	if state := m.slashCommandPresentation(); state.visible {
		return state
	}
	return m.pathReferencePicker()
}

func (m *uiModel) shouldBlockPathReferenceAcceptanceKey() bool {
	state := m.pathReferencePicker()
	if !state.visible {
		return false
	}
	return m.pathReference.pending || m.pathReference.loading
}

func sanitizePathReferenceDisplayText(path string) string {
	if path == "" {
		return ""
	}
	runes := []rune(path)
	filtered := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b {
			i = skipTerminalEscapeSequence(runes, i)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		filtered = append(filtered, r)
	}
	return string(filtered)
}

func skipTerminalEscapeSequence(runes []rune, start int) int {
	if start+1 >= len(runes) {
		return start
	}
	switch runes[start+1] {
	case '[':
		for i := start + 2; i < len(runes); i++ {
			if runes[i] >= 0x40 && runes[i] <= 0x7e {
				return i
			}
		}
		return len(runes) - 1
	case ']':
		for i := start + 2; i < len(runes); i++ {
			if runes[i] == 0x07 {
				return i
			}
			if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
				return i + 1
			}
		}
		return len(runes) - 1
	default:
		return start + 1
	}
}

func (m *uiModel) slashCommandPresentation() uiPickerPresentation {
	state := m.slashCommandPicker()
	if !state.visible {
		return uiPickerPresentation{}
	}
	rows := make([]uiPickerRow, 0, max(1, len(state.matches)))
	for idx := range state.matches {
		rows = append(rows, uiPickerRow{
			primary:     "/" + state.matches[idx].Name,
			secondary:   strings.TrimSpace(state.matches[idx].Description),
			boldPrimary: true,
			selectable:  true,
		})
	}
	if len(state.matches) == 0 {
		rows = append(rows, uiPickerRow{primary: "No matching commands", muted: true})
	}
	return uiPickerPresentation{
		kind:      uiPickerKindSlashCommand,
		visible:   true,
		rows:      rows,
		selection: state.selection,
		start:     state.start,
		lineCount: slashCommandPickerLines,
	}
}

func (m *uiModel) handlePathReferenceCorpusReady(msg uiPathReferenceCorpusReadyMsg) {
	if m.pathReference.workspaceRoot != strings.TrimSpace(msg.WorkspaceRoot) {
		return
	}
	if m.pathReference.corpusGeneration != 0 && msg.CorpusGeneration < m.pathReference.corpusGeneration {
		return
	}
	m.pathReference.corpusGeneration = msg.CorpusGeneration
}

func (m *uiModel) handlePathReferenceCorpusFailed(msg uiPathReferenceCorpusFailedMsg) {
	if m.pathReference.workspaceRoot != strings.TrimSpace(msg.WorkspaceRoot) {
		return
	}
	if m.pathReference.corpusGeneration != 0 && msg.CorpusGeneration < m.pathReference.corpusGeneration {
		return
	}
	m.pathReference.corpusGeneration = msg.CorpusGeneration
	m.pathReference.pending = false
	m.pathReference.loading = false
	m.pathReference.matches = nil
	m.pathReference.selection = 0
}

func (m *uiModel) handlePathReferenceMatchResult(msg uiPathReferenceMatchResultMsg) {
	if !m.pathReference.tracked.Active {
		return
	}
	if m.pathReference.workspaceRoot != strings.TrimSpace(msg.WorkspaceRoot) ||
		m.pathReference.draftToken != msg.DraftToken ||
		m.pathReference.queryToken != msg.QueryToken ||
		m.pathReference.normalizedQuery != msg.NormalizedQuery {
		return
	}
	if m.pathReference.corpusGeneration != 0 && msg.CorpusGeneration != m.pathReference.corpusGeneration {
		return
	}
	m.pathReference.corpusGeneration = msg.CorpusGeneration
	m.pathReference.pending = false
	m.pathReference.loading = false
	m.pathReference.matches = append([]uiPathReferenceCandidate(nil), msg.Matches...)
	m.pathReference.selection = 0
}

func (m *uiModel) handlePathReferenceLoadingDelay(msg uiPathReferenceLoadingDelayMsg) {
	if !m.pathReference.tracked.Active {
		return
	}
	if m.pathReference.workspaceRoot != strings.TrimSpace(msg.WorkspaceRoot) ||
		m.pathReference.draftToken != msg.DraftToken ||
		m.pathReference.queryToken != msg.QueryToken ||
		m.pathReference.normalizedQuery != msg.NormalizedQuery ||
		!m.pathReference.pending {
		return
	}
	if m.pathReference.corpusGeneration != 0 && msg.CorpusGeneration != m.pathReference.corpusGeneration {
		return
	}
	m.pathReference.corpusGeneration = msg.CorpusGeneration
	m.pathReference.loading = true
	m.pathReference.matches = nil
	m.pathReference.selection = 0
}
