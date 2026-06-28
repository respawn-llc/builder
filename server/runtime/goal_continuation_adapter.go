package runtime

import (
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
)

type goalContinuation struct {
	engine *Engine
}

func (e *Engine) goalContinuation() goalContinuation {
	return goalContinuation{engine: e}
}

func (c goalContinuation) activeGoal() (session.GoalState, bool) {
	goal := c.engine.Goal()
	if goal == nil || goal.Status != session.GoalStatusActive {
		return session.GoalState{}, false
	}
	return *goal, true
}

func (c goalContinuation) nudgeMessage() (llm.Message, bool) {
	goal, ok := c.activeGoal()
	if !ok {
		return llm.Message{}, false
	}
	return normalizeMessageForTranscript(llm.Message{
		Role:           llm.RoleDeveloper,
		MessageType:    llm.MessageTypeGoal,
		Content:        prompts.RenderGoalNudgePrompt(goal.Objective, string(goal.Status)),
		CompactContent: goalNudgeCompactText(goal),
	}, c.engine.transcriptWorkingDir()), true
}

func (c goalContinuation) reminderText() (string, bool) {
	goal, ok := c.activeGoal()
	if !ok {
		return "", false
	}
	return strings.TrimSpace(prompts.RenderGoalNudgePrompt(goal.Objective, string(goal.Status))), true
}
