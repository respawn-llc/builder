package openaiwire

import (
	"bytes"
	"encoding/json"
	"strings"
)

func NewFunctionCallOutput(callID string, output json.RawMessage) (Raw, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) > 0 && !json.Valid(trimmed) {
		return Raw{}, &ValidationError{Kind: ValidationInvalidOutput}
	}
	if len(trimmed) == 0 {
		trimmed = []byte(`""`)
	}
	var buffer bytes.Buffer
	if err := WriteFunctionCallOutput(
		&buffer,
		callID,
		bytesJSONSource{value: trimmed},
		heapScratchAllocator{},
	); err != nil {
		return Raw{}, err
	}
	return Raw{bytes: buffer.Bytes()}, nil
}

func NewCustomToolOutput(callID string, output json.RawMessage) (Raw, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) > 0 && !json.Valid(trimmed) {
		return Raw{}, &ValidationError{Kind: ValidationInvalidOutput}
	}
	if len(trimmed) == 0 {
		trimmed = []byte(`""`)
	}
	var buffer bytes.Buffer
	if err := WriteCustomToolOutput(
		&buffer,
		callID,
		bytesJSONSource{value: trimmed},
		heapScratchAllocator{},
	); err != nil {
		return Raw{}, err
	}
	return Raw{bytes: buffer.Bytes()}, nil
}

func normalizeCallID(callID string) (string, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", &ValidationError{Kind: ValidationMissingCallID}
	}
	return callID, nil
}
