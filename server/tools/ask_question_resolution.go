package tools

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/sessioncontract"
)

type AskQuestionResolution interface {
	askQuestionResolution()
}

type AskQuestionAnswer struct {
	SelectedOptionNumber *int    `json:"selected_option_number,omitempty"`
	Freeform             *string `json:"freeform,omitempty"`
}

func (AskQuestionAnswer) askQuestionResolution() {}

// AskQuestionAnswerFromLegacyFields translates the temporary single-prompt
// wire fields after request-memo admission. KENT-461 deletes those fields.
func AskQuestionAnswerFromLegacyFields(
	selectedOptionNumber *int,
	answer *string,
	freeformAnswer *string,
) AskQuestionAnswer {
	if freeformAnswer == nil {
		freeformAnswer = answer
	}
	return AskQuestionAnswer{
		SelectedOptionNumber: selectedOptionNumber,
		Freeform:             freeformAnswer,
	}
}

type AskQuestionApproval struct {
	Decision   AskQuestionApprovalDecision
	Commentary *string
}

func (AskQuestionApproval) askQuestionResolution() {}

func ValidateAskQuestionResolutionShape(resolution AskQuestionResolution) error {
	switch answer := resolution.(type) {
	case AskQuestionAnswer:
		return sessioncontract.ValidatePromptQuestionAnswerShape(
			answer.SelectedOptionNumber,
			answer.Freeform,
		)
	case AskQuestionApproval:
		return sessioncontract.ValidatePromptApprovalAnswerShape(
			answer.Decision,
			answer.Commentary,
		)
	default:
		return errors.New("Ask Question resolution variant is invalid")
	}
}

func ValidateAskQuestionResolution(req AskQuestionRequest, resolution AskQuestionResolution) error {
	if err := ValidateAskQuestionResolutionShape(resolution); err != nil {
		return err
	}
	switch answer := resolution.(type) {
	case AskQuestionAnswer:
		if req.Approval {
			return ErrAskQuestionApprovalRequiresResponse
		}
		return validateSelectedOptionOffer(req, answer.SelectedOptionNumber)
	case AskQuestionApproval:
		if !req.Approval {
			return ErrAskQuestionNonApprovalForbidsApproval
		}
		return validateOfferedApproval(req, answer.Decision)
	default:
		return errors.New("Ask Question resolution variant is invalid")
	}
}

func validateSelectedOptionOffer(req AskQuestionRequest, selected *int) error {
	if selected == nil {
		return nil
	}
	if len(req.Suggestions) == 0 {
		return ErrAskQuestionSelectedOptionRequiresSuggest
	}
	if *selected > len(req.Suggestions) {
		return fmt.Errorf("selected option number %d is out of range", *selected)
	}
	return nil
}

func validateOfferedApproval(req AskQuestionRequest, decision AskQuestionApprovalDecision) error {
	for _, option := range req.ApprovalOptions {
		if option.Decision == decision {
			return nil
		}
	}
	return fmt.Errorf("approval decision %q was not offered", decision)
}

type questionResolutionText struct {
	selected *int
	freeform *string
}

func resolutionQuestionText(resolution AskQuestionResolution) (questionResolutionText, error) {
	switch answer := resolution.(type) {
	case AskQuestionAnswer:
		return questionResolutionText{
			selected: answer.SelectedOptionNumber,
			freeform: normalizedResolutionText(answer.Freeform),
		}, nil
	default:
		return questionResolutionText{}, fmt.Errorf("Question resolution type %T is invalid", resolution)
	}
}

func normalizedResolutionText(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	return &normalized
}

func buildResolutionToolOutputSummary(resolution AskQuestionResolution) (string, error) {
	answer, err := resolutionQuestionText(resolution)
	if err != nil {
		return "", err
	}
	if answer.selected != nil {
		return selectedOptionToolOutputSummary(*answer.selected, answer.freeform), nil
	}
	if answer.freeform == nil {
		return "", ErrAskQuestionNonApprovalRequiresAnswer
	}
	return "User answered: " + *answer.freeform, nil
}

func buildResolutionCondensedToolOutputText(
	req AskQuestionRequest,
	resolution AskQuestionResolution,
) (string, error) {
	answer, err := resolutionQuestionText(resolution)
	if err != nil {
		return "", err
	}
	if answer.selected == nil {
		if answer.freeform == nil {
			return "", nil
		}
		return *answer.freeform, nil
	}
	suggestions := normalizedSuggestions(req.Suggestions)
	optionIndex := *answer.selected - 1
	if optionIndex < 0 || optionIndex >= len(suggestions) {
		return "", nil
	}
	base := suggestions[optionIndex]
	if answer.freeform == nil {
		return base, nil
	}
	return base + "\nUser also said:\n" + *answer.freeform, nil
}
