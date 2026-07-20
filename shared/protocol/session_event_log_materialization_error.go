package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/invariant"
)

type SessionEventLogMaterializationReason string

const (
	SessionEventLogMaterializationUnsupportedVersion    SessionEventLogMaterializationReason = "unsupported_version"
	SessionEventLogMaterializationStructuralFailure     SessionEventLogMaterializationReason = "structural_failure"
	SessionEventLogMaterializationInsufficientSpace     SessionEventLogMaterializationReason = "insufficient_space"
	SessionEventLogMaterializationFailure               SessionEventLogMaterializationReason = "materialization_failure"
	SessionEventLogMaterializationReconciliationPending SessionEventLogMaterializationReason = "reconciliation_pending"
)

type SessionEventLogMaterializationStage string

const (
	SessionEventLogMaterializationPreparation    SessionEventLogMaterializationStage = "preparation"
	SessionEventLogMaterializationReconciliation SessionEventLogMaterializationStage = "reconciliation"
)

type SessionEventLogMaterializationError struct {
	Reason           SessionEventLogMaterializationReason `json:"reason"`
	Stage            SessionEventLogMaterializationStage  `json:"stage"`
	Committed        bool                                 `json:"committed"`
	PendingRepair    bool                                 `json:"pending_repair"`
	FoundVersion     *int                                 `json:"found_version,omitempty"`
	SupportedVersion *int                                 `json:"supported_version,omitempty"`
}

func (e *SessionEventLogMaterializationError) Error() string {
	if e == nil {
		return "session event log materialization failed"
	}
	return fmt.Sprintf(
		"session event log materialization failed: reason=%s stage=%s committed=%t pending_repair=%t",
		e.Reason,
		e.Stage,
		e.Committed,
		e.PendingRepair,
	)
}

func (e *SessionEventLogMaterializationError) Validate() error {
	if e == nil {
		return errors.New("session event log materialization error is required")
	}
	switch e.Stage {
	case SessionEventLogMaterializationPreparation,
		SessionEventLogMaterializationReconciliation:
	default:
		return errors.New("session event log materialization stage is invalid")
	}
	switch e.Reason {
	case SessionEventLogMaterializationUnsupportedVersion:
		if e.Stage != SessionEventLogMaterializationPreparation {
			return errors.New("unsupported-version materialization error requires preparation stage")
		}
		if e.Committed || e.PendingRepair {
			return errors.New("unsupported-version materialization error cannot be committed or pending repair")
		}
		if e.FoundVersion == nil || e.SupportedVersion == nil {
			return errors.New("unsupported-version materialization error requires version facts")
		}
		if *e.FoundVersion < 1 || *e.SupportedVersion < 1 {
			return errors.New("materialization version facts must be positive")
		}
		if *e.FoundVersion <= *e.SupportedVersion {
			return errors.New("found version must be newer than supported version")
		}
	case SessionEventLogMaterializationStructuralFailure,
		SessionEventLogMaterializationInsufficientSpace,
		SessionEventLogMaterializationFailure:
		if e.PendingRepair {
			return errors.New("pending repair requires reconciliation-pending reason")
		}
		if e.FoundVersion != nil || e.SupportedVersion != nil {
			return errors.New("materialization version facts require unsupported-version reason")
		}
	case SessionEventLogMaterializationReconciliationPending:
		if !e.Committed || !e.PendingRepair {
			return errors.New("reconciliation-pending materialization error requires committed pending repair facts")
		}
		if e.FoundVersion != nil || e.SupportedVersion != nil {
			return errors.New("materialization version facts require unsupported-version reason")
		}
	default:
		return errors.New("session event log materialization reason is invalid")
	}
	return nil
}

func (e *SessionEventLogMaterializationError) RPCErrorCode() int {
	return ErrCodeSessionEventLogMaterialization
}

func (e *SessionEventLogMaterializationError) RPCErrorData() (json.RawMessage, error) {
	if err := e.Validate(); err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeProtocolEncoding,
			"marshal_session_event_log_materialization_error",
			err,
		))
		return nil, err
	}
	data, err := json.Marshal(struct {
		Type string `json:"type"`
		*SessionEventLogMaterializationError
	}{
		Type:                                "session_event_log_materialization_error",
		SessionEventLogMaterializationError: e,
	})
	if err != nil {
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeProtocolEncoding,
			"marshal_session_event_log_materialization_error",
			err,
		))
		return nil, fmt.Errorf("marshal session event-log materialization error: %w", err)
	}
	return data, nil
}

func DecodeSessionEventLogMaterializationError(data json.RawMessage, fallback string) error {
	var envelope struct {
		Type string `json:"type"`
		SessionEventLogMaterializationError
	}
	if err := DecodeStrictJSON(data, &envelope); err != nil ||
		envelope.Type != "session_event_log_materialization_error" ||
		envelope.SessionEventLogMaterializationError.Validate() != nil {
		message := strings.TrimSpace(fallback)
		if message == "" {
			message = "session event log materialization failed"
		}
		return errors.New(message)
	}
	return &envelope.SessionEventLogMaterializationError
}
