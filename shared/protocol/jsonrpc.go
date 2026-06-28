package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const JSONRPCVersion = "2.0"

const (
	// JSON-RPC reserves -32000..-32099 for implementation-defined server
	// errors. These values are part of Kent's wire contract; clients must
	// map ErrCodeRequestCanceled back to context.Canceled so user interrupts
	// remain normal cancellation flow instead of transcript-visible errors.
	ErrCodeParseError                    = -32700
	ErrCodeInvalidRequest                = -32600
	ErrCodeMethodNotFound                = -32601
	ErrCodeInvalidParams                 = -32602
	ErrCodeInternalError                 = -32603
	ErrCodeStreamGap                     = -32010
	ErrCodeStreamUnavailable             = -32011
	ErrCodeStreamFailed                  = -32012
	ErrCodeWorkspaceNotRegistered        = -32013
	ErrCodeProjectNotFound               = -32014
	ErrCodeProjectUnavailable            = -32015
	ErrCodeAuthRequired                  = -32018
	ErrCodeRuntimeUnavailable            = -32019
	ErrCodePromptNotFound                = -32020
	ErrCodePromptResolved                = -32021
	ErrCodePromptUnsupported             = -32022
	ErrCodeRequestCanceled               = -32023
	ErrCodeWorkflowTaskNotFound          = -32024
	ErrCodeProtocolVersionMismatch       = -32025
	ErrCodeWorkflowTaskCompleteAmbiguous = -32026
	ErrCodeWorkflowTaskCompleteNotFound  = -32027
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.JSONRPC) != JSONRPCVersion {
		return fmt.Errorf("jsonrpc must be %q", JSONRPCVersion)
	}
	if strings.TrimSpace(r.Method) == "" {
		return errors.New("method is required")
	}
	return nil
}

func NewSuccessResponse(id string, result any) Response {
	resp := Response{JSONRPC: JSONRPCVersion, ID: strings.TrimSpace(id)}
	if result == nil {
		return resp
	}
	data, err := json.Marshal(result)
	if err != nil {
		return NewErrorResponse(id, ErrCodeInternalError, err.Error())
	}
	resp.Result = data
	return resp
}

func NewErrorResponse(id string, code int, message string) Response {
	return Response{
		JSONRPC: JSONRPCVersion,
		ID:      strings.TrimSpace(id),
		Error:   &ResponseError{Code: code, Message: strings.TrimSpace(message)},
	}
}
