package protocol_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowGraphImpactHardCutoverChangesProtocolFixture(t *testing.T) {
	version, err := strconv.Atoi(protocol.Version)
	if err != nil {
		t.Fatalf("parse protocol version %q: %v", protocol.Version, err)
	}
	if version <= 104 {
		t.Fatalf("Workflow graph impact protocol version = %d, want newer than 104", version)
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
