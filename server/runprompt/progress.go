package runprompt

import (
	"strings"

	"core/server/llm"
	"core/server/runtime"
	"core/shared/serverapi"
)

func PublishRunPromptProgress(progress serverapi.RunPromptProgressSink, evt runtime.Event) {
	if progress == nil {
		return
	}
	state, ok := RunPromptProgressFromRuntimeEvent(evt)
	if !ok {
		return
	}
	progress.PublishRunPromptProgress(state)
}

func RunPromptProgressFromRuntimeEvent(evt runtime.Event) (serverapi.RunPromptProgress, bool) {
	switch evt.Kind {
	case runtime.EventAssistantMessage:
		content := evt.Message.Content
		if evt.Message.Role != llm.RoleAssistant || strings.TrimSpace(content) == "" {
			return serverapi.RunPromptProgress{}, false
		}
		switch evt.Message.Phase {
		case llm.MessagePhaseCommentary, llm.MessagePhaseFinal:
		default:
			return serverapi.RunPromptProgress{}, false
		}
		return serverapi.RunPromptProgress{
			Kind: serverapi.RunPromptProgressKindAssistantMessage,
			AssistantMessage: &serverapi.RunPromptVisibleResponse{
				Phase:   evt.Message.Phase,
				Content: content,
			},
		}, true
	case runtime.EventCompactionStarted:
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindCompactionStarted}, true
	case runtime.EventCompactionFailed:
		var detail string
		if evt.Compaction != nil {
			detail = evt.Compaction.Error
		}
		return serverapi.RunPromptProgress{Kind: serverapi.RunPromptProgressKindCompactionFailed, Failure: runPromptFailure(detail)}, true
	case runtime.EventInFlightClearFailed:
		return serverapi.RunPromptProgress{
			Kind:    serverapi.RunPromptProgressKindRunCleanupFailed,
			Failure: runPromptFailure(evt.Error),
		}, true
	case runtime.EventQueuedUserMessageStatus:
		status := evt.QueuedUserMessageStatus
		if status == nil || status.Status != runtime.QueuedUserMessageAccepted {
			return serverapi.RunPromptProgress{}, false
		}
		content := status.RestoreText
		if strings.TrimSpace(content) == "" {
			return serverapi.RunPromptProgress{}, false
		}
		return serverapi.RunPromptProgress{
			Kind:           serverapi.RunPromptProgressKindSteeredMessage,
			SteeredMessage: &serverapi.RunPromptSteeredMessage{Content: content},
		}, true
	default:
		return serverapi.RunPromptProgress{}, false
	}
}

func runPromptFailure(raw string) *serverapi.RunPromptFailure {
	detail := strings.TrimSpace(raw)
	if detail == "" {
		return &serverapi.RunPromptFailure{}
	}
	return &serverapi.RunPromptFailure{Error: &detail}
}
