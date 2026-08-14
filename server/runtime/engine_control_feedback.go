package runtime

import (
	"errors"
	"strings"

	"core/server/session"
	"core/shared/transcript"
)

var (
	errCommittedFeedbackBuilderRequired = errors.New("committed feedback builder is required")
	errCommittedFeedbackTextRequired    = errors.New("committed feedback text is required")
)

func (e *Engine) appendCommittedEntryWithCommitReceipt(entry storedLocalEntry) (session.CommitReceipt, error) {
	if entry.Role == "" || entry.Text == "" {
		return session.CommitReceipt{}, nil
	}
	return e.steerRuntimeWithCommitReceipt(steerLocalEntryIntent(entry))
}

func committedControlFeedbackMutation(text string) (steeringMutation, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errCommittedFeedbackTextRequired
	}
	return &steeringLocalEntry{entry: storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       "system",
		Text:       text,
	}}, nil
}

func (e *Engine) SetFastModeEnabledWithCommittedFeedback(enabled bool, feedback func(changed bool) string) (bool, session.CommitReceipt, error) {
	var changed bool
	receipt, err := e.steerRuntimeWithCommitReceipt(steerFastModeIntent(enabled, feedback, &changed))
	return changed, receipt, err
}

func (e *Engine) applyFastModeWithCommittedFeedback(
	enabled bool,
	feedback func(changed bool) string,
	applyFeedback func(steeringMutation, *session.CommitReceipt) error,
) (bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	if enabled && !e.FastModeAvailable() {
		return false, session.CommitReceipt{}, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	if state := e.fastModeState(); state != nil {
		var receipt session.CommitReceipt
		var feedbackErr error
		changed, err := state.SetEnabledWithTransaction(enabled, func(changed bool) error {
			mutation, err := committedControlFeedbackMutation(feedback(changed))
			if err != nil {
				return err
			}
			feedbackErr = applyFeedback(mutation, &receipt)
			if !receipt.Committed {
				return feedbackErr
			}
			return nil
		})
		if err != nil {
			return false, receipt, err
		}
		if changed {
			e.markCurrentRequestShapeDirty()
		}
		return changed, receipt, feedbackErr
	}
	changed := e.localFastModeEnabledChange(enabled)
	var receipt session.CommitReceipt
	mutation, err := committedControlFeedbackMutation(feedback(changed))
	if err != nil {
		return false, receipt, err
	}
	feedbackErr := applyFeedback(mutation, &receipt)
	if !receipt.Committed {
		return false, receipt, feedbackErr
	}
	e.applyFastModeEnabled(enabled)
	return changed, receipt, feedbackErr
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
	var changed, resultEnabled bool
	receipt, err := e.steerRuntimeWithCommitReceipt(
		steerQuestionsIntent(enabled, feedback, &changed, &resultEnabled),
	)
	return changed, resultEnabled, receipt, err
}

func (e *Engine) applyQuestionsWithCommittedFeedback(
	enabled bool,
	feedback func(enabled bool, changed bool) string,
	applyFeedback func(steeringMutation, *session.CommitReceipt) error,
) (bool, bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.QuestionsEnabled(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	changed, current := e.questionsEnabledChange(enabled)
	resultEnabled := current
	if changed {
		resultEnabled = enabled
	}
	var receipt session.CommitReceipt
	mutation, err := committedControlFeedbackMutation(feedback(resultEnabled, changed))
	if err != nil {
		return false, current, receipt, err
	}
	feedbackErr := applyFeedback(mutation, &receipt)
	if !receipt.Committed {
		return false, current, receipt, feedbackErr
	}
	if changed {
		e.applyQuestionsEnabled(enabled)
	}
	return changed, resultEnabled, receipt, feedbackErr
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	var changed bool
	var mode string
	receipt, err := e.steerRuntimeWithCommitReceipt(
		steerReviewerModeIntent(enabled, feedback, &changed, &mode),
	)
	return changed, mode, receipt, err
}

func (e *Engine) applyReviewerWithCommittedFeedback(
	enabled bool,
	feedback func(enabled bool, mode string, changed bool) string,
	applyFeedback func(steeringMutation, *session.CommitReceipt) error,
) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	changed, mode, err := e.reviewerEnabledChange(enabled)
	if err != nil {
		return false, mode, session.CommitReceipt{}, err
	}
	var receipt session.CommitReceipt
	mutation, err := committedControlFeedbackMutation(feedback(mode != "off", mode, changed))
	if err != nil {
		return false, mode, receipt, err
	}
	feedbackErr := applyFeedback(mutation, &receipt)
	if !receipt.Committed {
		return false, mode, receipt, feedbackErr
	}
	e.applyReviewerEnabled(enabled, mode)
	return changed, mode, receipt, feedbackErr
}
