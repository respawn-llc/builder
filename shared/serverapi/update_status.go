package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

type UpdateStatusRequest struct{}

func (UpdateStatusRequest) Validate() error {
	return nil
}

func (r UpdateStatusRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return []byte("{}"), nil
}

func (r *UpdateStatusRequest) UnmarshalJSON(data []byte) error {
	var wire *struct{}
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire == nil {
		return errors.New("update status request is required")
	}
	*r = UpdateStatusRequest{}
	return nil
}

type UpdateStatusResultKind string

const (
	UpdateStatusCurrent          UpdateStatusResultKind = "current"
	UpdateStatusAvailable        UpdateStatusResultKind = "available"
	UpdateStatusCheckUnavailable UpdateStatusResultKind = "check_unavailable"
	UpdateStatusCheckFailed      UpdateStatusResultKind = "check_failed"
)

type UpdateStatusResult struct {
	kind           UpdateStatusResultKind
	currentVersion *string
	latestVersion  *string
	failureCause   *string
}

func CurrentUpdateStatusResult(currentVersion string, latestVersion string) UpdateStatusResult {
	return versionedUpdateStatusResult(UpdateStatusCurrent, currentVersion, latestVersion)
}

func AvailableUpdateStatusResult(currentVersion string, latestVersion string) UpdateStatusResult {
	return versionedUpdateStatusResult(UpdateStatusAvailable, currentVersion, latestVersion)
}

func versionedUpdateStatusResult(
	kind UpdateStatusResultKind,
	currentVersion string,
	latestVersion string,
) UpdateStatusResult {
	return UpdateStatusResult{
		kind:           kind,
		currentVersion: &currentVersion,
		latestVersion:  &latestVersion,
	}
}

func CheckUnavailableUpdateStatusResult() UpdateStatusResult {
	return UpdateStatusResult{kind: UpdateStatusCheckUnavailable}
}

func FailedUpdateStatusResult(cause string) UpdateStatusResult {
	return UpdateStatusResult{
		kind:         UpdateStatusCheckFailed,
		failureCause: &cause,
	}
}

func (r UpdateStatusResult) Kind() UpdateStatusResultKind {
	return r.kind
}

func (r UpdateStatusResult) Versions() (currentVersion string, latestVersion string, ok bool) {
	if r.currentVersion == nil || r.latestVersion == nil {
		return "", "", false
	}
	return *r.currentVersion, *r.latestVersion, true
}

func (r UpdateStatusResult) FailureCause() (string, bool) {
	if r.failureCause == nil {
		return "", false
	}
	return *r.failureCause, true
}

func (r UpdateStatusResult) Validate() error {
	switch r.kind {
	case UpdateStatusCurrent, UpdateStatusAvailable:
		if r.currentVersion == nil || r.latestVersion == nil || r.failureCause != nil {
			return fmt.Errorf("%s update status payload is invalid", r.kind)
		}
		if err := validateUpdateVersion("current_version", *r.currentVersion); err != nil {
			return err
		}
		if err := validateUpdateVersion("latest_version", *r.latestVersion); err != nil {
			return err
		}
	case UpdateStatusCheckUnavailable:
		if r.currentVersion != nil || r.latestVersion != nil || r.failureCause != nil {
			return errors.New("check_unavailable update status cannot contain versions or a failure cause")
		}
	case UpdateStatusCheckFailed:
		if r.currentVersion != nil || r.latestVersion != nil || r.failureCause == nil {
			return errors.New("check_failed update status payload is invalid")
		}
		if strings.TrimSpace(*r.failureCause) == "" {
			return errors.New("check_failed update status cause is required")
		}
	default:
		return fmt.Errorf("update status kind %q is invalid", r.kind)
	}
	return nil
}

func validateUpdateVersion(field string, version string) error {
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(version) != version {
		return fmt.Errorf("%s must not have leading or trailing whitespace", field)
	}
	return nil
}

type updateStatusResultWire struct {
	Kind           UpdateStatusResultKind `json:"kind"`
	CurrentVersion *string                `json:"current_version,omitempty"`
	LatestVersion  *string                `json:"latest_version,omitempty"`
	Cause          *string                `json:"cause,omitempty"`
}

func (r UpdateStatusResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(updateStatusResultWire{
		Kind:           r.kind,
		CurrentVersion: r.currentVersion,
		LatestVersion:  r.latestVersion,
		Cause:          r.failureCause,
	})
}

func (r *UpdateStatusResult) UnmarshalJSON(data []byte) error {
	var wire *updateStatusResultWire
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire == nil {
		return errors.New("update status result is required")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	currentVersionPresent := members["current_version"] != nil
	latestVersionPresent := members["latest_version"] != nil
	causePresent := members["cause"] != nil

	switch wire.Kind {
	case UpdateStatusCurrent, UpdateStatusAvailable:
		if !currentVersionPresent || wire.CurrentVersion == nil ||
			!latestVersionPresent || wire.LatestVersion == nil ||
			causePresent {
			return fmt.Errorf("%s update status payload is invalid", wire.Kind)
		}
	case UpdateStatusCheckUnavailable:
		if currentVersionPresent || latestVersionPresent || causePresent {
			return errors.New("check_unavailable update status cannot contain versions or a failure cause")
		}
	case UpdateStatusCheckFailed:
		if currentVersionPresent || latestVersionPresent || !causePresent || wire.Cause == nil {
			return errors.New("check_failed update status payload is invalid")
		}
	default:
		return fmt.Errorf("update status kind %q is invalid", wire.Kind)
	}

	decoded := UpdateStatusResult{
		kind:           wire.Kind,
		currentVersion: wire.CurrentVersion,
		latestVersion:  wire.LatestVersion,
		failureCause:   wire.Cause,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*r = decoded
	return nil
}

type UpdateStatusResponse struct {
	Result UpdateStatusResult `json:"result"`
}

func (r UpdateStatusResponse) Validate() error {
	return r.Result.Validate()
}

func (r UpdateStatusResponse) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Result UpdateStatusResult `json:"result"`
	}
	return json.Marshal(wire{Result: r.Result})
}

func (r *UpdateStatusResponse) UnmarshalJSON(data []byte) error {
	var wire *struct {
		Result *UpdateStatusResult `json:"result"`
	}
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	if wire == nil || wire.Result == nil {
		return errors.New("update status response result is required")
	}
	decoded := UpdateStatusResponse{Result: *wire.Result}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*r = decoded
	return nil
}
