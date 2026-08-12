package workflowview

import (
	"encoding/json"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestValidationErrorsInheritOnlyAnExplicitOptionalWorkflowID(t *testing.T) {
	inheritedID := runtimeids.NewWorkflowID()
	explicitID := runtimeids.NewWorkflowID()
	projected := ValidationErrors(&inheritedID, []workflow.ValidationError{
		{Code: workflow.CodeMissingNodeID},
		{Code: workflow.CodeMissingEdgeID, WorkflowID: &explicitID},
	})

	if projected[0].WorkflowID == nil || *projected[0].WorkflowID != inheritedID {
		t.Fatalf("inherited workflow id = %v, want %q", projected[0].WorkflowID, inheritedID)
	}
	if projected[1].WorkflowID == nil || *projected[1].WorkflowID != explicitID {
		t.Fatalf("explicit workflow id = %v, want %q", projected[1].WorkflowID, explicitID)
	}
}

func TestValidationErrorsProjectOptionalGraphIdentitiesWithoutEmptySentinels(t *testing.T) {
	nodeID := workflow.NodeID("3f154c4e-b76f-48a8-9439-4ad0b0ad0827")
	groupID := workflow.TransitionGroupID("58e548ca-6673-4b2d-9227-af64b09a7242")
	edgeID := workflow.EdgeID("5d96dace-5465-4484-bf1c-1f319e549973")
	providerEdgeID := workflow.EdgeID("a6d17470-e2e7-4e69-bb66-8b357956be90")

	projected := ValidationErrors(nil, []workflow.ValidationError{
		{},
		{
			NodeID:            &nodeID,
			TransitionGroupID: &groupID,
			EdgeID:            &edgeID,
			ProviderEdgeID:    &providerEdgeID,
		},
	})

	if projected[0].NodeID != nil || projected[0].TransitionGroupID != nil || projected[0].EdgeID != nil {
		t.Fatalf("absent graph identities = %#v, want nil", projected[0])
	}
	if projected[0].Details != nil {
		t.Fatalf("absent details = %#v, want nil", projected[0].Details)
	}
	encoded, err := json.Marshal(projected[0])
	if err != nil {
		t.Fatalf("marshal absent graph identities: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode absent graph identities: %v", err)
	}
	for _, field := range []string{"node_id", "transition_group_id", "edge_id"} {
		value, present := payload[field]
		if !present || value != nil {
			t.Fatalf("%s = %#v (present=%t), want explicit null", field, value, present)
		}
	}
	if projected[1].NodeID == nil || *projected[1].NodeID != string(nodeID) {
		t.Fatalf("node id = %v, want %q", projected[1].NodeID, nodeID)
	}
	if projected[1].TransitionGroupID == nil || *projected[1].TransitionGroupID != string(groupID) {
		t.Fatalf("transition group id = %v, want %q", projected[1].TransitionGroupID, groupID)
	}
	if projected[1].EdgeID == nil || *projected[1].EdgeID != string(edgeID) {
		t.Fatalf("edge id = %v, want %q", projected[1].EdgeID, edgeID)
	}
	if projected[1].Details == nil || projected[1].Details.ProviderEdgeID == nil || *projected[1].Details.ProviderEdgeID != string(providerEdgeID) {
		t.Fatalf("provider edge id = %#v, want %q", projected[1].Details, providerEdgeID)
	}
}
