package runtime

import (
	"strings"

	"core/prompts"
	"core/server/llm"
)

type compactionCarryoverCoordinator struct {
	engine *Engine
}

func newCompactionCarryoverCoordinator(engine *Engine) compactionCarryoverCoordinator {
	return compactionCarryoverCoordinator{engine: engine}
}

func manualCompactionCarryoverMessage(text string) (llm.Message, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return llm.Message{}, false
	}
	content := trimCompactionCarryoverText(trimmed, manualCompactionCarryoverMaxChars)
	return llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeManualCompactionCarryover,
		Content:     manualCompactionCarryoverHeader + "\n\n" + content,
	}, true
}

func handoffFutureAgentMessage(text string) (llm.Message, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return llm.Message{}, false
	}
	return llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeHandoffFutureMessage,
		Content:     prompts.FormatHandoffFutureAgentMessage(trimmed),
	}, true
}

func (c compactionCarryoverCoordinator) appendHandoffFutureMessage(stepID string) error {
	e := c.engine
	req := e.handoffRuntimeState().RequestSnapshot()
	if req == nil {
		return nil
	}
	futureMessage, ok := handoffFutureAgentMessage(req.futureAgentMessage)
	if !ok {
		return nil
	}
	receipt, err := e.steerWithCommitReceipt(
		stepID,
		steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{futureMessage}),
	)
	if receipt.Committed {
		e.handoffRuntimeState().ClearFutureMessage()
	} else if err != nil {
		e.handoffRuntimeState().QueueFutureMessage(req.futureAgentMessage)
	}
	return err
}

func trimCompactionCarryoverText(text string, maxChars int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxChars <= 0 {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= maxChars {
		return trimmed
	}
	if maxChars < 4 {
		return string(runes[:maxChars])
	}
	return string(runes[:maxChars-4]) + "\n..."
}
