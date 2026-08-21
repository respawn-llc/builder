package runtime

import (
	"context"
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
	if e != nil && e.runtimeFIFO != nil {
		return awaitEngineRuntimeOperation(
			context.Background(),
			e,
			func(context.Context) (session.CommitReceipt, error) {
				return e.appendCommittedEntryWithCommitReceiptRaw(entry)
			},
		)
	}
	return e.appendCommittedEntryWithCommitReceiptRaw(entry)
}

func (e *Engine) appendCommittedEntryWithCommitReceiptRaw(entry storedLocalEntry) (session.CommitReceipt, error) {
	if entry.Role == "" || entry.Text == "" {
		return session.CommitReceipt{}, nil
	}
	return e.steerWithCommitReceiptRaw("", steerLocalEntryIntent(entry))
}

func (e *Engine) appendCommittedControlFeedback(text string) (session.CommitReceipt, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return session.CommitReceipt{}, errCommittedFeedbackTextRequired
	}
	return e.appendCommittedEntryWithCommitReceiptRaw(storedLocalEntry{
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
	result, err := awaitEngineRuntimeOperation(context.Background(), e, func(context.Context) (struct {
		changed bool
		receipt session.CommitReceipt
	}, error) {
		e.controlMutationMu.Lock()
		defer e.controlMutationMu.Unlock()
		changed := e.localFastModeEnabledChange(enabled)
		receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(changed))
		if receipt.Committed {
			e.applyFastModeEnabled(enabled)
		}
		return struct {
			changed bool
			receipt session.CommitReceipt
		}{changed: changed, receipt: receipt}, feedbackErr
	})
	return result.changed, result.receipt, err
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.QuestionsEnabled(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	result, err := awaitEngineRuntimeOperation(context.Background(), e, func(context.Context) (struct {
		changed bool
		enabled bool
		receipt session.CommitReceipt
	}, error) {
		e.controlMutationMu.Lock()
		defer e.controlMutationMu.Unlock()
		changed, current := e.questionsEnabledChange(enabled)
		resultEnabled := current
		if changed {
			resultEnabled = enabled
		}
		receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(resultEnabled, changed))
		if receipt.Committed && changed {
			e.applyQuestionsEnabled(enabled)
		}
		return struct {
			changed bool
			enabled bool
			receipt session.CommitReceipt
		}{changed: changed, enabled: resultEnabled, receipt: receipt}, feedbackErr
	})
	return result.changed, result.enabled, result.receipt, err
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	result, err := awaitEngineRuntimeOperation(context.Background(), e, func(context.Context) (struct {
		changed bool
		mode    string
		receipt session.CommitReceipt
	}, error) {
		e.controlMutationMu.Lock()
		defer e.controlMutationMu.Unlock()
		changed, mode, prepareErr := e.reviewerEnabledChange(enabled)
		if prepareErr != nil {
			return struct {
				changed bool
				mode    string
				receipt session.CommitReceipt
			}{mode: mode}, prepareErr
		}
		receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(mode != "off", mode, changed))
		if receipt.Committed {
			e.applyReviewerEnabled(enabled, mode)
		}
		return struct {
			changed bool
			mode    string
			receipt session.CommitReceipt
		}{changed: changed, mode: mode, receipt: receipt}, feedbackErr
	})
	return result.changed, result.mode, result.receipt, err
}

func (e *Engine) SetReviewerFrequencyWithCommittedFeedback(frequency string, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	target, err := e.PrepareReviewerFrequency(frequency)
	if err != nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, err
	}
	result, err := awaitEngineRuntimeOperation(context.Background(), e, func(context.Context) (struct {
		changed bool
		receipt session.CommitReceipt
	}, error) {
		e.controlMutationMu.Lock()
		defer e.controlMutationMu.Unlock()
		changed := e.ReviewerFrequency() != target
		receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(target != "off", target, changed))
		if receipt.Committed {
			e.setReviewerFrequency(target)
		}
		return struct {
			changed bool
			receipt session.CommitReceipt
		}{changed: changed, receipt: receipt}, feedbackErr
	})
	return result.changed, target, result.receipt, err
}
