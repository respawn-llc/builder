package serverjsoncontract

import (
	"encoding/json"
	"fmt"

	"core/shared/jsoncontract"
	"core/shared/serverapi"

	invjsonschema "github.com/invopop/jsonschema"
)

type updateStatusCurrentResultSource struct {
	Kind           serverapi.UpdateStatusResultKind `json:"kind" jsonschema:"enum=current"`
	CurrentVersion string                           `json:"current_version"`
	LatestVersion  string                           `json:"latest_version"`
}

type updateStatusAvailableResultSource struct {
	Kind           serverapi.UpdateStatusResultKind `json:"kind" jsonschema:"enum=available"`
	CurrentVersion string                           `json:"current_version"`
	LatestVersion  string                           `json:"latest_version"`
}

type updateStatusCheckUnavailableResultSource struct {
	Kind serverapi.UpdateStatusResultKind `json:"kind" jsonschema:"enum=check_unavailable"`
}

type updateStatusCheckFailedResultSource struct {
	Kind  serverapi.UpdateStatusResultKind `json:"kind" jsonschema:"enum=check_failed"`
	Cause string                           `json:"cause"`
}

type updateStatusResultContractSource struct{}

func (updateStatusResultContractSource) JSONSchema() *invjsonschema.Schema {
	reflector := invjsonschema.Reflector{Anonymous: true, DoNotReference: true}
	variants := []*invjsonschema.Schema{
		reflector.Reflect(updateStatusCurrentResultSource{}),
		reflector.Reflect(updateStatusAvailableResultSource{}),
		reflector.Reflect(updateStatusCheckUnavailableResultSource{}),
		reflector.Reflect(updateStatusCheckFailedResultSource{}),
	}
	for _, variant := range variants {
		variant.Version = ""
	}
	return &invjsonschema.Schema{OneOf: variants}
}

type updateStatusResponseContractSource struct {
	Result updateStatusResultContractSource `json:"result"`
}

type UpdateStatusResponse struct {
	schema jsoncontract.Internal
}

func PrepareUpdateStatusResponse(preparer jsoncontract.Preparer) (UpdateStatusResponse, error) {
	schema, err := preparer.Internal("Update Status response", updateStatusResponseContractSource{})
	if err != nil {
		return UpdateStatusResponse{}, err
	}
	return UpdateStatusResponse{schema: schema}, nil
}

func (c UpdateStatusResponse) Decode(raw []byte) (serverapi.UpdateStatusResponse, error) {
	if err := c.schema.Validate(raw); err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	var discriminator struct {
		Result struct {
			Kind serverapi.UpdateStatusResultKind `json:"kind"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	var result serverapi.UpdateStatusResult
	switch discriminator.Result.Kind {
	case serverapi.UpdateStatusCurrent:
		var source struct {
			Result updateStatusCurrentResultSource `json:"result"`
		}
		if err := json.Unmarshal(raw, &source); err != nil {
			return serverapi.UpdateStatusResponse{}, err
		}
		result = serverapi.CurrentUpdateStatusResult(source.Result.CurrentVersion, source.Result.LatestVersion)
	case serverapi.UpdateStatusAvailable:
		var source struct {
			Result updateStatusAvailableResultSource `json:"result"`
		}
		if err := json.Unmarshal(raw, &source); err != nil {
			return serverapi.UpdateStatusResponse{}, err
		}
		result = serverapi.AvailableUpdateStatusResult(source.Result.CurrentVersion, source.Result.LatestVersion)
	case serverapi.UpdateStatusCheckUnavailable:
		result = serverapi.CheckUnavailableUpdateStatusResult()
	case serverapi.UpdateStatusCheckFailed:
		var source struct {
			Result updateStatusCheckFailedResultSource `json:"result"`
		}
		if err := json.Unmarshal(raw, &source); err != nil {
			return serverapi.UpdateStatusResponse{}, err
		}
		result = serverapi.FailedUpdateStatusResult(source.Result.Cause)
	default:
		return serverapi.UpdateStatusResponse{}, fmt.Errorf("update status kind %q is invalid", discriminator.Result.Kind)
	}
	response := serverapi.UpdateStatusResponse{Result: result}
	if err := response.Validate(); err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	return response, nil
}
