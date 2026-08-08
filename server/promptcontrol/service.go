package promptcontrol

import (
	"context"
	"errors"
	"fmt"

	"core/server/requestmemo"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type PendingPromptResponder interface {
	AcceptPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) (PromptResponseAcceptance, error)
	ResolvePromptBatch(
		context.Context,
		runtimeids.SessionID,
		runtimeids.StepID,
		[]sessionruntime.PromptAnswerCommand,
	) ([]sessionruntime.PromptAnswerResult, error)
}

type PromptResponseAcceptance interface {
	AwaitSuccessor(context.Context) error
}

type PromptControlService struct {
	prompts   PendingPromptResponder
	asks      *requestmemo.Memo[askAnswerMemoRequest, PromptResponseAcceptance]
	approvals *requestmemo.Memo[approvalAnswerMemoRequest, struct{}]
}

type askAnswerMemoRequest struct {
	SessionID            string
	AskID                string
	ErrorMessage         string
	Answer               string
	SelectedOptionNumber *int
	FreeformAnswer       string
}

type approvalAnswerMemoRequest struct {
	SessionID    string
	ApprovalID   string
	ErrorMessage string
	Decision     clientui.ApprovalDecision
	Commentary   string
}

func NewPromptControlService(prompts PendingPromptResponder) *PromptControlService {
	return &PromptControlService{
		prompts:   prompts,
		asks:      requestmemo.New[askAnswerMemoRequest, PromptResponseAcceptance](),
		approvals: requestmemo.New[approvalAnswerMemoRequest, struct{}](),
	}
}

func (s *PromptControlService) AnswerAsk(ctx context.Context, req serverapi.AskAnswerRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if s == nil || s.prompts == nil {
		return errors.New("prompt responder is required")
	}
	memoReq := askAnswerMemoRequest{
		SessionID:            req.SessionID,
		AskID:                req.AskID,
		ErrorMessage:         req.ErrorMessage,
		Answer:               req.Answer,
		SelectedOptionNumber: textutil.Pointer(req.SelectedOptionNumber),
		FreeformAnswer:       req.FreeformAnswer,
	}
	acceptance, err := s.asks.Do(ctx, req.ClientRequestID, memoReq, sameAskAnswerMemoRequest, func(context.Context) (PromptResponseAcceptance, error) {
		if req.ErrorMessage != "" {
			return s.prompts.AcceptPromptResponse(req.SessionID, askquestion.AskQuestionResponse{RequestID: req.AskID}, errors.New(req.ErrorMessage))
		}
		return s.prompts.AcceptPromptResponse(req.SessionID, askquestion.AskQuestionResponse{
			RequestID:            req.AskID,
			Answer:               req.Answer,
			SelectedOptionNumber: textutil.Pointer(req.SelectedOptionNumber),
			FreeformAnswer:       req.FreeformAnswer,
		}, nil)
	})
	if err != nil {
		return err
	}
	return acceptance.AwaitSuccessor(ctx)
}

func (s *PromptControlService) AnswerApproval(ctx context.Context, req serverapi.ApprovalAnswerRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if s == nil || s.prompts == nil {
		return errors.New("prompt responder is required")
	}
	commentary := ""
	if req.Commentary != nil {
		commentary = *req.Commentary
	}
	memoReq := approvalAnswerMemoRequest{
		SessionID:    req.SessionID,
		ApprovalID:   req.ApprovalID,
		ErrorMessage: req.ErrorMessage,
		Decision:     req.Decision,
		Commentary:   commentary,
	}
	_, err := s.approvals.Do(ctx, req.ClientRequestID, memoReq, sameApprovalAnswerMemoRequest, func(ctx context.Context) (struct{}, error) {
		if req.ErrorMessage != "" {
			_, err := s.prompts.AcceptPromptResponse(req.SessionID, askquestion.AskQuestionResponse{RequestID: req.ApprovalID}, errors.New(req.ErrorMessage))
			return struct{}{}, err
		}
		_, err := s.prompts.AcceptPromptResponse(req.SessionID, askquestion.AskQuestionResponse{
			RequestID: req.ApprovalID,
			Approval: &askquestion.AskQuestionApprovalPayload{
				Decision:   askquestion.AskQuestionApprovalDecision(req.Decision),
				Commentary: commentary,
			},
		}, nil)
		return struct{}{}, err
	})
	return err
}

func (s *PromptControlService) AnswerPromptBatch(
	ctx context.Context,
	req serverapi.PromptAnswerBatchRequest,
) (serverapi.PromptAnswerBatchResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.PromptAnswerBatchResponse{}, err
	}
	if s == nil || s.prompts == nil {
		return serverapi.PromptAnswerBatchResponse{}, errors.New("prompt responder is required")
	}
	commands := make([]sessionruntime.PromptAnswerCommand, 0, len(req.Entries))
	for _, entry := range req.Entries {
		command := sessionruntime.PromptAnswerCommand{PromptID: entry.PromptID}
		switch {
		case entry.QuestionAnswer != nil:
			command.Disposition = sessionruntime.PromptAnswerDispositionAnswered
			command.Response = askquestion.AskQuestionResponse{
				RequestID:            string(entry.PromptID),
				SelectedOptionNumber: textutil.Pointer(entry.QuestionAnswer.SelectedOptionNumber),
			}
			if entry.QuestionAnswer.Freeform != nil {
				command.Response.FreeformAnswer = *entry.QuestionAnswer.Freeform
			}
		case entry.ApprovalAnswer != nil:
			command.Disposition = sessionruntime.PromptAnswerDispositionAnswered
			commentary := ""
			if entry.ApprovalAnswer.Commentary != nil {
				commentary = *entry.ApprovalAnswer.Commentary
			}
			command.Response = askquestion.AskQuestionResponse{
				RequestID: string(entry.PromptID),
				Approval: &askquestion.AskQuestionApprovalPayload{
					Decision:   askquestion.AskQuestionApprovalDecision(entry.ApprovalAnswer.Decision),
					Commentary: commentary,
				},
			}
		case entry.Declined != nil:
			command.Disposition = sessionruntime.PromptAnswerDispositionDeclined
		default:
			panic("validated prompt answer batch entry has no disposition")
		}
		commands = append(commands, command)
	}
	results, err := s.prompts.ResolvePromptBatch(ctx, req.SessionID, req.StepID, commands)
	if err != nil {
		return serverapi.PromptAnswerBatchResponse{}, err
	}
	response := serverapi.PromptAnswerBatchResponse{
		Results: make([]serverapi.PromptAnswerBatchResult, 0, len(results)),
	}
	for _, result := range results {
		outcome := serverapi.PromptAnswerBatchOutcome("")
		switch result.Outcome {
		case sessionruntime.PromptAnswerOutcomeResolved:
			outcome = serverapi.PromptAnswerBatchOutcomeResolved
		case sessionruntime.PromptAnswerOutcomeSkipped:
			outcome = serverapi.PromptAnswerBatchOutcomeSkipped
		default:
			return serverapi.PromptAnswerBatchResponse{}, fmt.Errorf("prompt batch responder returned invalid outcome %q", result.Outcome)
		}
		response.Results = append(response.Results, serverapi.PromptAnswerBatchResult{
			PromptID: result.PromptID,
			Outcome:  outcome,
		})
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(req, response); err != nil {
		return serverapi.PromptAnswerBatchResponse{}, fmt.Errorf("validate prompt answer batch response: %w", err)
	}
	return response, nil
}

func sameAskAnswerMemoRequest(a askAnswerMemoRequest, b askAnswerMemoRequest) bool {
	return a.SessionID == b.SessionID &&
		a.AskID == b.AskID &&
		a.ErrorMessage == b.ErrorMessage &&
		a.Answer == b.Answer &&
		textutil.EqualOptional(a.SelectedOptionNumber, b.SelectedOptionNumber) &&
		a.FreeformAnswer == b.FreeformAnswer
}

func sameApprovalAnswerMemoRequest(a approvalAnswerMemoRequest, b approvalAnswerMemoRequest) bool {
	return a.SessionID == b.SessionID &&
		a.ApprovalID == b.ApprovalID &&
		a.ErrorMessage == b.ErrorMessage &&
		a.Decision == b.Decision &&
		a.Commentary == b.Commentary
}

var _ servicecontract.PromptControlService = (*PromptControlService)(nil)
