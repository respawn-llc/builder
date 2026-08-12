package serverjsoncontract

import (
	"encoding/json"
	"fmt"

	"core/shared/jsoncontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	invjsonschema "github.com/invopop/jsonschema"
)

func sessionExecutionFieldContractSchema[T any, R ~string]() *invjsonschema.Schema {
	reflector := invjsonschema.Reflector{Anonymous: true, DoNotReference: true}
	variants := []*invjsonschema.Schema{
		reflector.Reflect(serverapi.AvailableSessionExecutionFieldWire[T]{}),
		reflector.Reflect(serverapi.UnavailableSessionExecutionFieldWire[R]{}),
		reflector.Reflect(serverapi.FailedSessionExecutionFieldWire{}),
	}
	for _, variant := range variants {
		variant.Version = ""
	}
	return &invjsonschema.Schema{OneOf: variants}
}

type sessionExecutionWorkspaceFieldContractSource struct{}
type sessionExecutionBranchFieldContractSource struct{}
type sessionExecutionAuthFieldContractSource struct{}
type sessionExecutionModelFieldContractSource struct{}

func (sessionExecutionWorkspaceFieldContractSource) JSONSchema() *invjsonschema.Schema {
	return sessionExecutionFieldContractSchema[
		serverapi.SessionExecutionWorkspace,
		serverapi.SessionExecutionWorkspaceUnavailableReason,
	]()
}

func (sessionExecutionBranchFieldContractSource) JSONSchema() *invjsonschema.Schema {
	return sessionExecutionFieldContractSchema[
		serverapi.SessionExecutionBranch,
		serverapi.SessionExecutionBranchUnavailableReason,
	]()
}

func (sessionExecutionAuthFieldContractSource) JSONSchema() *invjsonschema.Schema {
	return sessionExecutionFieldContractSchema[
		serverapi.SessionExecutionAuth,
		serverapi.SessionExecutionAuthUnavailableReason,
	]()
}

func (sessionExecutionModelFieldContractSource) JSONSchema() *invjsonschema.Schema {
	return sessionExecutionFieldContractSchema[
		serverapi.SessionExecutionModel,
		serverapi.SessionExecutionModelUnavailableReason,
	]()
}

type sessionExecutionEnvironmentRequestContractSource struct {
	SessionID string `json:"session_id"`
}

type sessionExecutionEnvironmentResponseContractSource struct{}

func (sessionExecutionEnvironmentResponseContractSource) JSONSchema() *invjsonschema.Schema {
	type environmentSource = serverapi.SessionExecutionEnvironmentWire[
		sessionExecutionWorkspaceFieldContractSource,
		sessionExecutionBranchFieldContractSource,
		sessionExecutionAuthFieldContractSource,
		sessionExecutionModelFieldContractSource,
	]
	return (&invjsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
	}).Reflect(serverapi.SessionExecutionEnvironmentResponseWire[environmentSource]{})
}

type SessionExecutionEnvironmentRequest struct {
	schema jsoncontract.Internal
}

func PrepareSessionExecutionEnvironmentRequest(preparer jsoncontract.Preparer) (SessionExecutionEnvironmentRequest, error) {
	schema, err := preparer.Internal("Session execution environment request", sessionExecutionEnvironmentRequestContractSource{})
	if err != nil {
		return SessionExecutionEnvironmentRequest{}, err
	}
	return SessionExecutionEnvironmentRequest{schema: schema}, nil
}

func (c SessionExecutionEnvironmentRequest) Decode(raw []byte) (serverapi.SessionExecutionEnvironmentRequest, error) {
	if err := c.schema.Validate(raw); err != nil {
		return serverapi.SessionExecutionEnvironmentRequest{}, err
	}
	var source sessionExecutionEnvironmentRequestContractSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return serverapi.SessionExecutionEnvironmentRequest{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(source.SessionID)
	if err != nil {
		return serverapi.SessionExecutionEnvironmentRequest{}, err
	}
	request := serverapi.SessionExecutionEnvironmentRequest{SessionID: sessionID}
	if err := request.Validate(); err != nil {
		return serverapi.SessionExecutionEnvironmentRequest{}, err
	}
	return request, nil
}

type SessionExecutionEnvironmentResponse struct {
	schema jsoncontract.Internal
}

func PrepareSessionExecutionEnvironmentResponse(preparer jsoncontract.Preparer) (SessionExecutionEnvironmentResponse, error) {
	schema, err := preparer.Internal(
		"Session execution environment response",
		sessionExecutionEnvironmentResponseContractSource{},
	)
	if err != nil {
		return SessionExecutionEnvironmentResponse{}, err
	}
	return SessionExecutionEnvironmentResponse{schema: schema}, nil
}

func (c SessionExecutionEnvironmentResponse) Decode(raw []byte) (serverapi.SessionExecutionEnvironmentResponse, error) {
	if err := c.schema.Validate(raw); err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	type environmentWire = serverapi.SessionExecutionEnvironmentWire[
		serverapi.SessionExecutionFieldWire[
			serverapi.SessionExecutionWorkspace,
			serverapi.SessionExecutionWorkspaceUnavailableReason,
		],
		serverapi.SessionExecutionFieldWire[
			serverapi.SessionExecutionBranch,
			serverapi.SessionExecutionBranchUnavailableReason,
		],
		serverapi.SessionExecutionFieldWire[
			serverapi.SessionExecutionAuth,
			serverapi.SessionExecutionAuthUnavailableReason,
		],
		serverapi.SessionExecutionFieldWire[
			serverapi.SessionExecutionModel,
			serverapi.SessionExecutionModelUnavailableReason,
		],
	]
	var source serverapi.SessionExecutionEnvironmentResponseWire[environmentWire]
	if err := json.Unmarshal(raw, &source); err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(source.Environment.SessionID)
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	workspace, err := decodeSessionExecutionField(
		source.Environment.Workspace,
		func(value serverapi.SessionExecutionWorkspace) serverapi.SessionExecutionWorkspaceField {
			return serverapi.AvailableSessionExecutionWorkspace(value.Path)
		},
		serverapi.UnavailableSessionExecutionWorkspace,
		serverapi.FailedSessionExecutionWorkspace,
	)
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, fmt.Errorf("workspace: %w", err)
	}
	branch, err := decodeSessionExecutionField(
		source.Environment.Branch,
		func(value serverapi.SessionExecutionBranch) serverapi.SessionExecutionBranchField {
			return serverapi.AvailableSessionExecutionBranch(value.Name)
		},
		serverapi.UnavailableSessionExecutionBranch,
		serverapi.FailedSessionExecutionBranch,
	)
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, fmt.Errorf("branch: %w", err)
	}
	auth, err := decodeSessionExecutionField(
		source.Environment.Auth,
		serverapi.AvailableSessionExecutionAuth,
		serverapi.UnavailableSessionExecutionAuth,
		serverapi.FailedSessionExecutionAuth,
	)
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, fmt.Errorf("auth: %w", err)
	}
	model, err := decodeSessionExecutionField(
		source.Environment.Model,
		serverapi.AvailableSessionExecutionModel,
		serverapi.UnavailableSessionExecutionModel,
		serverapi.FailedSessionExecutionModel,
	)
	if err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, fmt.Errorf("model: %w", err)
	}
	response := serverapi.SessionExecutionEnvironmentResponse{
		Environment: serverapi.SessionExecutionEnvironment{
			SessionID: sessionID,
			Workspace: workspace,
			Branch:    branch,
			Auth:      auth,
			Model:     model,
		},
	}
	if err := response.Validate(); err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	return response, nil
}

func decodeSessionExecutionField[T any, R ~string, F any](
	source serverapi.SessionExecutionFieldWire[T, R],
	available func(T) F,
	unavailable func(R) F,
	failed func(serverapi.SessionExecutionFieldError) F,
) (F, error) {
	switch source.Kind {
	case serverapi.SessionExecutionFieldAvailable:
		if source.Value == nil {
			var zero F
			return zero, fmt.Errorf("available field payload is invalid")
		}
		return available(*source.Value), nil
	case serverapi.SessionExecutionFieldUnavailable:
		if source.Reason == nil {
			var zero F
			return zero, fmt.Errorf("unavailable field payload is invalid")
		}
		return unavailable(*source.Reason), nil
	case serverapi.SessionExecutionFieldFailed:
		if source.Error == nil {
			var zero F
			return zero, fmt.Errorf("failed field payload is invalid")
		}
		return failed(*source.Error), nil
	default:
		var zero F
		return zero, fmt.Errorf("field kind %q is invalid", source.Kind)
	}
}
