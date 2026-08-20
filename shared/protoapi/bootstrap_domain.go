package protoapi

import (
	"fmt"
	"math"
	"time"

	authpb "core/shared/protoapi/gen/kent/api/auth"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ServerReadinessToProto(response serverapi.ServerReadinessResponse) (*serverpb.GetReadinessSuccess, error) {
	causes, err := mapSliceError(response.Causes, func(cause serverapi.ServerReadinessCause) (*serverpb.ReadinessCause, error) {
		severity, err := readinessSeverityToProto(cause.Severity)
		if err != nil {
			return nil, err
		}
		return &serverpb.ReadinessCause{
			Code:         cause.Code,
			Severity:     severity,
			Summary:      clonePointer(cause.Summary),
			NextAction:   clonePointer(cause.NextAction),
			DiagnosticId: optionalString(cause.DiagnosticID),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	readiness := &serverpb.Readiness{
		Ready:           response.Ready,
		ServerId:        response.ServerID,
		ServerVersion:   response.ServerVersion,
		ServerBuild:     response.ServerBuild,
		ProtocolVersion: response.ProtocolVersion,
		AuthReady:       response.AuthReady,
		AuthRequired:    response.AuthRequired,
		Endpoint:        response.Endpoint,
		SubagentRoles: mapSlice(response.SubagentRoles, func(role serverapi.SubagentRoleSummary) *serverpb.SubagentRoleSummary {
			return &serverpb.SubagentRoleSummary{Name: role.Name}
		}),
		Causes: causes,
	}
	success := &serverpb.GetReadinessSuccess{Readiness: readiness}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert server readiness to protobuf: %w", err)
	}
	return success, nil
}

func ServerReadinessFromProto(success *serverpb.GetReadinessSuccess) (serverapi.ServerReadinessResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.ServerReadinessResponse{}, fmt.Errorf("convert server readiness from protobuf: %w", err)
	}
	readiness := success.Readiness
	causes, err := mapSliceError(readiness.Causes, func(cause *serverpb.ReadinessCause) (serverapi.ServerReadinessCause, error) {
		severity, err := readinessSeverityFromProto(cause.Severity)
		if err != nil {
			return serverapi.ServerReadinessCause{}, err
		}
		return serverapi.ServerReadinessCause{
			Code:         cause.Code,
			Severity:     severity,
			Summary:      clonePointer(cause.Summary),
			NextAction:   clonePointer(cause.NextAction),
			DiagnosticID: dereference(cause.DiagnosticId),
		}, nil
	})
	if err != nil {
		return serverapi.ServerReadinessResponse{}, err
	}
	response := serverapi.ServerReadinessResponse{
		Ready:           readiness.Ready,
		ServerID:        readiness.ServerId,
		ServerVersion:   readiness.ServerVersion,
		ServerBuild:     readiness.ServerBuild,
		ProtocolVersion: readiness.ProtocolVersion,
		AuthReady:       readiness.AuthReady,
		AuthRequired:    readiness.AuthRequired,
		Endpoint:        readiness.Endpoint,
		SubagentRoles: mapSlice(readiness.SubagentRoles, func(role *serverpb.SubagentRoleSummary) serverapi.SubagentRoleSummary {
			return serverapi.SubagentRoleSummary{Name: role.Name}
		}),
		Causes: causes,
	}
	return response, nil
}

func readinessSeverityToProto(severity string) (serverpb.ReadinessSeverity, error) {
	switch severity {
	case "error":
		return serverpb.ReadinessSeverity_READINESS_SEVERITY_ERROR, nil
	default:
		return serverpb.ReadinessSeverity_READINESS_SEVERITY_UNSPECIFIED, fmt.Errorf("server readiness severity %q is unsupported", severity)
	}
}

func readinessSeverityFromProto(severity serverpb.ReadinessSeverity) (string, error) {
	switch severity {
	case serverpb.ReadinessSeverity_READINESS_SEVERITY_ERROR:
		return "error", nil
	default:
		return "", fmt.Errorf("protobuf server readiness severity %v is unsupported", severity)
	}
}

func UpdateStatusToProto(response serverapi.UpdateStatusResponse) (*serverpb.GetUpdateStatusSuccess, error) {
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("validate update status response: %w", err)
	}
	status := &serverpb.UpdateStatus{}
	switch response.Result.Kind() {
	case serverapi.UpdateStatusCurrent:
		versions := response.Result.Versions()
		status.Status = &serverpb.UpdateStatus_Current{Current: &serverpb.UpdateVersions{
			CurrentVersion: versions.Current,
			LatestVersion:  versions.Latest,
		}}
	case serverapi.UpdateStatusAvailable:
		versions := response.Result.Versions()
		status.Status = &serverpb.UpdateStatus_Available{Available: &serverpb.UpdateVersions{
			CurrentVersion: versions.Current,
			LatestVersion:  versions.Latest,
		}}
	case serverapi.UpdateStatusCheckUnavailable:
		status.Status = &serverpb.UpdateStatus_CheckUnavailable{CheckUnavailable: &emptypb.Empty{}}
	case serverapi.UpdateStatusCheckFailed:
		status.Status = &serverpb.UpdateStatus_CheckFailed{CheckFailed: &serverpb.UpdateCheckFailed{
			Cause: response.Result.Failure().Cause,
		}}
	default:
		return nil, fmt.Errorf("update status kind %q is unsupported", response.Result.Kind())
	}
	success := &serverpb.GetUpdateStatusSuccess{Status: status}
	if err := Validate(success); err != nil {
		return nil, fmt.Errorf("convert update status to protobuf: %w", err)
	}
	return success, nil
}

func UpdateStatusFromProto(success *serverpb.GetUpdateStatusSuccess) (serverapi.UpdateStatusResponse, error) {
	if err := Validate(success); err != nil {
		return serverapi.UpdateStatusResponse{}, fmt.Errorf("convert update status from protobuf: %w", err)
	}
	var result serverapi.UpdateStatusResult
	switch status := success.Status.Status.(type) {
	case *serverpb.UpdateStatus_Current:
		result = serverapi.CurrentUpdateStatusResult(status.Current.CurrentVersion, status.Current.LatestVersion)
	case *serverpb.UpdateStatus_Available:
		result = serverapi.AvailableUpdateStatusResult(status.Available.CurrentVersion, status.Available.LatestVersion)
	case *serverpb.UpdateStatus_CheckUnavailable:
		result = serverapi.CheckUnavailableUpdateStatusResult()
	case *serverpb.UpdateStatus_CheckFailed:
		result = serverapi.FailedUpdateStatusResult(status.CheckFailed.Cause)
	default:
		return serverapi.UpdateStatusResponse{}, fmt.Errorf("protobuf update status has unsupported outcome %T", status)
	}
	response := serverapi.UpdateStatusResponse{Result: result}
	if err := response.Validate(); err != nil {
		return serverapi.UpdateStatusResponse{}, fmt.Errorf("validate converted update status response: %w", err)
	}
	return response, nil
}

func AuthBootstrapStatusToProto(response serverapi.AuthGetBootstrapStatusResponse) (*authpb.BootstrapStatus, error) {
	supportedModes, err := mapSliceError(response.SupportedModes, authBootstrapModeToProto)
	if err != nil {
		return nil, err
	}
	status := &authpb.BootstrapStatus{
		AuthReady:              response.AuthReady,
		AuthRequired:           response.AuthRequired,
		NoAuthSelected:         response.NoAuthSelected,
		AuthBootstrapSupported: response.AuthBootstrapSupported,
		AllowedPreAuthMethods:  cloneSlice(response.AllowedPreAuthMethods),
		SupportedModes:         supportedModes,
	}
	if response.OAuth.Issuer != "" || response.OAuth.ClientID != "" {
		status.Oauth = &authpb.BootstrapOAuthConfig{
			Issuer:   optionalString(response.OAuth.Issuer),
			ClientId: optionalString(response.OAuth.ClientID),
		}
	}
	if err := Validate(status); err != nil {
		return nil, fmt.Errorf("convert auth bootstrap status to protobuf: %w", err)
	}
	return status, nil
}

func AuthBootstrapStatusFromProto(status *authpb.BootstrapStatus) (serverapi.AuthGetBootstrapStatusResponse, error) {
	if err := Validate(status); err != nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, fmt.Errorf("convert auth bootstrap status from protobuf: %w", err)
	}
	supportedModes, err := mapSliceError(status.SupportedModes, authBootstrapModeFromProto)
	if err != nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, err
	}
	response := serverapi.AuthGetBootstrapStatusResponse{
		AuthReady:              status.AuthReady,
		AuthRequired:           status.AuthRequired,
		NoAuthSelected:         status.NoAuthSelected,
		AuthBootstrapSupported: status.AuthBootstrapSupported,
		AllowedPreAuthMethods:  cloneSlice(status.AllowedPreAuthMethods),
		SupportedModes:         supportedModes,
	}
	if status.Oauth != nil {
		response.OAuth = serverapi.AuthBootstrapOAuthConfig{
			Issuer:   dereference(status.Oauth.Issuer),
			ClientID: dereference(status.Oauth.ClientId),
		}
	}
	return response, nil
}

func AuthCompleteBootstrapRequestToProto(request serverapi.AuthCompleteBootstrapRequest) (*authpb.CompleteBootstrapRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate auth complete-bootstrap request: %w", err)
	}
	mode, err := authBootstrapModeToProto(request.Mode)
	if err != nil {
		return nil, err
	}
	message := &authpb.CompleteBootstrapRequest{
		Mode:                    mode,
		Force:                   request.Force,
		ApiKey:                  optionalString(request.APIKey),
		CallbackInput:           optionalString(request.CallbackInput),
		RedirectUri:             optionalString(request.RedirectURI),
		OauthState:              optionalString(request.OAuthState),
		OauthCodeVerifier:       optionalString(request.OAuthCodeVerifier),
		DeviceAuthorizationCode: optionalString(request.DeviceAuthorizationCode),
		DeviceCodeVerifier:      optionalString(request.DeviceCodeVerifier),
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert auth complete-bootstrap request to protobuf: %w", err)
	}
	return message, nil
}

func AuthCompleteBootstrapRequestFromProto(message *authpb.CompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapRequest, error) {
	if err := Validate(message); err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, fmt.Errorf("convert auth complete-bootstrap request from protobuf: %w", err)
	}
	mode, err := authBootstrapModeFromProto(message.Mode)
	if err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, err
	}
	request := serverapi.AuthCompleteBootstrapRequest{
		Mode:                    mode,
		Force:                   message.Force,
		APIKey:                  dereference(message.ApiKey),
		CallbackInput:           dereference(message.CallbackInput),
		RedirectURI:             dereference(message.RedirectUri),
		OAuthState:              dereference(message.OauthState),
		OAuthCodeVerifier:       dereference(message.OauthCodeVerifier),
		DeviceAuthorizationCode: dereference(message.DeviceAuthorizationCode),
		DeviceCodeVerifier:      dereference(message.DeviceCodeVerifier),
	}
	if err := request.Validate(); err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, fmt.Errorf("validate converted auth complete-bootstrap request: %w", err)
	}
	return request, nil
}

func AuthBootstrapCompletionToProto(response serverapi.AuthCompleteBootstrapResponse) (*authpb.BootstrapCompletion, error) {
	message := &authpb.BootstrapCompletion{
		AuthReady:      response.AuthReady,
		NoAuthSelected: response.NoAuthSelected,
		MethodType:     optionalString(response.MethodType),
		AccountId:      optionalString(response.AccountID),
		Email:          optionalString(response.Email),
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert auth bootstrap completion to protobuf: %w", err)
	}
	return message, nil
}

func AuthBootstrapCompletionFromProto(message *authpb.BootstrapCompletion) (serverapi.AuthCompleteBootstrapResponse, error) {
	if err := Validate(message); err != nil {
		return serverapi.AuthCompleteBootstrapResponse{}, fmt.Errorf("convert auth bootstrap completion from protobuf: %w", err)
	}
	return serverapi.AuthCompleteBootstrapResponse{
		AuthReady:      message.AuthReady,
		NoAuthSelected: message.NoAuthSelected,
		MethodType:     dereference(message.MethodType),
		AccountID:      dereference(message.AccountId),
		Email:          dereference(message.Email),
	}, nil
}

func AuthNoAuthAcknowledgementToProto(response serverapi.AuthAcknowledgeNoAuthResponse) (*authpb.NoAuthAcknowledgement, error) {
	message := &authpb.NoAuthAcknowledgement{
		AuthReady:      response.AuthReady,
		NoAuthSelected: response.NoAuthSelected,
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert auth no-auth acknowledgement to protobuf: %w", err)
	}
	return message, nil
}

func AuthNoAuthAcknowledgementFromProto(message *authpb.NoAuthAcknowledgement) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	if err := Validate(message); err != nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, fmt.Errorf("convert auth no-auth acknowledgement from protobuf: %w", err)
	}
	return serverapi.AuthAcknowledgeNoAuthResponse{
		AuthReady:      message.AuthReady,
		NoAuthSelected: message.NoAuthSelected,
	}, nil
}

func AuthStatusRequestToProto(request serverapi.AuthStatusRequest) (*authpb.GetStatusRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate auth status request: %w", err)
	}
	message := &authpb.GetStatusRequest{SkipSubscriptionUsage: request.SkipSubscriptionUsage}
	if request.Provider != nil {
		message.Provider = &authpb.ProviderSelection{
			Model:            clonePointer(request.Provider.Model),
			ProviderOverride: clonePointer(request.Provider.ProviderOverride),
			OpenaiBaseUrl:    clonePointer(request.Provider.OpenAIBaseURL),
		}
		if request.Provider.ProviderCapabilities != nil {
			message.Provider.ProviderCapabilities = &authpb.ProviderCapabilitySelection{
				ProviderId:         request.Provider.ProviderCapabilities.ProviderID,
				IsOpenaiFirstParty: request.Provider.ProviderCapabilities.IsOpenAIFirstParty,
			}
		}
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert auth status request to protobuf: %w", err)
	}
	return message, nil
}

func AuthStatusRequestFromProto(message *authpb.GetStatusRequest) (serverapi.AuthStatusRequest, error) {
	if err := Validate(message); err != nil {
		return serverapi.AuthStatusRequest{}, fmt.Errorf("convert auth status request from protobuf: %w", err)
	}
	request := serverapi.AuthStatusRequest{SkipSubscriptionUsage: message.SkipSubscriptionUsage}
	if message.Provider != nil {
		request.Provider = &serverapi.AuthProviderSelection{
			Model:            clonePointer(message.Provider.Model),
			ProviderOverride: clonePointer(message.Provider.ProviderOverride),
			OpenAIBaseURL:    clonePointer(message.Provider.OpenaiBaseUrl),
		}
		if message.Provider.ProviderCapabilities != nil {
			request.Provider.ProviderCapabilities = &serverapi.AuthProviderCapabilitySelection{
				ProviderID:         message.Provider.ProviderCapabilities.ProviderId,
				IsOpenAIFirstParty: message.Provider.ProviderCapabilities.IsOpenaiFirstParty,
			}
		}
	}
	if err := request.Validate(); err != nil {
		return serverapi.AuthStatusRequest{}, fmt.Errorf("validate converted auth status request: %w", err)
	}
	return request, nil
}

func AuthStatusToProto(response serverapi.AuthStatusResponse) (*authpb.Status, error) {
	if err := response.Validate(); err != nil {
		return nil, fmt.Errorf("validate auth status response: %w", err)
	}
	resolution, err := authStatusResolutionToProto(response.Resolution)
	if err != nil {
		return nil, err
	}
	subscription, err := authSubscriptionToProto(response.Subscription)
	if err != nil {
		return nil, err
	}
	message := &authpb.Status{Resolution: resolution, Subscription: subscription}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert auth status to protobuf: %w", err)
	}
	return message, nil
}

func AuthStatusFromProto(message *authpb.Status) (serverapi.AuthStatusResponse, error) {
	if err := Validate(message); err != nil {
		return serverapi.AuthStatusResponse{}, fmt.Errorf("convert auth status from protobuf: %w", err)
	}
	resolution, err := authStatusResolutionFromProto(message.Resolution)
	if err != nil {
		return serverapi.AuthStatusResponse{}, err
	}
	subscription, err := authSubscriptionFromProto(message.Subscription)
	if err != nil {
		return serverapi.AuthStatusResponse{}, err
	}
	response := serverapi.AuthStatusResponse{Resolution: resolution, Subscription: subscription}
	if err := response.Validate(); err != nil {
		return serverapi.AuthStatusResponse{}, fmt.Errorf("validate converted auth status response: %w", err)
	}
	return response, nil
}

func CapabilityFactsRequestToProto(request serverapi.CapabilityFactsRequest) (*capabilitypb.GetFactsRequest, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("validate capability facts request: %w", err)
	}
	message := &capabilitypb.GetFactsRequest{
		WorkspaceRoot:          clonePointer(request.WorkspaceRoot),
		ExplicitLlmProviderIds: cloneSlice(request.ExplicitLLMProviderIDs),
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert capability facts request to protobuf: %w", err)
	}
	return message, nil
}

func CapabilityFactsRequestFromProto(message *capabilitypb.GetFactsRequest) (serverapi.CapabilityFactsRequest, error) {
	if err := Validate(message); err != nil {
		return serverapi.CapabilityFactsRequest{}, fmt.Errorf("convert capability facts request from protobuf: %w", err)
	}
	request := serverapi.CapabilityFactsRequest{
		WorkspaceRoot:          clonePointer(message.WorkspaceRoot),
		ExplicitLLMProviderIDs: cloneSlice(message.ExplicitLlmProviderIds),
	}
	if err := request.Validate(); err != nil {
		return serverapi.CapabilityFactsRequest{}, fmt.Errorf("validate converted capability facts request: %w", err)
	}
	return request, nil
}

func CapabilityFactsToProto(response serverapi.CapabilityFactsResponse) (*capabilitypb.Facts, error) {
	models, err := capabilityModelsToProto(response.Models)
	if err != nil {
		return nil, err
	}
	providers := capabilityProvidersToProto(response.Providers)
	imports, err := capabilityImportsToProto(response.Imports)
	if err != nil {
		return nil, err
	}
	message := &capabilitypb.Facts{
		Models:          models,
		Providers:       providers,
		Imports:         imports,
		Defaults:        capabilityDefaultsToProto(response.Defaults),
		Recommendations: &capabilitypb.RecommendationFacts{},
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert capability facts to protobuf: %w", err)
	}
	return message, nil
}

func CapabilityFactsFromProto(message *capabilitypb.Facts) (serverapi.CapabilityFactsResponse, error) {
	if err := Validate(message); err != nil {
		return serverapi.CapabilityFactsResponse{}, fmt.Errorf("convert capability facts from protobuf: %w", err)
	}
	models, err := capabilityModelsFromProto(message.Models)
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	imports, err := capabilityImportsFromProto(message.Imports)
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	return serverapi.CapabilityFactsResponse{
		Models:          models,
		Providers:       capabilityProvidersFromProto(message.Providers),
		Imports:         imports,
		Defaults:        capabilityDefaultsFromProto(message.Defaults),
		Recommendations: serverapi.CapabilityRecommendationFacts{},
	}, nil
}

func OnboardingFinalizeRequestToProto(request serverapi.OnboardingFinalizeRequest) (*onboardingpb.FinalizeRequest, error) {
	if err := serverapi.ValidateOnboardingFinalizeRequest(request); err != nil {
		return nil, fmt.Errorf("validate onboarding finalize request: %w", err)
	}
	theme, err := optionalEnumToProto(request.Theme, onboardingThemeToProto)
	if err != nil {
		return nil, err
	}
	verbosity, err := optionalEnumToProto(request.Verbosity, onboardingVerbosityToProto)
	if err != nil {
		return nil, err
	}
	compaction, err := optionalEnumToProto(request.Compaction, onboardingCompactionToProto)
	if err != nil {
		return nil, err
	}
	timeout, err := optionalIntToUint32(request.ModelTimeoutSeconds, "model_timeout_seconds")
	if err != nil {
		return nil, err
	}
	model, err := onboardingModelToProto(request.Model)
	if err != nil {
		return nil, err
	}
	contextWindow, err := onboardingContextWindowToProto(request.ContextWindow)
	if err != nil {
		return nil, err
	}
	thinking, err := onboardingThinkingToProto(request.Thinking)
	if err != nil {
		return nil, err
	}
	supervisor, err := onboardingSupervisorToProto(request.Supervisor)
	if err != nil {
		return nil, err
	}
	toolOverrides, err := mapSliceError(request.ToolOverrides, onboardingToolOverrideToProto)
	if err != nil {
		return nil, err
	}
	skillsImport, err := onboardingImportToProto(request.SkillsImport)
	if err != nil {
		return nil, err
	}
	commandsImport, err := onboardingImportToProto(request.CommandsImport)
	if err != nil {
		return nil, err
	}
	message := &onboardingpb.FinalizeRequest{
		Theme:               theme,
		MainProvider:        onboardingProviderToProto(request.MainProvider),
		Model:               model,
		ContextWindow:       contextWindow,
		Thinking:            thinking,
		Verbosity:           verbosity,
		ModelTimeoutSeconds: timeout,
		AskQuestion:         clonePointer(request.AskQuestion),
		ToolOverrides:       toolOverrides,
		Supervisor:          supervisor,
		Compaction:          compaction,
		SkillsImport:        skillsImport,
		CommandsImport:      commandsImport,
		DisabledSkillNames:  cloneSlice(request.DisabledSkillNames),
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert onboarding finalize request to protobuf: %w", err)
	}
	return message, nil
}

func OnboardingFinalizeRequestFromProto(message *onboardingpb.FinalizeRequest) (serverapi.OnboardingFinalizeRequest, error) {
	if err := Validate(message); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, fmt.Errorf("convert onboarding finalize request from protobuf: %w", err)
	}
	theme, err := optionalEnumFromProto(message.Theme, onboardingThemeFromProto)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	verbosity, err := optionalEnumFromProto(message.Verbosity, onboardingVerbosityFromProto)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	compaction, err := optionalEnumFromProto(message.Compaction, onboardingCompactionFromProto)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	model, err := onboardingModelFromProto(message.Model)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	contextWindow, err := onboardingContextWindowFromProto(message.ContextWindow)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	thinking, err := onboardingThinkingFromProto(message.Thinking)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	supervisor, err := onboardingSupervisorFromProto(message.Supervisor)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	toolOverrides, err := mapSliceError(message.ToolOverrides, onboardingToolOverrideFromProto)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	skillsImport, err := onboardingImportFromProto(message.SkillsImport)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	commandsImport, err := onboardingImportFromProto(message.CommandsImport)
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	modelTimeoutSeconds, err := optionalUint32ToInt(message.ModelTimeoutSeconds, "model_timeout_seconds")
	if err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	request := serverapi.OnboardingFinalizeRequest{
		Theme:               theme,
		MainProvider:        onboardingProviderFromProto(message.MainProvider),
		Model:               model,
		ContextWindow:       contextWindow,
		Thinking:            thinking,
		Verbosity:           verbosity,
		ModelTimeoutSeconds: modelTimeoutSeconds,
		AskQuestion:         clonePointer(message.AskQuestion),
		ToolOverrides:       toolOverrides,
		Supervisor:          supervisor,
		Compaction:          compaction,
		SkillsImport:        skillsImport,
		CommandsImport:      commandsImport,
		DisabledSkillNames:  cloneSlice(message.DisabledSkillNames),
	}
	if err := serverapi.ValidateOnboardingFinalizeRequest(request); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, fmt.Errorf("validate converted onboarding finalize request: %w", err)
	}
	return request, nil
}

func OnboardingFinalizeSuccessToProto(response serverapi.OnboardingFinalizeResponse) (*onboardingpb.FinalizeSuccess, error) {
	message := &onboardingpb.FinalizeSuccess{
		Completed:    response.Completed,
		SettingsPath: response.SettingsPath,
	}
	if err := Validate(message); err != nil {
		return nil, fmt.Errorf("convert onboarding finalize success to protobuf: %w", err)
	}
	return message, nil
}

func OnboardingFinalizeSuccessFromProto(message *onboardingpb.FinalizeSuccess) (serverapi.OnboardingFinalizeResponse, error) {
	if err := Validate(message); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, fmt.Errorf("convert onboarding finalize success from protobuf: %w", err)
	}
	return serverapi.OnboardingFinalizeResponse{
		Completed:    message.Completed,
		SettingsPath: message.SettingsPath,
	}, nil
}

func authBootstrapModeToProto(mode serverapi.AuthBootstrapMode) (authpb.BootstrapMode, error) {
	switch mode {
	case serverapi.AuthBootstrapModeNone:
		return authpb.BootstrapMode_BOOTSTRAP_MODE_NONE, nil
	case serverapi.AuthBootstrapModeBrowserCallbackURL:
		return authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL, nil
	case serverapi.AuthBootstrapModeBrowserCallbackCode:
		return authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_CODE, nil
	case serverapi.AuthBootstrapModeDeviceCode:
		return authpb.BootstrapMode_BOOTSTRAP_MODE_DEVICE_CODE, nil
	case serverapi.AuthBootstrapModeAPIKey:
		return authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY, nil
	default:
		return authpb.BootstrapMode_BOOTSTRAP_MODE_UNSPECIFIED, fmt.Errorf("auth bootstrap mode %q is unsupported", mode)
	}
}

func authBootstrapModeFromProto(mode authpb.BootstrapMode) (serverapi.AuthBootstrapMode, error) {
	switch mode {
	case authpb.BootstrapMode_BOOTSTRAP_MODE_NONE:
		return serverapi.AuthBootstrapModeNone, nil
	case authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL:
		return serverapi.AuthBootstrapModeBrowserCallbackURL, nil
	case authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_CODE:
		return serverapi.AuthBootstrapModeBrowserCallbackCode, nil
	case authpb.BootstrapMode_BOOTSTRAP_MODE_DEVICE_CODE:
		return serverapi.AuthBootstrapModeDeviceCode, nil
	case authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY:
		return serverapi.AuthBootstrapModeAPIKey, nil
	default:
		return "", fmt.Errorf("protobuf auth bootstrap mode %v is unsupported", mode)
	}
}

func authStatusResolutionToProto(resolution serverapi.AuthStatusResolution) (*authpb.StatusResolution, error) {
	message := &authpb.StatusResolution{}
	switch resolution.Kind {
	case serverapi.AuthStatusResolutionKnown:
		facts, err := authStatusFactsToProto(*resolution.Facts)
		if err != nil {
			return nil, err
		}
		message.Resolution = &authpb.StatusResolution_Known{Known: facts}
		message.PartialFailure = authStatusFailureToProto(resolution.Failure)
	case serverapi.AuthStatusResolutionUnavailable:
		message.Resolution = &authpb.StatusResolution_Unavailable{Unavailable: authStatusFailureToProto(resolution.Failure)}
	default:
		return nil, fmt.Errorf("auth status resolution kind %q is unsupported", resolution.Kind)
	}
	return message, nil
}

func authStatusResolutionFromProto(message *authpb.StatusResolution) (serverapi.AuthStatusResolution, error) {
	switch resolution := message.Resolution.(type) {
	case *authpb.StatusResolution_Known:
		facts, err := authStatusFactsFromProto(resolution.Known)
		if err != nil {
			return serverapi.AuthStatusResolution{}, err
		}
		return serverapi.KnownAuthStatusResolution(facts, authStatusFailureFromProto(message.PartialFailure)), nil
	case *authpb.StatusResolution_Unavailable:
		return serverapi.UnavailableAuthStatusResolution(*authStatusFailureFromProto(resolution.Unavailable)), nil
	default:
		return serverapi.AuthStatusResolution{}, fmt.Errorf("protobuf auth status resolution has unsupported outcome %T", resolution)
	}
}

func authStatusFactsToProto(facts serverapi.AuthStatusFacts) (*authpb.StatusFacts, error) {
	method, err := authMethodToProto(facts.Method)
	if err != nil {
		return nil, err
	}
	providerKind, err := authProviderKindToProto(facts.Provider.Kind)
	if err != nil {
		return nil, err
	}
	envPreference, err := authEnvPreferenceToProto(facts.EnvPreference)
	if err != nil {
		return nil, err
	}
	message := &authpb.StatusFacts{
		Method: method,
		Provider: &authpb.ProviderFacts{
			Kind:       providerKind,
			Identifier: facts.Provider.Identifier,
		},
		EnvPreference: envPreference,
	}
	if facts.Provider.DisplayOrigin != nil {
		message.Provider.DisplayOrigin = &authpb.ProviderDisplayOrigin{
			Scheme:   facts.Provider.DisplayOrigin.Scheme,
			Hostname: facts.Provider.DisplayOrigin.Hostname,
			Port:     clonePointer(facts.Provider.DisplayOrigin.Port),
		}
	}
	switch facts.Method {
	case serverapi.AuthStatusMethodNone:
		message.MethodFacts = &authpb.StatusFacts_NoAuth{NoAuth: &emptypb.Empty{}}
	case serverapi.AuthStatusMethodAPIKey:
		message.MethodFacts = &authpb.StatusFacts_ApiKey{ApiKey: &authpb.APIKeyFacts{
			Suffix: clonePointer(facts.APIKey.Suffix),
		}}
	case serverapi.AuthStatusMethodOAuth:
		message.MethodFacts = &authpb.StatusFacts_Oauth{Oauth: &authpb.OAuthFacts{
			AccountId: clonePointer(facts.OAuth.AccountID),
			Email:     clonePointer(facts.OAuth.Email),
		}}
	}
	return message, nil
}

func authStatusFactsFromProto(message *authpb.StatusFacts) (serverapi.AuthStatusFacts, error) {
	method, err := authMethodFromProto(message.Method)
	if err != nil {
		return serverapi.AuthStatusFacts{}, err
	}
	providerKind, err := authProviderKindFromProto(message.Provider.Kind)
	if err != nil {
		return serverapi.AuthStatusFacts{}, err
	}
	envPreference, err := authEnvPreferenceFromProto(message.EnvPreference)
	if err != nil {
		return serverapi.AuthStatusFacts{}, err
	}
	facts := serverapi.AuthStatusFacts{
		Method: method,
		Provider: serverapi.AuthProviderFacts{
			Kind:       providerKind,
			Identifier: message.Provider.Identifier,
		},
		EnvPreference: envPreference,
	}
	if message.Provider.DisplayOrigin != nil {
		facts.Provider.DisplayOrigin = &serverapi.AuthProviderDisplayOrigin{
			Scheme:   message.Provider.DisplayOrigin.Scheme,
			Hostname: message.Provider.DisplayOrigin.Hostname,
			Port:     clonePointer(message.Provider.DisplayOrigin.Port),
		}
	}
	switch methodFacts := message.MethodFacts.(type) {
	case *authpb.StatusFacts_NoAuth:
	case *authpb.StatusFacts_ApiKey:
		facts.APIKey = &serverapi.AuthAPIKeyFacts{Suffix: clonePointer(methodFacts.ApiKey.Suffix)}
	case *authpb.StatusFacts_Oauth:
		facts.OAuth = &serverapi.AuthOAuthFacts{
			AccountID: clonePointer(methodFacts.Oauth.AccountId),
			Email:     clonePointer(methodFacts.Oauth.Email),
		}
	default:
		return serverapi.AuthStatusFacts{}, fmt.Errorf("protobuf auth method facts have unsupported outcome %T", methodFacts)
	}
	return facts, nil
}

func authSubscriptionToProto(subscription serverapi.AuthSubscriptionFacts) (*authpb.SubscriptionFacts, error) {
	windows, err := mapSliceError(subscription.Windows, authSubscriptionWindowToProto)
	if err != nil {
		return nil, err
	}
	return &authpb.SubscriptionFacts{
		Applicable: subscription.Applicable,
		Plan:       clonePointer(subscription.Plan),
		Windows:    windows,
		Failure:    authStatusFailureToProto(subscription.Failure),
	}, nil
}

func authSubscriptionFromProto(message *authpb.SubscriptionFacts) (serverapi.AuthSubscriptionFacts, error) {
	windows, err := mapSliceError(message.Windows, authSubscriptionWindowFromProto)
	if err != nil {
		return serverapi.AuthSubscriptionFacts{}, err
	}
	return serverapi.AuthSubscriptionFacts{
		Applicable: message.Applicable,
		Plan:       clonePointer(message.Plan),
		Windows:    windows,
		Failure:    authStatusFailureFromProto(message.Failure),
	}, nil
}

func authSubscriptionWindowToProto(window serverapi.AuthSubscriptionWindowFacts) (*authpb.SubscriptionWindowFacts, error) {
	bucket, err := authSubscriptionBucketToProto(window.Bucket)
	if err != nil {
		return nil, err
	}
	duration, err := intToUint32(window.DurationSecs, "subscription duration_seconds")
	if err != nil {
		return nil, err
	}
	var resetAt *timestamppb.Timestamp
	if window.ResetAt != nil {
		resetAt = timestamppb.New(*window.ResetAt)
		if err := resetAt.CheckValid(); err != nil {
			return nil, fmt.Errorf("subscription reset_at: %w", err)
		}
	}
	return &authpb.SubscriptionWindowFacts{
		Bucket:          bucket,
		DurationSeconds: duration,
		UsedPercent:     window.UsedPercent,
		ResetAt:         resetAt,
		LimitName:       clonePointer(window.LimitName),
		MeteredFeature:  clonePointer(window.MeteredFeature),
	}, nil
}

func authSubscriptionWindowFromProto(message *authpb.SubscriptionWindowFacts) (serverapi.AuthSubscriptionWindowFacts, error) {
	bucket, err := authSubscriptionBucketFromProto(message.Bucket)
	if err != nil {
		return serverapi.AuthSubscriptionWindowFacts{}, err
	}
	duration, err := uint32ToInt(message.DurationSeconds, "subscription duration_seconds")
	if err != nil {
		return serverapi.AuthSubscriptionWindowFacts{}, err
	}
	var resetAt *time.Time
	if message.ResetAt != nil {
		if err := message.ResetAt.CheckValid(); err != nil {
			return serverapi.AuthSubscriptionWindowFacts{}, fmt.Errorf("protobuf subscription reset_at: %w", err)
		}
		value := message.ResetAt.AsTime()
		resetAt = &value
	}
	return serverapi.AuthSubscriptionWindowFacts{
		Bucket:         bucket,
		DurationSecs:   duration,
		UsedPercent:    message.UsedPercent,
		ResetAt:        resetAt,
		LimitName:      clonePointer(message.LimitName),
		MeteredFeature: clonePointer(message.MeteredFeature),
	}, nil
}

func authStatusFailureToProto(failure *serverapi.AuthStatusFailure) *authpb.StatusFailure {
	if failure == nil {
		return nil
	}
	return &authpb.StatusFailure{Cause: failure.Cause}
}

func authStatusFailureFromProto(failure *authpb.StatusFailure) *serverapi.AuthStatusFailure {
	if failure == nil {
		return nil
	}
	return &serverapi.AuthStatusFailure{Cause: failure.Cause}
}

func authMethodToProto(method serverapi.AuthStatusMethod) (authpb.AuthMethod, error) {
	switch method {
	case serverapi.AuthStatusMethodNone:
		return authpb.AuthMethod_AUTH_METHOD_NONE, nil
	case serverapi.AuthStatusMethodAPIKey:
		return authpb.AuthMethod_AUTH_METHOD_API_KEY, nil
	case serverapi.AuthStatusMethodOAuth:
		return authpb.AuthMethod_AUTH_METHOD_OAUTH, nil
	default:
		return authpb.AuthMethod_AUTH_METHOD_UNSPECIFIED, fmt.Errorf("auth method %q is unsupported", method)
	}
}

func authMethodFromProto(method authpb.AuthMethod) (serverapi.AuthStatusMethod, error) {
	switch method {
	case authpb.AuthMethod_AUTH_METHOD_NONE:
		return serverapi.AuthStatusMethodNone, nil
	case authpb.AuthMethod_AUTH_METHOD_API_KEY:
		return serverapi.AuthStatusMethodAPIKey, nil
	case authpb.AuthMethod_AUTH_METHOD_OAUTH:
		return serverapi.AuthStatusMethodOAuth, nil
	default:
		return "", fmt.Errorf("protobuf auth method %v is unsupported", method)
	}
}

func authEnvPreferenceToProto(preference serverapi.AuthStatusEnvPreference) (authpb.EnvironmentPreference, error) {
	switch preference {
	case serverapi.AuthStatusEnvPreferenceUnspecified:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_UNSPECIFIED, nil
	case serverapi.AuthStatusEnvPreferencePreferSaved:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH, nil
	case serverapi.AuthStatusEnvPreferencePreferEnv:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_ENV_API_KEY, nil
	default:
		return authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_UNSPECIFIED, fmt.Errorf("auth environment preference %q is unsupported", preference)
	}
}

func authEnvPreferenceFromProto(preference authpb.EnvironmentPreference) (serverapi.AuthStatusEnvPreference, error) {
	switch preference {
	case authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_UNSPECIFIED:
		return serverapi.AuthStatusEnvPreferenceUnspecified, nil
	case authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_SAVED_AUTH:
		return serverapi.AuthStatusEnvPreferencePreferSaved, nil
	case authpb.EnvironmentPreference_ENVIRONMENT_PREFERENCE_PREFER_ENV_API_KEY:
		return serverapi.AuthStatusEnvPreferencePreferEnv, nil
	default:
		return "", fmt.Errorf("protobuf auth environment preference %v is unsupported", preference)
	}
}

func authProviderKindToProto(kind serverapi.AuthProviderKind) (authpb.ProviderKind, error) {
	switch kind {
	case serverapi.AuthProviderKindOpenAI:
		return authpb.ProviderKind_PROVIDER_KIND_OPENAI, nil
	case serverapi.AuthProviderKindOpenAICompatible:
		return authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE, nil
	case serverapi.AuthProviderKindConfiguredProvider:
		return authpb.ProviderKind_PROVIDER_KIND_CONFIGURED_PROVIDER, nil
	default:
		return authpb.ProviderKind_PROVIDER_KIND_UNSPECIFIED, fmt.Errorf("auth provider kind %q is unsupported", kind)
	}
}

func authProviderKindFromProto(kind authpb.ProviderKind) (serverapi.AuthProviderKind, error) {
	switch kind {
	case authpb.ProviderKind_PROVIDER_KIND_OPENAI:
		return serverapi.AuthProviderKindOpenAI, nil
	case authpb.ProviderKind_PROVIDER_KIND_OPENAI_COMPATIBLE:
		return serverapi.AuthProviderKindOpenAICompatible, nil
	case authpb.ProviderKind_PROVIDER_KIND_CONFIGURED_PROVIDER:
		return serverapi.AuthProviderKindConfiguredProvider, nil
	default:
		return "", fmt.Errorf("protobuf auth provider kind %v is unsupported", kind)
	}
}

func authSubscriptionBucketToProto(bucket serverapi.AuthSubscriptionWindowBucket) (authpb.SubscriptionWindowBucket, error) {
	switch bucket {
	case serverapi.AuthSubscriptionWindowBucketDefault:
		return authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT, nil
	case serverapi.AuthSubscriptionWindowBucketAdditional:
		return authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL, nil
	default:
		return authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_UNSPECIFIED, fmt.Errorf("auth subscription bucket %q is unsupported", bucket)
	}
}

func authSubscriptionBucketFromProto(bucket authpb.SubscriptionWindowBucket) (serverapi.AuthSubscriptionWindowBucket, error) {
	switch bucket {
	case authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_DEFAULT:
		return serverapi.AuthSubscriptionWindowBucketDefault, nil
	case authpb.SubscriptionWindowBucket_SUBSCRIPTION_WINDOW_BUCKET_ADDITIONAL:
		return serverapi.AuthSubscriptionWindowBucketAdditional, nil
	default:
		return "", fmt.Errorf("protobuf auth subscription bucket %v is unsupported", bucket)
	}
}

func capabilityModelsToProto(facts serverapi.ModelCapabilityFacts) (*capabilitypb.ModelFacts, error) {
	knownModels, err := mapSliceError(facts.KnownModels, capabilityModelToProto)
	if err != nil {
		return nil, err
	}
	unknownFallback, err := capabilityModelToProto(facts.UnknownFallback)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ModelFacts{KnownModels: knownModels, UnknownFallback: unknownFallback}, nil
}

func capabilityModelsFromProto(message *capabilitypb.ModelFacts) (serverapi.ModelCapabilityFacts, error) {
	knownModels, err := mapSliceError(message.KnownModels, capabilityModelFromProto)
	if err != nil {
		return serverapi.ModelCapabilityFacts{}, err
	}
	unknownFallback, err := capabilityModelFromProto(message.UnknownFallback)
	if err != nil {
		return serverapi.ModelCapabilityFacts{}, err
	}
	return serverapi.ModelCapabilityFacts{KnownModels: knownModels, UnknownFallback: unknownFallback}, nil
}

func capabilityModelToProto(fact serverapi.ModelCapabilityFact) (*capabilitypb.ModelFact, error) {
	contextWindowTokens, err := optionalIntToUint32(fact.ContextWindowTokens, "model context_window_tokens")
	if err != nil {
		return nil, err
	}
	message := &capabilitypb.ModelFact{
		ModelId:                  clonePointer(fact.ModelID),
		Known:                    fact.Known,
		ContextWindowTokens:      contextWindowTokens,
		DefaultContextWindowMode: clonePointer(fact.DefaultContextWindowMode),
		SupportsThinking:         fact.SupportsThinking,
		SupportedThinkingLevels:  cloneSlice(fact.SupportedThinkingLevels),
		SupportsReasoningSummary: fact.SupportsReasoningSummary,
		SupportsVisionInputs:     fact.SupportsVisionInputs,
		Verbosity: &capabilitypb.ModelVerbosityFact{
			Supported: fact.Verbosity.Supported,
			Source:    fact.Verbosity.Source,
			Levels:    cloneSlice(fact.Verbosity.Levels),
		},
	}
	if fact.LargeWindow != nil {
		tokens, err := intToUint32(fact.LargeWindow.Tokens, "model large-window tokens")
		if err != nil {
			return nil, err
		}
		message.LargeWindow = &capabilitypb.ModelLargeWindowFact{Tokens: tokens}
	}
	return message, nil
}

func capabilityModelFromProto(message *capabilitypb.ModelFact) (serverapi.ModelCapabilityFact, error) {
	var largeWindow *serverapi.ModelLargeWindowFact
	if message.LargeWindow != nil {
		tokens, err := uint32ToInt(message.LargeWindow.Tokens, "model large-window tokens")
		if err != nil {
			return serverapi.ModelCapabilityFact{}, err
		}
		largeWindow = &serverapi.ModelLargeWindowFact{Tokens: tokens}
	}
	contextWindowTokens, err := optionalUint32ToInt(message.ContextWindowTokens, "model context_window_tokens")
	if err != nil {
		return serverapi.ModelCapabilityFact{}, err
	}
	return serverapi.ModelCapabilityFact{
		ModelID:                  clonePointer(message.ModelId),
		Known:                    message.Known,
		ContextWindowTokens:      contextWindowTokens,
		LargeWindow:              largeWindow,
		DefaultContextWindowMode: clonePointer(message.DefaultContextWindowMode),
		SupportsThinking:         message.SupportsThinking,
		SupportedThinkingLevels:  cloneSlice(message.SupportedThinkingLevels),
		SupportsReasoningSummary: message.SupportsReasoningSummary,
		SupportsVisionInputs:     message.SupportsVisionInputs,
		Verbosity: serverapi.ModelVerbosityFact{
			Supported: message.Verbosity.Supported,
			Source:    message.Verbosity.Source,
			Levels:    cloneSlice(message.Verbosity.Levels),
		},
	}, nil
}

func capabilityProvidersToProto(facts serverapi.ProviderCapabilityFacts) *capabilitypb.ProviderFacts {
	message := &capabilitypb.ProviderFacts{
		Explicit: mapSlice(facts.Explicit, capabilityProviderToProto),
	}
	if facts.CurrentEffective != nil {
		message.CurrentEffective = capabilityProviderToProto(*facts.CurrentEffective)
	}
	return message
}

func capabilityProvidersFromProto(message *capabilitypb.ProviderFacts) serverapi.ProviderCapabilityFacts {
	facts := serverapi.ProviderCapabilityFacts{
		Explicit: mapSlice(message.Explicit, capabilityProviderFromProto),
	}
	if message.CurrentEffective != nil {
		current := capabilityProviderFromProto(message.CurrentEffective)
		facts.CurrentEffective = &current
	}
	return facts
}

func capabilityProviderToProto(fact serverapi.LLMProviderCapabilityFact) *capabilitypb.ProviderFact {
	return &capabilitypb.ProviderFact{
		LlmProviderId:                 fact.LLMProviderID,
		Role:                          fact.Role,
		SupportsResponsesApi:          fact.SupportsResponsesAPI,
		SupportsNativeCompaction:      fact.SupportsNativeCompaction,
		SupportsPromptCacheKey:        fact.SupportsPromptCacheKey,
		SupportsNativeWebSearch:       fact.SupportsNativeWebSearch,
		SupportsReasoningEncryption:   fact.SupportsReasoningEncryption,
		SupportsServerSideContextEdit: fact.SupportsServerSideContextEdit,
		IsOpenaiFirstParty:            fact.IsOpenAIFirstParty,
		SupportsProviderVerbosity:     fact.SupportsProviderVerbosity,
	}
}

func capabilityProviderFromProto(message *capabilitypb.ProviderFact) serverapi.LLMProviderCapabilityFact {
	return serverapi.LLMProviderCapabilityFact{
		LLMProviderID:                 message.LlmProviderId,
		Role:                          message.Role,
		SupportsResponsesAPI:          message.SupportsResponsesApi,
		SupportsNativeCompaction:      message.SupportsNativeCompaction,
		SupportsPromptCacheKey:        message.SupportsPromptCacheKey,
		SupportsNativeWebSearch:       message.SupportsNativeWebSearch,
		SupportsReasoningEncryption:   message.SupportsReasoningEncryption,
		SupportsServerSideContextEdit: message.SupportsServerSideContextEdit,
		IsOpenAIFirstParty:            message.IsOpenaiFirstParty,
		SupportsProviderVerbosity:     message.SupportsProviderVerbosity,
	}
}

func capabilityImportsToProto(facts serverapi.ImportCapabilityFacts) (*capabilitypb.ImportFacts, error) {
	skills, err := capabilityImportGroupToProto(facts.Skills)
	if err != nil {
		return nil, err
	}
	commands, err := capabilityImportGroupToProto(facts.Commands)
	if err != nil {
		return nil, err
	}
	skillEnablement, err := mapSliceError(facts.SkillEnablement, capabilitySkillEnablementToProto)
	if err != nil {
		return nil, err
	}
	errors, err := mapSliceError(facts.Errors, capabilityImportErrorToProto)
	if err != nil {
		return nil, err
	}
	recommendations, err := capabilityRecommendationsToProto(facts.Recommendations)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportFacts{
		Workspace:       &capabilitypb.ImportWorkspaceFact{Root: clonePointer(facts.Workspace.Root)},
		Skills:          skills,
		Commands:        commands,
		SkillEnablement: skillEnablement,
		Errors:          errors,
		Recommendations: recommendations,
	}, nil
}

func capabilityImportsFromProto(message *capabilitypb.ImportFacts) (serverapi.ImportCapabilityFacts, error) {
	skills, err := capabilityImportGroupFromProto(message.Skills)
	if err != nil {
		return serverapi.ImportCapabilityFacts{}, err
	}
	commands, err := capabilityImportGroupFromProto(message.Commands)
	if err != nil {
		return serverapi.ImportCapabilityFacts{}, err
	}
	skillEnablement, err := mapSliceError(message.SkillEnablement, capabilitySkillEnablementFromProto)
	if err != nil {
		return serverapi.ImportCapabilityFacts{}, err
	}
	errors, err := mapSliceError(message.Errors, capabilityImportErrorFromProto)
	if err != nil {
		return serverapi.ImportCapabilityFacts{}, err
	}
	recommendations, err := capabilityRecommendationsFromProto(message.Recommendations)
	if err != nil {
		return serverapi.ImportCapabilityFacts{}, err
	}
	return serverapi.ImportCapabilityFacts{
		Workspace:       serverapi.ImportWorkspaceFact{Root: clonePointer(message.Workspace.Root)},
		Skills:          skills,
		Commands:        commands,
		SkillEnablement: skillEnablement,
		Errors:          errors,
		Recommendations: recommendations,
	}, nil
}

func capabilityImportGroupToProto(fact serverapi.ImportItemGroupFact) (*capabilitypb.ImportItemGroupFact, error) {
	choices, err := mapSliceError(fact.Choices, capabilityImportChoiceToProto)
	if err != nil {
		return nil, err
	}
	roots, err := mapSliceError(fact.Roots, capabilityImportRootToProto)
	if err != nil {
		return nil, err
	}
	items, err := mapSliceError(fact.Items, capabilityImportItemToProto)
	if err != nil {
		return nil, err
	}
	conflicts, err := mapSliceError(fact.Target.Conflicts, capabilityImportConflictToProto)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportItemGroupFact{
		Choices: choices,
		Roots:   roots,
		Items:   items,
		Target:  &capabilitypb.ImportTargetFact{Skip: fact.Target.Skip, Conflicts: conflicts},
	}, nil
}

func capabilityImportGroupFromProto(message *capabilitypb.ImportItemGroupFact) (serverapi.ImportItemGroupFact, error) {
	choices, err := mapSliceError(message.Choices, capabilityImportChoiceFromProto)
	if err != nil {
		return serverapi.ImportItemGroupFact{}, err
	}
	roots, err := mapSliceError(message.Roots, capabilityImportRootFromProto)
	if err != nil {
		return serverapi.ImportItemGroupFact{}, err
	}
	items, err := mapSliceError(message.Items, capabilityImportItemFromProto)
	if err != nil {
		return serverapi.ImportItemGroupFact{}, err
	}
	conflicts, err := mapSliceError(message.Target.Conflicts, capabilityImportConflictFromProto)
	if err != nil {
		return serverapi.ImportItemGroupFact{}, err
	}
	return serverapi.ImportItemGroupFact{
		Choices: choices,
		Roots:   roots,
		Items:   items,
		Target:  serverapi.ImportTargetFact{Skip: message.Target.Skip, Conflicts: conflicts},
	}, nil
}

func capabilityImportChoiceToProto(fact serverapi.ImportChoiceFact) (*capabilitypb.ImportChoiceFact, error) {
	ref, err := capabilityImportChoiceRefToProto(fact.Ref)
	if err != nil {
		return nil, err
	}
	itemCount, err := intToUint32(fact.ItemCount, "import choice item_count")
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportChoiceFact{
		Ref:              ref,
		ImportProviderId: clonePointer(fact.ImportProviderID),
		SourceRootPath:   clonePointer(fact.SourceRootPath),
		ItemCount:        itemCount,
	}, nil
}

func capabilityImportChoiceFromProto(message *capabilitypb.ImportChoiceFact) (serverapi.ImportChoiceFact, error) {
	ref, err := capabilityImportChoiceRefFromProto(message.Ref)
	if err != nil {
		return serverapi.ImportChoiceFact{}, err
	}
	itemCount, err := uint32ToInt(message.ItemCount, "import choice item_count")
	if err != nil {
		return serverapi.ImportChoiceFact{}, err
	}
	return serverapi.ImportChoiceFact{
		Ref:              ref,
		ImportProviderID: clonePointer(message.ImportProviderId),
		SourceRootPath:   clonePointer(message.SourceRootPath),
		ItemCount:        itemCount,
	}, nil
}

func capabilityImportRootToProto(fact serverapi.ImportRootFact) (*capabilitypb.ImportRootFact, error) {
	sourceKind, err := capabilitySourceKindToProto(fact.SourceKind)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportRootFact{
		SourceKind:       sourceKind,
		ImportProviderId: clonePointer(fact.ImportProviderID),
		Path:             fact.Path,
		Exists:           fact.Exists,
	}, nil
}

func capabilityImportRootFromProto(message *capabilitypb.ImportRootFact) (serverapi.ImportRootFact, error) {
	sourceKind, err := capabilitySourceKindFromProto(message.SourceKind)
	if err != nil {
		return serverapi.ImportRootFact{}, err
	}
	return serverapi.ImportRootFact{
		SourceKind:       sourceKind,
		ImportProviderID: clonePointer(message.ImportProviderId),
		Path:             message.Path,
		Exists:           message.Exists,
	}, nil
}

func capabilityImportItemToProto(fact serverapi.ImportItemFact) (*capabilitypb.ImportItemFact, error) {
	ref, err := capabilityImportItemRefToProto(fact.Ref)
	if err != nil {
		return nil, err
	}
	conflicts, err := mapSliceError(fact.Conflicts, capabilityImportConflictToProto)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportItemFact{
		Ref:            ref,
		Conflicts:      conflicts,
		DefaultEnabled: clonePointer(fact.DefaultEnabled),
	}, nil
}

func capabilityImportItemFromProto(message *capabilitypb.ImportItemFact) (serverapi.ImportItemFact, error) {
	ref, err := capabilityImportItemRefFromProto(message.Ref)
	if err != nil {
		return serverapi.ImportItemFact{}, err
	}
	conflicts, err := mapSliceError(message.Conflicts, capabilityImportConflictFromProto)
	if err != nil {
		return serverapi.ImportItemFact{}, err
	}
	return serverapi.ImportItemFact{
		Ref:            ref,
		Conflicts:      conflicts,
		DefaultEnabled: clonePointer(message.DefaultEnabled),
	}, nil
}

func capabilityImportItemRefToProto(ref serverapi.ImportItemRef) (*capabilitypb.ImportItemRef, error) {
	itemKind, err := capabilityItemKindToProto(ref.ItemKind)
	if err != nil {
		return nil, err
	}
	sourceKind, err := capabilitySourceKindToProto(ref.SourceKind)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportItemRef{
		ItemKind:         itemKind,
		SourceKind:       sourceKind,
		ImportProviderId: clonePointer(ref.ImportProviderID),
		SourceRootPath:   clonePointer(ref.SourceRootPath),
		SourcePath:       clonePointer(ref.SourcePath),
		TargetName:       ref.TargetName,
		Name:             clonePointer(ref.Name),
		ModifiedUnixMs:   clonePointer(ref.ModifiedUnixMs),
	}, nil
}

func capabilityImportItemRefFromProto(message *capabilitypb.ImportItemRef) (serverapi.ImportItemRef, error) {
	itemKind, err := capabilityItemKindFromProto(message.ItemKind)
	if err != nil {
		return serverapi.ImportItemRef{}, err
	}
	sourceKind, err := capabilitySourceKindFromProto(message.SourceKind)
	if err != nil {
		return serverapi.ImportItemRef{}, err
	}
	return serverapi.ImportItemRef{
		ItemKind:         itemKind,
		SourceKind:       sourceKind,
		ImportProviderID: clonePointer(message.ImportProviderId),
		SourceRootPath:   clonePointer(message.SourceRootPath),
		SourcePath:       clonePointer(message.SourcePath),
		TargetName:       message.TargetName,
		Name:             clonePointer(message.Name),
		ModifiedUnixMs:   clonePointer(message.ModifiedUnixMs),
	}, nil
}

func capabilityImportConflictToProto(fact serverapi.ImportConflictFact) (*capabilitypb.ImportConflictFact, error) {
	sourceKind, err := capabilitySourceKindToProto(fact.SourceKind)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportConflictFact{
		SourceKind:       sourceKind,
		ImportProviderId: clonePointer(fact.ImportProviderID),
		Path:             clonePointer(fact.Path),
	}, nil
}

func capabilityImportConflictFromProto(message *capabilitypb.ImportConflictFact) (serverapi.ImportConflictFact, error) {
	sourceKind, err := capabilitySourceKindFromProto(message.SourceKind)
	if err != nil {
		return serverapi.ImportConflictFact{}, err
	}
	return serverapi.ImportConflictFact{
		SourceKind:       sourceKind,
		ImportProviderID: clonePointer(message.ImportProviderId),
		Path:             clonePointer(message.Path),
	}, nil
}

func capabilitySkillEnablementToProto(fact serverapi.SkillEnablementProjectionFact) (*capabilitypb.SkillEnablementProjectionFact, error) {
	ref, err := capabilityImportChoiceRefToProto(fact.ChoiceRef)
	if err != nil {
		return nil, err
	}
	candidates, err := mapSliceError(fact.Candidates, capabilityImportItemToProto)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.SkillEnablementProjectionFact{ChoiceRef: ref, Candidates: candidates}, nil
}

func capabilitySkillEnablementFromProto(message *capabilitypb.SkillEnablementProjectionFact) (serverapi.SkillEnablementProjectionFact, error) {
	ref, err := capabilityImportChoiceRefFromProto(message.ChoiceRef)
	if err != nil {
		return serverapi.SkillEnablementProjectionFact{}, err
	}
	candidates, err := mapSliceError(message.Candidates, capabilityImportItemFromProto)
	if err != nil {
		return serverapi.SkillEnablementProjectionFact{}, err
	}
	return serverapi.SkillEnablementProjectionFact{ChoiceRef: ref, Candidates: candidates}, nil
}

func capabilityImportErrorToProto(fact serverapi.ImportErrorFact) (*capabilitypb.ImportErrorFact, error) {
	itemKind, err := optionalEnumToProto(fact.ItemKind, capabilityErrorItemKindToProto)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportErrorFact{
		Code:             fact.Code,
		Scope:            fact.Scope,
		ItemKind:         itemKind,
		ImportProviderId: clonePointer(fact.ImportProviderID),
		Path:             clonePointer(fact.Path),
		Operation:        fact.Operation,
		Message:          fact.Message,
	}, nil
}

func capabilityImportErrorFromProto(message *capabilitypb.ImportErrorFact) (serverapi.ImportErrorFact, error) {
	itemKind, err := optionalEnumFromProto(message.ItemKind, capabilityErrorItemKindFromProto)
	if err != nil {
		return serverapi.ImportErrorFact{}, err
	}
	return serverapi.ImportErrorFact{
		Code:             message.Code,
		Scope:            message.Scope,
		ItemKind:         itemKind,
		ImportProviderID: clonePointer(message.ImportProviderId),
		Path:             clonePointer(message.Path),
		Operation:        message.Operation,
		Message:          message.Message,
	}, nil
}

func capabilityRecommendationsToProto(facts serverapi.ImportRecommendationFacts) (*capabilitypb.ImportRecommendationFacts, error) {
	skills, err := capabilityRecommendationToProto(facts.Skills)
	if err != nil {
		return nil, err
	}
	commands, err := capabilityRecommendationToProto(facts.Commands)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportRecommendationFacts{Skills: skills, Commands: commands}, nil
}

func capabilityRecommendationsFromProto(message *capabilitypb.ImportRecommendationFacts) (serverapi.ImportRecommendationFacts, error) {
	skills, err := capabilityRecommendationFromProto(message.Skills)
	if err != nil {
		return serverapi.ImportRecommendationFacts{}, err
	}
	commands, err := capabilityRecommendationFromProto(message.Commands)
	if err != nil {
		return serverapi.ImportRecommendationFacts{}, err
	}
	return serverapi.ImportRecommendationFacts{Skills: skills, Commands: commands}, nil
}

func capabilityRecommendationToProto(fact *serverapi.ImportModeRecommendationFact) (*capabilitypb.ImportModeRecommendationFact, error) {
	if fact == nil {
		return nil, nil
	}
	ref, err := capabilityImportChoiceRefToProto(fact.ChoiceRef)
	if err != nil {
		return nil, err
	}
	itemCount, err := intToUint32(fact.ItemCount, "import recommendation item_count")
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportModeRecommendationFact{
		ChoiceRef:   ref,
		ItemCount:   itemCount,
		SourcePaths: cloneSlice(fact.SourcePaths),
	}, nil
}

func capabilityRecommendationFromProto(message *capabilitypb.ImportModeRecommendationFact) (*serverapi.ImportModeRecommendationFact, error) {
	if message == nil {
		return nil, nil
	}
	ref, err := capabilityImportChoiceRefFromProto(message.ChoiceRef)
	if err != nil {
		return nil, err
	}
	itemCount, err := uint32ToInt(message.ItemCount, "import recommendation item_count")
	if err != nil {
		return nil, err
	}
	return &serverapi.ImportModeRecommendationFact{
		ChoiceRef:   ref,
		ItemCount:   itemCount,
		SourcePaths: cloneSlice(message.SourcePaths),
	}, nil
}

func capabilityImportChoiceRefToProto(ref serverapi.ImportChoiceRef) (*capabilitypb.ImportChoiceRef, error) {
	mode, err := capabilityChoiceModeToProto(ref.Mode)
	if err != nil {
		return nil, err
	}
	sourceKind, err := optionalEnumToProto(ref.SourceKind, capabilitySourceKindToProto)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.ImportChoiceRef{
		Mode:             mode,
		SourceKind:       sourceKind,
		ImportProviderId: clonePointer(ref.ImportProviderID),
		SourceRootPath:   clonePointer(ref.SourceRootPath),
	}, nil
}

func capabilityImportChoiceRefFromProto(message *capabilitypb.ImportChoiceRef) (serverapi.ImportChoiceRef, error) {
	mode, err := capabilityChoiceModeFromProto(message.Mode)
	if err != nil {
		return serverapi.ImportChoiceRef{}, err
	}
	sourceKind, err := optionalEnumFromProto(message.SourceKind, capabilitySourceKindFromProto)
	if err != nil {
		return serverapi.ImportChoiceRef{}, err
	}
	return serverapi.ImportChoiceRef{
		Mode:             mode,
		SourceKind:       sourceKind,
		ImportProviderID: clonePointer(message.ImportProviderId),
		SourceRootPath:   clonePointer(message.SourceRootPath),
	}, nil
}

func capabilityDefaultsToProto(facts serverapi.CapabilityDefaultFacts) *capabilitypb.DefaultFacts {
	message := &capabilitypb.DefaultFacts{
		PrimaryModelId: facts.PrimaryModelID,
		Thinking: &capabilitypb.ThinkingDefaultFact{
			Mode:  facts.Thinking.Mode,
			Level: clonePointer(facts.Thinking.Level),
			Value: clonePointer(facts.Thinking.Value),
		},
		CompactionMode: facts.CompactionMode,
	}
	if facts.Verbosity != nil {
		message.Verbosity = &capabilitypb.VerbosityDefaultFact{Level: facts.Verbosity.Level}
	}
	return message
}

func capabilityDefaultsFromProto(message *capabilitypb.DefaultFacts) serverapi.CapabilityDefaultFacts {
	facts := serverapi.CapabilityDefaultFacts{
		PrimaryModelID: message.PrimaryModelId,
		Thinking: serverapi.ThinkingDefaultFact{
			Mode:  message.Thinking.Mode,
			Level: clonePointer(message.Thinking.Level),
			Value: clonePointer(message.Thinking.Value),
		},
		CompactionMode: message.CompactionMode,
	}
	if message.Verbosity != nil {
		facts.Verbosity = &serverapi.VerbosityDefaultFact{Level: message.Verbosity.Level}
	}
	return facts
}

func capabilityChoiceModeToProto(mode string) (capabilitypb.ImportChoiceMode, error) {
	switch mode {
	case "none":
		return capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_NONE, nil
	case "symlink_source":
		return capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_SYMLINK_SOURCE, nil
	default:
		return capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_UNSPECIFIED, fmt.Errorf("capability import choice mode %q is unsupported", mode)
	}
}

func capabilityChoiceModeFromProto(mode capabilitypb.ImportChoiceMode) (string, error) {
	switch mode {
	case capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_NONE:
		return "none", nil
	case capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_SYMLINK_SOURCE:
		return "symlink_source", nil
	default:
		return "", fmt.Errorf("protobuf capability import choice mode %v is unsupported", mode)
	}
}

func capabilitySourceKindToProto(kind string) (capabilitypb.ImportSourceKind, error) {
	switch kind {
	case "external_provider":
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_EXTERNAL_PROVIDER, nil
	case "generated":
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GENERATED, nil
	case "global":
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GLOBAL, nil
	case "workspace":
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_WORKSPACE, nil
	default:
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_UNSPECIFIED, fmt.Errorf("capability import source kind %q is unsupported", kind)
	}
}

func capabilitySourceKindFromProto(kind capabilitypb.ImportSourceKind) (string, error) {
	switch kind {
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_EXTERNAL_PROVIDER:
		return "external_provider", nil
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GENERATED:
		return "generated", nil
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GLOBAL:
		return "global", nil
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_WORKSPACE:
		return "workspace", nil
	default:
		return "", fmt.Errorf("protobuf capability import source kind %v is unsupported", kind)
	}
}

func capabilityItemKindToProto(kind string) (capabilitypb.ImportItemKind, error) {
	switch kind {
	case "skill":
		return capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_SKILL, nil
	case "command":
		return capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_COMMAND, nil
	default:
		return capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_UNSPECIFIED, fmt.Errorf("capability import item kind %q is unsupported", kind)
	}
}

func capabilityItemKindFromProto(kind capabilitypb.ImportItemKind) (string, error) {
	switch kind {
	case capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_SKILL:
		return "skill", nil
	case capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_COMMAND:
		return "command", nil
	default:
		return "", fmt.Errorf("protobuf capability import item kind %v is unsupported", kind)
	}
}

func capabilityErrorItemKindToProto(kind serverapi.ImportErrorItemKind) (capabilitypb.ImportItemKind, error) {
	itemKind, err := capabilityItemKindToProto(string(kind))
	return itemKind, err
}

func capabilityErrorItemKindFromProto(kind capabilitypb.ImportItemKind) (serverapi.ImportErrorItemKind, error) {
	itemKind, err := capabilityItemKindFromProto(kind)
	return serverapi.ImportErrorItemKind(itemKind), err
}

func onboardingProviderToProto(choice *serverapi.OnboardingProviderChoice) *onboardingpb.ProviderChoice {
	if choice == nil {
		return nil
	}
	return &onboardingpb.ProviderChoice{
		ProviderOverride: clonePointer(choice.ProviderOverride),
		OpenaiBaseUrl:    clonePointer(choice.OpenAIBaseURL),
	}
}

func onboardingProviderFromProto(message *onboardingpb.ProviderChoice) *serverapi.OnboardingProviderChoice {
	if message == nil {
		return nil
	}
	return &serverapi.OnboardingProviderChoice{
		ProviderOverride: clonePointer(message.ProviderOverride),
		OpenAIBaseURL:    clonePointer(message.OpenaiBaseUrl),
	}
}

func onboardingModelToProto(choice *serverapi.OnboardingModelChoice) (*onboardingpb.ModelChoice, error) {
	if choice == nil {
		return nil, nil
	}
	kind, err := onboardingModelKindToProto(choice.Kind)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.ModelChoice{
		Kind:    kind,
		ModelId: optionalString(choice.ModelID),
		Alias:   optionalString(choice.Alias),
	}, nil
}

func onboardingModelFromProto(message *onboardingpb.ModelChoice) (*serverapi.OnboardingModelChoice, error) {
	if message == nil {
		return nil, nil
	}
	kind, err := onboardingModelKindFromProto(message.Kind)
	if err != nil {
		return nil, err
	}
	return &serverapi.OnboardingModelChoice{
		Kind:    kind,
		ModelID: dereference(message.ModelId),
		Alias:   dereference(message.Alias),
	}, nil
}

func onboardingContextWindowToProto(choice *serverapi.OnboardingContextWindowChoice) (*onboardingpb.ContextWindowChoice, error) {
	if choice == nil {
		return nil, nil
	}
	kind, err := onboardingContextWindowKindToProto(choice.Kind)
	if err != nil {
		return nil, err
	}
	var tokens *uint32
	if choice.Tokens != 0 {
		value, err := intToUint32(choice.Tokens, "onboarding context-window tokens")
		if err != nil {
			return nil, err
		}
		tokens = &value
	}
	return &onboardingpb.ContextWindowChoice{Kind: kind, Tokens: tokens}, nil
}

func onboardingContextWindowFromProto(message *onboardingpb.ContextWindowChoice) (*serverapi.OnboardingContextWindowChoice, error) {
	if message == nil {
		return nil, nil
	}
	kind, err := onboardingContextWindowKindFromProto(message.Kind)
	if err != nil {
		return nil, err
	}
	tokens, err := uint32ToInt(dereference(message.Tokens), "onboarding context-window tokens")
	if err != nil {
		return nil, err
	}
	return &serverapi.OnboardingContextWindowChoice{
		Kind:   kind,
		Tokens: tokens,
	}, nil
}

func onboardingThinkingToProto(choice *serverapi.OnboardingThinkingChoice) (*onboardingpb.ThinkingChoice, error) {
	if choice == nil {
		return nil, nil
	}
	kind, err := onboardingThinkingKindToProto(choice.Kind)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.ThinkingChoice{
		Kind:  kind,
		Level: optionalString(choice.Level),
		Value: optionalString(choice.Value),
	}, nil
}

func onboardingThinkingFromProto(message *onboardingpb.ThinkingChoice) (*serverapi.OnboardingThinkingChoice, error) {
	if message == nil {
		return nil, nil
	}
	kind, err := onboardingThinkingKindFromProto(message.Kind)
	if err != nil {
		return nil, err
	}
	return &serverapi.OnboardingThinkingChoice{
		Kind:  kind,
		Level: dereference(message.Level),
		Value: dereference(message.Value),
	}, nil
}

func onboardingSupervisorToProto(choice *serverapi.OnboardingSupervisorChoice) (*onboardingpb.SupervisorChoice, error) {
	if choice == nil {
		return nil, nil
	}
	frequency, err := onboardingSupervisorFrequencyToProto(choice.Frequency)
	if err != nil {
		return nil, err
	}
	model, err := onboardingModelToProto(choice.Model)
	if err != nil {
		return nil, err
	}
	thinking, err := onboardingThinkingToProto(choice.Thinking)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.SupervisorChoice{Frequency: frequency, Model: model, Thinking: thinking}, nil
}

func onboardingSupervisorFromProto(message *onboardingpb.SupervisorChoice) (*serverapi.OnboardingSupervisorChoice, error) {
	if message == nil {
		return nil, nil
	}
	frequency, err := onboardingSupervisorFrequencyFromProto(message.Frequency)
	if err != nil {
		return nil, err
	}
	model, err := onboardingModelFromProto(message.Model)
	if err != nil {
		return nil, err
	}
	thinking, err := onboardingThinkingFromProto(message.Thinking)
	if err != nil {
		return nil, err
	}
	return &serverapi.OnboardingSupervisorChoice{Frequency: frequency, Model: model, Thinking: thinking}, nil
}

func onboardingToolOverrideToProto(override serverapi.OnboardingToolOverride) (*onboardingpb.ToolOverride, error) {
	id, err := onboardingToolIDToProto(override.ID)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.ToolOverride{Id: id, Enabled: override.Enabled}, nil
}

func onboardingToolOverrideFromProto(message *onboardingpb.ToolOverride) (serverapi.OnboardingToolOverride, error) {
	id, err := onboardingToolIDFromProto(message.Id)
	if err != nil {
		return serverapi.OnboardingToolOverride{}, err
	}
	return serverapi.OnboardingToolOverride{ID: id, Enabled: message.Enabled}, nil
}

func onboardingImportToProto(selection *serverapi.OnboardingImportSelection) (*onboardingpb.ImportSelection, error) {
	if selection == nil {
		return nil, nil
	}
	mode, err := onboardingImportModeToProto(selection.Mode)
	if err != nil {
		return nil, err
	}
	var providerUUID *string
	if selection.ProviderUUID != nil {
		value := selection.ProviderUUID.String()
		providerUUID = &value
	}
	return &onboardingpb.ImportSelection{
		Mode:             mode,
		ProviderUuid:     providerUUID,
		ImportProviderId: clonePointer(selection.ImportProviderID),
		SourceRootPath:   clonePointer(selection.SourceRootPath),
	}, nil
}

func onboardingImportFromProto(message *onboardingpb.ImportSelection) (*serverapi.OnboardingImportSelection, error) {
	if message == nil {
		return nil, nil
	}
	mode, err := onboardingImportModeFromProto(message.Mode)
	if err != nil {
		return nil, err
	}
	selection := &serverapi.OnboardingImportSelection{
		Mode:             mode,
		ImportProviderID: clonePointer(message.ImportProviderId),
		SourceRootPath:   clonePointer(message.SourceRootPath),
	}
	if message.ProviderUuid != nil {
		parsed, err := uuid.Parse(*message.ProviderUuid)
		if err != nil {
			return nil, fmt.Errorf("onboarding import provider UUID: %w", err)
		}
		selection.ProviderUUID = &parsed
	}
	return selection, nil
}

func onboardingThemeToProto(theme serverapi.OnboardingTheme) (onboardingpb.Theme, error) {
	switch theme {
	case serverapi.OnboardingThemeAuto:
		return onboardingpb.Theme_THEME_AUTO, nil
	case serverapi.OnboardingThemeLight:
		return onboardingpb.Theme_THEME_LIGHT, nil
	case serverapi.OnboardingThemeDark:
		return onboardingpb.Theme_THEME_DARK, nil
	default:
		return onboardingpb.Theme_THEME_UNSPECIFIED, fmt.Errorf("onboarding theme %q is unsupported", theme)
	}
}

func onboardingThemeFromProto(theme onboardingpb.Theme) (serverapi.OnboardingTheme, error) {
	switch theme {
	case onboardingpb.Theme_THEME_AUTO:
		return serverapi.OnboardingThemeAuto, nil
	case onboardingpb.Theme_THEME_LIGHT:
		return serverapi.OnboardingThemeLight, nil
	case onboardingpb.Theme_THEME_DARK:
		return serverapi.OnboardingThemeDark, nil
	default:
		return "", fmt.Errorf("protobuf onboarding theme %v is unsupported", theme)
	}
}

func onboardingModelKindToProto(kind serverapi.OnboardingModelKind) (onboardingpb.ModelKind, error) {
	switch kind {
	case serverapi.OnboardingModelKnown:
		return onboardingpb.ModelKind_MODEL_KIND_KNOWN, nil
	case serverapi.OnboardingModelCustom:
		return onboardingpb.ModelKind_MODEL_KIND_CUSTOM, nil
	default:
		return onboardingpb.ModelKind_MODEL_KIND_UNSPECIFIED, fmt.Errorf("onboarding model kind %q is unsupported", kind)
	}
}

func onboardingModelKindFromProto(kind onboardingpb.ModelKind) (serverapi.OnboardingModelKind, error) {
	switch kind {
	case onboardingpb.ModelKind_MODEL_KIND_KNOWN:
		return serverapi.OnboardingModelKnown, nil
	case onboardingpb.ModelKind_MODEL_KIND_CUSTOM:
		return serverapi.OnboardingModelCustom, nil
	default:
		return "", fmt.Errorf("protobuf onboarding model kind %v is unsupported", kind)
	}
}

func onboardingContextWindowKindToProto(kind serverapi.OnboardingContextWindowKind) (onboardingpb.ContextWindowKind, error) {
	switch kind {
	case serverapi.OnboardingContextWindowDefault:
		return onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_DEFAULT, nil
	case serverapi.OnboardingContextWindowLarge:
		return onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_LARGE, nil
	case serverapi.OnboardingContextWindowCustom:
		return onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_CUSTOM, nil
	default:
		return onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_UNSPECIFIED, fmt.Errorf("onboarding context-window kind %q is unsupported", kind)
	}
}

func onboardingContextWindowKindFromProto(kind onboardingpb.ContextWindowKind) (serverapi.OnboardingContextWindowKind, error) {
	switch kind {
	case onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_DEFAULT:
		return serverapi.OnboardingContextWindowDefault, nil
	case onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_LARGE:
		return serverapi.OnboardingContextWindowLarge, nil
	case onboardingpb.ContextWindowKind_CONTEXT_WINDOW_KIND_CUSTOM:
		return serverapi.OnboardingContextWindowCustom, nil
	default:
		return "", fmt.Errorf("protobuf onboarding context-window kind %v is unsupported", kind)
	}
}

func onboardingThinkingKindToProto(kind serverapi.OnboardingThinkingKind) (onboardingpb.ThinkingKind, error) {
	switch kind {
	case serverapi.OnboardingThinkingDefault:
		return onboardingpb.ThinkingKind_THINKING_KIND_DEFAULT, nil
	case serverapi.OnboardingThinkingDisabled:
		return onboardingpb.ThinkingKind_THINKING_KIND_DISABLED, nil
	case serverapi.OnboardingThinkingLevel:
		return onboardingpb.ThinkingKind_THINKING_KIND_LEVEL, nil
	case serverapi.OnboardingThinkingCustom:
		return onboardingpb.ThinkingKind_THINKING_KIND_CUSTOM, nil
	default:
		return onboardingpb.ThinkingKind_THINKING_KIND_UNSPECIFIED, fmt.Errorf("onboarding thinking kind %q is unsupported", kind)
	}
}

func onboardingThinkingKindFromProto(kind onboardingpb.ThinkingKind) (serverapi.OnboardingThinkingKind, error) {
	switch kind {
	case onboardingpb.ThinkingKind_THINKING_KIND_DEFAULT:
		return serverapi.OnboardingThinkingDefault, nil
	case onboardingpb.ThinkingKind_THINKING_KIND_DISABLED:
		return serverapi.OnboardingThinkingDisabled, nil
	case onboardingpb.ThinkingKind_THINKING_KIND_LEVEL:
		return serverapi.OnboardingThinkingLevel, nil
	case onboardingpb.ThinkingKind_THINKING_KIND_CUSTOM:
		return serverapi.OnboardingThinkingCustom, nil
	default:
		return "", fmt.Errorf("protobuf onboarding thinking kind %v is unsupported", kind)
	}
}

func onboardingVerbosityToProto(verbosity serverapi.OnboardingVerbosity) (onboardingpb.Verbosity, error) {
	switch verbosity {
	case serverapi.OnboardingVerbosityLow:
		return onboardingpb.Verbosity_VERBOSITY_LOW, nil
	case serverapi.OnboardingVerbosityMedium:
		return onboardingpb.Verbosity_VERBOSITY_MEDIUM, nil
	case serverapi.OnboardingVerbosityHigh:
		return onboardingpb.Verbosity_VERBOSITY_HIGH, nil
	default:
		return onboardingpb.Verbosity_VERBOSITY_UNSPECIFIED, fmt.Errorf("onboarding verbosity %q is unsupported", verbosity)
	}
}

func onboardingVerbosityFromProto(verbosity onboardingpb.Verbosity) (serverapi.OnboardingVerbosity, error) {
	switch verbosity {
	case onboardingpb.Verbosity_VERBOSITY_LOW:
		return serverapi.OnboardingVerbosityLow, nil
	case onboardingpb.Verbosity_VERBOSITY_MEDIUM:
		return serverapi.OnboardingVerbosityMedium, nil
	case onboardingpb.Verbosity_VERBOSITY_HIGH:
		return serverapi.OnboardingVerbosityHigh, nil
	default:
		return "", fmt.Errorf("protobuf onboarding verbosity %v is unsupported", verbosity)
	}
}

func onboardingSupervisorFrequencyToProto(frequency serverapi.OnboardingSupervisorFrequency) (onboardingpb.SupervisorFrequency, error) {
	switch frequency {
	case serverapi.OnboardingSupervisorOff:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_OFF, nil
	case serverapi.OnboardingSupervisorEdits:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_EDITS, nil
	case serverapi.OnboardingSupervisorAll:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_ALL, nil
	default:
		return onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_UNSPECIFIED, fmt.Errorf("onboarding supervisor frequency %q is unsupported", frequency)
	}
}

func onboardingSupervisorFrequencyFromProto(frequency onboardingpb.SupervisorFrequency) (serverapi.OnboardingSupervisorFrequency, error) {
	switch frequency {
	case onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_OFF:
		return serverapi.OnboardingSupervisorOff, nil
	case onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_EDITS:
		return serverapi.OnboardingSupervisorEdits, nil
	case onboardingpb.SupervisorFrequency_SUPERVISOR_FREQUENCY_ALL:
		return serverapi.OnboardingSupervisorAll, nil
	default:
		return "", fmt.Errorf("protobuf onboarding supervisor frequency %v is unsupported", frequency)
	}
}

func onboardingCompactionToProto(mode serverapi.OnboardingCompactionMode) (onboardingpb.CompactionMode, error) {
	switch mode {
	case serverapi.OnboardingCompactionNative:
		return onboardingpb.CompactionMode_COMPACTION_MODE_NATIVE, nil
	case serverapi.OnboardingCompactionLocal:
		return onboardingpb.CompactionMode_COMPACTION_MODE_LOCAL, nil
	case serverapi.OnboardingCompactionNone:
		return onboardingpb.CompactionMode_COMPACTION_MODE_NONE, nil
	default:
		return onboardingpb.CompactionMode_COMPACTION_MODE_UNSPECIFIED, fmt.Errorf("onboarding compaction mode %q is unsupported", mode)
	}
}

func onboardingCompactionFromProto(mode onboardingpb.CompactionMode) (serverapi.OnboardingCompactionMode, error) {
	switch mode {
	case onboardingpb.CompactionMode_COMPACTION_MODE_NATIVE:
		return serverapi.OnboardingCompactionNative, nil
	case onboardingpb.CompactionMode_COMPACTION_MODE_LOCAL:
		return serverapi.OnboardingCompactionLocal, nil
	case onboardingpb.CompactionMode_COMPACTION_MODE_NONE:
		return serverapi.OnboardingCompactionNone, nil
	default:
		return "", fmt.Errorf("protobuf onboarding compaction mode %v is unsupported", mode)
	}
}

func onboardingImportModeToProto(mode serverapi.OnboardingImportMode) (onboardingpb.ImportMode, error) {
	switch mode {
	case serverapi.OnboardingImportModeNone:
		return onboardingpb.ImportMode_IMPORT_MODE_NONE, nil
	case serverapi.OnboardingImportModeSymlinkSource:
		return onboardingpb.ImportMode_IMPORT_MODE_SYMLINK_SOURCE, nil
	default:
		return onboardingpb.ImportMode_IMPORT_MODE_UNSPECIFIED, fmt.Errorf("onboarding import mode %q is unsupported", mode)
	}
}

func onboardingImportModeFromProto(mode onboardingpb.ImportMode) (serverapi.OnboardingImportMode, error) {
	switch mode {
	case onboardingpb.ImportMode_IMPORT_MODE_NONE:
		return serverapi.OnboardingImportModeNone, nil
	case onboardingpb.ImportMode_IMPORT_MODE_SYMLINK_SOURCE:
		return serverapi.OnboardingImportModeSymlinkSource, nil
	default:
		return "", fmt.Errorf("protobuf onboarding import mode %v is unsupported", mode)
	}
}

func onboardingToolIDToProto(id toolspec.ID) (onboardingpb.ToolID, error) {
	switch id {
	case toolspec.ToolExecCommand:
		return onboardingpb.ToolID_TOOL_ID_EXEC_COMMAND, nil
	case toolspec.ToolWriteStdin:
		return onboardingpb.ToolID_TOOL_ID_WRITE_STDIN, nil
	case toolspec.ToolViewImage:
		return onboardingpb.ToolID_TOOL_ID_VIEW_IMAGE, nil
	case toolspec.ToolPatch:
		return onboardingpb.ToolID_TOOL_ID_PATCH, nil
	case toolspec.ToolEdit:
		return onboardingpb.ToolID_TOOL_ID_EDIT, nil
	case toolspec.ToolAskQuestion:
		return onboardingpb.ToolID_TOOL_ID_ASK_QUESTION, nil
	case toolspec.ToolCompleteNode:
		return onboardingpb.ToolID_TOOL_ID_COMPLETE_NODE, nil
	case toolspec.ToolTriggerHandoff:
		return onboardingpb.ToolID_TOOL_ID_TRIGGER_HANDOFF, nil
	case toolspec.ToolWebSearch:
		return onboardingpb.ToolID_TOOL_ID_WEB_SEARCH, nil
	default:
		return onboardingpb.ToolID_TOOL_ID_UNSPECIFIED, fmt.Errorf("onboarding tool ID %q is unsupported", id)
	}
}

func onboardingToolIDFromProto(id onboardingpb.ToolID) (toolspec.ID, error) {
	switch id {
	case onboardingpb.ToolID_TOOL_ID_EXEC_COMMAND:
		return toolspec.ToolExecCommand, nil
	case onboardingpb.ToolID_TOOL_ID_WRITE_STDIN:
		return toolspec.ToolWriteStdin, nil
	case onboardingpb.ToolID_TOOL_ID_VIEW_IMAGE:
		return toolspec.ToolViewImage, nil
	case onboardingpb.ToolID_TOOL_ID_PATCH:
		return toolspec.ToolPatch, nil
	case onboardingpb.ToolID_TOOL_ID_EDIT:
		return toolspec.ToolEdit, nil
	case onboardingpb.ToolID_TOOL_ID_ASK_QUESTION:
		return toolspec.ToolAskQuestion, nil
	case onboardingpb.ToolID_TOOL_ID_COMPLETE_NODE:
		return toolspec.ToolCompleteNode, nil
	case onboardingpb.ToolID_TOOL_ID_TRIGGER_HANDOFF:
		return toolspec.ToolTriggerHandoff, nil
	case onboardingpb.ToolID_TOOL_ID_WEB_SEARCH:
		return toolspec.ToolWebSearch, nil
	default:
		return "", fmt.Errorf("protobuf onboarding tool ID %v is unsupported", id)
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalIntToUint32(value *int, field string) (*uint32, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := intToUint32(*value, field)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

func optionalUint32ToInt(value *uint32, field string) (*int, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := uint32ToInt(*value, field)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

func intToUint32(value int, field string) (uint32, error) {
	if value < 0 || uint64(value) > math.MaxUint32 {
		return 0, fmt.Errorf("%s value %d cannot be represented as uint32", field, value)
	}
	return uint32(value), nil
}

func uint32ToInt(value uint32, field string) (int, error) {
	if uint64(value) > uint64(math.MaxInt) {
		return 0, fmt.Errorf("%s value %d cannot be represented as int", field, value)
	}
	return int(value), nil
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func dereference[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

func mapSlice[A, B any](values []A, convert func(A) B) []B {
	if values == nil {
		return nil
	}
	converted := make([]B, len(values))
	for index, value := range values {
		converted[index] = convert(value)
	}
	return converted
}

func mapSliceError[A, B any](values []A, convert func(A) (B, error)) ([]B, error) {
	if values == nil {
		return nil, nil
	}
	converted := make([]B, len(values))
	for index, value := range values {
		item, err := convert(value)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index, err)
		}
		converted[index] = item
	}
	return converted, nil
}

func optionalEnumToProto[A, B any](value *A, convert func(A) (B, error)) (*B, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := convert(*value)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

func optionalEnumFromProto[A, B any](value *A, convert func(A) (B, error)) (*B, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := convert(*value)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}
