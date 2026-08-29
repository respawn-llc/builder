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

type immediateSessionSettingCommit struct {
	session.CommitReceipt
	Changed bool
}

func (e *Engine) mutateImmediateSessionSetting(
	ctx context.Context,
	prepare func() error,
	persist func() (immediateSessionSettingCommit, error),
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
	if prepare != nil {
		if err := prepare(); err != nil {
			return false, err
		}
	}
	commit, mutationErr := persist()
	if e.stopAfterDefinitelyUncommittedSessionSetting(commit.CommitReceipt, mutationErr) {
		return false, mutationErr
	}
	if apply != nil {
		apply()
	}
	if publish == nil {
		return commit.Changed, mutationErr
	}
	feedback.Changed = commit.Changed
	return commit.Changed, errors.Join(mutationErr, publish(feedback))
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
	prepare func() error,
	mutation func() session.ChatSettingsMutation,
	apply func(),
	feedback clientui.TranscriptSessionSettingFeedback,
	publish SessionSettingPublication,
) (bool, error) {
	return e.mutateImmediateSessionSetting(
		ctx,
		prepare,
		func() (immediateSessionSettingCommit, error) {
			result, err := e.store.MutateChatSettings(mutation())
			return immediateSessionSettingCommit{
				CommitReceipt: result.CommitReceipt,
				Changed:       result.Changed,
			}, err
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
		nil,
		func() session.ChatSettingsMutation { return session.ChatSettingsMutation{Fast: &enabled} },
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
		nil,
		func() (immediateSessionSettingCommit, error) {
			result, err := e.store.MutateName(normalized)
			return immediateSessionSettingCommit{
				CommitReceipt: result.CommitReceipt,
				Changed:       result.Changed,
			}, err
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
		nil,
		func() session.ChatSettingsMutation { return session.ChatSettingsMutation{Thinking: &normalized} },
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
		nil,
		func() session.ChatSettingsMutation {
			return session.ChatSettingsMutation{AutoCompaction: &enabled}
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
		nil,
		func() session.ChatSettingsMutation { return session.ChatSettingsMutation{Questions: &enabled} },
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
		func() error {
			var prepareErr error
			target, prepareErr = resolve()
			return prepareErr
		},
		func() session.ChatSettingsMutation {
			return session.ChatSettingsMutation{Supervisor: &target}
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
