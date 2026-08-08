package workflowscript

import (
	"fmt"

	"core/server/workflow"
)

type CurrentNodeIdentity struct {
	TaskID              workflow.TaskID               `json:"task_id"`
	NodeID              workflow.NodeID               `json:"node_id"`
	TransitionBranchKey *workflow.TransitionBranchKey `json:"transition_branch_key,omitempty"`
}

func IdentityForCurrentNode(reference workflow.CurrentNodeReference) (CurrentNodeIdentity, error) {
	if err := reference.Validate(); err != nil {
		return CurrentNodeIdentity{}, fmt.Errorf("workflow script current node: %w", err)
	}
	identity := CurrentNodeIdentity{
		TaskID: reference.TaskID,
		NodeID: reference.NodeID,
	}
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		identity.TransitionBranchKey = &branchKey
	}
	return identity, nil
}

func (i CurrentNodeIdentity) Validate() error {
	_, err := workflow.NewCurrentNodeReference(i.TaskID, i.NodeID, i.TransitionBranchKey)
	return err
}
