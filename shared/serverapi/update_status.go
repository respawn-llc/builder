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

type UpdateStatusVersions struct {
	Current string
	Latest  string
}

type UpdateStatusFailure struct {
	Cause string
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

func (r UpdateStatusResult) Versions() *UpdateStatusVersions {
	if r.currentVersion == nil || r.latestVersion == nil {
		return nil
	}
	return &UpdateStatusVersions{
		Current: *r.currentVersion,
		Latest:  *r.latestVersion,
	}
}

func (r UpdateStatusResult) Failure() *UpdateStatusFailure {
	if r.failureCause == nil {
		return nil
	}
	return &UpdateStatusFailure{Cause: *r.failureCause}
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

type UpdateStatusResultWire struct {
	Kind           UpdateStatusResultKind `json:"kind"`
	CurrentVersion *string                `json:"current_version,omitempty"`
	LatestVersion  *string                `json:"latest_version,omitempty"`
	Cause          *string                `json:"cause,omitempty"`
}

type CurrentUpdateStatusResultWire struct {
	Kind           UpdateStatusResultKind `json:"kind" jsonschema:"enum=current"`
	CurrentVersion string                 `json:"current_version"`
	LatestVersion  string                 `json:"latest_version"`
}

type AvailableUpdateStatusResultWire struct {
	Kind           UpdateStatusResultKind `json:"kind" jsonschema:"enum=available"`
	CurrentVersion string                 `json:"current_version"`
	LatestVersion  string                 `json:"latest_version"`
}

type CheckUnavailableUpdateStatusResultWire struct {
	Kind UpdateStatusResultKind `json:"kind" jsonschema:"enum=check_unavailable"`
}

type CheckFailedUpdateStatusResultWire struct {
	Kind  UpdateStatusResultKind `json:"kind" jsonschema:"enum=check_failed"`
	Cause string                 `json:"cause"`
}

func (r UpdateStatusResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	switch r.kind {
	case UpdateStatusCurrent:
		return json.Marshal(CurrentUpdateStatusResultWire{
			Kind:           r.kind,
			CurrentVersion: *r.currentVersion,
			LatestVersion:  *r.latestVersion,
		})
	case UpdateStatusAvailable:
		return json.Marshal(AvailableUpdateStatusResultWire{
			Kind:           r.kind,
			CurrentVersion: *r.currentVersion,
			LatestVersion:  *r.latestVersion,
		})
	case UpdateStatusCheckUnavailable:
		return json.Marshal(CheckUnavailableUpdateStatusResultWire{Kind: r.kind})
	case UpdateStatusCheckFailed:
		return json.Marshal(CheckFailedUpdateStatusResultWire{
			Kind:  r.kind,
			Cause: *r.failureCause,
		})
	default:
		panic(fmt.Sprintf("unknown update status kind %q", r.kind))
	}
}

type UpdateStatusResponse struct {
	Result UpdateStatusResult `json:"result"`
}

type UpdateStatusResponseWire[R any] struct {
	Result R `json:"result"`
}

func (r UpdateStatusResponse) Validate() error {
	return r.Result.Validate()
}

func (r UpdateStatusResponse) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(UpdateStatusResponseWire[UpdateStatusResult]{Result: r.Result})
}
