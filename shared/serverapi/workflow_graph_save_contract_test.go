package serverapi

import "testing"

func TestWorkflowGraphSaveContractRequiresExactCanonicalEntityImpact(t *testing.T) {
	entities := []WorkflowGraphEntityReference{{
		EntityType: WorkflowGraphEntityTypeNode,
		EntityID:   "node-1",
	}}
	impact := WorkflowGraphSaveImpact{
		RemovedNodeCount: 1, RemovedEntities: entities,
	}
	blocker := WorkflowGraphSaveBlocker{
		Code: "confirmation_required", Message: "confirm", Count: 1,
		AffectedEntities: entities,
	}
	if err := impact.Validate(); err != nil {
		t.Fatalf("valid impact: %v", err)
	} else if err := blocker.Validate(); err != nil {
		t.Fatalf("valid blocker: %v", err)
	}
	impact.RemovedEntities = nil
	blocker.AffectedEntities = nil
	if impact.Validate() == nil || blocker.Validate() == nil {
		t.Fatal("missing exact entity collections accepted")
	}
}
