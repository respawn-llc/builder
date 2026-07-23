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

type CurrentNodeSchedulingState string

const (
	CurrentNodeSchedulingReady       CurrentNodeSchedulingState = "ready"
	CurrentNodeSchedulingAdmitted    CurrentNodeSchedulingState = "admitted"
	CurrentNodeSchedulingInterrupted CurrentNodeSchedulingState = "interrupted"
	CurrentNodeSchedulingFailed      CurrentNodeSchedulingState = "failed"
)

type CurrentNodeInterruptionReason string

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

type CurrentNode struct {
	Reference          CurrentNodeReference
	CurrentInputValues map[string]string
	PriorNodeValues    map[string]map[string]string
	SessionID          *runtimeids.SessionID
	Scheduling         *CurrentNodeScheduling
}

func NewCurrentNode(reference CurrentNodeReference, sessionID *runtimeids.SessionID, scheduling *CurrentNodeScheduling) (CurrentNode, error) {
	return NewCurrentNodeWithMaterializedValues(reference, nil, nil, sessionID, scheduling)
}

func NewCurrentNodeWithMaterializedValues(
	reference CurrentNodeReference,
	currentInputValues map[string]string,
	priorNodeValues map[string]map[string]string,
	sessionID *runtimeids.SessionID,
	scheduling *CurrentNodeScheduling,
) (CurrentNode, error) {
	if err := reference.Validate(); err != nil {
		return CurrentNode{}, err
	}
	if err := validateCurrentNodeValueEnvironment(currentInputValues, priorNodeValues); err != nil {
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
		PriorNodeValues:    cloneMaterializedPriorNodeValues(priorNodeValues),
		SessionID:          cloneCurrentNodeSessionID(sessionID),
		Scheduling:         cloneCurrentNodeScheduling(scheduling),
	}
	return node, nil
}

func validateCurrentNodeValueEnvironment(currentInputValues map[string]string, priorNodeValues map[string]map[string]string) error {
	for name := range currentInputValues {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("current node input name is required")
		}
	}
	for nodeKey, values := range priorNodeValues {
		if strings.TrimSpace(nodeKey) == "" {
			return fmt.Errorf("current node prior node key is required")
		}
		if len(values) == 0 {
			return fmt.Errorf("current node prior node values are required")
		}
		for outputName := range values {
			if strings.TrimSpace(outputName) == "" {
				return fmt.Errorf("current node prior output name is required")
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

func cloneMaterializedPriorNodeValues(values map[string]map[string]string) map[string]map[string]string {
	cloned := make(map[string]map[string]string, len(values))
	for nodeKey, outputValues := range values {
		cloned[nodeKey] = cloneMaterializedInputValues(outputValues)
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

type CurrentNodeMutationResult struct {
	Removed []CurrentNodeReference
	Created []CurrentNode
	Updated []CurrentNode
}
