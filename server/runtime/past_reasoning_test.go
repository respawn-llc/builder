package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestPastReasoningBeforeLatestKentInstructionBoundary(t *testing.T) {
	reasoning := func(id string) llm.ResponseItem {
		return llm.ResponseItem{
			Type:             llm.ResponseItemTypeReasoning,
			ID:               textutil.Value(id),
			EncryptedContent: textutil.Value("encrypted-" + id),
		}
	}
	message := func(role llm.Role, messageType *llm.MessageType) llm.ResponseItem {
		return llm.ResponseItem{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(role), MessageType: messageType}
	}
	prior, current := reasoning("prior"), reasoning("current")
	tests := []struct {
		name  string
		items []llm.ResponseItem
		want  []string
	}{
		{name: "no boundary", items: []llm.ResponseItem{prior}},
		{
			name: "latest Agent Steer boundary",
			items: []llm.ResponseItem{
				reasoning("old"),
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeCompactionSummary)),
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeEnvironment)),
				message(llm.RoleUser, nil),
				prior,
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeAgentSteer)),
				current,
			},
			want: []string{"old", "prior"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := pastReasoningBeforeLatestKentInstructionBoundary(test.items)
			if len(got) != len(test.want) {
				t.Fatalf("selected reasoning = %+v, want IDs %v", got, test.want)
			}
			for index, item := range got {
				if item.ID == nil || *item.ID != test.want[index] {
					t.Fatalf("selected reasoning = %+v, want IDs %v", got, test.want)
				}
			}
		})
	}
}
