package app

import (
	"context"
	"errors"
	"time"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const transcriptPromptAnswerRetryDelay = 250 * time.Millisecond

type transcriptPromptAnswerer struct {
	ctx     context.Context
	control apicontract.PromptControlService
}

func newTranscriptPromptAnswerer(ctx context.Context, control apicontract.PromptControlService) *transcriptPromptAnswerer {
	if ctx == nil || control == nil {
		return nil
	}
	return &transcriptPromptAnswerer{ctx: ctx, control: control}
}

func (a *transcriptPromptAnswerer) event(prompt clientui.TranscriptPrompt) askEvent {
	prompt = cloneTranscriptPromptForAsk(prompt)
	reply := make(chan askReply, 1)
	if a == nil || a.ctx == nil || a.control == nil {
		return askEvent{prompt: prompt, reply: reply}
	}
	promptCtx, cancel := context.WithCancel(a.ctx)
	go func() {
		var result askReply
		select {
		case <-promptCtx.Done():
			return
		case result = <-reply:
		}
		if promptCtx.Err() != nil {
			return
		}
		operationID := runtimeids.NewRuntimeClientRequestID()
		if transcriptPromptIsApproval(prompt) {
			request := serverapi.ApprovalAnswerRequest{
				ClientRequestID: operationID.String(),
				SessionID:       prompt.SessionID.String(),
				ApprovalID:      string(prompt.PromptID),
			}
			if result.err != nil {
				request.ErrorMessage = result.err.Error()
			} else if result.response.Approval != nil {
				request.Decision = result.response.Approval.Decision
				request.Commentary = result.response.Approval.Commentary
			} else {
				request.ErrorMessage = "approval response is required"
			}
			retryTranscriptPromptAnswer(promptCtx, func() error {
				return a.control.AnswerApproval(promptCtx, request)
			})
			return
		}
		request := serverapi.AskAnswerRequest{
			ClientRequestID: operationID.String(),
			SessionID:       prompt.SessionID.String(),
			AskID:           string(prompt.PromptID),
		}
		if result.err != nil {
			request.ErrorMessage = result.err.Error()
		} else {
			request.Answer = result.response.Answer
			request.SelectedOptionNumber = result.response.SelectedOptionNumber
			request.FreeformAnswer = result.response.FreeformAnswer
		}
		retryTranscriptPromptAnswer(promptCtx, func() error {
			return a.control.AnswerAsk(promptCtx, request)
		})
	}()
	return askEvent{prompt: prompt, reply: reply, cancel: cancel}
}

func retryTranscriptPromptAnswer(ctx context.Context, submit func() error) {
	for {
		err := submit()
		if !shouldRetryTranscriptPromptAnswer(err) {
			return
		}
		timer := time.NewTimer(transcriptPromptAnswerRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
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
