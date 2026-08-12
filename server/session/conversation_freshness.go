package session

import "core/shared/transcript"

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
		return classifyCommittedMessageRecord(payload).TimestampEligible, nil
	case HistoryReplacementRecord:
		for _, item := range payload.Items {
			if item.Type != ProviderHistoryItemTypeMessage {
				continue
			}
			role := MessageRoleUser
			if item.Role != nil {
				role = *item.Role
			}
			if classifyCommittedHistoryItem(role, item).TimestampEligible {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

func classifyCommittedMessageRecord(payload MessageRecord) transcript.CommittedMessageProjection {
	return classifyCommittedMessage(
		payload.Role,
		true,
		payload.MessageType,
		payload.Content,
		transcript.CommittedMessageSourceEvent,
	)
}

func classifyCommittedHistoryItem(role MessageRole, item ProviderHistoryItem) transcript.CommittedMessageProjection {
	return classifyCommittedMessage(
		role,
		item.Role != nil,
		item.MessageType,
		item.Content,
		transcript.CommittedMessageSourceHistoryReplacement,
	)
}

func classifyCommittedMessage(
	role MessageRole,
	rolePresent bool,
	messageType *MessageType,
	content *string,
	source transcript.CommittedMessageSource,
) transcript.CommittedMessageProjection {
	var sharedMessageType *string
	if messageType != nil {
		value := string(*messageType)
		sharedMessageType = &value
	}
	return transcript.ClassifyCommittedMessageProjection(transcript.CommittedMessageProjectionInput{
		Role:        string(role),
		RolePresent: rolePresent,
		MessageType: sharedMessageType,
		Content:     content,
		Source:      source,
	})
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
