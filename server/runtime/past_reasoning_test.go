package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestPastReasoningBeforeLatestKentInstructionBoundary(t *testing.T) {
	reasoning := func(id, encrypted string) llm.ResponseItem {
		return llm.ResponseItem{
			Type:             llm.ResponseItemTypeReasoning,
			ID:               textutil.Value(id),
			EncryptedContent: textutil.Value(encrypted),
		}
	}
	message := func(role llm.Role, messageType *llm.MessageType, content string) llm.ResponseItem {
		return llm.ResponseItem{
			Type:        llm.ResponseItemTypeMessage,
			Role:        textutil.Value(role),
			MessageType: messageType,
			Content:     textutil.Value(content),
		}
	}

	tests := []struct {
		name  string
		items []llm.ResponseItem
		want  []llm.ResponseItem
	}{
		{
			name:  "no instruction boundary selects none",
			items: []llm.ResponseItem{reasoning("prior", "encrypted-prior")},
		},
		{
			name: "compaction summary and developer context are not boundaries",
			items: []llm.ResponseItem{
				reasoning("prior", "encrypted-prior"),
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeCompactionSummary), "summary"),
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeEnvironment), "context"),
			},
		},
		{
			name: "ordinary user message is boundary",
			items: []llm.ResponseItem{
				reasoning("prior", "encrypted-prior"),
				message(llm.RoleUser, nil, "instruction"),
				reasoning("current", "encrypted-current"),
			},
			want: []llm.ResponseItem{reasoning("prior", "encrypted-prior")},
		},
		{
			name: "agent steer is boundary",
			items: []llm.ResponseItem{
				reasoning("prior", "encrypted-prior"),
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeAgentSteer), "steer"),
				reasoning("current", "encrypted-current"),
			},
			want: []llm.ResponseItem{reasoning("prior", "encrypted-prior")},
		},
		{
			name: "latest boundary excludes reasoning after earlier boundary",
			items: []llm.ResponseItem{
				reasoning("old", "encrypted-old"),
				message(llm.RoleUser, nil, "first instruction"),
				reasoning("prior", "encrypted-prior"),
				message(llm.RoleDeveloper, textutil.Value(llm.MessageTypeAgentSteer), "latest instruction"),
				reasoning("current", "encrypted-current"),
			},
			want: []llm.ResponseItem{
				reasoning("old", "encrypted-old"),
				reasoning("prior", "encrypted-prior"),
			},
		},
		{
			name: "tool loop selects only prior turn reasoning",
			items: []llm.ResponseItem{
				reasoning("prior", "encrypted-prior"),
				message(llm.RoleUser, nil, "new task"),
				reasoning("current-one", "encrypted-current-one"),
				{Type: llm.ResponseItemTypeFunctionCall, CallID: textutil.Value("call-1"), Name: textutil.Value("exec_command"), Arguments: json.RawMessage(`{"cmd":"pwd"}`)},
				{Type: llm.ResponseItemTypeFunctionCallOutput, CallID: textutil.Value("call-1"), Output: json.RawMessage(`"done"`)},
				reasoning("current-two", "encrypted-current-two"),
			},
			want: []llm.ResponseItem{reasoning("prior", "encrypted-prior")},
		},
		{
			name: "invalid encrypted reasoning is excluded",
			items: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeReasoning, ID: textutil.Value("missing-encrypted")},
				{Type: llm.ResponseItemTypeReasoning, EncryptedContent: textutil.Value("missing-id")},
				reasoning("valid", "encrypted-valid"),
				message(llm.RoleUser, nil, "instruction"),
			},
			want: []llm.ResponseItem{reasoning("valid", "encrypted-valid")},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := pastReasoningBeforeLatestKentInstructionBoundary(test.items)
			if len(got) != len(test.want) {
				t.Fatalf("selected reasoning = %+v, want %+v", got, test.want)
			}
			for index := range got {
				if optionalString(got[index].ID) != optionalString(test.want[index].ID) ||
					optionalString(got[index].EncryptedContent) != optionalString(test.want[index].EncryptedContent) {
					t.Fatalf("selected reasoning = %+v, want %+v", got, test.want)
				}
			}
		})
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
