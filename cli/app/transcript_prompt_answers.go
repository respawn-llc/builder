package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

var transcriptPromptAnswerRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

type transcriptPromptAnswerer struct {
	ctx                   context.Context
	control               apicontract.PromptControlService
	connectionOutcomeSink func(error)
	retryDelays           []time.Duration
	retryWait             func(context.Context, time.Duration) error
}

type transcriptPromptKey struct {
	sessionID runtimeids.SessionID
	promptID  clientui.PromptID
}

type activePromptAnswerDelivery struct {
	key       transcriptPromptKey
	requestID runtimeids.RuntimeClientRequestID
	cancel    context.CancelFunc
}

type promptAnswerDeliveryResultMsg struct {
	key       transcriptPromptKey
	requestID runtimeids.RuntimeClientRequestID
	err       error
}

func newTranscriptPromptAnswerer(ctx context.Context, control apicontract.PromptControlService) *transcriptPromptAnswerer {
	if ctx == nil || control == nil {
		return nil
	}
	return &transcriptPromptAnswerer{
		ctx:         ctx,
		control:     control,
		retryDelays: transcriptPromptAnswerRetryDelays,
		retryWait:   rpcwire.WaitForRetry,
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
	requestID := runtimeids.NewRuntimeClientRequestID()
	deliveryCtx, cancel := context.WithCancel(a.ctx)
	active, err := newActivePromptAnswerDelivery(key, requestID, cancel)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	submit, err := a.submitter(prompt, clonePromptAnswer(answer), answerErr, requestID)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	delays := append([]time.Duration(nil), a.retryDelays...)
	wait := a.retryWait
	return active, func() tea.Msg {
		err := retryTranscriptPromptAnswer(deliveryCtx, delays, wait, func() error {
			err := submit(deliveryCtx)
			if a.connectionOutcomeSink != nil {
				a.connectionOutcomeSink(err)
			}
			return err
		})
		return promptAnswerDeliveryResultMsg{key: key, requestID: requestID, err: err}
	}, nil
}

func (a *transcriptPromptAnswerer) submitter(
	prompt clientui.TranscriptPrompt,
	answer clientui.PromptAnswer,
	answerErr error,
	requestID runtimeids.RuntimeClientRequestID,
) (func(context.Context) error, error) {
	if transcriptPromptIsApproval(prompt) {
		request := serverapi.ApprovalAnswerRequest{
			ClientRequestID: requestID.String(),
			SessionID:       prompt.SessionID.String(),
			ApprovalID:      string(prompt.PromptID),
		}
		switch {
		case answerErr != nil:
			request.ErrorMessage = answerErr.Error()
		case answer.Approval != nil:
			request.Decision = answer.Approval.Decision
			request.Commentary = answer.Approval.Commentary
		default:
			return nil, errors.New("approval response is required")
		}
		if err := request.Validate(); err != nil {
			return nil, fmt.Errorf("validate approval answer: %w", err)
		}
		return func(ctx context.Context) error {
			return a.control.AnswerApproval(ctx, request)
		}, nil
	}
	request := serverapi.AskAnswerRequest{
		ClientRequestID:      requestID.String(),
		SessionID:            prompt.SessionID.String(),
		AskID:                string(prompt.PromptID),
		Answer:               answer.Answer,
		SelectedOptionNumber: answer.SelectedOptionNumber,
		FreeformAnswer:       answer.FreeformAnswer,
	}
	if answerErr != nil {
		request.ErrorMessage = answerErr.Error()
		request.Answer = ""
		request.SelectedOptionNumber = nil
		request.FreeformAnswer = ""
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate ask answer: %w", err)
	}
	return func(ctx context.Context) error {
		return a.control.AnswerAsk(ctx, request)
	}, nil
}

func newTranscriptPromptKey(prompt clientui.TranscriptPrompt) (transcriptPromptKey, error) {
	if prompt.SessionID.IsZero() {
		return transcriptPromptKey{}, errors.New("prompt answer session id is required")
	}
	rawPromptID := string(prompt.PromptID)
	if strings.TrimSpace(rawPromptID) == "" || strings.TrimSpace(rawPromptID) != rawPromptID {
		return transcriptPromptKey{}, errors.New("prompt answer prompt id is required without surrounding whitespace")
	}
	return transcriptPromptKey{sessionID: prompt.SessionID, promptID: prompt.PromptID}, nil
}

func newActivePromptAnswerDelivery(
	key transcriptPromptKey,
	requestID runtimeids.RuntimeClientRequestID,
	cancel context.CancelFunc,
) (*activePromptAnswerDelivery, error) {
	if key.sessionID.IsZero() || strings.TrimSpace(string(key.promptID)) == "" {
		return nil, errors.New("prompt answer delivery key is required")
	}
	if requestID.IsZero() {
		return nil, errors.New("prompt answer delivery request id is required")
	}
	if cancel == nil {
		return nil, errors.New("prompt answer delivery cancellation is required")
	}
	return &activePromptAnswerDelivery{key: key, requestID: requestID, cancel: cancel}, nil
}

func (d *activePromptAnswerDelivery) cancelPending() {
	if d != nil {
		d.cancel()
	}
}

func (d *activePromptAnswerDelivery) matches(key transcriptPromptKey, requestID runtimeids.RuntimeClientRequestID) bool {
	return d != nil && d.key == key && d.requestID == requestID
}

func retryTranscriptPromptAnswer(
	ctx context.Context,
	delays []time.Duration,
	wait func(context.Context, time.Duration) error,
	submit func() error,
) error {
	submitIfActive := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return submit()
	}
	err := submitIfActive()
	for _, delay := range delays {
		if !shouldRetryTranscriptPromptAnswer(err) {
			return err
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
		err = submitIfActive()
	}
	return err
}

func shouldRetryTranscriptPromptAnswer(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, serverapi.ErrPromptNotFound) &&
		!errors.Is(err, serverapi.ErrPromptAlreadyResolved) &&
		!errors.Is(err, serverapi.ErrPromptUnsupported)
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
