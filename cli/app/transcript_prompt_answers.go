package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"

	tea "github.com/charmbracelet/bubbletea"
)

type transcriptPromptAnswerer struct {
	ctx                   context.Context
	control               promptBatchAnswerer
	connectionOutcomeSink func(error)
	nextGeneration        uint64
}

type promptBatchAnswerer interface {
	AnswerPromptBatch(context.Context, serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error)
}

type transcriptPromptKey struct {
	sessionID  runtimeids.SessionID
	stepID     runtimeids.StepID
	toolCallID clientui.ToolCallID
}

type activePromptAnswerDelivery struct {
	key        transcriptPromptKey
	generation uint64
	cancel     context.CancelFunc
}

type promptAnswerDeliveryResultMsg struct {
	key        transcriptPromptKey
	generation uint64
	err        error
}

func newTranscriptPromptAnswerer(ctx context.Context, control promptBatchAnswerer) *transcriptPromptAnswerer {
	if ctx == nil || control == nil {
		return nil
	}
	return &transcriptPromptAnswerer{
		ctx: ctx, control: control,
	}
}

func (a *transcriptPromptAnswerer) withConnectionOutcomeSink(sink func(error)) *transcriptPromptAnswerer {
	if a == nil {
		return nil
	}
	copy := *a
	copy.connectionOutcomeSink = sink
	return &copy
}

func (a *transcriptPromptAnswerer) event(prompt clientui.TranscriptPrompt) askEvent {
	return askEvent{prompt: cloneTranscriptPromptForAsk(prompt)}
}

func (a *transcriptPromptAnswerer) delivery(
	prompt clientui.TranscriptPrompt,
	answer clientui.PromptAnswer,
	answerErr error,
) (*activePromptAnswerDelivery, tea.Cmd, error) {
	if a == nil || a.ctx == nil || a.control == nil {
		return nil, nil, errors.New("prompt answer delivery is unavailable")
	}
	key, err := newTranscriptPromptKey(prompt)
	if err != nil {
		return nil, nil, err
	}
	a.nextGeneration++
	if a.nextGeneration == 0 {
		a.nextGeneration++
	}
	generation := a.nextGeneration
	deliveryCtx, cancel := context.WithCancel(a.ctx)
	active, err := newActivePromptAnswerDelivery(key, generation, cancel)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	submit, err := a.submitter(prompt, clonePromptAnswer(answer), answerErr)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return active, func() tea.Msg {
		err := context.Cause(deliveryCtx)
		if err == nil {
			err = submit(deliveryCtx)
			if a.connectionOutcomeSink != nil {
				a.connectionOutcomeSink(err)
			}
		}
		return promptAnswerDeliveryResultMsg{key: key, generation: generation, err: err}
	}, nil
}

func (a *transcriptPromptAnswerer) submitter(
	prompt clientui.TranscriptPrompt,
	answer clientui.PromptAnswer,
	answerErr error,
) (func(context.Context) error, error) {
	var answerPayload serverapi.PromptAnswer
	switch {
	case answerErr != nil:
		answerPayload = serverapi.DeclinedPromptAnswer()
	case transcriptPromptIsApproval(prompt) && answer.Approval != nil:
		answerPayload = serverapi.ApprovalPromptAnswer(serverapi.PromptApprovalAnswer{
			Decision:   answer.Approval.Decision,
			Commentary: textutil.OptionalExactString(answer.Approval.Commentary),
		})
	case transcriptPromptIsApproval(prompt):
		return nil, errors.New("approval response is required")
	case answer.Approval != nil:
		return nil, errors.New("question response cannot carry approval answer")
	default:
		answerPayload = serverapi.QuestionPromptAnswer(serverapi.PromptQuestionAnswer{
			SelectedOptionNumber: answer.SelectedOptionNumber,
			Freeform:             textutil.OptionalExactString(answer.FreeformAnswer),
		})
	}
	entry, err := serverapi.PromptAnswerBatchEntryFrom(prompt.ToolCallID, answerPayload)
	if err != nil {
		return nil, fmt.Errorf("convert prompt answer: %w", err)
	}
	request := serverapi.PromptAnswerBatchRequest{
		SessionID: prompt.SessionID,
		StepID:    prompt.StepID,
		Entries:   []serverapi.PromptAnswerBatchEntry{entry},
	}
	return func(ctx context.Context) error {
		_, err := a.control.AnswerPromptBatch(ctx, request)
		return err
	}, nil
}

func newTranscriptPromptKey(prompt clientui.TranscriptPrompt) (transcriptPromptKey, error) {
	if prompt.SessionID.IsZero() {
		return transcriptPromptKey{}, errors.New("prompt answer session id is required")
	}
	if prompt.StepID.IsZero() {
		return transcriptPromptKey{}, errors.New("prompt answer step id is required")
	}
	rawToolCallID := string(prompt.ToolCallID)
	if strings.TrimSpace(rawToolCallID) == "" || strings.TrimSpace(rawToolCallID) != rawToolCallID {
		return transcriptPromptKey{}, errors.New("prompt answer tool call id is required without surrounding whitespace")
	}
	return transcriptPromptKey{
		sessionID:  prompt.SessionID,
		stepID:     prompt.StepID,
		toolCallID: prompt.ToolCallID,
	}, nil
}

func newActivePromptAnswerDelivery(
	key transcriptPromptKey,
	generation uint64,
	cancel context.CancelFunc,
) (*activePromptAnswerDelivery, error) {
	if key.sessionID.IsZero() || strings.TrimSpace(string(key.toolCallID)) == "" {
		return nil, errors.New("prompt answer delivery key is required")
	}
	if key.stepID.IsZero() {
		return nil, errors.New("prompt answer delivery step id is required")
	}
	if generation == 0 {
		return nil, errors.New("prompt answer delivery generation is required")
	}
	if cancel == nil {
		return nil, errors.New("prompt answer delivery cancellation is required")
	}
	return &activePromptAnswerDelivery{key: key, generation: generation, cancel: cancel}, nil
}

func (d *activePromptAnswerDelivery) cancelPending() {
	if d != nil {
		d.cancel()
	}
}

func (d *activePromptAnswerDelivery) matches(key transcriptPromptKey, generation uint64) bool {
	return d != nil && d.key == key && d.generation == generation
}

func clonePromptAnswer(answer clientui.PromptAnswer) clientui.PromptAnswer {
	if answer.SelectedOptionNumber != nil {
		selected := *answer.SelectedOptionNumber
		answer.SelectedOptionNumber = &selected
	}
	if answer.Approval != nil {
		approval := *answer.Approval
		answer.Approval = &approval
	}
	return answer
}

func cloneTranscriptPromptForAsk(prompt clientui.TranscriptPrompt) clientui.TranscriptPrompt {
	prompt.Suggestions = append([]string(nil), prompt.Suggestions...)
	prompt.ApprovalOptions = append([]clientui.ApprovalDecision(nil), prompt.ApprovalOptions...)
	prompt.AccessTargets = append([]clientui.FileAccessTarget(nil), prompt.AccessTargets...)
	if prompt.RecommendedOptionIndex != nil {
		recommended := *prompt.RecommendedOptionIndex
		prompt.RecommendedOptionIndex = &recommended
	}
	return prompt
}
