package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/runtimeids"
)

type SubagentLaunchPolicyErrorKind string

const (
	SubagentLaunchPolicyMaxDepthExceeded SubagentLaunchPolicyErrorKind = "max_depth_exceeded"
	SubagentLaunchPolicyLineageCorrupt   SubagentLaunchPolicyErrorKind = "lineage_corrupt"
)

type SubagentLaunchPolicyError struct {
	Kind              SubagentLaunchPolicyErrorKind `json:"kind"`
	AttemptedDepth    *int                          `json:"attempted_depth,omitempty"`
	MaxDepth          *int                          `json:"max_depth,omitempty"`
	RepeatedSessionID *runtimeids.SessionID         `json:"repeated_session_id,omitempty"`
	VisitedSessionIDs []runtimeids.SessionID        `json:"visited_session_ids,omitempty"`
}

func NewMaxDepthExceededSubagentLaunchPolicyError(attemptedDepth int, maxDepth int) *SubagentLaunchPolicyError {
	return &SubagentLaunchPolicyError{
		Kind:           SubagentLaunchPolicyMaxDepthExceeded,
		AttemptedDepth: intPointer(attemptedDepth),
		MaxDepth:       intPointer(maxDepth),
	}
}

func NewLineageCorruptSubagentLaunchPolicyError(repeated runtimeids.SessionID, visited []runtimeids.SessionID) *SubagentLaunchPolicyError {
	copiedRepeated := repeated
	return &SubagentLaunchPolicyError{
		Kind:              SubagentLaunchPolicyLineageCorrupt,
		RepeatedSessionID: &copiedRepeated,
		VisitedSessionIDs: append([]runtimeids.SessionID(nil), visited...),
	}
}

func (e *SubagentLaunchPolicyError) Error() string {
	if e == nil {
		return "subagent launch rejected"
	}
	switch e.Kind {
	case SubagentLaunchPolicyMaxDepthExceeded:
		if e.AttemptedDepth != nil && e.MaxDepth != nil {
			return fmt.Sprintf(
				"subagent launch rejected at depth %d (maximum %d): You are already a subagent, so you shouldn't spawn more subagents to prevent overloading the machine and infinite recursion. Do not attempt to use subagents anymore and complete the task on your own",
				*e.AttemptedDepth,
				*e.MaxDepth,
			)
		}
	case SubagentLaunchPolicyLineageCorrupt:
		return "subagent launch rejected: parent-agent lineage is corrupt"
	}
	return "subagent launch rejected"
}

func (e *SubagentLaunchPolicyError) Validate() error {
	if e == nil {
		return errors.New("subagent launch policy error is required")
	}
	switch e.Kind {
	case SubagentLaunchPolicyMaxDepthExceeded:
		if e.AttemptedDepth == nil || *e.AttemptedDepth < 1 {
			return errors.New("max-depth policy error requires attempted_depth >= 1")
		}
		if e.MaxDepth == nil || *e.MaxDepth < 0 {
			return errors.New("max-depth policy error requires max_depth >= 0")
		}
		if *e.AttemptedDepth <= *e.MaxDepth {
			return errors.New("max-depth policy error requires attempted_depth greater than max_depth")
		}
		if e.RepeatedSessionID != nil || len(e.VisitedSessionIDs) != 0 {
			return errors.New("max-depth policy error cannot contain lineage corruption facts")
		}
	case SubagentLaunchPolicyLineageCorrupt:
		if e.AttemptedDepth != nil || e.MaxDepth != nil {
			return errors.New("lineage-corrupt policy error cannot contain depth facts")
		}
		if e.RepeatedSessionID == nil || e.RepeatedSessionID.IsZero() {
			return errors.New("lineage-corrupt policy error requires repeated_session_id")
		}
		if len(e.VisitedSessionIDs) == 0 {
			return errors.New("lineage-corrupt policy error requires visited_session_ids")
		}
		for _, id := range e.VisitedSessionIDs {
			if id.IsZero() {
				return errors.New("lineage-corrupt policy error contains invalid visited_session_id")
			}
		}
	default:
		return errors.New("subagent launch policy error kind is invalid")
	}
	return nil
}

func (e *SubagentLaunchPolicyError) RPCErrorCode() int {
	return ErrCodeSubagentLaunchPolicy
}

func (e *SubagentLaunchPolicyError) RPCErrorData() json.RawMessage {
	if err := e.Validate(); err != nil {
		panic("marshal subagent launch policy error: " + err.Error())
	}
	data, err := json.Marshal(e)
	if err != nil {
		panic("marshal subagent launch policy error: " + err.Error())
	}
	return data
}

func DecodeSubagentLaunchPolicyError(data json.RawMessage, fallback string) error {
	generic := errors.New(genericSubagentPolicyMessage(fallback))
	var decoded SubagentLaunchPolicyError
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return generic
	}
	if err := decoded.Validate(); err != nil {
		return generic
	}
	return &decoded
}

func genericSubagentPolicyMessage(fallback string) string {
	if message := strings.TrimSpace(fallback); message != "" {
		return message
	}
	return "subagent launch rejected"
}

func intPointer(value int) *int {
	return &value
}
