package workflow

import (
	"fmt"
	"strings"
	"time"

	"core/shared/runtimeids"
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
	Code   string
	Fields map[string]string
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
	NodeOutputs          map[ModelKey]map[string]string `json:"node_outputs"`
	TransitionParameters map[ModelKey]map[string]string `json:"transition_parameters"`
}

func (v MaterializedPriorValues) Clone() MaterializedPriorValues {
	return cloneMaterializedPriorValues(v)
}

func (v MaterializedPriorValues) NodeOutput(nodeKey ModelKey, outputName string) (string, bool) {
	value, exists := v.NodeOutputs[nodeKey][outputName]
	return value, exists
}

func (v MaterializedPriorValues) TransitionParameter(transitionKey ModelKey, parameterName string) (string, bool) {
	value, exists := v.TransitionParameters[transitionKey][parameterName]
	return value, exists
}

func (v MaterializedPriorValues) Value(requirement PriorValueRequirement) (string, bool) {
	switch requirement.Origin() {
	case PriorValueOriginNodeOutput:
		return v.NodeOutput(requirement.Namespace(), requirement.ValueName())
	case PriorValueOriginTransitionParameter:
		return v.TransitionParameter(requirement.Namespace(), requirement.ValueName())
	default:
		panic(fmt.Sprintf("unsupported prior value origin %q", requirement.Origin()))
	}
}

func (v *MaterializedPriorValues) SetNodeOutput(nodeKey ModelKey, outputName, value string) {
	if v.NodeOutputs == nil {
		v.NodeOutputs = make(map[ModelKey]map[string]string)
	}
	if v.NodeOutputs[nodeKey] == nil {
		v.NodeOutputs[nodeKey] = make(map[string]string)
	}
	v.NodeOutputs[nodeKey][outputName] = value
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

func (v *MaterializedPriorValues) Set(requirement PriorValueRequirement, value string) {
	switch requirement.Origin() {
	case PriorValueOriginNodeOutput:
		v.SetNodeOutput(requirement.Namespace(), requirement.ValueName(), value)
	case PriorValueOriginTransitionParameter:
		v.SetTransitionParameter(requirement.Namespace(), requirement.ValueName(), value)
	default:
		panic(fmt.Sprintf("unsupported prior value origin %q", requirement.Origin()))
	}
}

type CurrentNode struct {
	Reference          CurrentNodeReference
	EnteredByEdgeID    *EdgeID
	CurrentInputValues map[string]string
	PriorValues        MaterializedPriorValues
	SessionID          *runtimeids.SessionID
	Scheduling         *CurrentNodeScheduling
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
	node := CurrentNode{
		Reference:          reference,
		CurrentInputValues: cloneMaterializedInputValues(currentInputValues),
		PriorValues:        cloneMaterializedPriorValues(priorValues),
		SessionID:          cloneCurrentNodeSessionID(sessionID),
		Scheduling:         cloneCurrentNodeScheduling(scheduling),
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
	if err := validateMaterializedPriorValueNamespace("node output", priorValues.NodeOutputs); err != nil {
		return err
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
		NodeOutputs:          cloneMaterializedPriorValueNamespace(values.NodeOutputs),
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
}

type PendingApprovalContextSourceResolution struct {
	SessionID *runtimeids.SessionID
}

type CurrentNodeMutationResult struct {
	Removed []CurrentNodeReference
	Created []CurrentNode
	Updated []CurrentNode
}
