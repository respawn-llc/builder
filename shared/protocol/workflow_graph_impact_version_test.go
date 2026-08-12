package protocol_test

import (
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowGraphImpactHardCutoverChangesProtocolFixture(t *testing.T) {
	if protocol.Version != "119" {
		t.Fatalf("Workflow graph identity hard cutover protocol version = %q, want 119", protocol.Version)
	}
	const edgeID = "55555555-5555-4555-8555-555555555555"
	response := protocol.NewSuccessResponse("request", serverapi.WorkflowGraphSavePreviewResponse{
		Changed: true,
		Impact: serverapi.WorkflowGraphSaveImpact{
			RemovedEdgeCount: 1,
			RemovedEntities: []serverapi.WorkflowGraphEntityReference{{
				EntityType: serverapi.WorkflowGraphEntityTypeEdge,
				EntityID:   edgeID,
			}},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{{
			Code: "confirmation_required", Message: "confirm", Count: 1,
			AffectedEntities: []serverapi.WorkflowGraphEntityReference{{
				EntityType: serverapi.WorkflowGraphEntityTypeEdge,
				EntityID:   edgeID,
			}},
		}},
	})
	var fixture serverapi.WorkflowGraphSavePreviewResponse
	if err := json.Unmarshal(response.Result, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if !fixture.Changed || fixture.Impact.RemovedEntities[0].EntityID != edgeID ||
		fixture.Blockers[0].AffectedEntities[0].EntityID != edgeID {
		t.Fatalf("fixture = %+v", fixture)
	}
}
