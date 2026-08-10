package promptcontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	prompts PendingPromptResponder
}

func NewPromptControlService(prompts PendingPromptResponder) *PromptControlService {
	return &PromptControlService{prompts: prompts}
}

func (s *PromptControlService) AnswerAsk(ctx context.Context, req serverapi.AskAnswerRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if s == nil || s.prompts == nil {
		return errors.New("prompt responder is required")
	}
	var acceptance PromptResponseAcceptance
	var err error
	if strings.TrimSpace(req.ErrorMessage) != "" {
		acceptance, err = s.prompts.AcceptPromptResolution(
			req.SessionID,
			req.AskID,
			nil,
			errors.New(req.ErrorMessage),
		)
	} else {
		acceptance, err = s.prompts.AcceptPromptResolution(
			req.SessionID,
			req.AskID,
			askquestion.AskQuestionAnswerFromLegacyFields(
				textutil.Pointer(req.SelectedOptionNumber),
				textutil.OptionalExactString(req.Answer),
				textutil.OptionalExactString(req.FreeformAnswer),
			),
			nil,
		)
	}
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
	if strings.TrimSpace(req.ErrorMessage) != "" {
		_, err := s.prompts.AcceptPromptResolution(
			req.SessionID,
			req.ApprovalID,
			nil,
			errors.New(req.ErrorMessage),
		)
		return err
	}
	_, err := s.prompts.AcceptPromptResolution(
		req.SessionID,
		req.ApprovalID,
		askquestion.AskQuestionApproval{
			Decision:   askquestion.AskQuestionApprovalDecision(req.Decision),
			Commentary: textutil.Pointer(req.Commentary),
		},
		nil,
	)
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

var _ servicecontract.PromptControlService = (*PromptControlService)(nil)
