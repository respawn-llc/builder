package serverapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"core/shared/protocol"
)

type SubagentLaunchDenialKind string

const (
	SubagentLaunchDenialInvalidTarget SubagentLaunchDenialKind = "invalid_target"
	SubagentLaunchDenialTargetMissing SubagentLaunchDenialKind = "target_missing"
	SubagentLaunchDenialNotCallable   SubagentLaunchDenialKind = "not_callable"
	SubagentLaunchDenialCallerMissing SubagentLaunchDenialKind = "caller_missing"
	SubagentLaunchDenialParentMissing SubagentLaunchDenialKind = "parent_missing"
)

// SubagentLaunchDeniedError is the structured cross-process contract for a
// server-owned model-originated launch denial.
type SubagentLaunchDeniedError struct {
	Kind           SubagentLaunchDenialKind `json:"kind"`
	Target         *string                  `json:"target,omitempty"`
	AvailableRoles []string                 `json:"available_roles,omitempty"`
}

func (e *SubagentLaunchDeniedError) Error() string {
	if e == nil {
		return "subagent launch denied"
	}
	return fmt.Sprintf("subagent launch denied: %s", strings.TrimSpace(string(e.Kind)))
}

func (e *SubagentLaunchDeniedError) RPCErrorCode() int {
	return protocol.ErrCodeSubagentLaunchDenied
}

func (e *SubagentLaunchDeniedError) RPCErrorData() (json.RawMessage, error) {
	if e == nil {
		return nil, fmt.Errorf("subagent launch denial is required")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal subagent launch denial: %w", err)
	}
	return data, nil
}

func DecodeSubagentLaunchDeniedError(data json.RawMessage, fallback string) error {
	var decoded SubagentLaunchDeniedError
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(fallback), err)
	}
	if strings.TrimSpace(string(decoded.Kind)) == "" {
		return fmt.Errorf("%s: invalid subagent launch denial", strings.TrimSpace(fallback))
	}
	return &decoded
}
