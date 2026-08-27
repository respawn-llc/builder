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

func (e *Engine) appendCommittedEntryWithCommitReceipt(ctx context.Context, entry storedLocalEntry) (session.CommitReceipt, error) {
	return awaitEngineRuntimeOperation(
		ctx,
		e,
		func(context.Context) (session.CommitReceipt, error) {
			return e.appendCommittedEntryWithCommitReceiptRaw(entry)
		},
	)
}

func (e *Engine) appendCommittedEntryWithCommitReceiptRaw(entry storedLocalEntry) (session.CommitReceipt, error) {
	if entry.Role == "" || entry.Text == "" {
		return session.CommitReceipt{}, nil
	}
	return e.steerWithCommitReceiptRaw(sessionSteeringProvenance(), steerLocalEntryIntent(entry))
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

func (e *Engine) mutateChatSettingWithCommittedFeedback(
	mutation session.ChatSettingsMutation,
	feedback func(changed bool) string,
	apply func(),
) (bool, session.CommitReceipt, error) {
	settings, settingsErr := e.store.MutateChatSettings(mutation)
	if e.stopAfterDefinitelyUncommittedChatSetting(settings.CommitReceipt, settingsErr) {
		return false, settings.CommitReceipt, settingsErr
	}
	receipt, feedbackErr := e.appendCommittedControlFeedback(feedback(settings.Changed))
	if settings.Committed {
		receipt = settings.CommitReceipt
	}
	apply()
	return settings.Changed, receipt, errors.Join(
		settingsErr,
		feedbackErr,
		e.emitRaw(Event{Kind: EventSessionStatusChanged}),
	)
}

func (e *Engine) stopAfterDefinitelyUncommittedChatSetting(receipt session.CommitReceipt, err error) bool {
	if err == nil || receipt.Committed {
		return false
	}
	e.closeAndRetireAfterRuntimeAbort()
	return true
}

func (e *Engine) SetFastModeEnabledWithCommittedFeedback(ctx context.Context, enabled bool, feedback func(changed bool) string) (bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	if enabled && !e.FastModeAvailable() {
		return false, session.CommitReceipt{}, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	result, err := awaitEngineRuntimeOperation(ctx, e, func(context.Context) (struct {
		changed bool
		receipt session.CommitReceipt
	}, error) {
		changed, receipt, mutationErr := e.mutateChatSettingWithCommittedFeedback(
			session.ChatSettingsMutation{Fast: &enabled},
			feedback,
			func() { e.applyFastModeEnabled(enabled) },
		)
		return struct {
			changed bool
			receipt session.CommitReceipt
		}{changed: changed, receipt: receipt}, mutationErr
	})
	return result.changed, result.receipt, err
}

func (e *Engine) SetQuestionsEnabledWithCommittedFeedback(ctx context.Context, enabled bool, feedback func(enabled bool, changed bool) string) (bool, bool, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.QuestionsEnabled(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	result, err := awaitEngineRuntimeOperation(ctx, e, func(context.Context) (struct {
		changed bool
		enabled bool
		receipt session.CommitReceipt
	}, error) {
		changed, receipt, mutationErr := e.mutateChatSettingWithCommittedFeedback(
			session.ChatSettingsMutation{Questions: &enabled},
			func(changed bool) string { return feedback(enabled, changed) },
			func() { e.applyQuestionsEnabled(enabled) },
		)
		return struct {
			changed bool
			enabled bool
			receipt session.CommitReceipt
		}{changed: changed, enabled: enabled, receipt: receipt}, mutationErr
	})
	return result.changed, result.enabled, result.receipt, err
}

func (e *Engine) SetReviewerEnabledWithCommittedFeedback(ctx context.Context, enabled bool, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	result, err := awaitEngineRuntimeOperation(ctx, e, func(context.Context) (struct {
		changed bool
		mode    string
		receipt session.CommitReceipt
	}, error) {
		_, mode, prepareErr := e.reviewerEnabledChange(enabled)
		if prepareErr != nil {
			return struct {
				changed bool
				mode    string
				receipt session.CommitReceipt
			}{mode: mode}, prepareErr
		}
		changed, receipt, mutationErr := e.mutateChatSettingWithCommittedFeedback(
			session.ChatSettingsMutation{Supervisor: &mode},
			func(changed bool) string { return feedback(mode != "off", mode, changed) },
			func() { e.setReviewerFrequency(mode) },
		)
		return struct {
			changed bool
			mode    string
			receipt session.CommitReceipt
		}{changed: changed, mode: mode, receipt: receipt}, mutationErr
	})
	return result.changed, result.mode, result.receipt, err
}

func (e *Engine) SetReviewerFrequencyWithCommittedFeedback(ctx context.Context, frequency string, feedback func(enabled bool, mode string, changed bool) string) (bool, string, session.CommitReceipt, error) {
	if feedback == nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, errCommittedFeedbackBuilderRequired
	}
	target, err := e.PrepareReviewerFrequency(frequency)
	if err != nil {
		return false, e.ReviewerFrequency(), session.CommitReceipt{}, err
	}
	result, err := awaitEngineRuntimeOperation(ctx, e, func(context.Context) (struct {
		changed bool
		receipt session.CommitReceipt
	}, error) {
		changed, receipt, mutationErr := e.mutateChatSettingWithCommittedFeedback(
			session.ChatSettingsMutation{Supervisor: &target},
			func(changed bool) string { return feedback(target != "off", target, changed) },
			func() { e.setReviewerFrequency(target) },
		)
		return struct {
			changed bool
			receipt session.CommitReceipt
		}{changed: changed, receipt: receipt}, mutationErr
	})
	return result.changed, target, result.receipt, err
}
