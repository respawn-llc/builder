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
	return e.steerWithCommitReceipt("", steerLocalEntryIntent(entry))
}

func (e *Engine) appendCommittedControlFeedback(text string) (session.CommitReceipt, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return session.CommitReceipt{}, errCommittedFeedbackTextRequired
	}
	return e.appendCommittedEntryWithCommitReceipt(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       "system",
		Text:       text,
	})
}

func (e *Engine) SetFastModeEnabledWithCommittedFeedback(enabled bool, feedback func(changed bool) string) (bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	if enabled && !e.FastModeAvailable() {
		return false, session.CommitReceipt{}, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed := e.localFastModeEnabledChange(enabled)
	receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(changed))
	if !receipt.Committed {
		return changed, receipt, feedbackErr
	}
	e.applyFastModeEnabled(enabled)
	return changed, receipt, feedbackErr
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.QuestionsEnabled(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, current := e.questionsEnabledChange(enabled)
	resultEnabled := current
	if changed {
		resultEnabled = enabled
	}
	receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(resultEnabled, changed))
	if !receipt.Committed {
		return changed, resultEnabled, receipt, feedbackErr
	}
	if changed {
		e.applyQuestionsEnabled(enabled)
	}
	return changed, resultEnabled, receipt, feedbackErr
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, mode, err := e.reviewerEnabledChange(enabled)
	if err != nil {
		return false, mode, session.CommitReceipt{}, err
	}
	receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(mode != "off", mode, changed))
	if !receipt.Committed {
		return changed, mode, receipt, feedbackErr
	}
	e.applyReviewerEnabled(enabled, mode)
	return changed, mode, receipt, feedbackErr
}

func (e *Engine) SetReviewerFrequencyWithCommittedFeedback(frequency string, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	target, err := e.PrepareReviewerFrequency(frequency)
	if err != nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, err
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed := e.ReviewerFrequency() != target
	receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(target != "off", target, changed))
	if !receipt.Committed {
		return changed, target, receipt, feedbackErr
	}
	e.setReviewerFrequency(target)
	return changed, target, receipt, feedbackErr
}
