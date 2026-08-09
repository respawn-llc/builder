package protocol_test

import (
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowGraphImpactHardCutoverChangesProtocolFixture(t *testing.T) {
	if protocol.Version == "101" {
		t.Fatal("Workflow graph impact retained the pre-contract protocol version")
	}
	response := protocol.NewSuccessResponse("request", serverapi.WorkflowGraphSavePreviewResponse{
		Changed: true,
		Impact: serverapi.WorkflowGraphSaveImpact{
			RemovedEdgeCount: 1,
			RemovedEntities: []serverapi.WorkflowGraphEntityReference{{
				EntityType: serverapi.WorkflowGraphEntityTypeEdge,
				EntityID:   "edge-1",
			}},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{{
			Code: "confirmation_required", Message: "confirm", Count: 1,
			AffectedEntities: []serverapi.WorkflowGraphEntityReference{{
				EntityType: serverapi.WorkflowGraphEntityTypeEdge,
				EntityID:   "edge-1",
			}},
		}},
	})
	var fixture serverapi.WorkflowGraphSavePreviewResponse
	if err := json.Unmarshal(response.Result, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if !fixture.Changed || fixture.Impact.RemovedEntities[0].EntityID != "edge-1" ||
		fixture.Blockers[0].AffectedEntities[0].EntityID != "edge-1" {
		t.Fatalf("fixture = %+v", fixture)
	}
}
