package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	getError        error
	previewError    error
	saveError       error
	closeError      error
	previewCalls    int
	saveCalls       int
	closeCalls      int
	saveRequest     serverapi.WorkflowGraphSaveRequest
}

func (r *workflowGraphApplyRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return serverapi.WorkflowGetResponse{Definition: r.definition}, r.getError
}
func (r *workflowGraphApplyRemote) PreviewWorkflowGraphSave(context.Context, serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.previewCalls++
	return r.previewResponse, r.previewError
}
func (r *workflowGraphApplyRemote) SaveWorkflowGraph(_ context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.saveCalls++
	r.saveRequest = req
	return r.saveResponse, r.saveError
}
func (*workflowGraphApplyRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}
func (r *workflowGraphApplyRemote) Close() error { r.closeCalls++; return r.closeError }

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
			exit, outcome, _ := runWorkflowGraphApplyCommand(t, args, emptyWorkflowGraphDocumentJSON)
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
	exit, outcome, _ := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, input)
	if exit != 1 || outcome.Outcome != workflowGraphApplyBlocked || outcome.Blockers[0].Code != "version_changed" || remote.previewCalls != 0 {
		t.Fatalf("exit=%d outcome=%+v preview=%d", exit, outcome, remote.previewCalls)
	}
}

func TestWorkflowGraphAdditionIdentityContract(t *testing.T) {
	const canonical = "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name    string
		current serverapi.WorkflowDefinition
		graph   serverapi.WorkflowGraphDraft
		valid   bool
	}{
		{"Node Group addition", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{NodeGroups: []serverapi.WorkflowGraphDraftNodeGroup{{ID: canonical}}}, true},
		{"Node addition", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{Nodes: []serverapi.WorkflowGraphDraftNode{{ID: canonical}}}, true},
		{"Transition Group addition", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{{ID: canonical}}}, true},
		{"Transition Branch addition", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{Edges: []serverapi.WorkflowGraphDraftEdge{{ID: canonical}}}, true},
		{"prefixed Node Group", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{NodeGroups: []serverapi.WorkflowGraphDraftNodeGroup{{ID: "group-" + canonical}}}, false},
		{"prefixed Node", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{Nodes: []serverapi.WorkflowGraphDraftNode{{ID: "node-" + canonical}}}, false},
		{"prefixed Transition Group", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{{ID: "group-" + canonical}}}, false},
		{"prefixed Transition Branch", serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{Edges: []serverapi.WorkflowGraphDraftEdge{{ID: "edge-" + canonical}}}, false},
		{"same-type legacy", serverapi.WorkflowDefinition{NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "legacy"}}}, serverapi.WorkflowGraphDraft{NodeGroups: []serverapi.WorkflowGraphDraftNodeGroup{{ID: "legacy"}}}, true},
		{"cross-type legacy", serverapi.WorkflowDefinition{NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "legacy"}}}, serverapi.WorkflowGraphDraft{Nodes: []serverapi.WorkflowGraphDraftNode{{ID: "legacy"}}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWorkflowGraphAdditionIdentities(test.current, test.graph); (err == nil) != test.valid {
				t.Fatalf("error = %v, valid=%t", err, test.valid)
			}
		})
	}
}

func TestWorkflowGraphApplyFileOperationalCloseAndSaveBlockerPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(path, []byte(emptyWorkflowGraphDocumentJSON), 0o600); err != nil {
		t.Fatalf("write graph: %v", err)
	}
	remote := &workflowGraphApplyRemote{
		definition:      serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 1}},
		previewResponse: graphApplyPreview(1, false, true, false, nil),
		closeError:      errors.New("close"),
	}
	installWorkflowCommandRemote(t, remote)
	exit, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", path, "--json"}, "")
	if exit != 0 || outcome.Outcome != workflowGraphApplyUnchanged || remote.closeCalls != 1 || stderr == "" {
		t.Fatalf("exit=%d outcome=%+v close=%d stderr=%q", exit, outcome, remote.closeCalls, stderr)
	}

	remote = &workflowGraphApplyRemote{
		definition:      serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 1}},
		previewResponse: graphApplyPreview(1, true, true, false, nil),
		saveResponse: serverapi.WorkflowGraphSaveResponse{
			CurrentVersion: 2,
			Impact:         serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
			Blockers: []serverapi.WorkflowGraphSaveBlocker{{
				Code: "version_changed", Message: "changed", Count: 2, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
			}},
		},
	}
	installWorkflowCommandRemote(t, remote)
	exit, outcome, _ = runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
	if exit != 1 || outcome.Outcome != workflowGraphApplyBlocked || outcome.Blockers[0].Code != "version_changed" || remote.saveCalls != 1 {
		t.Fatalf("exit=%d outcome=%+v saves=%d", exit, outcome, remote.saveCalls)
	}

	remote = &workflowGraphApplyRemote{
		definition: serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 1}},
		getError:   errors.New("get"),
	}
	installWorkflowCommandRemote(t, remote)
	exit, outcome, _ = runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
	if exit != 1 || outcome.Outcome != workflowGraphApplyRequestFailed || remote.previewCalls != 0 {
		t.Fatalf("exit=%d outcome=%+v preview=%d", exit, outcome, remote.previewCalls)
	}
}

func TestWorkflowGraphApplyHumanDetailsWriteAllTypedSections(t *testing.T) {
	base := workflowGraphApplyOutcome{
		ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{
			serverapi.WorkflowValidationModeDraft: {
				Errors: []serverapi.WorkflowValidationError{{Code: "invalid", NodeID: "node"}},
			},
		},
		Impact: &serverapi.WorkflowGraphSaveImpact{
			RemovedEdgeCount: 1,
			RemovedEntities:  []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
		},
		Blockers: []serverapi.WorkflowGraphSaveBlocker{{
			Code: "confirmation_required", Message: "confirm", Count: 1,
			AffectedEntities: []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge"}},
		}},
	}
	render := func(outcome workflowGraphApplyOutcome) []byte {
		var output bytes.Buffer
		if err := writeWorkflowGraphApplyPreviewDetails(&output, outcome); err != nil {
			t.Fatalf("write details: %v", err)
		}
		return output.Bytes()
	}
	rendered := render(base)
	countChanged := base
	countImpact := *base.Impact
	countImpact.RemovedEdgeCount++
	countChanged.Impact = &countImpact
	removedIDChanged := base
	removedImpact := *base.Impact
	removedImpact.RemovedEntities = []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "other-edge"}}
	removedIDChanged.Impact = &removedImpact
	affectedIDChanged := base
	affectedIDChanged.Blockers = []serverapi.WorkflowGraphSaveBlocker{base.Blockers[0]}
	affectedIDChanged.Blockers[0].AffectedEntities = []serverapi.WorkflowGraphEntityReference{{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "other-edge"}}
	for name, changed := range map[string]workflowGraphApplyOutcome{
		"aggregate count": countChanged,
		"removed entity":  removedIDChanged,
		"affected entity": affectedIDChanged,
	} {
		if bytes.Equal(rendered, render(changed)) {
			t.Fatalf("%s did not affect human details", name)
		}
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

func runWorkflowGraphApplyCommand(t *testing.T, args []string, input string) (int, workflowGraphApplyOutcome, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := workflowSubcommandWithInput(args, bytes.NewBufferString(input), &stdout, &stderr)
	var outcome workflowGraphApplyOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v; stderr=%q", stdout.String(), err, stderr.String())
	}
	return exit, outcome, stderr.String()
}

func workflowGraphApplyID(t *testing.T) runtimeids.WorkflowID {
	t.Helper()
	id, err := runtimeids.ParseWorkflowID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Workflow ID: %v", err)
	}
	return id
}
