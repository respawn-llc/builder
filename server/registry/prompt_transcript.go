package registry

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
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
	if feed == nil || strings.TrimSpace(snapshot.Request.ToolCallID) == "" {
		return
	}
	prompt := transcriptPendingPromptFromSnapshot(sessionID, snapshot, eventType)
	feed.Publish([]clientui.TranscriptEvent{clientui.NewTranscriptEvent(prompt)})
}

func transcriptPendingPromptFromSnapshot(
	sessionID string,
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
		Kind:          kind,
		Status:        state,
		ToolCallID:    clientui.ToolCallID(strings.TrimSpace(snapshot.Request.ToolCallID)),
		SessionID:     mustPromptSessionID(sessionID),
		StepID:        mustPromptStepID(snapshot.Request.StepID),
		Question:      snapshot.Request.Question,
		CreatedAt:     snapshot.CreatedAt,
		Suggestions:   append([]string(nil), snapshot.Request.Suggestions...),
		AccessTargets: append([]clientui.FileAccessTarget(nil), snapshot.Request.AccessTargets...),
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
	return prompt
}

func mustPromptSessionID(raw string) runtimeids.SessionID {
	id, err := runtimeids.ParseSessionID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("pending prompt has invalid session id %q: %v", raw, err))
	}
	return id
}

func mustPromptStepID(raw string) runtimeids.StepID {
	id, err := runtimeids.ParseStepID(strings.TrimSpace(raw))
	if err != nil {
		panic(fmt.Sprintf("pending prompt has invalid step id %q: %v", raw, err))
	}
	return id
}
