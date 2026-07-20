package session

import (
	"encoding/json"
	"errors"
	"syscall"

	"core/shared/protocol"
)

// MapEventLogMaterializationError converts session-native materialization
// failures into the single client-visible wire contract. Unrelated errors are
// returned unchanged.
func MapEventLogMaterializationError(err error) error {
	if err == nil {
		return nil
	}
	var materialization *EventLogMaterializationError
	if !errors.As(err, &materialization) {
		var unsupported UnsupportedEventLogVersionError
		var malformed MalformedEventLogHeaderError
		if !errors.As(err, &unsupported) && !errors.As(err, &malformed) {
			return err
		}
	}

	wire := &protocol.SessionEventLogMaterializationError{
		Reason:        protocol.SessionEventLogMaterializationFailure,
		Stage:         protocol.SessionEventLogMaterializationPreparation,
		Committed:     materialization != nil && materialization.Committed,
		PendingRepair: materialization != nil && materialization.PendingRepair,
	}
	if materialization != nil &&
		materialization.Stage == EventLogMaterializationStageReconciliation {
		wire.Stage = protocol.SessionEventLogMaterializationReconciliation
	}

	var unsupported UnsupportedEventLogVersionError
	switch {
	case errors.As(err, &unsupported):
		wire.Reason = protocol.SessionEventLogMaterializationUnsupportedVersion
		found := unsupported.FoundVersion
		supported := unsupported.SupportedVersion
		wire.FoundVersion = &found
		wire.SupportedVersion = &supported
	case wire.PendingRepair:
		wire.Reason = protocol.SessionEventLogMaterializationReconciliationPending
	case errors.Is(err, syscall.ENOSPC):
		wire.Reason = protocol.SessionEventLogMaterializationInsufficientSpace
	case isEventLogStructuralFailure(err):
		wire.Reason = protocol.SessionEventLogMaterializationStructuralFailure
	}
	return mappedEventLogMaterializationError{wire: wire, native: err}
}

type mappedEventLogMaterializationError struct {
	wire   *protocol.SessionEventLogMaterializationError
	native error
}

func (e mappedEventLogMaterializationError) Error() string {
	return e.wire.Error()
}

func (e mappedEventLogMaterializationError) Unwrap() []error {
	return []error{e.wire, e.native}
}

func (e mappedEventLogMaterializationError) RPCErrorCode() int {
	return e.wire.RPCErrorCode()
}

func (e mappedEventLogMaterializationError) RPCErrorData() (json.RawMessage, error) {
	return e.wire.RPCErrorData()
}

func isEventLogStructuralFailure(err error) bool {
	var malformedHeader MalformedEventLogHeaderError
	var contract eventLogContractError
	if errors.As(err, &malformedHeader) ||
		errors.As(err, &contract) ||
		errors.Is(err, errMigrationJSONMalformed) ||
		errors.Is(err, errMigrationJSONComplex) ||
		errors.Is(err, ErrToolCompletionProviderItem) {
		return true
	}
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	return errors.As(err, &syntaxError) || errors.As(err, &typeError)
}
