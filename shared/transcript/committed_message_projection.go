package transcript

import "strings"

const (
	CommittedMessageRoleUser              = "user"
	CommittedMessageRoleAssistant         = "assistant"
	CommittedMessageTypeCompactionSummary = "compaction_summary"
)

type CommittedMessageSource uint8

const (
	CommittedMessageSourceEvent CommittedMessageSource = iota
	CommittedMessageSourceHistoryReplacement
)

type CommittedMessageRowKind uint8

const (
	CommittedMessageRowNone CommittedMessageRowKind = iota
	CommittedMessageRowUser
	CommittedMessageRowAssistant
)

// CommittedMessageProjectionInput contains semantic facts shared by
// persistence eligibility and transcript row projection.
type CommittedMessageProjectionInput struct {
	Role         string
	RolePresent  bool
	MessageType  *string
	Content      *string
	HasToolCalls bool
	Source       CommittedMessageSource
}

type CommittedMessageProjection struct {
	Kind              CommittedMessageRowKind
	TimestampEligible bool
}

// ClassifyCommittedMessageProjection is the single authority for whether a
// message projects an eligible committed user or assistant transcript row.
func ClassifyCommittedMessageProjection(input CommittedMessageProjectionInput) CommittedMessageProjection {
	var result CommittedMessageProjection
	if input.Content == nil || strings.TrimSpace(*input.Content) == "" {
		return result
	}

	role := strings.TrimSpace(input.Role)
	if !input.RolePresent {
		role = CommittedMessageRoleUser
	}
	switch role {
	case CommittedMessageRoleUser:
		if input.MessageType != nil &&
			strings.TrimSpace(*input.MessageType) == CommittedMessageTypeCompactionSummary {
			return result
		}
		result.Kind = CommittedMessageRowUser
		result.TimestampEligible = input.Source == CommittedMessageSourceEvent ||
			input.MessageType != nil
	case CommittedMessageRoleAssistant:
		result.Kind = CommittedMessageRowAssistant
		result.TimestampEligible = !input.HasToolCalls
	default:
		return result
	}
	return result
}
