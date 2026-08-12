package workflow

import (
	"core/shared/protocol"
	"core/shared/runtimeids"
	"encoding/json"
	"fmt"
	"strings"
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
type RetainedTargetStateKind string

const (
	RetainedTargetStateCurrent        RetainedTargetStateKind = "current"
	RetainedTargetStateHistoricalOnly RetainedTargetStateKind = "historical_only"
	RetainedTargetStateUnavailable    RetainedTargetStateKind = "unavailable"
	RetainedTargetStateInvalidCurrent RetainedTargetStateKind = "invalid_current"
)

type RetainedTargetState struct {
	kind            RetainedTargetStateKind
	retainedSession runtimeids.SessionID
	sourceSession   runtimeids.SessionID
	rejectedSession *runtimeids.SessionID
	invariantReason RetainedTargetInvariantReason
}

func HistoricalRetainedTarget() RetainedTargetState {
	return RetainedTargetState{kind: RetainedTargetStateHistoricalOnly}
}
func UnavailableRetainedTarget() RetainedTargetState {
	return RetainedTargetState{kind: RetainedTargetStateUnavailable}
}
func NewInvalidCurrentRetainedTarget(
	rejectedSessionID *runtimeids.SessionID,
	reason RetainedTargetInvariantReason,
) (RetainedTargetState, error) {
	switch reason {
	case RetainedTargetInvariantProoflessCurrentTarget, RetainedTargetInvariantStructurallyInvalidProof:
	default:
		return RetainedTargetState{}, fmt.Errorf("invalid current retained target reason %q is not supported", reason)
	}
	var rejected *runtimeids.SessionID
	if rejectedSessionID != nil {
		if rejectedSessionID.IsZero() {
			return RetainedTargetState{}, fmt.Errorf("rejected retained Session ID must be non-zero when present")
		}
		value := *rejectedSessionID
		rejected = &value
	}
	return RetainedTargetState{kind: RetainedTargetStateInvalidCurrent, rejectedSession: rejected, invariantReason: reason}, nil
}
func NewCurrentRetainedTarget(retainedSessionID, sourceSessionID runtimeids.SessionID) (RetainedTargetState, error) {
	if retainedSessionID.IsZero() {
		return RetainedTargetState{}, fmt.Errorf("retained target Session ID is required")
	}
	if sourceSessionID.IsZero() {
		return RetainedTargetState{}, fmt.Errorf("retained target source Session ID is required")
	}
	return RetainedTargetState{kind: RetainedTargetStateCurrent, retainedSession: retainedSessionID, sourceSession: sourceSessionID}, nil
}

type RetainedTargetEvaluationRequest struct {
	TaskID        TaskID
	SourceNodeID  NodeID
	TargetNodeID  NodeID
	ContextSource ContextSource
	ActiveSource  MaterializedContinuationSource
	Target        RetainedTargetState
}
type RetainedTargetDecision struct {
	TargetSession TargetSessionIntent
	ActiveSource  MaterializedContinuationSource
	invariant     *RetainedTargetInvariantDetail
}

func (d RetainedTargetDecision) InvariantDetail() (RetainedTargetInvariantDetail, bool) {
	if d.invariant == nil {
		return RetainedTargetInvariantDetail{}, false
	}
	return *d.invariant, true
}

type RetainedTargetInvariantReason string

const (
	RetainedTargetInvariantActiveSourceUnavailable  RetainedTargetInvariantReason = "active_source_unavailable"
	RetainedTargetInvariantProoflessCurrentTarget   RetainedTargetInvariantReason = "proofless_current_target"
	RetainedTargetInvariantStructurallyInvalidProof RetainedTargetInvariantReason = "structurally_invalid_proof"
)

type RetainedTargetInvariantDetail struct {
	TaskID                    TaskID
	SourceNodeID              NodeID
	TargetNodeID              NodeID
	ActiveSourceSessionID     *runtimeids.SessionID
	RejectedRetainedSessionID *runtimeids.SessionID
	Reason                    RetainedTargetInvariantReason
}
type RetainedTargetInvariantError struct {
	Detail RetainedTargetInvariantDetail
}

func (e RetainedTargetInvariantError) Error() string {
	return fmt.Sprintf("retained target invariant %q for Task %q from Node %q to Node %q",
		e.Detail.Reason, e.Detail.TaskID, e.Detail.SourceNodeID, e.Detail.TargetNodeID)
}

type RetainedTargetUnavailableError struct {
	TaskID       TaskID
	SourceNodeID NodeID
	TargetNodeID NodeID
}

func (e RetainedTargetUnavailableError) Error() string {
	return fmt.Sprintf("retained target unavailable for Task %q from Node %q to Node %q",
		e.TaskID, e.SourceNodeID, e.TargetNodeID)
}
func EvaluateRetainedTarget(request RetainedTargetEvaluationRequest) (RetainedTargetDecision, error) {
	if strings.TrimSpace(string(request.TaskID)) == "" {
		return RetainedTargetDecision{}, fmt.Errorf("retained target Task ID is required")
	}
	if strings.TrimSpace(string(request.SourceNodeID)) == "" {
		return RetainedTargetDecision{}, fmt.Errorf("retained target source Node ID is required")
	}
	if strings.TrimSpace(string(request.TargetNodeID)) == "" {
		return RetainedTargetDecision{}, fmt.Errorf("retained target target Node ID is required")
	}
	contextSource := CanonicalContextSource(request.ContextSource)
	if contextSource.Kind != ContextSourcePreviousTarget && contextSource.Kind != ContextSourcePreviousTargetOrNew {
		return RetainedTargetDecision{}, fmt.Errorf("retained target evaluation requires a retained-target Context Source")
	}
	if err := request.ActiveSource.Validate(); err != nil {
		return RetainedTargetDecision{}, fmt.Errorf("retained target active source: %w", err)
	}
	if request.Target.kind == RetainedTargetStateUnavailable {
		if contextSource.Kind == ContextSourcePreviousTargetOrNew {
			return RetainedTargetDecision{TargetSession: CreateTargetSessionIntent(), ActiveSource: request.ActiveSource}, nil
		}
		return RetainedTargetDecision{}, RetainedTargetUnavailableError{
			TaskID:       request.TaskID,
			SourceNodeID: request.SourceNodeID,
			TargetNodeID: request.TargetNodeID,
		}
	}
	activeSourceID, ok := request.ActiveSource.ExactSessionID()
	if !ok {
		detail := RetainedTargetInvariantDetail{
			TaskID:                    request.TaskID,
			SourceNodeID:              request.SourceNodeID,
			TargetNodeID:              request.TargetNodeID,
			RejectedRetainedSessionID: retainedTargetRejectedSessionID(request.Target),
			Reason:                    RetainedTargetInvariantActiveSourceUnavailable,
		}
		return RetainedTargetDecision{}, RetainedTargetInvariantError{Detail: detail}
	}
	switch request.Target.kind {
	case RetainedTargetStateInvalidCurrent:
		detail := RetainedTargetInvariantDetail{
			TaskID:                    request.TaskID,
			SourceNodeID:              request.SourceNodeID,
			TargetNodeID:              request.TargetNodeID,
			ActiveSourceSessionID:     cloneSessionID(&activeSourceID),
			RejectedRetainedSessionID: cloneSessionID(request.Target.rejectedSession),
			Reason:                    request.Target.invariantReason,
		}
		if contextSource.Kind == ContextSourcePreviousTargetOrNew {
			return RetainedTargetDecision{
				TargetSession: CreateTargetSessionIntent(), ActiveSource: request.ActiveSource, invariant: &detail,
			}, nil
		}
		return RetainedTargetDecision{}, RetainedTargetInvariantError{Detail: detail}
	case RetainedTargetStateHistoricalOnly:
		return RetainedTargetDecision{TargetSession: CreateTargetSessionIntent(), ActiveSource: request.ActiveSource}, nil
	case RetainedTargetStateCurrent:
	default:
		return RetainedTargetDecision{}, fmt.Errorf("retained target state is invalid")
	}
	if request.Target.sourceSession != activeSourceID {
		return RetainedTargetDecision{TargetSession: CreateTargetSessionIntent(), ActiveSource: request.ActiveSource}, nil
	}
	targetSession, err := NewReuseTargetSessionIntent(request.Target.retainedSession)
	if err != nil {
		return RetainedTargetDecision{}, err
	}
	return RetainedTargetDecision{TargetSession: targetSession, ActiveSource: request.ActiveSource}, nil
}
func retainedTargetRejectedSessionID(target RetainedTargetState) *runtimeids.SessionID {
	switch target.kind {
	case RetainedTargetStateCurrent:
		return cloneSessionID(&target.retainedSession)
	case RetainedTargetStateInvalidCurrent:
		return cloneSessionID(target.rejectedSession)
	default:
		return nil
	}
}
func cloneSessionID(sessionID *runtimeids.SessionID) *runtimeids.SessionID {
	if sessionID == nil {
		return nil
	}
	value := *sessionID
	return &value
}
