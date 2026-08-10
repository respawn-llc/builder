package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/worktreecontract"
	"github.com/google/uuid"
)

type TransitionBranchKey string

type CurrentNodeReference struct {
	TaskID              TaskID
	NodeID              NodeID
	transitionBranchKey *TransitionBranchKey
}

func NewCurrentNodeReference(taskID TaskID, nodeID NodeID, transitionBranchKey *TransitionBranchKey) (CurrentNodeReference, error) {
	ref := CurrentNodeReference{
		TaskID: TaskID(strings.TrimSpace(string(taskID))),
		NodeID: NodeID(strings.TrimSpace(string(nodeID))),
	}
	if transitionBranchKey != nil {
		branchKey := TransitionBranchKey(strings.TrimSpace(string(*transitionBranchKey)))
		ref.transitionBranchKey = &branchKey
	}
	if err := ref.Validate(); err != nil {
		return CurrentNodeReference{}, err
	}
	return ref, nil
}

func (r CurrentNodeReference) Validate() error {
	if r.TaskID == "" {
		return fmt.Errorf("current node task id is required")
	}
	if r.NodeID == "" {
		return fmt.Errorf("current node id is required")
	}
	if r.transitionBranchKey != nil && *r.transitionBranchKey == "" {
		return fmt.Errorf("current node transition branch key must be non-empty when present")
	}
	return nil
}

func (r CurrentNodeReference) Equal(other CurrentNodeReference) bool {
	if r.TaskID != other.TaskID || r.NodeID != other.NodeID {
		return false
	}
	left, leftOK := r.TransitionBranchKey()
	right, rightOK := other.TransitionBranchKey()
	return leftOK == rightOK && (!leftOK || left == right)
}

func (r CurrentNodeReference) TransitionBranchKey() (TransitionBranchKey, bool) {
	if r.transitionBranchKey == nil {
		return "", false
	}
	return *r.transitionBranchKey, true
}

func (r CurrentNodeReference) IsBranchScoped() bool {
	_, ok := r.TransitionBranchKey()
	return ok
}

// CurrentNodeReferenceKey is the canonical comparable identity of a Current
// Node reference. The two concrete variants make branch absence structural:
// callers never invent an empty branch-key sentinel to index live state.
type CurrentNodeReferenceKey interface {
	currentNodeReferenceKey()
}

type serialCurrentNodeReferenceKey struct {
	taskID TaskID
	nodeID NodeID
}

func (serialCurrentNodeReferenceKey) currentNodeReferenceKey() {}

type branchCurrentNodeReferenceKey struct {
	taskID    TaskID
	nodeID    NodeID
	branchKey TransitionBranchKey
}

func (branchCurrentNodeReferenceKey) currentNodeReferenceKey() {}

func (r CurrentNodeReference) Key() (CurrentNodeReferenceKey, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if branchKey, branchScoped := r.TransitionBranchKey(); branchScoped {
		return branchCurrentNodeReferenceKey{
			taskID:    r.TaskID,
			nodeID:    r.NodeID,
			branchKey: branchKey,
		}, nil
	}
	return serialCurrentNodeReferenceKey{taskID: r.TaskID, nodeID: r.NodeID}, nil
}

type CurrentNodeSchedulingState string

const (
	CurrentNodeSchedulingReady       CurrentNodeSchedulingState = "ready"
	CurrentNodeSchedulingAdmitted    CurrentNodeSchedulingState = "admitted"
	CurrentNodeSchedulingInterrupted CurrentNodeSchedulingState = "interrupted"
	CurrentNodeSchedulingFailed      CurrentNodeSchedulingState = "failed"
)

type CurrentNodeInterruptionReason string

const (
	CurrentNodeInterruptionReasonUserInterrupt   CurrentNodeInterruptionReason = "user_interrupt"
	CurrentNodeInterruptionReasonRuntimeCanceled CurrentNodeInterruptionReason = "workflow_runtime_canceled"
)

func IsActionableCurrentNodeInterruptionReason(reason CurrentNodeInterruptionReason) bool {
	switch CurrentNodeInterruptionReason(strings.TrimSpace(string(reason))) {
	case "", CurrentNodeInterruptionReasonUserInterrupt, CurrentNodeInterruptionReasonRuntimeCanceled:
		return false
	default:
		return true
	}
}

type CurrentNodeInterruptionDetail struct {
	Code                                 string
	Fields                               map[string]string
	ConfiguredExecutionTargetUnavailable *ConfiguredExecutionTargetUnavailable `json:"configured_execution_target_unavailable,omitempty"`
	SetupRecovery                        *CurrentNodeSetupRecoveryDetail       `json:"setup_recovery,omitempty"`
}

func DecodeCurrentNodeInterruptionDetail(raw string) (CurrentNodeInterruptionDetail, error) {
	var detail CurrentNodeInterruptionDetail
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return CurrentNodeInterruptionDetail{}, fmt.Errorf("decode current node interruption detail: %w", err)
	}
	return detail, nil
}

func (d *CurrentNodeInterruptionDetail) UnmarshalJSON(data []byte) error {
	type wire CurrentNodeInterruptionDetail
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*d = CurrentNodeInterruptionDetail(decoded)
	return d.Validate()
}

func (d CurrentNodeInterruptionDetail) Validate() error {
	for name := range d.Fields {
		if strings.TrimSpace(name) == "" {
			return errors.New("current node interruption detail field name is required")
		}
	}
	if d.ConfiguredExecutionTargetUnavailable != nil {
		if err := d.ConfiguredExecutionTargetUnavailable.Validate(); err != nil {
			return err
		}
	}
	if d.SetupRecovery != nil {
		if _, duplicated := d.Fields[CurrentNodeInterruptionDiagnosticField]; duplicated {
			return errors.New("setup recovery diagnostic must not be duplicated in interruption fields")
		}
		return d.SetupRecovery.Validate()
	}
	return nil
}

func (d CurrentNodeInterruptionDetail) SetupOperationID() (*uuid.UUID, error) {
	if d.SetupRecovery == nil {
		return nil, nil
	}
	if err := d.SetupRecovery.Validate(); err != nil {
		return nil, err
	}
	value := d.SetupRecovery.SetupOperationID
	return &value, nil
}

type CurrentNodeSetupRecoveryDetail = worktreecontract.SetupRecoveryDetail[uuid.UUID, ExecutionTargetSelection]

type CurrentNodeSetupRecoveryCause = worktreecontract.SetupFailureKind

type CurrentNodeSetupRequirement = worktreecontract.SetupRequirement

const (
	CurrentNodeSetupRecoveryCauseProcessExit       = worktreecontract.SetupFailureProcessExit
	CurrentNodeSetupRecoveryCauseTimeout           = worktreecontract.SetupFailureTimeout
	CurrentNodeSetupRecoveryCauseTargetPreparation = worktreecontract.SetupFailureTargetPreparation
	CurrentNodeSetupRecoveryCauseOperational       = worktreecontract.SetupFailureOperational
	CurrentNodeSetupRequirementRequired            = worktreecontract.SetupRequirementRequired
	CurrentNodeSetupRequirementAlreadyCompleted    = worktreecontract.SetupRequirementAlreadyCompleted
)

type CurrentNodeRetainedWorktree = worktreecontract.RetainedWorktree

const CurrentNodeInterruptionDiagnosticField = "error"

func NewCurrentNodeInterruptionDetail(code string, diagnostic error) CurrentNodeInterruptionDetail {
	detail := CurrentNodeInterruptionDetail{
		Code:   strings.TrimSpace(code),
		Fields: map[string]string{},
	}
	if diagnostic != nil {
		detail.Fields = map[string]string{CurrentNodeInterruptionDiagnosticField: diagnostic.Error()}
	}
	return detail
}

func (d CurrentNodeInterruptionDetail) Diagnostic() *string {
	if d.SetupRecovery != nil {
		value := d.SetupRecovery.Diagnostic
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return &value
	}
	if d.Fields == nil {
		return nil
	}
	value, ok := d.Fields[CurrentNodeInterruptionDiagnosticField]
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

type CurrentNodeInterruption struct {
	Reason     CurrentNodeInterruptionReason
	Detail     CurrentNodeInterruptionDetail
	OccurredAt time.Time
}

type CurrentNodeScheduling struct {
	State        CurrentNodeSchedulingState
	Interruption *CurrentNodeInterruption
}

type MaterializedPriorValues struct {
	TransitionParameters map[ModelKey]map[string]string `json:"transition_parameters"`
}

func (v MaterializedPriorValues) Clone() MaterializedPriorValues {
	return cloneMaterializedPriorValues(v)
}

func (v MaterializedPriorValues) TransitionParameter(transitionKey ModelKey, parameterName string) (string, bool) {
	value, exists := v.TransitionParameters[transitionKey][parameterName]
	return value, exists
}

func (v *MaterializedPriorValues) SetTransitionParameter(transitionKey ModelKey, parameterName, value string) {
	if v.TransitionParameters == nil {
		v.TransitionParameters = make(map[ModelKey]map[string]string)
	}
	if v.TransitionParameters[transitionKey] == nil {
		v.TransitionParameters[transitionKey] = make(map[string]string)
	}
	v.TransitionParameters[transitionKey][parameterName] = value
}

type CurrentNode struct {
	Reference               CurrentNodeReference
	EnteredByEdgeID         *EdgeID
	CurrentInputValues      map[string]string
	PriorValues             MaterializedPriorValues
	SessionID               *runtimeids.SessionID
	Scheduling              *CurrentNodeScheduling
	AgentExecutionSelection *AgentExecutionSelection
}

func NewCurrentNode(reference CurrentNodeReference, sessionID *runtimeids.SessionID, scheduling *CurrentNodeScheduling) (CurrentNode, error) {
	return NewCurrentNodeWithMaterializedValues(reference, nil, MaterializedPriorValues{}, sessionID, scheduling)
}

func NewCurrentNodeWithMaterializedValues(
	reference CurrentNodeReference,
	currentInputValues map[string]string,
	priorValues MaterializedPriorValues,
	sessionID *runtimeids.SessionID,
	scheduling *CurrentNodeScheduling,
) (CurrentNode, error) {
	return NewCurrentNodeWithExecutionSelection(reference, currentInputValues, priorValues, sessionID, scheduling, nil)
}

func NewCurrentNodeWithExecutionSelection(
	reference CurrentNodeReference,
	currentInputValues map[string]string,
	priorValues MaterializedPriorValues,
	sessionID *runtimeids.SessionID,
	scheduling *CurrentNodeScheduling,
	selection *AgentExecutionSelection,
) (CurrentNode, error) {
	if err := reference.Validate(); err != nil {
		return CurrentNode{}, err
	}
	if err := validateCurrentNodeValueEnvironment(currentInputValues, priorValues); err != nil {
		return CurrentNode{}, err
	}
	if sessionID != nil && sessionID.IsZero() {
		return CurrentNode{}, fmt.Errorf("current node session id must be non-zero when present")
	}
	if err := validateCurrentNodeScheduling(scheduling); err != nil {
		return CurrentNode{}, err
	}
	var clonedSelection *AgentExecutionSelection
	if selection != nil {
		if err := selection.Validate(); err != nil {
			return CurrentNode{}, fmt.Errorf("current node Agent execution selection: %w", err)
		}
		value := selection.Clone()
		clonedSelection = &value
	}
	node := CurrentNode{
		Reference:               reference,
		CurrentInputValues:      cloneMaterializedInputValues(currentInputValues),
		PriorValues:             cloneMaterializedPriorValues(priorValues),
		SessionID:               cloneCurrentNodeSessionID(sessionID),
		Scheduling:              cloneCurrentNodeScheduling(scheduling),
		AgentExecutionSelection: clonedSelection,
	}
	return node, nil
}

func NewCurrentNodeWithEntry(
	reference CurrentNodeReference,
	enteredByEdgeID *EdgeID,
	currentInputValues map[string]string,
	priorValues MaterializedPriorValues,
	sessionID *runtimeids.SessionID,
	scheduling *CurrentNodeScheduling,
) (CurrentNode, error) {
	node, err := NewCurrentNodeWithMaterializedValues(reference, currentInputValues, priorValues, sessionID, scheduling)
	if err != nil {
		return CurrentNode{}, err
	}
	if enteredByEdgeID == nil {
		return node, nil
	}
	edgeID := EdgeID(strings.TrimSpace(string(*enteredByEdgeID)))
	if edgeID == "" {
		return CurrentNode{}, fmt.Errorf("current node entering edge id must be non-empty when present")
	}
	node.EnteredByEdgeID = &edgeID
	return node, nil
}

func validateCurrentNodeValueEnvironment(currentInputValues map[string]string, priorValues MaterializedPriorValues) error {
	for name := range currentInputValues {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("current node input name is required")
		}
	}
	if err := validateMaterializedPriorValueNamespace("transition parameter", priorValues.TransitionParameters); err != nil {
		return err
	}
	return nil
}

func validateMaterializedPriorValueNamespace(kind string, values map[ModelKey]map[string]string) error {
	for namespace, fields := range values {
		if strings.TrimSpace(string(namespace)) == "" {
			return fmt.Errorf("current node prior %s key is required", kind)
		}
		if len(fields) == 0 {
			return fmt.Errorf("current node prior %s values are required", kind)
		}
		for fieldName := range fields {
			if strings.TrimSpace(fieldName) == "" {
				return fmt.Errorf("current node prior %s field name is required", kind)
			}
		}
	}
	return nil
}

func validateCurrentNodeScheduling(scheduling *CurrentNodeScheduling) error {
	if scheduling == nil {
		return nil
	}
	switch scheduling.State {
	case CurrentNodeSchedulingReady, CurrentNodeSchedulingAdmitted, CurrentNodeSchedulingInterrupted, CurrentNodeSchedulingFailed:
	default:
		return fmt.Errorf("current node scheduling state is invalid")
	}
	if scheduling.State == CurrentNodeSchedulingInterrupted {
		if scheduling.Interruption == nil {
			return fmt.Errorf("interrupted current node requires interruption details")
		}
		if strings.TrimSpace(string(scheduling.Interruption.Reason)) == "" {
			return fmt.Errorf("current node interruption reason is required")
		}
		if scheduling.Interruption.OccurredAt.IsZero() {
			return fmt.Errorf("current node interruption occurrence time is required")
		}
		if unavailable := scheduling.Interruption.Detail.ConfiguredExecutionTargetUnavailable; unavailable != nil {
			if err := unavailable.Validate(); err != nil {
				return fmt.Errorf("current node configured execution target interruption: %w", err)
			}
		}
		return nil
	}
	if scheduling.Interruption != nil {
		return fmt.Errorf("only interrupted current nodes may carry interruption details")
	}
	return nil
}

func cloneCurrentNodeSessionID(sessionID *runtimeids.SessionID) *runtimeids.SessionID {
	if sessionID == nil {
		return nil
	}
	cloned := *sessionID
	return &cloned
}

func cloneCurrentNodeScheduling(scheduling *CurrentNodeScheduling) *CurrentNodeScheduling {
	if scheduling == nil {
		return nil
	}
	cloned := *scheduling
	if scheduling.Interruption != nil {
		interruption := *scheduling.Interruption
		interruption.Detail.Fields = cloneStringMap(interruption.Detail.Fields)
		if unavailable := interruption.Detail.ConfiguredExecutionTargetUnavailable; unavailable != nil {
			clonedUnavailable := *unavailable
			if unavailable.RequestedRef != nil {
				requestedRef := *unavailable.RequestedRef
				clonedUnavailable.RequestedRef = &requestedRef
			}
			interruption.Detail.ConfiguredExecutionTargetUnavailable = &clonedUnavailable
		}
		cloned.Interruption = &interruption
	}
	return &cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneMaterializedInputValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneMaterializedPriorValues(values MaterializedPriorValues) MaterializedPriorValues {
	return MaterializedPriorValues{
		TransitionParameters: cloneMaterializedPriorValueNamespace(values.TransitionParameters),
	}
}

func cloneMaterializedPriorValueNamespace(values map[ModelKey]map[string]string) map[ModelKey]map[string]string {
	cloned := make(map[ModelKey]map[string]string, len(values))
	for namespace, fields := range values {
		cloned[namespace] = cloneMaterializedInputValues(fields)
	}
	return cloned
}

type ApprovalID string

func NewApprovalID() ApprovalID {
	return ApprovalID(uuid.NewString())
}

func ParseApprovalID(raw string) (ApprovalID, error) {
	trimmed := strings.TrimSpace(raw)
	if err := runtimeids.ValidateUUIDv4(trimmed, "approval_id"); err != nil {
		return "", err
	}
	return ApprovalID(trimmed), nil
}

func (id ApprovalID) String() string {
	return string(id)
}

func (id ApprovalID) Validate() error {
	_, err := ParseApprovalID(id.String())
	return err
}

type PendingApproval struct {
	ID              ApprovalID
	Source          CurrentNodeReference
	SourceSessionID *runtimeids.SessionID
	WorkflowVersion int64
	Transition      PendingApprovalTransition
	Commentary      string
	OutputValues    map[string]string
	Branches        []PendingApprovalBranch
	CreatedAt       time.Time
}

type PendingApprovalTransition struct {
	Group             TransitionGroup
	SourceDisplayName string
}

type PendingApprovalBranch struct {
	TransitionBranchKey     TransitionBranchKey
	Target                  PendingApprovalTarget
	EffectiveEdge           Edge
	ContextSourceResolution PendingApprovalContextSourceResolution
}

type PendingApprovalTarget struct {
	CurrentNode CurrentNode
	DisplayName string
	NodeKind    NodeKind
}

type PendingApprovalContextSourceResolution struct {
	SessionID *runtimeids.SessionID
}

type CurrentNodeMutationResult struct {
	Removed []CurrentNodeReference
	Created []CurrentNode
	Updated []CurrentNode
}
