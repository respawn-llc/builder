package protoapi

import (
	"errors"
	"fmt"

	pb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func ChatSettingsReadTargetToProto(target serverapi.ChatSettingsReadTarget) (*pb.ReadRequest, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if target.TargetKind == serverapi.ChatSettingsReadTargetNewChat {
		return &pb.ReadRequest{Target: &pb.ReadRequest_NewChat{NewChat: &pb.NewChatTarget{
			ProjectId: *target.ProjectID, WorkspaceId: *target.WorkspaceID,
		}}}, nil
	}
	return &pb.ReadRequest{Target: &pb.ReadRequest_Session{Session: &pb.SessionTarget{SessionId: target.Session.String()}}}, nil
}

func ChatSettingsReadFromProto(value *pb.ReadSuccess, target serverapi.ChatSettingsReadTarget) (serverapi.ChatSettingsReadResponse, error) {
	if err := Validate(value); err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	var result serverapi.ChatSettingsReadResponse
	switch value := value.Target.(type) {
	case *pb.ReadSuccess_NewChat:
		result.NewChat = &serverapi.NewChatCatalog{InitialSettings: initialChatSettingsFromProto(value.NewChat.InitialSettings)}
		for _, choice := range value.NewChat.Choices {
			result.NewChat.Choices = append(result.NewChat.Choices, serverapi.NewChatAgentChoice{
				Agent: chatSettingsAgentFromProto(choice.Agent), Baseline: initialChatSettingsFromProto(choice.Baseline),
				Supervisor: chatSettingsSupervisorFromProto(choice.Supervisor), Thinking: chatSettingsThinkingFromProto(choice.Thinking),
				Fast: chatSettingsFastFromProto(choice.Fast), Questions: chatSettingsQuestionsFromProto(choice.Questions),
				AutoCompaction: chatSettingsAutoCompactionFromProto(choice.AutoCompaction),
			})
		}
	case *pb.ReadSuccess_Session:
		facts, err := chatSettingsFactsFromProto(value.Session.Session)
		if err != nil {
			return result, err
		}
		result.Session = &serverapi.SessionChatSettings{Settings: chatSettingsFromProto(value.Session.Settings), Session: facts}
	}
	return result, result.ValidateForTarget(target)
}

func InitialChatSettingsFromProto(value *pb.InitialChatSettings) (serverapi.InitialChatSettings, error) {
	if err := Validate(value); err != nil {
		return serverapi.InitialChatSettings{}, err
	}
	result := initialChatSettingsFromProto(value)
	return result, result.Validate()
}

func initialChatSettingsFromProto(value *pb.InitialChatSettings) serverapi.InitialChatSettings {
	return serverapi.InitialChatSettings{
		AgentRole: value.AgentRole, Supervisor: chatSettingsSupervisorValueFromProto(value.Supervisor),
		Thinking: textutil.Pointer(value.Thinking), Fast: textutil.Pointer(value.Fast),
		QuestionsEnabled: *value.QuestionsEnabled, AutoCompactionEnabled: *value.AutoCompactionEnabled,
	}
}

func chatSettingsFromProto(value *pb.Settings) serverapi.ChatSettings {
	result := serverapi.ChatSettings{
		SelectedAgent: serverapi.ChatSettingsAgentSummary{
			Role: value.SelectedAgent.Role, Model: value.SelectedAgent.Model, Thinking: value.SelectedAgent.Thinking,
		},
		AgentEditability: chatSettingsEditabilityFromProto(value.AgentEditability),
		Supervisor:       chatSettingsSupervisorFromProto(value.Supervisor), Thinking: chatSettingsThinkingFromProto(value.Thinking),
		Fast: chatSettingsFastFromProto(value.Fast), Questions: chatSettingsQuestionsFromProto(value.Questions),
		AutoCompaction: chatSettingsAutoCompactionFromProto(value.AutoCompaction),
		AgentLocked:    value.AgentLocked, WorkflowLocked: value.WorkflowLocked, CachingLocked: value.CachingLocked,
	}
	for _, choice := range value.AgentChoices {
		result.AgentChoices = append(result.AgentChoices, chatSettingsAgentFromProto(choice))
	}
	return result
}

func chatSettingsAgentFromProto(value *pb.AgentChoice) serverapi.ChatSettingsAgentChoice {
	return serverapi.ChatSettingsAgentChoice{
		Role: value.Role, Model: value.Model, Thinking: value.Thinking, Tools: value.Tools,
		CustomSystemPrompt: value.CustomSystemPrompt, CustomCapabilities: value.CustomCapabilities, AgentCallable: value.AgentCallable,
	}
}

func chatSettingsFactsFromProto(value *pb.SessionFacts) (serverapi.ChatSettingsSessionFacts, error) {
	id, err := runtimeids.ParseSessionID(value.SessionId)
	if err != nil {
		return serverapi.ChatSettingsSessionFacts{}, err
	}
	result := serverapi.ChatSettingsSessionFacts{SessionID: id, TaskID: textutil.Pointer(value.TaskId), TaskShortID: textutil.Pointer(value.TaskShortId)}
	if value.PreviousSessionId != nil {
		previous, err := runtimeids.ParseSessionID(*value.PreviousSessionId)
		if err != nil {
			return result, err
		}
		result.PreviousSessionID = &previous
	}
	return result, nil
}

func chatSettingsSupervisorFromProto(value *pb.Supervisor) serverapi.ChatSettingsSupervisor {
	return serverapi.ChatSettingsSupervisor{
		Value: chatSettingsSupervisorValueFromProto(value.Value), Baseline: chatSettingsSupervisorValueFromProto(value.Baseline),
		Editability: chatSettingsEditabilityFromProto(value.Editability),
	}
}

func chatSettingsThinkingFromProto(value *pb.Thinking) *serverapi.ChatSettingsThinking {
	if value == nil {
		return nil
	}
	return &serverapi.ChatSettingsThinking{
		Kind: map[pb.ThinkingKind]serverapi.ChatSettingsThinkingKind{
			pb.ThinkingKind_THINKING_KIND_ENUMERATED: serverapi.ChatSettingsThinkingEnumerated,
			pb.ThinkingKind_THINKING_KIND_CUSTOM:     serverapi.ChatSettingsThinkingCustom,
		}[value.Kind],
		Value: value.Value, BaselineValue: value.BaselineValue, Values: value.Values,
		Editability: chatSettingsEditabilityFromProto(value.Editability),
	}
}

func chatSettingsFastFromProto(value *pb.Fast) *serverapi.ChatSettingsFast {
	if value == nil {
		return nil
	}
	return &serverapi.ChatSettingsFast{Value: value.Value, Editability: chatSettingsEditabilityFromProto(value.Editability)}
}

func chatSettingsQuestionsFromProto(value *pb.Questions) serverapi.ChatSettingsQuestions {
	return serverapi.ChatSettingsQuestions{Capable: value.Capable, Enabled: value.Enabled, Editability: chatSettingsEditabilityFromProto(value.Editability)}
}

func chatSettingsAutoCompactionFromProto(value *pb.AutoCompaction) serverapi.ChatSettingsAutoCompaction {
	return serverapi.ChatSettingsAutoCompaction{
		Policy: map[pb.AutoCompactionPolicy]serverapi.ChatSettingsAutoCompactionPolicy{
			pb.AutoCompactionPolicy_AUTO_COMPACTION_POLICY_OPTIONAL: serverapi.ChatSettingsAutoCompactionOptional,
			pb.AutoCompactionPolicy_AUTO_COMPACTION_POLICY_REQUIRED: serverapi.ChatSettingsAutoCompactionRequired,
			pb.AutoCompactionPolicy_AUTO_COMPACTION_POLICY_DISABLED: serverapi.ChatSettingsAutoCompactionDisabled,
		}[value.Policy],
		Stored: value.Stored, Effective: value.Effective, Editability: chatSettingsEditabilityFromProto(value.Editability),
	}
}

func chatSettingsSupervisorValueFromProto(value pb.SupervisorValue) serverapi.ChatSettingsSupervisorValue {
	return map[pb.SupervisorValue]serverapi.ChatSettingsSupervisorValue{
		pb.SupervisorValue_SUPERVISOR_VALUE_OFF:         serverapi.ChatSettingsSupervisorOff,
		pb.SupervisorValue_SUPERVISOR_VALUE_AFTER_EDITS: serverapi.ChatSettingsSupervisorAfterEdits,
		pb.SupervisorValue_SUPERVISOR_VALUE_ALWAYS:      serverapi.ChatSettingsSupervisorAlways,
	}[value]
}

func chatSettingsEditabilityFromProto(value pb.Editability) serverapi.ChatSettingsEditability {
	return map[pb.Editability]serverapi.ChatSettingsEditability{
		pb.Editability_EDITABILITY_EDITABLE:        serverapi.ChatSettingsEditable,
		pb.Editability_EDITABILITY_WORKFLOW_LOCK:   serverapi.ChatSettingsWorkflowLock,
		pb.Editability_EDITABILITY_CACHING_LOCK:    serverapi.ChatSettingsCachingLock,
		pb.Editability_EDITABILITY_POLICY_DISABLED: serverapi.ChatSettingsPolicyDisabled,
	}[value]
}

func ChatSettingsErrorFromProto(value *pb.ReadError) error {
	if err := Validate(value); err != nil {
		return err
	}
	switch detail := value.Detail.(type) {
	case *pb.ReadError_AuthRequired:
		return serverapi.ErrServerAuthRequired
	case *pb.ReadError_ServerNotReady:
		return ServerNotReadyFromProto(detail.ServerNotReady)
	case *pb.ReadError_WorkspaceNotRegistered:
		return serverapi.ErrWorkspaceNotRegistered
	case *pb.ReadError_SessionNotFound:
		sessionID, err := runtimeids.ParseSessionID(detail.SessionNotFound.SessionId)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", sessioncontract.ErrSessionNotFound, sessionID)
	case *pb.ReadError_InternalFailure:
		return InternalFailureFromProto(detail.InternalFailure)
	case *pb.ReadError_ChatSettingsAgentPreparation:
		return &serverapi.ChatSettingsAgentPreparationError{
			Agent: detail.ChatSettingsAgentPreparation.Agent,
			Category: map[pb.AgentPreparationCategory]serverapi.ChatSettingsAgentPreparationCategory{
				pb.AgentPreparationCategory_AGENT_PREPARATION_CATEGORY_INVALID_CONFIGURATION: serverapi.ChatSettingsAgentInvalidConfiguration,
				pb.AgentPreparationCategory_AGENT_PREPARATION_CATEGORY_PROVIDER_UNAVAILABLE:  serverapi.ChatSettingsAgentProviderUnavailable,
				pb.AgentPreparationCategory_AGENT_PREPARATION_CATEGORY_INTERNAL_PREPARATION:  serverapi.ChatSettingsAgentInternalPreparation,
			}[detail.ChatSettingsAgentPreparation.Category],
		}
	default:
		return errors.New(value.Code)
	}
}
