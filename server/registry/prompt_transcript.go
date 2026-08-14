package registry

import (
	"strings"

	"core/shared/clientui"
	"core/shared/toolspec"
)

type pendingPromptEventType uint8

const (
	pendingPromptEventPending pendingPromptEventType = iota + 1
	pendingPromptEventResolved
)

func publishPendingPrompt(
	feed *sessionFeedSequencer,
	sessionID string,
	snapshot PendingPromptSnapshot,
	eventType pendingPromptEventType,
) {
	if feed == nil {
		return
	}
	prompt := transcriptPendingPromptFromSnapshot(sessionID, snapshot, eventType)
	feed.Publish([]clientui.TranscriptEvent{clientui.NewTranscriptEvent(prompt)})
}

func transcriptPendingPromptFromSnapshot(
	_ string,
	snapshot PendingPromptSnapshot,
	eventType pendingPromptEventType,
) clientui.TranscriptPrompt {
	state := clientui.TranscriptPromptStatusPending
	if eventType == pendingPromptEventResolved {
		state = clientui.TranscriptPromptStatusResolved
	}
	kind := clientui.TranscriptPromptKindQuestion
	if snapshot.Request.Approval {
		kind = clientui.TranscriptPromptKindApproval
	}
	prompt := clientui.TranscriptPrompt{
		Kind:        kind,
		Status:      state,
		PromptID:    snapshot.PromptID,
		SessionID:   snapshot.SessionID,
		StepID:      snapshot.StepID,
		Question:    snapshot.Request.Question,
		CreatedAt:   snapshot.CreatedAt,
		Suggestions: append([]string(nil), snapshot.Request.Suggestions...),
	}
	if snapshot.Request.RecommendedOptionIndex > 0 {
		recommended := snapshot.Request.RecommendedOptionIndex
		prompt.RecommendedOptionIndex = &recommended
	}
	if len(snapshot.Request.ApprovalOptions) > 0 {
		prompt.ApprovalOptions = make([]clientui.ApprovalDecision, 0, len(snapshot.Request.ApprovalOptions))
		for _, option := range snapshot.Request.ApprovalOptions {
			prompt.ApprovalOptions = append(prompt.ApprovalOptions, clientui.ApprovalDecision(option.Decision))
		}
	}
	if toolCallID := strings.TrimSpace(snapshot.Request.ToolCallID); toolCallID != "" {
		prompt.Tool = &clientui.ToolProvenance{
			ToolCallID: clientui.ToolCallID(toolCallID),
			ToolName:   string(toolspec.ToolAskQuestion),
		}
	}
	return prompt
}
