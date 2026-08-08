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
	"core/shared/invariant"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type PendingPromptResponder interface {
	AcceptPromptResolution(
		sessionID string,
		promptID string,
		resolution askquestion.AskQuestionResolution,
		err error,
	) (PromptResponseAcceptance, error)
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
	ErrorMessage         *string
	Answer               *string
	SelectedOptionNumber *int
	FreeformAnswer       *string
}

type approvalAnswerMemoRequest struct {
	SessionID    string
	ApprovalID   string
	ErrorMessage *string
	Decision     clientui.ApprovalDecision
	Commentary   *string
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
	errorMessage := textutil.OptionalExactString(req.ErrorMessage)
	answer := textutil.OptionalExactString(req.Answer)
	freeformAnswer := textutil.OptionalExactString(req.FreeformAnswer)
	memoReq := askAnswerMemoRequest{
		SessionID:            req.SessionID,
		AskID:                req.AskID,
		ErrorMessage:         errorMessage,
		Answer:               answer,
		SelectedOptionNumber: textutil.Pointer(req.SelectedOptionNumber),
		FreeformAnswer:       freeformAnswer,
	}
	acceptance, err := s.asks.Do(ctx, req.ClientRequestID, memoReq, sameAskAnswerMemoRequest, func(context.Context) (PromptResponseAcceptance, error) {
		if errorMessage != nil {
			return s.prompts.AcceptPromptResolution(req.SessionID, req.AskID, nil, errors.New(*errorMessage))
		}
		return s.prompts.AcceptPromptResolution(req.SessionID, req.AskID, askquestion.AskQuestionLegacyAnswer{
			Answer:               answer,
			SelectedOptionNumber: textutil.Pointer(req.SelectedOptionNumber),
			FreeformAnswer:       freeformAnswer,
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
	errorMessage := textutil.OptionalExactString(req.ErrorMessage)
	memoReq := approvalAnswerMemoRequest{
		SessionID:    req.SessionID,
		ApprovalID:   req.ApprovalID,
		ErrorMessage: errorMessage,
		Decision:     req.Decision,
		Commentary:   textutil.Pointer(req.Commentary),
	}
	_, err := s.approvals.Do(ctx, req.ClientRequestID, memoReq, sameApprovalAnswerMemoRequest, func(ctx context.Context) (struct{}, error) {
		if errorMessage != nil {
			_, err := s.prompts.AcceptPromptResolution(req.SessionID, req.ApprovalID, nil, errors.New(*errorMessage))
			return struct{}{}, err
		}
		_, err := s.prompts.AcceptPromptResolution(req.SessionID, req.ApprovalID, askquestion.AskQuestionApproval{
			Decision:   askquestion.AskQuestionApprovalDecision(req.Decision),
			Commentary: textutil.Pointer(req.Commentary),
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
			command.Payload = sessionruntime.PromptQuestionAnswerCommand{
				Answer: askquestion.AskQuestionAnswer{
					SelectedOptionNumber: textutil.Pointer(entry.QuestionAnswer.SelectedOptionNumber),
					Freeform:             entry.QuestionAnswer.Freeform,
				},
			}
		case entry.ApprovalAnswer != nil:
			command.Payload = sessionruntime.PromptApprovalAnswerCommand{
				Answer: askquestion.AskQuestionApproval{
					Decision:   askquestion.AskQuestionApprovalDecision(entry.ApprovalAnswer.Decision),
					Commentary: entry.ApprovalAnswer.Commentary,
				},
			}
		case entry.Declined != nil:
			command.Payload = sessionruntime.PromptDeclinedCommand{}
		default:
			return serverapi.PromptAnswerBatchResponse{}, reportPromptBatchTranslationInvariant(entry.PromptID)
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
		switch result.Outcome {
		case sessionruntime.PromptAnswerOutcomeResolved:
			response.Results = appendPromptAnswerBatchResult(
				response.Results,
				result.PromptID,
				serverapi.PromptAnswerBatchOutcomeResolved,
			)
		case sessionruntime.PromptAnswerOutcomeSkipped:
			response.Results = appendPromptAnswerBatchResult(
				response.Results,
				result.PromptID,
				serverapi.PromptAnswerBatchOutcomeSkipped,
			)
		default:
			return serverapi.PromptAnswerBatchResponse{}, fmt.Errorf(
				"prompt batch responder returned invalid outcome %q",
				result.Outcome,
			)
		}
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(req, response); err != nil {
		return serverapi.PromptAnswerBatchResponse{}, fmt.Errorf("validate prompt answer batch response: %w", err)
	}
	return response, nil
}

func reportPromptBatchTranslationInvariant(promptID clientui.PromptID) error {
	err := fmt.Errorf("validated prompt answer batch entry %q has no disposition", promptID)
	invariant.NewPolicy().Check(false, invariant.WorkflowPromptDiagnostic(
		"translate_prompt_answer_batch_entry",
		string(promptID),
		err,
	))
	return err
}

func appendPromptAnswerBatchResult(
	results []serverapi.PromptAnswerBatchResult,
	promptID clientui.PromptID,
	outcome serverapi.PromptAnswerBatchOutcome,
) []serverapi.PromptAnswerBatchResult {
	return append(results, serverapi.PromptAnswerBatchResult{
		PromptID: promptID,
		Outcome:  outcome,
	})
}

func sameAskAnswerMemoRequest(a askAnswerMemoRequest, b askAnswerMemoRequest) bool {
	return a.SessionID == b.SessionID &&
		a.AskID == b.AskID &&
		textutil.EqualOptional(a.ErrorMessage, b.ErrorMessage) &&
		textutil.EqualOptional(a.Answer, b.Answer) &&
		textutil.EqualOptional(a.SelectedOptionNumber, b.SelectedOptionNumber) &&
		textutil.EqualOptional(a.FreeformAnswer, b.FreeformAnswer)
}

func sameApprovalAnswerMemoRequest(a approvalAnswerMemoRequest, b approvalAnswerMemoRequest) bool {
	return a.SessionID == b.SessionID &&
		a.ApprovalID == b.ApprovalID &&
		textutil.EqualOptional(a.ErrorMessage, b.ErrorMessage) &&
		a.Decision == b.Decision &&
		textutil.EqualOptional(a.Commentary, b.Commentary)
}

var _ servicecontract.PromptControlService = (*PromptControlService)(nil)
