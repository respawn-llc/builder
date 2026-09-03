package app

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"

	"core/cli/tui"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type questionRenderIdentity struct {
	questionSource   string
	terminalWidth    int
	theme            string
	linkPresentation transcriptrender.MarkdownLinkPresentation
}

type questionRenderRequest struct {
	currentToken   uint64
	operationToken uuid.UUID
	identity       questionRenderIdentity
	questionSource string
}

type questionRenderResultMsg struct {
	request             questionRenderRequest
	rows                []string
	notificationPreview string
	err                 error
	stack               []byte
}

type activeQuestionProjection struct {
	renderedAt               questionRenderIdentity
	rows                     []string
	pendingActivationPreview *string
}

type desiredQuestionProjection struct {
	candidate askEvent
	identity  questionRenderIdentity
}

type questionProjector func(questionRenderRequest) questionRenderResultMsg

func projectAskQuestionMarkdown(request questionRenderRequest) questionRenderResultMsg {
	return questionRenderResultMsg{
		request: request,
		rows: tui.RenderAskQuestionMarkdownLines(
			request.questionSource,
			request.identity.theme,
			request.identity.terminalWidth,
			request.identity.linkPresentation,
		),
	}
}

func (m *uiModel) currentQuestionRenderIdentity() (questionRenderIdentity, bool) {
	if m == nil || !m.ask.hasCurrent() {
		return questionRenderIdentity{}, false
	}
	size := m.terminalGeometry.Size()
	if size == nil || size.width < 1 {
		return questionRenderIdentity{}, false
	}
	prompt := m.ask.current.prompt
	question := prompt.Question
	if len(prompt.AccessTargets) > 0 {
		question = clientui.FormatFileAccessApprovalMarkdown(prompt.AccessTargets)
	}
	return questionRenderIdentity{
		questionSource:   question,
		terminalWidth:    size.width,
		theme:            m.theme,
		linkPresentation: m.markdownLinks,
	}, true
}

func (m *uiModel) scheduleCurrentQuestionProjection() tea.Cmd {
	identity, ok := m.currentQuestionRenderIdentity()
	if !ok {
		return nil
	}
	if m.ask.activeProjection != nil && m.ask.activeProjection.renderedAt == identity {
		m.ask.latestDesiredProjection = nil
		return nil
	}
	candidate := cloneAskEventForProjection(*m.ask.current)
	m.ask.latestDesiredProjection = &desiredQuestionProjection{
		candidate: candidate,
		identity:  identity,
	}
	if m.ask.inFlightProjection != nil {
		return nil
	}
	return m.startLatestDesiredQuestionProjection()
}

func (m *uiModel) startLatestDesiredQuestionProjection() tea.Cmd {
	desired := m.ask.latestDesiredProjection
	if desired == nil || m.ask.inFlightProjection != nil {
		return nil
	}
	request := questionRenderRequest{
		currentToken:   m.ask.currentToken,
		operationToken: uuid.New(),
		identity:       desired.identity,
		questionSource: desired.identity.questionSource,
	}
	m.ask.inFlightProjection = &request
	projector := m.questionProjector
	return func() tea.Msg {
		if projector == nil {
			return questionRenderResultMsg{
				request: request,
				err:     errors.New("ask question Markdown projector is not configured"),
				stack:   debug.Stack(),
			}
		}
		result := projector(request)
		if result.err == nil {
			result.notificationPreview = projectedQuestionNotificationPreview(result.rows)
		}
		if result.err != nil && len(result.stack) == 0 {
			result.stack = debug.Stack()
		}
		return result
	}
}

func (m *uiModel) applyQuestionRenderResult(result questionRenderResultMsg) (tea.Cmd, bool) {
	inFlight := m.ask.inFlightProjection
	if inFlight == nil || result.request != *inFlight {
		return nil, false
	}
	m.ask.inFlightProjection = nil
	desired := m.ask.latestDesiredProjection
	if result.request.currentToken != m.ask.currentToken ||
		desired == nil ||
		result.request.identity != desired.identity {
		return m.startLatestDesiredQuestionProjection(), false
	}
	if result.err != nil {
		return m.handleQuestionProjectionError(result), false
	}
	initialActivation := m.ask.activeProjection == nil
	activationPending := initialActivation ||
		m.ask.activeProjection != nil && m.ask.activeProjection.pendingActivationPreview != nil
	candidate := cloneAskEventForProjection(desired.candidate)
	m.ask.current = &candidate
	var pendingActivationPreview *string
	if activationPending {
		preview := result.notificationPreview
		pendingActivationPreview = &preview
	}
	m.ask.activeProjection = &activeQuestionProjection{
		renderedAt:               result.request.identity,
		rows:                     append([]string(nil), result.rows...),
		pendingActivationPreview: pendingActivationPreview,
	}
	m.ask.latestDesiredProjection = nil
	m.notifyPendingTranscriptPromptActivation()
	return nil, true
}

func cloneAskEventForProjection(event askEvent) askEvent {
	event.prompt = cloneTranscriptPromptForAsk(event.prompt)
	return event
}

func (m *uiModel) handleQuestionProjectionError(result questionRenderResultMsg) tea.Cmd {
	toolCallID := ""
	if m.ask.current != nil {
		toolCallID = string(m.ask.current.prompt.ToolCallID)
	}
	m.logf(
		"ask.question_projection.error tool_call_id=%q current_token=%d operation_token=%s rendered_at=%+v desired=%+v delivery_generation=%s err=%q stack=%s",
		toolCallID,
		m.ask.currentToken,
		result.request.operationToken,
		questionRenderIdentityDiagnosticsFor(m.ask.activeProjection),
		questionRenderIdentityDiagnostics(result.request.identity),
		activePromptDeliveryGeneration(m.ask.activeDelivery),
		result.err.Error(),
		result.stack,
	)
	return m.handleFatalUIError(
		fmt.Sprintf("Could not render the question prompt: %v", result.err),
		result.err,
	)
}

type questionRenderIdentityDiagnostic struct {
	QuestionBytes    int
	TerminalWidth    int
	Theme            string
	LinkPresentation transcriptrender.MarkdownLinkPresentation
}

func questionRenderIdentityDiagnostics(identity questionRenderIdentity) questionRenderIdentityDiagnostic {
	return questionRenderIdentityDiagnostic{
		QuestionBytes:    len(identity.questionSource),
		TerminalWidth:    identity.terminalWidth,
		Theme:            identity.theme,
		LinkPresentation: identity.linkPresentation,
	}
}

func questionRenderIdentityDiagnosticsFor(projection *activeQuestionProjection) *questionRenderIdentityDiagnostic {
	if projection == nil {
		return nil
	}
	diagnostic := questionRenderIdentityDiagnostics(projection.renderedAt)
	return &diagnostic
}

type promptDeliveryGenerationDiagnostic uint64

func (g *promptDeliveryGenerationDiagnostic) String() string {
	if g == nil {
		return "<none>"
	}
	return strconv.FormatUint(uint64(*g), 10)
}

func activePromptDeliveryGeneration(delivery *activePromptAnswerDelivery) *promptDeliveryGenerationDiagnostic {
	if delivery == nil {
		return nil
	}
	generation := promptDeliveryGenerationDiagnostic(delivery.generation)
	return &generation
}
