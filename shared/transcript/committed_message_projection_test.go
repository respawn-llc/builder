package transcript

import "testing"

func TestClassifyCommittedMessageProjection(t *testing.T) {
	content := "visible"
	typed := "agents.md"
	tests := []struct {
		name string
		in   CommittedMessageProjectionInput
		kind CommittedMessageRowKind
		time bool
	}{
		{"event user", CommittedMessageProjectionInput{Role: "user", RolePresent: true, Content: &content, Source: CommittedMessageSourceEvent}, CommittedMessageRowUser, true},
		{"typed history user", CommittedMessageProjectionInput{Role: "user", RolePresent: true, MessageType: &typed, Content: &content, Source: CommittedMessageSourceHistoryReplacement}, CommittedMessageRowUser, true},
		{"untyped history user", CommittedMessageProjectionInput{Role: "user", RolePresent: true, Content: &content, Source: CommittedMessageSourceHistoryReplacement}, CommittedMessageRowUser, false},
		{"absent role typed history", CommittedMessageProjectionInput{MessageType: &typed, Content: &content, Source: CommittedMessageSourceHistoryReplacement}, CommittedMessageRowUser, true},
		{"summary", CommittedMessageProjectionInput{Role: "user", RolePresent: true, MessageType: func() *string { value := CommittedMessageTypeCompactionSummary; return &value }(), Content: &content, Source: CommittedMessageSourceHistoryReplacement}, CommittedMessageRowNone, false},
		{"assistant", CommittedMessageProjectionInput{Role: "assistant", RolePresent: true, Content: &content, Source: CommittedMessageSourceEvent}, CommittedMessageRowAssistant, true},
		{"assistant with tools", CommittedMessageProjectionInput{Role: "assistant", RolePresent: true, Content: &content, HasToolCalls: true, Source: CommittedMessageSourceEvent}, CommittedMessageRowAssistant, false},
		{"blank", CommittedMessageProjectionInput{Role: "user", RolePresent: true, Content: func() *string { value := " "; return &value }(), Source: CommittedMessageSourceEvent}, CommittedMessageRowNone, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyCommittedMessageProjection(test.in)
			if got.Kind != test.kind || got.TimestampEligible != test.time {
				t.Fatalf("classification=%+v, want kind=%d time=%t", got, test.kind, test.time)
			}
		})
	}
}
