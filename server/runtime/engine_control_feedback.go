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
	return e.appendCommittedEntryWithCommitReceiptAndOrderedTurn(nil, entry)
}

func (e *Engine) appendCommittedControlFeedback(text string) (session.CommitReceipt, error) {
	return e.appendCommittedControlFeedbackWithOrderedTurn(nil, text)
}

func (e *Engine) appendCommittedControlFeedbackWithOrderedTurn(turn OrderedMutationTurn, text string) (session.CommitReceipt, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return session.CommitReceipt{}, errCommittedFeedbackTextRequired
	}
	return e.appendCommittedEntryWithCommitReceiptAndOrderedTurn(turn, storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       "system",
		Text:       text,
	})
}

func (e *Engine) SetFastModeEnabledWithCommittedFeedback(enabled bool, feedback func(changed bool) string) (bool, session.CommitReceipt, error) {
	return e.SetFastModeEnabledWithCommittedFeedbackAndOrderedTurn(nil, enabled, feedback)
}

func (e *Engine) SetFastModeEnabledWithCommittedFeedbackAndOrderedTurn(turn OrderedMutationTurn, enabled bool, feedback func(changed bool) string) (bool, session.CommitReceipt, error) {
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
			receipt, feedbackErr = e.appendCommittedControlFeedbackWithOrderedTurn(turn, feedback(changed))
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
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed := e.localFastModeEnabledChange(enabled)
	receipt, feedbackErr := e.appendCommittedControlFeedbackWithOrderedTurn(turn, feedback(changed))
	if !receipt.Committed {
		return false, receipt, feedbackErr
	}
	e.applyFastModeEnabled(enabled)
	return changed, receipt, feedbackErr
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
	return e.SetQuestionsEnabledWithCommittedFeedbackAndOrderedTurn(nil, enabled, feedback)
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedbackAndOrderedTurn(turn OrderedMutationTurn, enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
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
	receipt, feedbackErr := e.appendCommittedControlFeedbackWithOrderedTurn(turn, feedback(resultEnabled, changed))
	if !receipt.Committed {
		return false, current, receipt, feedbackErr
	}
	if changed {
		e.applyQuestionsEnabled(enabled)
	}
	return changed, resultEnabled, receipt, feedbackErr
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	return e.SetReviewerEnabledWithCommittedFeedbackAndOrderedTurn(nil, enabled, feedback)
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedbackAndOrderedTurn(turn OrderedMutationTurn, enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	changed, mode, err := e.reviewerEnabledChange(enabled)
	if err != nil {
		return false, mode, session.CommitReceipt{}, err
	}
	receipt, feedbackErr := e.appendCommittedControlFeedbackWithOrderedTurn(turn, feedback(mode != "off", mode, changed))
	if !receipt.Committed {
		return false, mode, receipt, feedbackErr
	}
	e.applyReviewerEnabled(enabled, mode)
	return changed, mode, receipt, feedbackErr
}
