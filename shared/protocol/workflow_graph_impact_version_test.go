package protocol_test

import (
	"encoding/json"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestWorkflowGraphImpactChangesProtocolVersionAndResponseFixture(t *testing.T) {
	if protocol.Version == "101" {
		t.Fatal("Workflow graph impact retained the pre-contract protocol version")
	}
	response := protocol.NewSuccessResponse("request-1", serverapi.WorkflowGraphSavePreviewResponse{
		Changed:        true,
		CurrentVersion: 12,
		Impact: serverapi.WorkflowGraphSaveImpact{
			RemovedNodeGroupCount: 1,
			RemovedEntities: []serverapi.WorkflowGraphEntityReference{{
				EntityType: serverapi.WorkflowGraphEntityTypeNodeGroup,
				EntityID:   "group-1",
			}},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{{
			Code:    "confirmation_required",
			Message: "confirmation required",
			Count:   1,
			AffectedEntities: []serverapi.WorkflowGraphEntityReference{{
				EntityType: serverapi.WorkflowGraphEntityTypeNodeGroup,
				EntityID:   "group-1",
			}},
		}},
	})
	var fixture struct {
		Changed bool `json:"changed"`
		Impact  struct {
			RemovedNodeGroupCount int64                                    `json:"removed_node_group_count"`
			RemovedEntities       []serverapi.WorkflowGraphEntityReference `json:"removed_entities"`
		} `json:"impact"`
		Blockers []serverapi.WorkflowGraphSaveBlocker `json:"blockers"`
	}
	if err := json.Unmarshal(response.Result, &fixture); err != nil {
		t.Fatalf("decode Workflow graph impact fixture: %v", err)
	}
	if !fixture.Changed || fixture.Impact.RemovedNodeGroupCount != 1 {
		t.Fatalf("Workflow graph impact fixture = %+v, want changed response with one removed Node Group", fixture)
	}
	if len(fixture.Impact.RemovedEntities) != 1 ||
		fixture.Impact.RemovedEntities[0].EntityType != serverapi.WorkflowGraphEntityTypeNodeGroup ||
		len(fixture.Blockers) != 1 ||
		len(fixture.Blockers[0].AffectedEntities) != 1 {
		t.Fatalf("Workflow graph identity fixture = %+v, want exact removed and affected identities", fixture)
	}

	workflowID, err := runtimeids.ParseWorkflowID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Workflow graph confirmation fixture ID: %v", err)
	}
	requestJSON, err := json.Marshal(serverapi.WorkflowGraphSaveRequest{
		WorkflowID:   workflowID,
		Confirmation: &serverapi.WorkflowGraphSaveConfirmation{ExpectedRemovedNodeGroupCount: 1},
	})
	if err != nil {
		t.Fatalf("encode Workflow graph confirmation fixture: %v", err)
	}
	var requestFixture struct {
		Confirmation struct {
			ExpectedRemovedNodeGroupCount int64 `json:"expected_removed_node_group_count"`
		} `json:"confirmation"`
	}
	if err := json.Unmarshal(requestJSON, &requestFixture); err != nil {
		t.Fatalf("decode Workflow graph confirmation fixture: %v", err)
	}
	if requestFixture.Confirmation.ExpectedRemovedNodeGroupCount != 1 {
		t.Fatalf("Workflow graph confirmation fixture = %+v, want one removed Node Group", requestFixture)
	}
}
