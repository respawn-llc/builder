package session

import "strings"

const askQuestionToolName = "ask_question"

type ConversationFreshness uint8

const (
	ConversationFreshnessFresh ConversationFreshness = iota
	ConversationFreshnessEstablished
)

func (f ConversationFreshness) IsFresh() bool {
	return f == ConversationFreshnessFresh
}

func hasVisibleUserMessageRecord(record EventRecord) (bool, error) {
	payload, err := record.Payload()
	if err != nil {
		return false, err
	}
	switch payload := payload.(type) {
	case MessageRecord:
		return hasVisibleUserMessageFields(
			payload.Role,
			payload.MessageType,
			payload.Content,
		), nil
	case HistoryReplacementRecord:
		for _, item := range payload.Items {
			if item.Type == ProviderHistoryItemTypeMessage &&
				item.Role != nil &&
				hasVisibleUserMessageFields(*item.Role, item.MessageType, item.Content) {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

func eventPayloadEligibleForCommittedTime(payload EventRecordPayload) (bool, error) {
	switch payload := payload.(type) {
	case MessageRecord:
		if payload.Content == nil || strings.TrimSpace(*payload.Content) == "" {
			return false, nil
		}
		switch payload.Role {
		case MessageRoleUser:
			return payload.MessageType == nil ||
				*payload.MessageType != MessageTypeCompactionSummary, nil
		case MessageRoleAssistant:
			return true, nil
		default:
			return false, nil
		}
	case HistoryReplacementRecord:
		for _, item := range payload.Items {
			if item.Type != ProviderHistoryItemTypeMessage ||
				item.Content == nil ||
				strings.TrimSpace(*item.Content) == "" {
				continue
			}
			role := MessageRoleUser
			if item.Role != nil {
				role = *item.Role
			}
			switch role {
			case MessageRoleUser:
				if item.MessageType != nil && *item.MessageType != MessageTypeCompactionSummary {
					return true, nil
				}
			case MessageRoleAssistant:
				return true, nil
			}
		}
		return false, nil
	case ToolCompletionRecord:
		return payload.Name == askQuestionToolName &&
			!payload.IsError &&
			payload.QuestionAnswer != nil, nil
	default:
		return false, nil
	}
}

func hasVisibleUserMessageFields(
	role MessageRole,
	messageType *MessageType,
	content *string,
) bool {
	var messageTypeText string
	if messageType != nil {
		messageTypeText = string(*messageType)
	}
	var text string
	if content != nil {
		text = *content
	}
	return isVisibleUserMessageFields(string(role), messageTypeText, text)
}
