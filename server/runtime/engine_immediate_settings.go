package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"

	"core/server/session"
	"core/shared/clientui"
)

type SessionSettingPublication func(clientui.TranscriptSessionSettingFeedback) error

type immediateSessionSettingOwner struct {
	mu sync.Mutex
}

func (e *Engine) mutateImmediateSessionSetting(
	ctx context.Context,
	persist func() (session.CommitReceipt, bool, error),
	apply func(),
	feedback clientui.TranscriptSessionSettingFeedback,
	publish SessionSettingPublication,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if e.closed.Load() {
		return false, ErrEngineClosed
	}

	e.immediateSettings.mu.Lock()
	defer e.immediateSettings.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, err
	}
	if e.closed.Load() {
		return false, ErrEngineClosed
	}
	receipt, changed, mutationErr := persist()
	if e.stopAfterDefinitelyUncommittedSessionSetting(receipt, mutationErr) {
		return false, mutationErr
	}
	if apply != nil {
		apply()
	}
	if publish == nil {
		return changed, mutationErr
	}
	feedback.Changed = changed
	return changed, errors.Join(mutationErr, publish(feedback))
}

func (e *Engine) stopAfterDefinitelyUncommittedSessionSetting(receipt session.CommitReceipt, err error) bool {
	if err == nil || receipt.Committed {
		return false
	}
	e.closeAndRetireAfterRuntimeAbort()
	return true
}

func (e *Engine) mutateImmediateChatSetting(
	ctx context.Context,
	mutation func() (session.ChatSettingsMutation, error),
	apply func(),
	feedback clientui.TranscriptSessionSettingFeedback,
	publish SessionSettingPublication,
) (bool, error) {
	return e.mutateImmediateSessionSetting(
		ctx,
		func() (session.CommitReceipt, bool, error) {
			update, err := mutation()
			if err != nil {
				return session.CommitReceipt{}, false, err
			}
			result, err := e.store.MutateChatSettings(update)
			return result.CommitReceipt, result.Changed, err
		},
		apply,
		feedback,
		publish,
	)
}

func (e *Engine) SetFastModeEnabledWithPublication(
	ctx context.Context,
	enabled bool,
	publish SessionSettingPublication,
) (bool, error) {
	if enabled && !e.FastModeAvailable() {
		return false, errors.New("fast mode is only available for OpenAI-based Responses providers")
	}
	return e.mutateImmediateChatSetting(
		ctx,
		func() (session.ChatSettingsMutation, error) {
			return session.ChatSettingsMutation{Fast: &enabled}, nil
		},
		func() { e.applyFastModeEnabled(enabled) },
		clientui.TranscriptSessionSettingFeedback{
			Kind:     clientui.SessionSettingFastMode,
			FastMode: &enabled,
		},
		publish,
	)
}

func (e *Engine) SetSessionNameWithPublication(
	ctx context.Context,
	name string,
	publish SessionSettingPublication,
) (bool, error) {
	normalized := strings.TrimSpace(name)
	return e.mutateImmediateSessionSetting(
		ctx,
		func() (session.CommitReceipt, bool, error) {
			result, err := e.store.MutateName(normalized)
			return result.CommitReceipt, result.Changed, err
		},
		nil,
		clientui.TranscriptSessionSettingFeedback{
			Kind:        clientui.SessionSettingSessionName,
			SessionName: &normalized,
		},
		publish,
	)
}

func (e *Engine) SetThinkingLevelWithPublication(
	ctx context.Context,
	level string,
	publish SessionSettingPublication,
) (bool, error) {
	normalized := strings.TrimSpace(level)
	if normalized == "" {
		return false, errors.New("thinking level is required")
	}
	return e.mutateImmediateChatSetting(
		ctx,
		func() (session.ChatSettingsMutation, error) {
			return session.ChatSettingsMutation{Thinking: &normalized}, nil
		},
		func() { _ = e.setThinkingValue(normalized) },
		clientui.TranscriptSessionSettingFeedback{
			Kind:     clientui.SessionSettingThinking,
			Thinking: &normalized,
		},
		publish,
	)
}

func (e *Engine) SetAutoCompactionEnabledWithPublication(
	ctx context.Context,
	enabled bool,
	publish SessionSettingPublication,
) (bool, bool, error) {
	changed, err := e.mutateImmediateChatSetting(
		ctx,
		func() (session.ChatSettingsMutation, error) {
			return session.ChatSettingsMutation{AutoCompaction: &enabled}, nil
		},
		func() { e.applyAutoCompactionEnabled(enabled) },
		clientui.TranscriptSessionSettingFeedback{
			Kind:           clientui.SessionSettingAutoCompaction,
			AutoCompaction: &enabled,
		},
		publish,
	)
	if err != nil {
		return changed, e.AutoCompactionEnabled(), err
	}
	return changed, enabled, nil
}

func (e *Engine) SetQuestionsEnabledWithPublication(
	ctx context.Context,
	enabled bool,
	publish SessionSettingPublication,
) (bool, bool, error) {
	changed, err := e.mutateImmediateChatSetting(
		ctx,
		func() (session.ChatSettingsMutation, error) {
			return session.ChatSettingsMutation{Questions: &enabled}, nil
		},
		func() { e.applyQuestionsEnabled(enabled) },
		clientui.TranscriptSessionSettingFeedback{
			Kind:      clientui.SessionSettingQuestions,
			Questions: &enabled,
		},
		publish,
	)
	if err != nil {
		return changed, e.QuestionsEnabled(), err
	}
	return changed, enabled, nil
}

func (e *Engine) SetReviewerEnabledWithPublication(
	ctx context.Context,
	enabled bool,
	publish SessionSettingPublication,
) (bool, string, error) {
	return e.setReviewerSettingWithPublication(ctx, func() (string, error) {
		_, target, err := e.reviewerEnabledChange(enabled)
		return target, err
	}, publish)
}

func (e *Engine) SetReviewerFrequencyWithPublication(
	ctx context.Context,
	frequency string,
	publish SessionSettingPublication,
) (bool, string, error) {
	return e.setReviewerSettingWithPublication(ctx, func() (string, error) {
		return e.PrepareReviewerFrequency(frequency)
	}, publish)
}

func (e *Engine) setReviewerSettingWithPublication(
	ctx context.Context,
	resolve func() (string, error),
	publish SessionSettingPublication,
) (bool, string, error) {
	target := ""
	changed, err := e.mutateImmediateChatSetting(
		ctx,
		func() (session.ChatSettingsMutation, error) {
			var err error
			target, err = resolve()
			return session.ChatSettingsMutation{Supervisor: &target}, err
		},
		func() { e.setReviewerFrequency(target) },
		clientui.TranscriptSessionSettingFeedback{
			Kind:       clientui.SessionSettingSupervisor,
			Supervisor: &target,
		},
		publish,
	)
	if err != nil {
		return changed, e.ReviewerFrequency(), err
	}
	return changed, target, nil
}
