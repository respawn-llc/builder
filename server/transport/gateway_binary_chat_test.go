package transport

import (
	"testing"

	chatpb "core/shared/protoapi/gen/kent/api/chat"
	chatsettingspb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestBinaryChatFailurePreservesAgentPreparationDetails(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	request := &chatpb.SteerRequest{
		Target: &chatpb.ChatTarget{
			Target: &chatpb.ChatTarget_Session{
				Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
			},
		},
	}
	for _, test := range []struct {
		name     string
		category serverapi.ChatSettingsAgentPreparationCategory
		want     chatsettingspb.AgentPreparationCategory
	}{
		{
			name:     "invalid configuration",
			category: serverapi.ChatSettingsAgentInvalidConfiguration,
			want:     chatsettingspb.AgentPreparationCategory_AGENT_PREPARATION_CATEGORY_INVALID_CONFIGURATION,
		},
		{
			name:     "provider unavailable",
			category: serverapi.ChatSettingsAgentProviderUnavailable,
			want:     chatsettingspb.AgentPreparationCategory_AGENT_PREPARATION_CATEGORY_PROVIDER_UNAVAILABLE,
		},
		{
			name:     "internal preparation",
			category: serverapi.ChatSettingsAgentInternalPreparation,
			want:     chatsettingspb.AgentPreparationCategory_AGENT_PREPARATION_CATEGORY_INTERNAL_PREPARATION,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail := binaryChatFailure(
				nil,
				nil,
				request,
				&serverapi.ChatSettingsAgentPreparationError{
					Agent:    "reviewer",
					Category: test.category,
				},
			)

			preparation, ok := detail.(*chatsettingspb.AgentPreparationDetails)
			if !ok {
				t.Fatalf("Chat failure detail = %T, want Agent preparation details", detail)
			}
			if preparation.Agent != "reviewer" || preparation.Category != test.want {
				t.Fatalf("Agent preparation details = %+v", preparation)
			}
		})
	}
}
