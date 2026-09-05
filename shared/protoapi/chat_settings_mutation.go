package protoapi

import (
	"errors"

	contextpb "core/shared/protoapi/gen/kent/api/chat_context"
	pb "core/shared/protoapi/gen/kent/api/chat_settings"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func ChatSettingsMutationFromProto(request *pb.MutationRequest) (serverapi.ChatSettingsMutationRequest, error) {
	if err := Validate(request); err != nil {
		return serverapi.ChatSettingsMutationRequest{}, err
	}
	id, err := runtimeids.ParseSessionID(request.Session.SessionId)
	if err != nil {
		return serverapi.ChatSettingsMutationRequest{}, err
	}
	result := serverapi.ChatSettingsMutationRequest{SessionID: id}
	switch value := request.Operation.Operation.(type) {
	case *pb.MutationOperation_AgentRole:
		result.Operation = serverapi.ChatSettingsMutationOperation{Kind: serverapi.ChatSettingsMutationAgent, Role: &value.AgentRole}
	case *pb.MutationOperation_Supervisor:
		result.Operation = serverapi.ChatSettingsMutationOperation{Kind: serverapi.ChatSettingsMutationSupervisor, Value: textutil.Value(string(chatSettingsSupervisorValueFromProto(value.Supervisor)))}
	case *pb.MutationOperation_Thinking:
		result.Operation = serverapi.ChatSettingsMutationOperation{Kind: serverapi.ChatSettingsMutationThinking, Value: &value.Thinking}
	case *pb.MutationOperation_FastEnabled:
		result.Operation = serverapi.ChatSettingsMutationOperation{Kind: serverapi.ChatSettingsMutationFast, Enabled: &value.FastEnabled}
	case *pb.MutationOperation_QuestionsEnabled:
		result.Operation = serverapi.ChatSettingsMutationOperation{Kind: serverapi.ChatSettingsMutationQuestions, Enabled: &value.QuestionsEnabled}
	case *pb.MutationOperation_AutoCompactionEnabled:
		result.Operation = serverapi.ChatSettingsMutationOperation{Kind: serverapi.ChatSettingsMutationAutoCompaction, Enabled: &value.AutoCompactionEnabled}
	}
	return result, result.Validate()
}

func ChatSettingsMutationToProto(request serverapi.ChatSettingsMutationRequest) (*pb.MutationRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	operation := &pb.MutationOperation{}
	switch value := request.Operation; value.Kind {
	case serverapi.ChatSettingsMutationAgent:
		operation.Operation = &pb.MutationOperation_AgentRole{AgentRole: *value.Role}
	case serverapi.ChatSettingsMutationSupervisor:
		operation.Operation = &pb.MutationOperation_Supervisor{Supervisor: chatSettingsSupervisorValueToProto(serverapi.ChatSettingsSupervisorValue(*value.Value))}
	case serverapi.ChatSettingsMutationThinking:
		operation.Operation = &pb.MutationOperation_Thinking{Thinking: *value.Value}
	case serverapi.ChatSettingsMutationFast:
		operation.Operation = &pb.MutationOperation_FastEnabled{FastEnabled: *value.Enabled}
	case serverapi.ChatSettingsMutationQuestions:
		operation.Operation = &pb.MutationOperation_QuestionsEnabled{QuestionsEnabled: *value.Enabled}
	case serverapi.ChatSettingsMutationAutoCompaction:
		operation.Operation = &pb.MutationOperation_AutoCompactionEnabled{AutoCompactionEnabled: *value.Enabled}
	}
	result := &pb.MutationRequest{Session: &pb.SessionTarget{SessionId: request.SessionID.String()}, Operation: operation}
	return result, Validate(result)
}

func ChatSettingsMutationResponseToProto(value serverapi.ChatSettingsMutationResponse, id runtimeids.SessionID) (*pb.MutationSuccess, error) {
	if err := value.ValidateForSession(id); err != nil {
		return nil, err
	}
	result := &pb.MutationSuccess{
		Settings: chatSettingsToProto(value.Settings), Session: chatSettingsFactsToProto(*value.Session),
		Context: chatContextToProto(value.Context), Result: &pb.MutationResult{},
	}
	if value.Result.Applied != nil {
		result.Result.Outcome = &pb.MutationResult_Applied{Applied: &pb.MutationApplied{Changed: value.Result.Applied.Changed}}
	} else {
		result.Result.Outcome = &pb.MutationResult_Rejected{Rejected: &pb.MutationRejected{Reason: chatSettingsRejectionToProto(value.Result.Rejected.Reason)}}
	}
	return result, Validate(result)
}

func ChatSettingsMutationResponseFromProto(value *pb.MutationSuccess, id runtimeids.SessionID) (serverapi.ChatSettingsMutationResponse, error) {
	if err := Validate(value); err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	facts, err := chatSettingsFactsFromProto(value.Session)
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	result := serverapi.ChatSettingsMutationResponse{Settings: chatSettingsFromProto(value.Settings), Session: &facts, Context: chatContextFromProto(value.Context)}
	switch outcome := value.Result.Outcome.(type) {
	case *pb.MutationResult_Applied:
		result.Result = serverapi.NewChatSettingsMutationApplied(outcome.Applied.Changed)
	case *pb.MutationResult_Rejected:
		result.Result = serverapi.NewChatSettingsMutationRejected(chatSettingsRejectionFromProto(outcome.Rejected.Reason))
	default:
		return result, errors.New("Chat settings mutation outcome is required")
	}
	return result, result.ValidateForSession(id)
}

func chatSettingsRejectionToProto(value serverapi.ChatSettingsMutationRejectionReason) pb.MutationRejectionReason {
	return map[serverapi.ChatSettingsMutationRejectionReason]pb.MutationRejectionReason{
		serverapi.ChatSettingsMutationAgentLocked:              pb.MutationRejectionReason_MUTATION_REJECTION_REASON_AGENT_LOCKED,
		serverapi.ChatSettingsMutationAgentUnavailable:         pb.MutationRejectionReason_MUTATION_REJECTION_REASON_AGENT_UNAVAILABLE,
		serverapi.ChatSettingsMutationThinkingUnavailable:      pb.MutationRejectionReason_MUTATION_REJECTION_REASON_THINKING_UNAVAILABLE,
		serverapi.ChatSettingsMutationFastUnavailable:          pb.MutationRejectionReason_MUTATION_REJECTION_REASON_FAST_UNAVAILABLE,
		serverapi.ChatSettingsMutationAutoCompactionPolicyLock: pb.MutationRejectionReason_MUTATION_REJECTION_REASON_AUTO_COMPACTION_POLICY_LOCKED,
	}[value]
}

func chatSettingsRejectionFromProto(value pb.MutationRejectionReason) serverapi.ChatSettingsMutationRejectionReason {
	return map[pb.MutationRejectionReason]serverapi.ChatSettingsMutationRejectionReason{
		pb.MutationRejectionReason_MUTATION_REJECTION_REASON_AGENT_LOCKED:                  serverapi.ChatSettingsMutationAgentLocked,
		pb.MutationRejectionReason_MUTATION_REJECTION_REASON_AGENT_UNAVAILABLE:             serverapi.ChatSettingsMutationAgentUnavailable,
		pb.MutationRejectionReason_MUTATION_REJECTION_REASON_THINKING_UNAVAILABLE:          serverapi.ChatSettingsMutationThinkingUnavailable,
		pb.MutationRejectionReason_MUTATION_REJECTION_REASON_FAST_UNAVAILABLE:              serverapi.ChatSettingsMutationFastUnavailable,
		pb.MutationRejectionReason_MUTATION_REJECTION_REASON_AUTO_COMPACTION_POLICY_LOCKED: serverapi.ChatSettingsMutationAutoCompactionPolicyLock,
	}[value]
}

func chatContextToProto(value serverapi.ChatContext) *contextpb.Context {
	return &contextpb.Context{
		ContextWindowTokens: value.ContextWindowTokens, UsedTokens: value.UsedTokens, RemainingTokens: value.RemainingTokens,
		AutomaticThresholdTokens: value.AutomaticThresholdTokens, AutoCompactionEnabled: value.AutoCompactionEnabled,
		CompactionMode: map[serverapi.ChatContextCompactionMode]contextpb.CompactionMode{
			serverapi.ChatContextCompactionModeDisabled:       contextpb.CompactionMode_COMPACTION_MODE_DISABLED,
			serverapi.ChatContextCompactionModeLocal:          contextpb.CompactionMode_COMPACTION_MODE_LOCAL,
			serverapi.ChatContextCompactionModeProviderNative: contextpb.CompactionMode_COMPACTION_MODE_PROVIDER_NATIVE,
		}[value.CompactionMode],
		CompletedCompactionCount: value.CompletedCompactionCount, CompactionRunning: value.CompactionRunning,
		ManualCompactAvailable: value.ManualCompactAvailable,
	}
}

func chatContextFromProto(value *contextpb.Context) serverapi.ChatContext {
	return serverapi.ChatContext{
		ContextWindowTokens: value.ContextWindowTokens, UsedTokens: value.UsedTokens, RemainingTokens: value.RemainingTokens,
		AutomaticThresholdTokens: value.AutomaticThresholdTokens, AutoCompactionEnabled: value.AutoCompactionEnabled,
		CompactionMode: map[contextpb.CompactionMode]serverapi.ChatContextCompactionMode{
			contextpb.CompactionMode_COMPACTION_MODE_DISABLED:        serverapi.ChatContextCompactionModeDisabled,
			contextpb.CompactionMode_COMPACTION_MODE_LOCAL:           serverapi.ChatContextCompactionModeLocal,
			contextpb.CompactionMode_COMPACTION_MODE_PROVIDER_NATIVE: serverapi.ChatContextCompactionModeProviderNative,
		}[value.CompactionMode],
		CompletedCompactionCount: value.CompletedCompactionCount, CompactionRunning: value.CompactionRunning,
		ManualCompactAvailable: value.ManualCompactAvailable,
	}
}
