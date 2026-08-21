package protoapi

import (
	"errors"
	"fmt"
	"strings"

	authpb "core/shared/protoapi/gen/kent/api/auth"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"google.golang.org/protobuf/types/known/emptypb"
)

func SessionPlanErrorFromProto(failure *sessionlaunchpb.SessionPlanError) error {
	if err := Validate(failure); err != nil {
		return err
	}
	switch detail := failure.Detail.(type) {
	case *sessionlaunchpb.SessionPlanError_AuthRequired:
		return serverapi.ErrServerAuthRequired
	case *sessionlaunchpb.SessionPlanError_WorkspaceNotRegistered:
		return serverapi.ErrWorkspaceNotRegistered
	case *sessionlaunchpb.SessionPlanError_SubagentLaunchDenied:
		kind, err := subagentLaunchDenialKindFromProto(detail.SubagentLaunchDenied.Kind)
		if err != nil {
			return err
		}
		return &serverapi.SubagentLaunchDeniedError{
			Kind:           kind,
			Target:         clonePointer(detail.SubagentLaunchDenied.Target),
			AvailableRoles: append([]string(nil), detail.SubagentLaunchDenied.AvailableRoles...),
		}
	case *sessionlaunchpb.SessionPlanError_MaxDepthExceeded:
		return protocol.NewMaxDepthExceededSubagentLaunchPolicyError(
			int(detail.MaxDepthExceeded.AttemptedDepth),
			int(detail.MaxDepthExceeded.MaxDepth),
		)
	case *sessionlaunchpb.SessionPlanError_LineageCorrupt:
		repeated, err := runtimeids.ParseSessionID(detail.LineageCorrupt.RepeatedSessionId)
		if err != nil {
			return err
		}
		visited := make([]runtimeids.SessionID, 0, len(detail.LineageCorrupt.VisitedSessionIds))
		for _, raw := range detail.LineageCorrupt.VisitedSessionIds {
			sessionID, parseErr := runtimeids.ParseSessionID(raw)
			if parseErr != nil {
				return parseErr
			}
			visited = append(visited, sessionID)
		}
		return protocol.NewLineageCorruptSubagentLaunchPolicyError(repeated, visited)
	case *sessionlaunchpb.SessionPlanError_InternalFailure:
		return InternalFailureFromProto(detail.InternalFailure)
	default:
		return fmt.Errorf("session plan failure %q has unsupported detail %T", failure.Code, failure.Detail)
	}
}

func SessionPlanErrorToProto(
	err error,
	workspace *projectpb.WorkspaceNotRegisteredDetails,
) (*sessionlaunchpb.SessionPlanError, bool, error) {
	failure := &sessionlaunchpb.SessionPlanError{}
	switch {
	case errors.Is(err, serverapi.ErrServerAuthRequired):
		failure.Code = "auth_required"
		failure.Detail = &sessionlaunchpb.SessionPlanError_AuthRequired{
			AuthRequired: &authpb.AuthRequiredDetails{},
		}
	case errors.Is(err, serverapi.ErrWorkspaceNotRegistered):
		failure.Code = "workspace_not_registered"
		failure.Detail = &sessionlaunchpb.SessionPlanError_WorkspaceNotRegistered{
			WorkspaceNotRegistered: workspace,
		}
	default:
		var denied *serverapi.SubagentLaunchDeniedError
		if errors.As(err, &denied) {
			kind, conversionErr := subagentLaunchDenialKindToProto(denied.Kind)
			if conversionErr != nil {
				return nil, true, conversionErr
			}
			failure.Code = "subagent_launch_denied"
			failure.Detail = &sessionlaunchpb.SessionPlanError_SubagentLaunchDenied{
				SubagentLaunchDenied: &sessionlaunchpb.SubagentLaunchDeniedDetails{
					Kind: kind, Target: clonePointer(denied.Target),
					AvailableRoles: append([]string(nil), denied.AvailableRoles...),
				},
			}
			break
		}
		var policy *protocol.SubagentLaunchPolicyError
		if !errors.As(err, &policy) {
			return nil, false, nil
		}
		if validationErr := policy.Validate(); validationErr != nil {
			return nil, true, validationErr
		}
		switch policy.Kind {
		case protocol.SubagentLaunchPolicyMaxDepthExceeded:
			attempted, conversionErr := projectInt32(*policy.AttemptedDepth, "attempted subagent depth")
			if conversionErr != nil {
				return nil, true, conversionErr
			}
			maximum, conversionErr := projectInt32(*policy.MaxDepth, "maximum subagent depth")
			if conversionErr != nil {
				return nil, true, conversionErr
			}
			failure.Code = "max_depth_exceeded"
			failure.Detail = &sessionlaunchpb.SessionPlanError_MaxDepthExceeded{
				MaxDepthExceeded: &sessionlaunchpb.SubagentLaunchMaxDepthExceededDetails{
					AttemptedDepth: attempted, MaxDepth: maximum,
				},
			}
		case protocol.SubagentLaunchPolicyLineageCorrupt:
			visited := make([]string, 0, len(policy.VisitedSessionIDs))
			for _, sessionID := range policy.VisitedSessionIDs {
				visited = append(visited, sessionID.String())
			}
			failure.Code = "lineage_corrupt"
			failure.Detail = &sessionlaunchpb.SessionPlanError_LineageCorrupt{
				LineageCorrupt: &sessionlaunchpb.SubagentLaunchLineageCorruptDetails{
					RepeatedSessionId: policy.RepeatedSessionID.String(),
					VisitedSessionIds: visited,
				},
			}
		default:
			return nil, true, fmt.Errorf("subagent launch policy kind %q is unsupported", policy.Kind)
		}
	}
	if validationErr := Validate(failure); validationErr != nil {
		return nil, true, validationErr
	}
	return failure, true, nil
}

func SessionLaunchIntentToProto(intent serverapi.SessionLaunchIntent) (*sessionlaunchpb.SessionLaunchIntent, error) {
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	message := &sessionlaunchpb.SessionLaunchIntent{}
	switch intent.Kind() {
	case serverapi.SessionLaunchIntentCreateNew:
		origin, present := intent.CreateOrigin()
		if !present {
			return nil, errors.New("session launch create origin is required")
		}
		converted, err := sessionCreateOriginToProto(origin)
		if err != nil {
			return nil, err
		}
		message.Intent = &sessionlaunchpb.SessionLaunchIntent_CreateNew{CreateNew: converted}
	case serverapi.SessionLaunchIntentOpenExisting:
		sessionID, present := intent.SessionID()
		if !present {
			return nil, errors.New("open-existing session id is required")
		}
		message.Intent = &sessionlaunchpb.SessionLaunchIntent_OpenExistingSessionId{
			OpenExistingSessionId: sessionID.String(),
		}
	default:
		return nil, fmt.Errorf("session launch intent kind %q is unsupported", intent.Kind())
	}
	return message, Validate(message)
}

func SessionLaunchIntentFromProto(message *sessionlaunchpb.SessionLaunchIntent) (serverapi.SessionLaunchIntent, error) {
	if message == nil {
		return serverapi.SessionLaunchIntent{}, errors.New("generated Session launch intent is required")
	}
	switch intent := message.Intent.(type) {
	case *sessionlaunchpb.SessionLaunchIntent_CreateNew:
		origin, err := sessionCreateOriginFromProto(intent.CreateNew)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, err
		}
		return serverapi.CreateNewSessionLaunchIntent(origin), nil
	case *sessionlaunchpb.SessionLaunchIntent_OpenExistingSessionId:
		sessionID, err := runtimeids.ParseSessionID(intent.OpenExistingSessionId)
		if err != nil {
			return serverapi.SessionLaunchIntent{}, err
		}
		return serverapi.OpenExistingSessionLaunchIntent(sessionID), nil
	default:
		return serverapi.SessionLaunchIntent{}, fmt.Errorf("protobuf session launch intent %T is unsupported", message.Intent)
	}
}

func sessionCreateOriginToProto(origin serverapi.SessionCreateOrigin) (*sessionlaunchpb.SessionCreateOrigin, error) {
	if err := origin.Validate(); err != nil {
		return nil, err
	}
	message := &sessionlaunchpb.SessionCreateOrigin{}
	switch origin.Kind() {
	case serverapi.SessionCreateOriginIndependent:
		message.Origin = &sessionlaunchpb.SessionCreateOrigin_Independent{Independent: &emptypb.Empty{}}
	case serverapi.SessionCreateOriginPreviousSession, serverapi.SessionCreateOriginParentAgent:
		sessionID, present := origin.SessionID()
		if !present {
			return nil, fmt.Errorf("%s session create origin id is required", origin.Kind())
		}
		if origin.Kind() == serverapi.SessionCreateOriginPreviousSession {
			message.Origin = &sessionlaunchpb.SessionCreateOrigin_PreviousSessionId{PreviousSessionId: sessionID.String()}
		} else {
			message.Origin = &sessionlaunchpb.SessionCreateOrigin_ParentAgentSessionId{ParentAgentSessionId: sessionID.String()}
		}
	default:
		return nil, fmt.Errorf("session create origin kind %q is unsupported", origin.Kind())
	}
	return message, Validate(message)
}

func sessionCreateOriginFromProto(message *sessionlaunchpb.SessionCreateOrigin) (serverapi.SessionCreateOrigin, error) {
	switch origin := message.Origin.(type) {
	case *sessionlaunchpb.SessionCreateOrigin_Independent:
		return serverapi.IndependentSessionCreateOrigin(), nil
	case *sessionlaunchpb.SessionCreateOrigin_PreviousSessionId:
		sessionID, err := runtimeids.ParseSessionID(origin.PreviousSessionId)
		if err != nil {
			return serverapi.SessionCreateOrigin{}, err
		}
		return serverapi.PreviousSessionCreateOrigin(sessionID), nil
	case *sessionlaunchpb.SessionCreateOrigin_ParentAgentSessionId:
		sessionID, err := runtimeids.ParseSessionID(origin.ParentAgentSessionId)
		if err != nil {
			return serverapi.SessionCreateOrigin{}, err
		}
		return serverapi.ParentAgentSessionCreateOrigin(sessionID), nil
	default:
		return serverapi.SessionCreateOrigin{}, fmt.Errorf("protobuf session create origin %T is unsupported", message.Origin)
	}
}

func RunPromptOverridesToProto(overrides serverapi.RunPromptOverrides) (*sessionlaunchpb.RunPromptOverrides, error) {
	if err := overrides.ValidateAgentRoleOverride(); err != nil {
		return nil, err
	}
	message := &sessionlaunchpb.RunPromptOverrides{AgentRole: clonePointer(overrides.AgentRole)}
	setOptionalNonblank(&message.Model, overrides.Model)
	setOptionalNonblank(&message.ProviderOverride, overrides.ProviderOverride)
	setOptionalNonblank(&message.ThinkingLevel, overrides.ThinkingLevel)
	setOptionalNonblank(&message.Theme, overrides.Theme)
	setOptionalNonblank(&message.Tools, overrides.Tools)
	setOptionalNonblank(&message.OpenaiBaseUrl, overrides.OpenAIBaseURL)
	if overrides.ModelTimeoutSeconds != 0 {
		value, err := projectInt32(overrides.ModelTimeoutSeconds, "model timeout seconds")
		if err != nil {
			return nil, err
		}
		message.ModelTimeoutSeconds = &value
	}
	return message, Validate(message)
}

func RunPromptOverridesFromProto(message *sessionlaunchpb.RunPromptOverrides) (serverapi.RunPromptOverrides, error) {
	if message == nil {
		return serverapi.RunPromptOverrides{}, errors.New("generated Run Prompt overrides are required")
	}
	overrides := serverapi.RunPromptOverrides{
		AgentRole:        clonePointer(message.AgentRole),
		Model:            dereference(message.Model),
		ProviderOverride: dereference(message.ProviderOverride),
		ThinkingLevel:    dereference(message.ThinkingLevel),
		Theme:            dereference(message.Theme),
		Tools:            dereference(message.Tools),
		OpenAIBaseURL:    dereference(message.OpenaiBaseUrl),
	}
	if message.ModelTimeoutSeconds != nil {
		overrides.ModelTimeoutSeconds = int(*message.ModelTimeoutSeconds)
	}
	return overrides, overrides.ValidateAgentRoleOverride()
}

func setOptionalNonblank(target **string, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	copied := value
	*target = &copied
}

func SessionToolIDToProto(toolID toolspec.ID) (sessionlaunchpb.ToolID, error) {
	switch toolID {
	case toolspec.ToolExecCommand:
		return sessionlaunchpb.ToolID_TOOL_ID_EXEC_COMMAND, nil
	case toolspec.ToolWriteStdin:
		return sessionlaunchpb.ToolID_TOOL_ID_WRITE_STDIN, nil
	case toolspec.ToolViewImage:
		return sessionlaunchpb.ToolID_TOOL_ID_VIEW_IMAGE, nil
	case toolspec.ToolPatch:
		return sessionlaunchpb.ToolID_TOOL_ID_PATCH, nil
	case toolspec.ToolEdit:
		return sessionlaunchpb.ToolID_TOOL_ID_EDIT, nil
	case toolspec.ToolAskQuestion:
		return sessionlaunchpb.ToolID_TOOL_ID_ASK_QUESTION, nil
	case toolspec.ToolCompleteNode:
		return sessionlaunchpb.ToolID_TOOL_ID_COMPLETE_NODE, nil
	case toolspec.ToolTriggerHandoff:
		return sessionlaunchpb.ToolID_TOOL_ID_TRIGGER_HANDOFF, nil
	case toolspec.ToolWebSearch:
		return sessionlaunchpb.ToolID_TOOL_ID_WEB_SEARCH, nil
	default:
		return 0, fmt.Errorf("tool id %q is unsupported", toolID)
	}
}

func SessionToolIDFromProto(toolID sessionlaunchpb.ToolID) (toolspec.ID, error) {
	switch toolID {
	case sessionlaunchpb.ToolID_TOOL_ID_EXEC_COMMAND:
		return toolspec.ToolExecCommand, nil
	case sessionlaunchpb.ToolID_TOOL_ID_WRITE_STDIN:
		return toolspec.ToolWriteStdin, nil
	case sessionlaunchpb.ToolID_TOOL_ID_VIEW_IMAGE:
		return toolspec.ToolViewImage, nil
	case sessionlaunchpb.ToolID_TOOL_ID_PATCH:
		return toolspec.ToolPatch, nil
	case sessionlaunchpb.ToolID_TOOL_ID_EDIT:
		return toolspec.ToolEdit, nil
	case sessionlaunchpb.ToolID_TOOL_ID_ASK_QUESTION:
		return toolspec.ToolAskQuestion, nil
	case sessionlaunchpb.ToolID_TOOL_ID_COMPLETE_NODE:
		return toolspec.ToolCompleteNode, nil
	case sessionlaunchpb.ToolID_TOOL_ID_TRIGGER_HANDOFF:
		return toolspec.ToolTriggerHandoff, nil
	case sessionlaunchpb.ToolID_TOOL_ID_WEB_SEARCH:
		return toolspec.ToolWebSearch, nil
	default:
		return "", fmt.Errorf("protobuf tool id %v is unsupported", toolID)
	}
}

func subagentLaunchDenialKindToProto(kind serverapi.SubagentLaunchDenialKind) (sessionlaunchpb.SubagentLaunchDenialKind, error) {
	switch kind {
	case serverapi.SubagentLaunchDenialInvalidTarget:
		return sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_INVALID_TARGET, nil
	case serverapi.SubagentLaunchDenialTargetMissing:
		return sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_TARGET_MISSING, nil
	case serverapi.SubagentLaunchDenialNotCallable:
		return sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_NOT_CALLABLE, nil
	case serverapi.SubagentLaunchDenialCallerMissing:
		return sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_CALLER_MISSING, nil
	case serverapi.SubagentLaunchDenialParentMissing:
		return sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_PARENT_MISSING, nil
	default:
		return 0, fmt.Errorf("subagent launch denial kind %q is unsupported", kind)
	}
}

func subagentLaunchDenialKindFromProto(kind sessionlaunchpb.SubagentLaunchDenialKind) (serverapi.SubagentLaunchDenialKind, error) {
	switch kind {
	case sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_INVALID_TARGET:
		return serverapi.SubagentLaunchDenialInvalidTarget, nil
	case sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_TARGET_MISSING:
		return serverapi.SubagentLaunchDenialTargetMissing, nil
	case sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_NOT_CALLABLE:
		return serverapi.SubagentLaunchDenialNotCallable, nil
	case sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_CALLER_MISSING:
		return serverapi.SubagentLaunchDenialCallerMissing, nil
	case sessionlaunchpb.SubagentLaunchDenialKind_SUBAGENT_LAUNCH_DENIAL_KIND_PARENT_MISSING:
		return serverapi.SubagentLaunchDenialParentMissing, nil
	default:
		return "", fmt.Errorf("protobuf subagent launch denial kind %v is unsupported", kind)
	}
}
