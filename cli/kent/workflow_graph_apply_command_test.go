package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowGraphApplyRemote struct {
	apicontract.WorkflowService
	definition      serverapi.WorkflowDefinition
	previewResponse serverapi.WorkflowGraphSavePreviewResponse
	saveResponse    serverapi.WorkflowGraphSaveResponse
	previewCalls    int
	saveCalls       int
	saveRequest     serverapi.WorkflowGraphSaveRequest
}

func (r *workflowGraphApplyRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return serverapi.WorkflowGetResponse{Definition: r.definition}, nil
}
func (r *workflowGraphApplyRemote) PreviewWorkflowGraphSave(context.Context, serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.previewCalls++
	return r.previewResponse, nil
}
func (r *workflowGraphApplyRemote) SaveWorkflowGraph(_ context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.saveCalls++
	r.saveRequest = req
	return r.saveResponse, nil
}
func (*workflowGraphApplyRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}
func (*workflowGraphApplyRemote) Close() error { return nil }

func TestWorkflowGraphApplyProjectsTypedOutcomes(t *testing.T) {
	blocker := serverapi.WorkflowGraphSaveBlocker{
		Code: "confirmation_required", Message: "confirm", Count: 1,
		AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
	}
	for _, test := range []struct {
		name      string
		preview   serverapi.WorkflowGraphSavePreviewResponse
		save      serverapi.WorkflowGraphSaveResponse
		confirm   bool
		want      workflowGraphApplyOutcomeKind
		wantExit  int
		wantSaves int
	}{
		{"unchanged", graphApplyPreview(1, false, true, false, nil), serverapi.WorkflowGraphSaveResponse{}, false, workflowGraphApplyUnchanged, 0, 0},
		{"blocked", graphApplyPreview(1, true, false, false, []serverapi.WorkflowGraphSaveBlocker{{Code: "validation_failed", Message: "invalid", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{}}}), serverapi.WorkflowGraphSaveResponse{}, false, workflowGraphApplyBlocked, 1, 0},
		{"confirmation", graphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{blocker}), serverapi.WorkflowGraphSaveResponse{}, false, workflowGraphApplyConfirmationRequired, 1, 0},
		{"saved", graphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{blocker}), graphApplySavedResponse(t), true, workflowGraphApplySaved, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := &workflowGraphApplyRemote{
				definition:      serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 1}},
				previewResponse: test.preview,
				saveResponse:    test.save,
			}
			installWorkflowCommandRemote(t, remote)
			args := []string{"graph", "apply", "-", "--json"}
			if test.confirm {
				args = append(args, "--confirm")
			}
			exit, outcome := runWorkflowGraphApplyCommand(t, args, emptyWorkflowGraphDocumentJSON)
			if exit != test.wantExit || outcome.Outcome != test.want || remote.saveCalls != test.wantSaves {
				t.Fatalf("exit=%d outcome=%+v saves=%d", exit, outcome, remote.saveCalls)
			}
			if test.confirm && (remote.saveRequest.Confirmation == nil || remote.saveRequest.Confirmation.ExpectedRemovedEdgeCount != 1) {
				t.Fatalf("confirmation = %+v", remote.saveRequest.Confirmation)
			}
		})
	}
}

func TestWorkflowGraphApplyStaleVersionPrecedesIdentityValidation(t *testing.T) {
	remote := &workflowGraphApplyRemote{definition: serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 2},
	}}
	installWorkflowCommandRemote(t, remote)
	input := `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"not-a-uuid","key":"node","kind":"agent","display_name":"Node"}],"transition_groups":[],"edges":[]}}`
	exit, outcome := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, input)
	if exit != 1 || outcome.Outcome != workflowGraphApplyBlocked || outcome.Blockers[0].Code != "version_changed" || remote.previewCalls != 0 {
		t.Fatalf("exit=%d outcome=%+v preview=%d", exit, outcome, remote.previewCalls)
	}
}

func TestWorkflowGraphAdditionIdentityContract(t *testing.T) {
	current := serverapi.WorkflowDefinition{
		NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "legacy"}},
	}
	valid := serverapi.WorkflowGraphDraft{
		NodeGroups: []serverapi.WorkflowGraphDraftNodeGroup{{ID: "legacy"}},
		Nodes:      []serverapi.WorkflowGraphDraftNode{{ID: "11111111-1111-4111-8111-111111111111"}},
	}
	if err := validateWorkflowGraphAdditionIdentities(current, valid); err != nil {
		t.Fatalf("valid identities: %v", err)
	}
	valid.Nodes[0].ID = "node-prefixed"
	if err := validateWorkflowGraphAdditionIdentities(current, valid); err == nil {
		t.Fatal("prefixed addition ID accepted")
	}
	valid.Nodes[0].ID = "legacy"
	if err := validateWorkflowGraphAdditionIdentities(current, valid); err == nil {
		t.Fatal("cross-type identity accepted")
	}
}

func TestWorkflowGraphApplyHumanDetailsPreserveTypedPreview(t *testing.T) {
	outcome := workflowGraphApplyOutcome{
		Impact: &serverapi.WorkflowGraphSaveImpact{
			RemovedEdgeCount: 1,
			RemovedEntities:  []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{{
			Code: "confirmation_required", Message: "confirm", Count: 1,
			AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
		}},
	}
	var output bytes.Buffer
	if err := writeWorkflowGraphApplyPreviewDetails(&output, outcome); err != nil {
		t.Fatalf("write details: %v", err)
	}
	var details struct {
		Impact   serverapi.WorkflowGraphSaveImpact    `json:"impact"`
		Blockers []serverapi.WorkflowGraphSaveBlocker `json:"blockers"`
	}
	if err := json.Unmarshal(output.Bytes(), &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if details.Impact.RemovedEdgeCount != 1 || details.Blockers[0].AffectedEntities[0].EntityID != "edge" {
		t.Fatalf("details = %+v", details)
	}
}

func graphApplyPreview(version int64, changed, canSave, confirmation bool, blockers []serverapi.WorkflowGraphSaveBlocker) serverapi.WorkflowGraphSavePreviewResponse {
	if blockers == nil {
		blockers = []serverapi.WorkflowGraphSaveBlocker{}
	}
	impact := serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}}
	if confirmation {
		impact.RemovedEdgeCount = 1
		impact.RemovedEntities = []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}}
	}
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion: version, Changed: changed, CanSave: canSave, ConfirmationRequired: confirmation,
		ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
		Impact:            impact, Blockers: blockers,
	}
}

func graphApplySavedResponse(t *testing.T) serverapi.WorkflowGraphSaveResponse {
	definition := serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 2}}
	return serverapi.WorkflowGraphSaveResponse{
		Saved: true, Changed: true, Definition: &definition, CurrentVersion: 2, CanSave: true,
		ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
		Impact: serverapi.WorkflowGraphSaveImpact{
			RemovedEdgeCount: 1,
			RemovedEntities:  []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{},
	}
}

func runWorkflowGraphApplyCommand(t *testing.T, args []string, input string) (int, workflowGraphApplyOutcome) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := workflowSubcommandWithInput(args, bytes.NewBufferString(input), &stdout, &stderr)
	var outcome workflowGraphApplyOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v; stderr=%q", stdout.String(), err, stderr.String())
	}
	return exit, outcome
}

func workflowGraphApplyID(t *testing.T) runtimeids.WorkflowID {
	t.Helper()
	id, err := runtimeids.ParseWorkflowID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Workflow ID: %v", err)
	}
	return id
}
