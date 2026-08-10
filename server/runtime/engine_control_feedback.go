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

func (e *Engine) SetFastModeEnabledWithCommittedFeedback(enabled bool, feedback func(changed bool) string) (bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	if enabled && !e.FastModeAvailable() {
		return false, session.CommitReceipt{}, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	result, err := submitRuntimeEvent(e, enabled, func(
		admission runtimeEventAdmission,
		enabled bool,
	) (committedBoolControlResult, error) {
		if state := e.fastModeState(); state != nil {
			var receipt session.CommitReceipt
			var feedbackErr error
			changed, transactionErr := state.SetEnabledWithTransaction(enabled, func(changed bool) error {
				receipt, feedbackErr = e.appendCommittedControlFeedbackUnderAdmission(admission, feedback(changed))
				if !receipt.Committed {
					return feedbackErr
				}
				return nil
			})
			if transactionErr != nil {
				return committedBoolControlResult{receipt: receipt}, transactionErr
			}
			if changed {
				e.markCurrentRequestShapeDirty()
			}
			return committedBoolControlResult{changed: changed, receipt: receipt}, feedbackErr
		}
		changed := e.localFastModeEnabledChange(enabled)
		receipt, feedbackErr := e.appendCommittedControlFeedbackUnderAdmission(admission, feedback(changed))
		if !receipt.Committed {
			return committedBoolControlResult{receipt: receipt}, feedbackErr
		}
		e.applyFastModeEnabled(enabled)
		return committedBoolControlResult{changed: changed, receipt: receipt}, feedbackErr
	})
	return result.changed, result.receipt, err
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.QuestionsEnabled(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	result, err := submitRuntimeEvent(e, enabled, func(
		admission runtimeEventAdmission,
		enabled bool,
	) (committedQuestionsControlResult, error) {
		changed, current := e.questionsEnabledChange(enabled)
		resultEnabled := current
		if changed {
			resultEnabled = enabled
		}
		receipt, feedbackErr := e.appendCommittedControlFeedbackUnderAdmission(
			admission,
			feedback(resultEnabled, changed),
		)
		if !receipt.Committed {
			return committedQuestionsControlResult{enabled: current, receipt: receipt}, feedbackErr
		}
		if changed {
			e.applyQuestionsEnabled(enabled)
		}
		return committedQuestionsControlResult{
			changed: changed,
			enabled: resultEnabled,
			receipt: receipt,
		}, feedbackErr
	})
	return result.changed, result.enabled, result.receipt, err
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	result, err := submitRuntimeEvent(e, enabled, func(
		admission runtimeEventAdmission,
		enabled bool,
	) (committedReviewerControlResult, error) {
		changed, mode, changeErr := e.reviewerEnabledChange(enabled)
		if changeErr != nil {
			return committedReviewerControlResult{mode: mode}, changeErr
		}
		receipt, feedbackErr := e.appendCommittedControlFeedbackUnderAdmission(
			admission,
			feedback(mode != "off", mode, changed),
		)
		if !receipt.Committed {
			return committedReviewerControlResult{mode: mode, receipt: receipt}, feedbackErr
		}
		e.applyReviewerEnabled(enabled, mode)
		return committedReviewerControlResult{
			changed: changed,
			mode:    mode,
			receipt: receipt,
		}, feedbackErr
	})
	return result.changed, result.mode, result.receipt, err
}

type committedBoolControlResult struct {
	changed bool
	receipt session.CommitReceipt
}

type committedQuestionsControlResult struct {
	changed bool
	enabled bool
	receipt session.CommitReceipt
}

type committedReviewerControlResult struct {
	changed bool
	mode    string
	receipt session.CommitReceipt
}

func (e *Engine) appendCommittedControlFeedbackUnderAdmission(
	admission runtimeEventAdmission,
	text string,
) (session.CommitReceipt, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return session.CommitReceipt{}, errCommittedFeedbackTextRequired
	}
	receipt := session.CommitReceipt{}
	intent := steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       "system",
		Text:       text,
	})
	intent.items[0].commitReceipt = &receipt
	err := admission.applySteering("", intent)
	return receipt, err
}
