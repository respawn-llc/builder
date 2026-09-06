package protoapi

import (
	"errors"

	pb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func ChatSettingsReadTargetFromProto(request *pb.ReadRequest) (serverapi.ChatSettingsReadTarget, error) {
	switch target := request.Target.(type) {
	case *pb.ReadRequest_NewChat:
		return serverapi.NewChatSettingsTarget(target.NewChat.ProjectId, target.NewChat.WorkspaceId), nil
	case *pb.ReadRequest_Session:
		id, err := runtimeids.ParseSessionID(target.Session.SessionId)
		return serverapi.SessionChatSettingsTarget(id), err
	default:
		return serverapi.ChatSettingsReadTarget{}, errors.New("Chat settings target is required")
	}
}

func ChatSettingsReadToProto(response serverapi.ChatSettingsReadResponse, target serverapi.ChatSettingsReadTarget) (*pb.ReadSuccess, error) {
	if err := response.ValidateForTarget(target); err != nil {
		return nil, err
	}
	result := &pb.ReadSuccess{}
	if response.NewChat != nil {
		catalog := &pb.NewChatCatalog{InitialSettings: initialChatSettingsToProto(response.NewChat.InitialSettings)}
		for _, choice := range response.NewChat.Choices {
			catalog.Choices = append(catalog.Choices, &pb.NewChatAgentChoice{
				Agent: chatSettingsAgentToProto(choice.Agent), Baseline: initialChatSettingsToProto(choice.Baseline),
				Supervisor: chatSettingsSupervisorToProto(choice.Supervisor), Thinking: chatSettingsThinkingToProto(choice.Thinking),
				Fast: chatSettingsFastToProto(choice.Fast), Questions: chatSettingsQuestionsToProto(choice.Questions),
				AutoCompaction: chatSettingsAutoCompactionToProto(choice.AutoCompaction),
			})
		}
		result.Target = &pb.ReadSuccess_NewChat{NewChat: catalog}
	} else {
		result.Target = &pb.ReadSuccess_Session{Session: &pb.SessionSettings{
			Settings: chatSettingsToProto(response.Session.Settings), Session: chatSettingsFactsToProto(response.Session.Session),
		}}
	}
	return result, Validate(result)
}

func initialChatSettingsToProto(value serverapi.InitialChatSettings) *pb.InitialChatSettings {
	return &pb.InitialChatSettings{
		AgentRole: value.AgentRole, Supervisor: chatSettingsSupervisorValueToProto(value.Supervisor),
		Thinking: textutil.Pointer(value.Thinking), Fast: textutil.Pointer(value.Fast),
		QuestionsEnabled: &value.QuestionsEnabled, AutoCompactionEnabled: &value.AutoCompactionEnabled,
	}
}

func chatSettingsToProto(value serverapi.ChatSettings) *pb.Settings {
	result := &pb.Settings{
		SelectedAgent:    &pb.AgentSummary{Role: value.SelectedAgent.Role, Model: value.SelectedAgent.Model, Thinking: value.SelectedAgent.Thinking},
		AgentEditability: chatSettingsEditabilityToProto(value.AgentEditability),
		Supervisor:       chatSettingsSupervisorToProto(value.Supervisor), Thinking: chatSettingsThinkingToProto(value.Thinking),
		Fast: chatSettingsFastToProto(value.Fast), Questions: chatSettingsQuestionsToProto(value.Questions),
		AutoCompaction: chatSettingsAutoCompactionToProto(value.AutoCompaction),
		AgentLocked:    value.AgentLocked, WorkflowLocked: value.WorkflowLocked, CachingLocked: value.CachingLocked,
	}
	for _, choice := range value.AgentChoices {
		result.AgentChoices = append(result.AgentChoices, chatSettingsAgentToProto(choice))
	}
	return result
}

func chatSettingsAgentToProto(value serverapi.ChatSettingsAgentChoice) *pb.AgentChoice {
	return &pb.AgentChoice{
		Role: value.Role, Model: value.Model, Thinking: value.Thinking, Tools: value.Tools,
		CustomSystemPrompt: value.CustomSystemPrompt, CustomCapabilities: value.CustomCapabilities, AgentCallable: value.AgentCallable,
	}
}

func chatSettingsFactsToProto(value serverapi.ChatSettingsSessionFacts) *pb.SessionFacts {
	result := &pb.SessionFacts{
		SessionId: value.SessionID.String(), TaskId: textutil.Pointer(value.TaskID), TaskShortId: textutil.Pointer(value.TaskShortID),
	}
	if value.PreviousSessionID != nil {
		result.PreviousSessionId = textutil.Value(value.PreviousSessionID.String())
	}
	return result
}

func chatSettingsSupervisorToProto(value serverapi.ChatSettingsSupervisor) *pb.Supervisor {
	return &pb.Supervisor{
		Value: chatSettingsSupervisorValueToProto(value.Value), Baseline: chatSettingsSupervisorValueToProto(value.Baseline),
		Editability: chatSettingsEditabilityToProto(value.Editability),
	}
}

func chatSettingsThinkingToProto(value *serverapi.ChatSettingsThinking) *pb.Thinking {
	if value == nil {
		return nil
	}
	return &pb.Thinking{
		Kind: map[serverapi.ChatSettingsThinkingKind]pb.ThinkingKind{
			serverapi.ChatSettingsThinkingEnumerated: pb.ThinkingKind_THINKING_KIND_ENUMERATED,
			serverapi.ChatSettingsThinkingCustom:     pb.ThinkingKind_THINKING_KIND_CUSTOM,
		}[value.Kind],
		Value: value.Value, BaselineValue: value.BaselineValue, Values: value.Values,
		Editability: chatSettingsEditabilityToProto(value.Editability),
	}
}

func chatSettingsFastToProto(value *serverapi.ChatSettingsFast) *pb.Fast {
	if value == nil {
		return nil
	}
	return &pb.Fast{Value: value.Value, Editability: chatSettingsEditabilityToProto(value.Editability)}
}

func chatSettingsQuestionsToProto(value serverapi.ChatSettingsQuestions) *pb.Questions {
	return &pb.Questions{Capable: value.Capable, Enabled: value.Enabled, Editability: chatSettingsEditabilityToProto(value.Editability)}
}

func chatSettingsAutoCompactionToProto(value serverapi.ChatSettingsAutoCompaction) *pb.AutoCompaction {
	return &pb.AutoCompaction{
		Policy: map[serverapi.ChatSettingsAutoCompactionPolicy]pb.AutoCompactionPolicy{
			serverapi.ChatSettingsAutoCompactionOptional: pb.AutoCompactionPolicy_AUTO_COMPACTION_POLICY_OPTIONAL,
			serverapi.ChatSettingsAutoCompactionRequired: pb.AutoCompactionPolicy_AUTO_COMPACTION_POLICY_REQUIRED,
			serverapi.ChatSettingsAutoCompactionDisabled: pb.AutoCompactionPolicy_AUTO_COMPACTION_POLICY_DISABLED,
		}[value.Policy],
		Stored: value.Stored, Effective: value.Effective, Editability: chatSettingsEditabilityToProto(value.Editability),
	}
}

func chatSettingsSupervisorValueToProto(value serverapi.ChatSettingsSupervisorValue) pb.SupervisorValue {
	return map[serverapi.ChatSettingsSupervisorValue]pb.SupervisorValue{
		serverapi.ChatSettingsSupervisorOff:        pb.SupervisorValue_SUPERVISOR_VALUE_OFF,
		serverapi.ChatSettingsSupervisorAfterEdits: pb.SupervisorValue_SUPERVISOR_VALUE_AFTER_EDITS,
		serverapi.ChatSettingsSupervisorAlways:     pb.SupervisorValue_SUPERVISOR_VALUE_ALWAYS,
	}[value]
}

func chatSettingsEditabilityToProto(value serverapi.ChatSettingsEditability) pb.Editability {
	return map[serverapi.ChatSettingsEditability]pb.Editability{
		serverapi.ChatSettingsEditable:       pb.Editability_EDITABILITY_EDITABLE,
		serverapi.ChatSettingsWorkflowLock:   pb.Editability_EDITABILITY_WORKFLOW_LOCK,
		serverapi.ChatSettingsCachingLock:    pb.Editability_EDITABILITY_CACHING_LOCK,
		serverapi.ChatSettingsPolicyDisabled: pb.Editability_EDITABILITY_POLICY_DISABLED,
	}[value]
}
