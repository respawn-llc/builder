package runtime

import (
	"errors"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
)

type GoalNoticeKind uint8

const (
	GoalNoticeSet GoalNoticeKind = iota + 1
	GoalNoticeStatus
	GoalNoticeClear
)

func SteerPersistedGoalNotice(
	store *session.Store,
	kind GoalNoticeKind,
	goal *session.GoalState,
) (session.CommitReceipt, error) {
	message, err := goalNoticeMessage(kind, goal)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return SteerPersistedMessage(store, message)
}

func goalNoticeMessage(kind GoalNoticeKind, goal *session.GoalState) (llm.Message, error) {
	message := llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeGoal),
	}
	switch kind {
	case GoalNoticeSet:
		if goal == nil {
			return llm.Message{}, errors.New("goal set notice requires goal state")
		}
		message.Content = textutil.Value(prompts.RenderGoalSetPrompt(goal.Objective))
		message.CompactContent = textutil.Value(goalSetCompactText(goal.Objective))
	case GoalNoticeStatus:
		if goal == nil {
			return llm.Message{}, errors.New("goal status notice requires goal state")
		}
		message.Content = textutil.Value(goalStatusPrompt(*goal))
		message.CompactContent = textutil.Value(goalStatusCompactText(*goal))
	case GoalNoticeClear:
		message.Content = textutil.Value(prompts.GoalClearPrompt)
		message.CompactContent = textutil.Value("Goal cleared")
	default:
		return llm.Message{}, errors.New("goal notice kind is required")
	}
	return message, nil
}
