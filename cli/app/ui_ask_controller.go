package app

import (
	"context"
	"core/cli/app/internal/runtimeattach"
	"core/cli/tui"
	tuiinput "core/cli/tui/input"
	"core/shared/clientui"
	"core/shared/textutil"
	"errors"
	"fmt"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type uiAskController struct {
	model *uiModel
}

type askPromptLineKind int

const (
	askPromptLineKindQuestion askPromptLineKind = iota
	askPromptLineKindOption
	askPromptLineKindHint
	askPromptLineKindInput
)

type askPromptLine struct {
	Text        string
	Kind        askPromptLineKind
	Selected    bool
	Recommended bool
	MutedSuffix string
	Disabled    bool
	InputPrefix string
	InputEditor tuiinput.Editor
	ShowsCursor bool
}

type askFreeformMode int

const (
	askFreeformModeGeneric askFreeformMode = iota
	askFreeformModeApprovalCommentary
)

const askFreeformSelectionOptionText = "Freeform answer"

func (c uiAskController) acceptEvent(evt askEvent) tea.Cmd {
	m := c.model
	if evt.isResolution() {
		return c.resolvePrompt(evt.toolCallID())
	}
	incomingToolCallID := strings.TrimSpace(string(evt.prompt.ToolCallID))
	if incomingToolCallID != "" && m.ask.hasCurrent() && strings.TrimSpace(string(m.ask.current.prompt.ToolCallID)) == incomingToolCallID {
		m.ask.current.prompt = evt.prompt
		return m.scheduleCurrentQuestionProjection()
	}
	if !m.ask.hasCurrent() {
		c.setActiveAsk(evt)
		m.activity = uiActivityQuestion
		if m.inputMode() == uiInputModeMain && (m.view.Mode() == "" || m.view.Mode() == tui.ModeOngoing) {
			m.setInputMode(uiInputModeAsk)
		}
		return m.scheduleCurrentQuestionProjection()
	}
	m.ask.queue = append(m.ask.queue, evt)
	return nil
}

func (c uiAskController) resolvePrompt(toolCallID string) tea.Cmd {
	m := c.model
	targetID := strings.TrimSpace(toolCallID)
	if targetID == "" {
		return nil
	}
	filteredQueue := m.ask.queue[:0]
	for _, queued := range m.ask.queue {
		if strings.TrimSpace(string(queued.prompt.ToolCallID)) == targetID {
			continue
		}
		filteredQueue = append(filteredQueue, queued)
	}
	m.ask.queue = filteredQueue
	if !m.ask.hasCurrent() || strings.TrimSpace(string(m.ask.current.prompt.ToolCallID)) != targetID {
		return nil
	}
	c.cancelActiveDelivery()
	if len(m.ask.queue) > 0 {
		next := m.ask.queue[0]
		m.ask.queue = m.ask.queue[1:]
		c.setActiveAsk(next)
		m.activity = uiActivityQuestion
		m.setInputMode(uiInputModeAsk)
		return m.scheduleCurrentQuestionProjection()
	}
	m.ask.current = nil
	m.ask.currentToken = nextNonZeroToken(m.ask.currentToken)
	m.ask.activeProjection = nil
	m.ask.latestDesiredProjection = nil
	m.ask.cursor = 0
	m.clearAskInput()
	m.ask.freeform = false
	m.ask.freeformMode = askFreeformModeGeneric
	m.restorePrimaryInputMode()
	if m.activity == uiActivityQuestion {
		if m.isBusy() {
			m.activity = uiActivityRunning
		} else {
			m.activity = uiActivityIdle
		}
	}
	return nil
}

func (c uiAskController) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if !m.ask.hasCurrent() {
		return m, nil
	}
	if msg.Type != tea.KeyEnter && msg.Type != keyTypeShiftEnterCSI {
		m.inputController().clearPendingCSIShiftEnter()
	}
	req := m.ask.current.prompt
	if m.ask.freeform && isClipboardPasteKey(msg) {
		return m, m.pasteClipboardCmd(uiClipboardPasteTargetAsk)
	}
	if m.ask.freeform && applySharedInputEditKeyForGOOS(msg, &m.ask.editor, runtime.GOOS).Handled {
		return m, nil
	}
	if m.ask.freeform && applySharedInputMovementKey(msg, &m.ask.editor) {
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		return c.handleCtrlC()
	case tea.KeyEsc:
		c.cancelActiveDelivery()
		_, hasNext, answerCmd := c.answer(clientui.PromptAnswer{}, errors.New("question canceled"))
		if hasNext {
			m.activity = uiActivityQuestion
		} else {
			m.activity = uiActivityIdle
		}
		return m, answerCmd
	case tea.KeyTab:
		if m.ask.freeform {
			if !askSupportsDraftRoundTrip(req) {
				return m, nil
			}
			m.ask.freeform = false
			return m, nil
		}
		m.ask.freeform = true
		if approvalSupportsCommentary(req) {
			m.ask.freeformMode = askFreeformModeApprovalCommentary
			m.clearAskInput()
		}
		return m, nil
	case tea.KeyEnter:
		if m.ask.activeDelivery != nil {
			return m, m.sendTransientStatusWithNoticeID("Answer is already sending", uiStatusNoticeInfo, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		m.inputController().normalizePendingCSIShiftEnterOnEnter()
		if m.ask.freeform {
			commentary := strings.TrimSpace(m.ask.editor.Text())
			if askRequiresFreeformSelectionCommentary(req, m.ask.cursor) && commentary == "" {
				return m, sequenceCmds(
					c.model.sendTransientStatusWithNoticeID("Write your response before submitting the freeform option", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""),
					ringBellCmd(),
				)
			}
			resp := clientui.PromptAnswer{FreeformAnswer: commentary}
			if optionNumber, ok := selectedAskOptionNumber(req, m.ask.cursor); ok {
				resp.SelectedOptionNumber = textutil.Value(optionNumber)
			}
			if transcriptPromptIsApproval(req) {
				if m.ask.freeformMode == askFreeformModeApprovalCommentary {
					decision, ok := selectedApprovalDecision(req, m.ask.cursor)
					if !ok {
						return m, nil
					}
					resp = clientui.PromptAnswer{
						ToolCallID: req.ToolCallID,
						Approval:   &clientui.ApprovalPromptAnswer{Decision: decision, Commentary: commentary},
					}
					_, hasNext, answerCmd := c.answer(resp, nil)
					if hasNext {
						m.activity = uiActivityQuestion
					} else {
						m.activity = uiActivityRunning
					}
					return m, answerCmd
				}
			}
			_, hasNext, answerCmd := c.answer(resp, nil)
			if hasNext {
				m.activity = uiActivityQuestion
			} else {
				m.activity = uiActivityRunning
			}
			return m, answerCmd
		}
		optionCount := askOptionCount(req)
		if optionCount == 0 {
			m.ask.freeform = true
			return m, nil
		}
		if askHasFreeformSelectionOption(req) && m.ask.cursor == len(askVisibleOptions(req)) {
			commentary := strings.TrimSpace(m.ask.editor.Text())
			if commentary == "" {
				m.ask.freeform = true
				return m, nil
			}
			_, hasNext, answerCmd := c.answer(clientui.PromptAnswer{FreeformAnswer: commentary}, nil)
			if hasNext {
				m.activity = uiActivityQuestion
			} else {
				m.activity = uiActivityRunning
			}
			return m, answerCmd
		}
		visibleOptions := askVisibleOptions(req)
		if m.ask.cursor < 0 || m.ask.cursor >= len(visibleOptions) {
			m.ask.freeform = true
			m.clearAskInput()
			return m, nil
		}
		resp := clientui.PromptAnswer{SelectedOptionNumber: textutil.Value(m.ask.cursor + 1)}
		if commentary := strings.TrimSpace(m.ask.editor.Text()); askSupportsDraftRoundTrip(req) && commentary != "" {
			resp.FreeformAnswer = commentary
		}
		if transcriptPromptIsApproval(req) && m.ask.cursor < len(req.ApprovalOptions) {
			resp = clientui.PromptAnswer{Approval: &clientui.ApprovalPromptAnswer{Decision: req.ApprovalOptions[m.ask.cursor]}}
		}
		_, hasNext, answerCmd := c.answer(resp, nil)
		if hasNext {
			m.activity = uiActivityQuestion
		} else {
			m.activity = uiActivityRunning
		}
		return m, answerCmd
	case tea.KeyUp:
		if m.ask.freeform {
			m.moveAskCursorVertical(-1)
			return m, nil
		}
		if m.ask.cursor > 0 {
			m.ask.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.ask.freeform {
			m.moveAskCursorVertical(1)
			return m, nil
		}
		maxIdx := askOptionCount(req) - 1
		if maxIdx >= 0 && m.ask.cursor < maxIdx {
			m.ask.cursor++
		}
		return m, nil
	case tea.KeyCtrlJ, keyTypeShiftEnterCSI:
		if !m.ask.freeform {
			return m, nil
		}
		m.insertAskInputRunes([]rune{'\n'})
		if msg.Type == keyTypeShiftEnterCSI {
			m.inputController().markPendingCSIShiftEnter()
		}
		return m, nil
	case tea.KeySpace:
		if m.ask.freeform {
			m.insertAskInputRunes([]rune{' '})
		}
		return m, nil
	case tea.KeyHome, tea.KeyCtrlA:
		if m.ask.freeform {
			m.ask.editor.MoveLineStart()
		}
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE, tea.KeyCtrlEnd:
		if m.ask.freeform {
			m.ask.editor.MoveLineEnd()
		}
		return m, nil
	default:
		if isShiftEnterKey(msg) {
			if !m.ask.freeform {
				return m, nil
			}
			m.insertAskInputRunes([]rune{'\n'})
			return m, nil
		}
		if m.ask.freeform && msg.Type == tea.KeyRunes {
			m.insertAskInputRunes(msg.Runes)
			return m, nil
		}
		return m, nil
	}
}

func (c uiAskController) handleCtrlC() (tea.Model, tea.Cmd) {
	m := c.model
	c.cancelActiveDelivery()
	_, runtimeCtrlCCmd := m.inputController().handleRuntimeCtrlC(nil)
	m.activity = uiActivityInterrupted
	return m, tea.Batch(runtimeCtrlCCmd, m.interruptedStatusNoticeCmd())
}

func (c uiAskController) renderPriorityPromptLines() []askPromptLine {
	m := c.model
	if !m.ask.hasCurrent() {
		return nil
	}
	req := m.ask.current.prompt
	if isApprovalCommentaryPrompt(req, m.ask.freeform, m.ask.freeformMode) {
		return []askPromptLine{
			{Text: approvalCommentaryLabel(req, m.ask.cursor), Kind: askPromptLineKindHint},
			{Kind: askPromptLineKindInput, InputPrefix: "› ", InputEditor: m.ask.editor, ShowsCursor: true},
		}
	}
	lines := make([]askPromptLine, 0)
	if askOptionCount(req) > 0 && !m.ask.freeform {
		visibleOptions := askVisibleOptions(req)
		for i, s := range visibleOptions {
			selected := i == m.ask.cursor
			recommended := askOptionIsRecommended(req, i)
			marker := "  "
			if selected {
				marker = "✔︎ "
			} else if recommended {
				marker = "★ "
			}
			text := fmt.Sprintf("%s%d. %s", marker, i+1, s)
			mutedSuffix := ""
			if recommended {
				mutedSuffix = " • recommended"
				text += mutedSuffix
			}
			lines = append(lines, askPromptLine{Text: text, Kind: askPromptLineKindOption, Selected: selected, Recommended: recommended, MutedSuffix: mutedSuffix})
		}
		if askHasFreeformSelectionOption(req) {
			idx := len(visibleOptions) + 1
			selected := m.ask.cursor == len(visibleOptions)
			prefix := "  "
			if selected {
				prefix = "✔︎ "
			}
			lines = append(lines, askPromptLine{Text: fmt.Sprintf("%s%d. %s", prefix, idx, askFreeformSelectionOptionText), Kind: askPromptLineKindOption, Selected: selected})
		}
		if askSupportsDraftRoundTrip(req) && askHasPendingFreeformDraft(m.ask.editor.Text()) {
			lines = append(lines, askPromptLine{Kind: askPromptLineKindInput, Disabled: true, InputPrefix: "› ", InputEditor: m.ask.editor, ShowsCursor: false})
			return lines
		}
		hint := "Tab to add commentary • Enter to submit"
		if approvalSupportsCommentary(req) {
			hint = "Tab to add commentary • Enter to submit"
		}
		lines = append(lines, askPromptLine{Text: hint, Kind: askPromptLineKindHint})
		return lines
	}

	inputLabel := ""
	if isApprovalCommentaryPrompt(req, m.ask.freeform, m.ask.freeformMode) {
		inputLabel = approvalCommentaryLabel(req, m.ask.cursor)
	}
	if inputLabel != "" {
		lines = append(lines, askPromptLine{Text: inputLabel, Kind: askPromptLineKindHint})
	}
	lines = append(lines, askPromptLine{Kind: askPromptLineKindInput, InputPrefix: "› ", InputEditor: m.ask.editor, ShowsCursor: true})
	hint := "Enter to submit"
	if askSupportsDraftRoundTrip(req) {
		hint = "Tab to return to picker • Enter to submit"
	}
	lines = append(lines, askPromptLine{Text: hint, Kind: askPromptLineKindHint})
	return lines
}

func (c uiAskController) answer(resp clientui.PromptAnswer, err error) (bool, bool, tea.Cmd) {
	m := c.model
	if !m.ask.hasCurrent() {
		return false, false, nil
	}
	currentToolCallID := strings.TrimSpace(string(m.ask.current.prompt.ToolCallID))
	answerToolCallID := strings.TrimSpace(string(resp.ToolCallID))
	if answerToolCallID != "" && answerToolCallID != currentToolCallID {
		return false, false, nil
	}
	if answerToolCallID == "" {
		resp.ToolCallID = clientui.ToolCallID(currentToolCallID)
	} else {
		resp.ToolCallID = clientui.ToolCallID(answerToolCallID)
	}
	if m.promptAnswers == nil || m.ask.current.prompt.SessionID.IsZero() || currentToolCallID == "" {
		return true, c.finishDeliveredPrompt(), nil
	}
	active, cmd, deliveryErr := m.promptAnswers.delivery(m.ask.current.prompt, resp, err)
	if deliveryErr != nil {
		return true, true, m.sendTransientStatusWithNoticeID(deliveryErr.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	m.ask.activeDelivery = active
	return true, false, cmd
}

func (c uiAskController) finishDeliveredPrompt() bool {
	m := c.model
	c.cancelActiveDelivery()
	if len(m.ask.queue) == 0 {
		m.ask.current = nil
		m.ask.currentToken = nextNonZeroToken(m.ask.currentToken)
		m.ask.activeProjection = nil
		m.ask.latestDesiredProjection = nil
		m.ask.cursor = 0
		m.clearAskInput()
		m.ask.freeform = false
		m.ask.freeformMode = askFreeformModeGeneric
		m.restorePrimaryInputMode()
		return false
	}
	next := m.ask.queue[0]
	m.ask.queue = m.ask.queue[1:]
	c.setActiveAsk(next)
	m.setInputMode(uiInputModeAsk)
	return true
}

func (c uiAskController) setActiveAsk(evt askEvent) {
	m := c.model
	c.cancelActiveDelivery()
	current := evt
	m.ask.currentToken = nextNonZeroToken(m.ask.currentToken)
	m.ask.current = &current
	m.ask.activeProjection = nil
	m.ask.latestDesiredProjection = nil
	m.ask.cursor = initialAskCursor(current.prompt)
	m.clearAskInput()
	m.ask.freeform = askOptionCount(current.prompt) == 0
	m.ask.freeformMode = askFreeformModeGeneric
}

func (c uiAskController) cancelActiveDelivery() {
	if c.model == nil || c.model.ask.activeDelivery == nil {
		return
	}
	c.model.ask.activeDelivery.cancelPending()
	c.model.ask.activeDelivery = nil
}

func (c uiAskController) applyDeliveryResult(result promptAnswerDeliveryResultMsg) tea.Cmd {
	m := c.model
	if m == nil || !m.ask.activeDelivery.matches(result.key, result.generation) {
		return nil
	}
	if result.err == nil {
		c.cancelActiveDelivery()
		hasNext := c.finishDeliveredPrompt()
		if hasNext {
			m.activity = uiActivityQuestion
		} else if m.isBusy() {
			m.activity = uiActivityRunning
		} else {
			m.activity = uiActivityIdle
		}
		return nil
	}
	c.cancelActiveDelivery()
	m.activity = uiActivityQuestion
	if errors.Is(result.err, context.Canceled) || runtimeattach.IsRuntimeConnectionError(result.err) {
		return nil
	}
	return m.sendTransientStatusWithNoticeID(result.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}

func askVisibleOptions(req clientui.TranscriptPrompt) []string {
	if transcriptPromptIsApproval(req) && len(req.ApprovalOptions) > 0 {
		out := make([]string, 0, len(req.ApprovalOptions))
		for _, decision := range req.ApprovalOptions {
			out = append(out, approvalDecisionLabel(decision))
		}
		return out
	}
	return req.Suggestions
}

func approvalSupportsCommentary(req clientui.TranscriptPrompt) bool {
	if !transcriptPromptIsApproval(req) {
		return false
	}
	return len(askVisibleOptions(req)) > 0
}

func askHasFreeformSelectionOption(req clientui.TranscriptPrompt) bool {
	if transcriptPromptIsApproval(req) {
		return false
	}
	return len(askVisibleOptions(req)) > 0
}

func askOptionCount(req clientui.TranscriptPrompt) int {
	count := len(askVisibleOptions(req))
	if askHasFreeformSelectionOption(req) {
		count++
	}
	return count
}

func isApprovalCommentaryPrompt(req clientui.TranscriptPrompt, freeform bool, mode askFreeformMode) bool {
	if !freeform || mode != askFreeformModeApprovalCommentary {
		return false
	}
	return transcriptPromptIsApproval(req)
}

func selectedApprovalDecision(req clientui.TranscriptPrompt, cursor int) (clientui.ApprovalDecision, bool) {
	if !transcriptPromptIsApproval(req) || cursor < 0 || cursor >= len(req.ApprovalOptions) {
		return "", false
	}
	return req.ApprovalOptions[cursor], true
}

func approvalCommentaryLabel(req clientui.TranscriptPrompt, cursor int) string {
	if !transcriptPromptIsApproval(req) || cursor < 0 || cursor >= len(req.ApprovalOptions) {
		return "Commentary:"
	}
	return fmt.Sprintf("Commentary for %s:", approvalDecisionLabel(req.ApprovalOptions[cursor]))
}

func selectedAskOptionNumber(req clientui.TranscriptPrompt, cursor int) (int, bool) {
	if transcriptPromptIsApproval(req) {
		return 0, false
	}
	visibleOptions := askVisibleOptions(req)
	if cursor < 0 || cursor >= len(visibleOptions) {
		return 0, false
	}
	return cursor + 1, true
}

func askOptionIsRecommended(req clientui.TranscriptPrompt, index int) bool {
	if transcriptPromptIsApproval(req) {
		return false
	}
	return req.RecommendedOptionIndex != nil && *req.RecommendedOptionIndex == index+1
}

func initialAskCursor(req clientui.TranscriptPrompt) int {
	if transcriptPromptIsApproval(req) {
		for index, decision := range req.ApprovalOptions {
			if decision == clientui.ApprovalDecisionAllowOnce {
				return index
			}
		}
		return 0
	}
	for index := range askVisibleOptions(req) {
		if askOptionIsRecommended(req, index) {
			return index
		}
	}
	return 0
}

func askRequiresFreeformSelectionCommentary(req clientui.TranscriptPrompt, cursor int) bool {
	if !askHasFreeformSelectionOption(req) {
		return false
	}
	return cursor == len(askVisibleOptions(req))
}

func askHasPendingFreeformDraft(input string) bool {
	return strings.TrimSpace(input) != ""
}

func askSupportsDraftRoundTrip(req clientui.TranscriptPrompt) bool {
	return !transcriptPromptIsApproval(req) && len(askVisibleOptions(req)) > 0
}

func transcriptPromptIsApproval(prompt clientui.TranscriptPrompt) bool {
	return prompt.Kind == clientui.TranscriptPromptKindApproval
}

func approvalDecisionLabel(decision clientui.ApprovalDecision) string {
	switch decision {
	case clientui.ApprovalDecisionAllowOnce:
		return "Allow once"
	case clientui.ApprovalDecisionAllowSession:
		return "Allow for this session"
	case clientui.ApprovalDecisionDeny:
		return "Deny"
	default:
		panic(fmt.Sprintf("unsupported approval decision %q", decision))
	}
}
