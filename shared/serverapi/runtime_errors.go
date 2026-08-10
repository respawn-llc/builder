package serverapi

import (
	"encoding/json"
	"errors"
	"strings"

	"core/shared/protocol"
)

var ErrRuntimeOperationCanceled = errors.New("runtime operation canceled before execution")
var ErrRuntimeCommandNotAccepted = errors.New("runtime command was not accepted")

type RuntimeCommandNotAcceptedError struct {
	Cause error
}

func NewRuntimeCommandNotAcceptedError(cause error) *RuntimeCommandNotAcceptedError {
	return &RuntimeCommandNotAcceptedError{Cause: cause}
}

func (e *RuntimeCommandNotAcceptedError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrRuntimeCommandNotAccepted.Error()
	}
	return ErrRuntimeCommandNotAccepted.Error() + ": " + e.Cause.Error()
}

func (e *RuntimeCommandNotAcceptedError) Unwrap() []error {
	if e == nil || e.Cause == nil {
		return []error{ErrRuntimeCommandNotAccepted}
	}
	return []error{ErrRuntimeCommandNotAccepted, e.Cause}
}

func (e *RuntimeCommandNotAcceptedError) RPCErrorCode() int {
	return protocol.ErrCodeRuntimeCommandNotAccepted
}

func (e *RuntimeCommandNotAcceptedError) RPCErrorData() json.RawMessage {
	cause := protocol.ResponseError{
		Code:    protocol.ErrCodeInternalError,
		Message: ErrRuntimeCommandNotAccepted.Error(),
	}
	if e != nil && e.Cause != nil {
		cause.Message = strings.TrimSpace(e.Cause.Error())
		if cause.Message == "" {
			cause.Message = ErrRuntimeCommandNotAccepted.Error()
		}
		var structured protocol.StructuredRPCError
		if errors.As(e.Cause, &structured) {
			cause.Code = structured.RPCErrorCode()
			cause.Data = structured.RPCErrorData()
		}
	}
	return marshalRPCErrorData(struct {
		Cause protocol.ResponseError `json:"cause"`
	}{Cause: cause})
}

var _ protocol.StructuredRPCError = (*RuntimeCommandNotAcceptedError)(nil)
