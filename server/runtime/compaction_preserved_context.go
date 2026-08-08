package runtime

import (
	"strings"

	"core/prompts"
	"core/server/llm"
	"core/shared/textutil"
)

type compactionPreservedContextCoordinator struct {
	engine *Engine
}

func newCompactionPreservedContextCoordinator(engine *Engine) compactionPreservedContextCoordinator {
	return compactionPreservedContextCoordinator{engine: engine}
}

func compactionPreservedUserMessage(text string) (llm.Message, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return llm.Message{}, false
	}
	content := trimCompactionPreservedUserMessageText(trimmed, compactionPreservedUserMessageMaxChars)
	return llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeCompactionPreservedUserMessage),
		Content:     textutil.Value(compactionPreservedUserMessageHeader + "\n\n" + content),
	}, true
}

func handoffFutureAgentMessage(text string) (llm.Message, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return llm.Message{}, false
	}
	return llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeHandoffFutureMessage),
		Content:     textutil.Value(prompts.FormatHandoffFutureAgentMessage(trimmed)),
	}, true
}

func (c compactionPreservedContextCoordinator) appendHandoffFutureMessage(stepID string) error {
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

func trimCompactionPreservedUserMessageText(text string, maxChars int) string {
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
