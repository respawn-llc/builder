package workflowcontract

import "strings"

type WorkflowGraphEntityType string

const (
	WorkflowGraphEntityTypeEdge            WorkflowGraphEntityType = "edge"
	WorkflowGraphEntityTypeNode            WorkflowGraphEntityType = "node"
	WorkflowGraphEntityTypeNodeGroup       WorkflowGraphEntityType = "node_group"
	WorkflowGraphEntityTypeTransitionGroup WorkflowGraphEntityType = "transition_group"
)

type WorkflowGraphEntityReference struct {
	EntityType WorkflowGraphEntityType `json:"entity_type"`
	EntityID   string                  `json:"entity_id"`
}

// CompareWorkflowGraphEntityReferences defines canonical ordering by entity type, then persistent ID.
func CompareWorkflowGraphEntityReferences(left WorkflowGraphEntityReference, right WorkflowGraphEntityReference) int {
	if left.EntityType != right.EntityType {
		return strings.Compare(string(left.EntityType), string(right.EntityType))
	}
	return strings.Compare(left.EntityID, right.EntityID)
}
