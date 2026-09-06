package sessionruntime

import (
	"errors"
	"strings"

	"core/server/launch"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func ActivationRequestFromSessionPlan(
	plan launch.SessionPlan,
	ownerID string,
) (serverapi.SessionRuntimeActivateRequest, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return serverapi.SessionRuntimeActivateRequest{}, errors.New("runtime owner id is required")
	}
	var agentSelection *serverapi.SessionRuntimeAgentSelection
	if plan.ActivationAgentSelection != nil {
		settings := plan.ActivationAgentSelection.Settings
		if settings == nil ||
			settings.Supervisor == nil ||
			settings.Thinking == nil ||
			settings.Fast == nil ||
			settings.Questions == nil ||
			settings.AutoCompaction == nil {
			return serverapi.SessionRuntimeActivateRequest{}, errors.New("complete Runtime Agent selection is required")
		}
		agentSelection = &serverapi.SessionRuntimeAgentSelection{
			Agent: plan.ActivationAgentSelection.Agent,
			Baseline: serverapi.SessionRuntimeChatSettings{
				Supervisor:     *settings.Supervisor,
				Thinking:       *settings.Thinking,
				Fast:           *settings.Fast,
				Questions:      *settings.Questions,
				AutoCompaction: *settings.AutoCompaction,
			},
		}
	}
	request := serverapi.SessionRuntimeActivateRequest{
		SessionID:                plan.Descriptor.SessionID().String(),
		OwnerID:                  ownerID,
		ActiveSettings:           plan.ActiveSettings,
		EnabledToolIDs:           toolspec.IDStrings(plan.EnabledTools),
		QuestionsEnabled:         textutil.Value(plan.QuestionsEnabled),
		AutoCompactionEnabled:    textutil.Value(plan.AutoCompactionEnabled),
		ThinkingOverrideExplicit: plan.ThinkingOverrideExplicit,
		AgentSelection:           agentSelection,
		Source:                   plan.Source,
	}
	if err := request.Validate(); err != nil {
		return serverapi.SessionRuntimeActivateRequest{}, err
	}
	return request, nil
}
