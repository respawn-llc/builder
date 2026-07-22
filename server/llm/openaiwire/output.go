package openaiwire

import (
	"bytes"
	"encoding/json"
	"strings"
)

func NewFunctionCallOutput(callID string, output json.RawMessage) (Raw, error) {
	callID, err := normalizeCallID(callID)
	if err != nil {
		return Raw{}, err
	}
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) > 0 && !json.Valid(trimmed) {
		return Raw{}, &ValidationError{Kind: ValidationInvalidOutput}
	}
	blank := len(trimmed) == 0
	if len(trimmed) == 0 {
		trimmed = []byte(`""`)
	}
	value := any(string(trimmed))
	if blank {
		value = ""
	} else if items, ok := InputContentItems(trimmed); ok {
		value = items
	} else {
		value = providerOutputString(trimmed)
	}
	return encodeOutputEnvelope("function_call_output", callID, value)
}

func NewCustomToolOutput(callID string, output json.RawMessage) (Raw, error) {
	callID, err := normalizeCallID(callID)
	if err != nil {
		return Raw{}, err
	}
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) > 0 && !json.Valid(trimmed) {
		return Raw{}, &ValidationError{Kind: ValidationInvalidOutput}
	}
	if len(trimmed) == 0 {
		return encodeOutputEnvelope("custom_tool_call_output", callID, "")
	}
	return encodeOutputEnvelope(
		"custom_tool_call_output",
		callID,
		providerOutputString(trimmed),
	)
}

func providerOutputString(value []byte) string {
	var decoded string
	if json.Unmarshal(value, &decoded) == nil {
		return decoded
	}
	return string(value)
}

func encodeOutputEnvelope(kind, callID string, output any) (Raw, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output any    `json:"output"`
	}{
		Type:   kind,
		CallID: callID,
		Output: output,
	}); err != nil {
		return Raw{}, err
	}
	return Raw{bytes: bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})}, nil
}

func normalizeCallID(callID string) (string, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", &ValidationError{Kind: ValidationMissingCallID}
	}
	return callID, nil
}
