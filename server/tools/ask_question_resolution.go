package tools

import (
	"errors"
	"fmt"
	"strings"
)

type AskQuestionResolution interface {
	askQuestionResolution()
}

type AskQuestionAnswer struct {
	SelectedOptionNumber *int
	Freeform             *string
}

func (AskQuestionAnswer) askQuestionResolution() {}

// AskQuestionLegacyAnswer preserves both exact temporary single-prompt text
// slots until KENT-461 deletes that API. Nil means the corresponding legacy
// wire slot was semantically absent.
type AskQuestionLegacyAnswer struct {
	Answer               *string
	SelectedOptionNumber *int
	FreeformAnswer       *string
}

func (AskQuestionLegacyAnswer) askQuestionResolution() {}

type AskQuestionApproval struct {
	Decision   AskQuestionApprovalDecision
	Commentary *string
}

func (AskQuestionApproval) askQuestionResolution() {}

func ValidateAskQuestionResolutionShape(resolution AskQuestionResolution) error {
	switch answer := resolution.(type) {
	case AskQuestionAnswer:
		if answer.SelectedOptionNumber != nil && *answer.SelectedOptionNumber <= 0 {
			return errors.New("selected option number must be positive when present")
		}
		if answer.Freeform != nil && strings.TrimSpace(*answer.Freeform) == "" {
			return errors.New("freeform answer must be non-blank when present")
		}
		if answer.SelectedOptionNumber == nil && answer.Freeform == nil {
			return ErrAskQuestionNonApprovalRequiresAnswer
		}
		return nil
	case AskQuestionLegacyAnswer:
		if answer.SelectedOptionNumber != nil && *answer.SelectedOptionNumber <= 0 {
			return errors.New("selected option number must be positive when present")
		}
		if answer.Answer != nil && strings.TrimSpace(*answer.Answer) == "" {
			return errors.New("legacy answer must be non-blank when present")
		}
		if answer.FreeformAnswer != nil && strings.TrimSpace(*answer.FreeformAnswer) == "" {
			return errors.New("legacy freeform answer must be non-blank when present")
		}
		if answer.SelectedOptionNumber == nil &&
			answer.Answer == nil &&
			answer.FreeformAnswer == nil {
			return ErrAskQuestionNonApprovalRequiresAnswer
		}
		return nil
	case AskQuestionApproval:
		if answer.Commentary != nil && strings.TrimSpace(*answer.Commentary) == "" {
			return errors.New("approval commentary must be non-blank when present")
		}
		return validateApprovalDecision(answer.Decision)
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
	case AskQuestionLegacyAnswer:
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
	case AskQuestionLegacyAnswer:
		freeform := answer.FreeformAnswer
		if freeform == nil {
			freeform = answer.Answer
		}
		return questionResolutionText{
			selected: answer.SelectedOptionNumber,
			freeform: normalizedResolutionText(freeform),
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
