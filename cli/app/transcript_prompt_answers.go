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
	sessionID runtimeids.SessionID
	stepID    runtimeids.StepID
	promptID  clientui.PromptID
}

type promptAnswerDeliveryContinuation uint8

const (
	promptAnswerDeliveryContinuationNone promptAnswerDeliveryContinuation = iota
	promptAnswerDeliveryContinuationRuntimeCtrlC
)

type activePromptAnswerDelivery struct {
	key          transcriptPromptKey
	generation   uint64
	cancel       context.CancelFunc
	continuation promptAnswerDeliveryContinuation
}

type promptAnswerDeliveryResultMsg struct {
	key        transcriptPromptKey
	generation uint64
	err        error
}

type promptCtrlCContinuationMsg struct{}

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
	entry, err := serverapi.PromptAnswerBatchEntryFrom(prompt.PromptID, answerPayload)
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
	rawPromptID := string(prompt.PromptID)
	if strings.TrimSpace(rawPromptID) == "" || strings.TrimSpace(rawPromptID) != rawPromptID {
		return transcriptPromptKey{}, errors.New("prompt answer prompt id is required without surrounding whitespace")
	}
	return transcriptPromptKey{
		sessionID: prompt.SessionID,
		stepID:    prompt.StepID,
		promptID:  prompt.PromptID,
	}, nil
}

func newActivePromptAnswerDelivery(
	key transcriptPromptKey,
	generation uint64,
	cancel context.CancelFunc,
) (*activePromptAnswerDelivery, error) {
	if key.sessionID.IsZero() || strings.TrimSpace(string(key.promptID)) == "" {
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
	if prompt.RecommendedOptionIndex != nil {
		recommended := *prompt.RecommendedOptionIndex
		prompt.RecommendedOptionIndex = &recommended
	}
	if prompt.Tool != nil {
		tool := *prompt.Tool
		prompt.Tool = &tool
	}
	return prompt
}
