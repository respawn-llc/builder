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
// slots until KENT-461 deletes that API. Both slots are required so empty
// values are payload content, never absence sentinels.
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

// AskQuestionLegacyApproval preserves the exact commentary slot used by the
// temporary Task Detail single-prompt operation until KENT-461.
type AskQuestionLegacyApproval struct {
	Decision   AskQuestionApprovalDecision
	Commentary *string
}

func (AskQuestionLegacyApproval) askQuestionResolution() {}

type AskQuestionDeclined struct{}

func (AskQuestionDeclined) askQuestionResolution() {}

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
		if answer.Answer == nil || answer.FreeformAnswer == nil {
			return errors.New("legacy Question answer requires both exact text slots")
		}
		if answer.SelectedOptionNumber != nil && *answer.SelectedOptionNumber <= 0 {
			return errors.New("selected option number must be positive when present")
		}
		if answer.SelectedOptionNumber == nil &&
			strings.TrimSpace(*answer.Answer) == "" &&
			strings.TrimSpace(*answer.FreeformAnswer) == "" {
			return ErrAskQuestionNonApprovalRequiresAnswer
		}
		return nil
	case AskQuestionApproval:
		if answer.Commentary != nil && strings.TrimSpace(*answer.Commentary) == "" {
			return errors.New("approval commentary must be non-blank when present")
		}
		return validateApprovalDecision(answer.Decision)
	case AskQuestionLegacyApproval:
		if answer.Commentary == nil {
			return errors.New("legacy Approval answer requires its exact commentary slot")
		}
		return validateApprovalDecision(answer.Decision)
	case AskQuestionDeclined:
		return nil
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
	case AskQuestionLegacyApproval:
		if !req.Approval {
			return ErrAskQuestionNonApprovalForbidsApproval
		}
		return validateOfferedApproval(req, answer.Decision)
	case AskQuestionDeclined:
		return nil
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

func resolutionQuestionText(resolution AskQuestionResolution) (selected *int, freeform string, err error) {
	switch answer := resolution.(type) {
	case AskQuestionAnswer:
		if answer.Freeform != nil {
			freeform = strings.TrimSpace(*answer.Freeform)
		}
		return answer.SelectedOptionNumber, freeform, nil
	case AskQuestionLegacyAnswer:
		if answer.Answer == nil || answer.FreeformAnswer == nil {
			return nil, "", errors.New("legacy Question answer requires both exact text slots")
		}
		if freeform = strings.TrimSpace(*answer.FreeformAnswer); freeform == "" {
			freeform = strings.TrimSpace(*answer.Answer)
		}
		return answer.SelectedOptionNumber, freeform, nil
	default:
		return nil, "", fmt.Errorf("Question resolution type %T is invalid", resolution)
	}
}

func buildResolutionToolOutputSummary(resolution AskQuestionResolution) (string, error) {
	selected, freeform, err := resolutionQuestionText(resolution)
	if err != nil {
		return "", err
	}
	if selected != nil {
		return selectedOptionToolOutputSummary(*selected, freeform), nil
	}
	if freeform == "" {
		return "", ErrAskQuestionNonApprovalRequiresAnswer
	}
	return "User answered: " + freeform, nil
}

func buildResolutionCondensedToolOutputText(
	req AskQuestionRequest,
	resolution AskQuestionResolution,
) (string, error) {
	selected, freeform, err := resolutionQuestionText(resolution)
	if err != nil {
		return "", err
	}
	if selected == nil {
		return freeform, nil
	}
	suggestions := normalizedSuggestions(req.Suggestions)
	optionIndex := *selected - 1
	if optionIndex < 0 || optionIndex >= len(suggestions) {
		return "", nil
	}
	base := suggestions[optionIndex]
	if freeform == "" {
		return base, nil
	}
	return base + "\nUser also said:\n" + freeform, nil
}
