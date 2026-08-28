package workflow

import (
	"core/shared/protocol"
	"core/shared/runtimeids"
	"encoding/json"
	"fmt"
)

type MaterializedContinuationSourceKind string

const (
	MaterializedContinuationSourceExact        MaterializedContinuationSourceKind = "exact"
	MaterializedContinuationSourceDeferredSelf MaterializedContinuationSourceKind = "deferred_self"
	MaterializedContinuationSourceLegacy       MaterializedContinuationSourceKind = "legacy"
	MaterializedContinuationSourceAbsent       MaterializedContinuationSourceKind = "absent"
)

type MaterializedContinuationSource struct {
	kind      MaterializedContinuationSourceKind
	sessionID *runtimeids.SessionID
}

func NewExactMaterializedContinuationSource(sessionID runtimeids.SessionID) (MaterializedContinuationSource, error) {
	if sessionID.IsZero() {
		return MaterializedContinuationSource{}, fmt.Errorf("materialized continuation source Session ID is required")
	}
	value := sessionID
	return MaterializedContinuationSource{kind: MaterializedContinuationSourceExact, sessionID: &value}, nil
}
func DeferredSelfMaterializedContinuationSource() MaterializedContinuationSource {
	return MaterializedContinuationSource{kind: MaterializedContinuationSourceDeferredSelf}
}

// TODO(KENT-538): Remove the legacy continuation-source kind in the first
// release after the KENT-522 migration release.
func LegacyMaterializedContinuationSource() MaterializedContinuationSource {
	return MaterializedContinuationSource{kind: MaterializedContinuationSourceLegacy}
}
func AbsentMaterializedContinuationSource() MaterializedContinuationSource {
	return MaterializedContinuationSource{kind: MaterializedContinuationSourceAbsent}
}
func (s MaterializedContinuationSource) Kind() MaterializedContinuationSourceKind { return s.kind }
func (s MaterializedContinuationSource) ExactSessionID() (runtimeids.SessionID, bool) {
	if s.kind != MaterializedContinuationSourceExact || s.sessionID == nil {
		return runtimeids.SessionID{}, false
	}
	return *s.sessionID, true
}
func (s MaterializedContinuationSource) Validate() error {
	switch s.kind {
	case MaterializedContinuationSourceExact:
		if s.sessionID == nil || s.sessionID.IsZero() {
			return fmt.Errorf("exact materialized continuation source requires a Session ID")
		}
	case MaterializedContinuationSourceDeferredSelf,
		MaterializedContinuationSourceLegacy,
		MaterializedContinuationSourceAbsent:
		if s.sessionID != nil {
			return fmt.Errorf("%s materialized continuation source must not carry a Session ID", s.kind)
		}
	default:
		return fmt.Errorf("materialized continuation source kind %q is invalid", s.kind)
	}
	return nil
}
func (s MaterializedContinuationSource) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(materializedContinuationSourceWire{Kind: s.kind, SessionID: cloneSessionID(s.sessionID)})
}
func (s *MaterializedContinuationSource) UnmarshalJSON(data []byte) error {
	var wire materializedContinuationSourceWire
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := MaterializedContinuationSource{kind: wire.Kind, sessionID: cloneSessionID(wire.SessionID)}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*s = decoded
	return nil
}

type materializedContinuationSourceWire struct {
	Kind      MaterializedContinuationSourceKind `json:"kind"`
	SessionID *runtimeids.SessionID              `json:"session_id,omitempty"`
}
type TargetSessionIntentKind string

const (
	TargetSessionIntentReuse   TargetSessionIntentKind = "reuse"
	TargetSessionIntentCreate  TargetSessionIntentKind = "create"
	TargetSessionIntentNoAgent TargetSessionIntentKind = "no_agent"
)

type TargetSessionIntent struct {
	kind      TargetSessionIntentKind
	sessionID *runtimeids.SessionID
}

func NewReuseTargetSessionIntent(sessionID runtimeids.SessionID) (TargetSessionIntent, error) {
	if sessionID.IsZero() {
		return TargetSessionIntent{}, fmt.Errorf("reused target Session ID is required")
	}
	value := sessionID
	return TargetSessionIntent{kind: TargetSessionIntentReuse, sessionID: &value}, nil
}
func CreateTargetSessionIntent() TargetSessionIntent {
	return TargetSessionIntent{kind: TargetSessionIntentCreate}
}
func NoAgentTargetSessionIntent() TargetSessionIntent {
	return TargetSessionIntent{kind: TargetSessionIntentNoAgent}
}
func (i TargetSessionIntent) Kind() TargetSessionIntentKind { return i.kind }
func (i TargetSessionIntent) SessionID() (runtimeids.SessionID, bool) {
	if i.kind != TargetSessionIntentReuse || i.sessionID == nil {
		return runtimeids.SessionID{}, false
	}
	return *i.sessionID, true
}
func (i TargetSessionIntent) Validate() error {
	switch i.kind {
	case TargetSessionIntentReuse:
		if i.sessionID == nil || i.sessionID.IsZero() {
			return fmt.Errorf("reuse target Session intent requires a Session ID")
		}
	case TargetSessionIntentCreate, TargetSessionIntentNoAgent:
		if i.sessionID != nil {
			return fmt.Errorf("%s target Session intent must not carry a Session ID", i.kind)
		}
	default:
		return fmt.Errorf("target Session intent kind %q is invalid", i.kind)
	}
	return nil
}
func (i TargetSessionIntent) MarshalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(targetSessionIntentWire{Kind: i.kind, SessionID: cloneSessionID(i.sessionID)})
}
func (i *TargetSessionIntent) UnmarshalJSON(data []byte) error {
	var wire targetSessionIntentWire
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := TargetSessionIntent{kind: wire.Kind, sessionID: cloneSessionID(wire.SessionID)}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*i = decoded
	return nil
}

type targetSessionIntentWire struct {
	Kind      TargetSessionIntentKind `json:"kind"`
	SessionID *runtimeids.SessionID   `json:"session_id,omitempty"`
}

type RetainedTargetUnavailableError struct {
	TaskID       TaskID
	SourceNodeID NodeID
	TargetNodeID NodeID
}

func (e RetainedTargetUnavailableError) Error() string {
	return fmt.Sprintf("retained target unavailable for Task %q from Node %q to Node %q", e.TaskID, e.SourceNodeID, e.TargetNodeID)
}
func cloneSessionID(sessionID *runtimeids.SessionID) *runtimeids.SessionID {
	if sessionID == nil {
		return nil
	}
	value := *sessionID
	return &value
}
