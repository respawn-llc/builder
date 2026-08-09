package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowGraphApplyRemote struct {
	apicontract.WorkflowService
	definition      serverapi.WorkflowDefinition
	getError        error
	previewResponse serverapi.WorkflowGraphSavePreviewResponse
	previewError    error
	saveResponse    serverapi.WorkflowGraphSaveResponse
	saveError       error
	previewRequest  serverapi.WorkflowGraphSavePreviewRequest
	saveRequest     serverapi.WorkflowGraphSaveRequest
	closeError      error
	getCalls        int
	previewCalls    int
	saveCalls       int
	closeCalls      int
}

func (r *workflowGraphApplyRemote) GetWorkflow(context.Context, serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	r.getCalls++
	return serverapi.WorkflowGetResponse{Definition: r.definition}, r.getError
}

func (r *workflowGraphApplyRemote) PreviewWorkflowGraphSave(_ context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.previewCalls++
	r.previewRequest = req
	return r.previewResponse, r.previewError
}

func (r *workflowGraphApplyRemote) SaveWorkflowGraph(_ context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.saveCalls++
	r.saveRequest = req
	if r.saveError != nil {
		return serverapi.WorkflowGraphSaveResponse{}, r.saveError
	}
	return r.saveResponse, nil
}

func (*workflowGraphApplyRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *workflowGraphApplyRemote) Close() error {
	r.closeCalls++
	return r.closeError
}

func TestWorkflowGraphApplyProjectsInvalidDocumentAndRequestFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "malformed JSON", input: `{`},
		{
			name:  "null required Edge fields",
			input: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[{"id":"edge-1","transition_group_id":"group-1","key":"edge","target_node_id":"node-1","assignee_selection":"configured","thinking_selection":"configured","requires_approval":null,"context_mode":"new_session","context_source":null}]}}`,
		},
	} {
		t.Run("invalid document/"+test.name, func(t *testing.T) {
			previous := workflowCommandRemoteOpener
			defer func() { workflowCommandRemoteOpener = previous }()
			opened := 0
			workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
				opened++
				return config.App{}, nil, errors.New("unexpected open")
			}
			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, test.input)
			if exitCode != 1 || outcome.Outcome != workflowGraphApplyInvalidDocument || opened != 0 || stderr != "" {
				t.Fatalf("exit=%d outcome=%+v opened=%d stderr=%q", exitCode, outcome, opened, stderr)
			}
		})
	}

	t.Run("file read", func(t *testing.T) {
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", filepath.Join(t.TempDir(), "missing"), "--json"}, "")
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyRequestFailed || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
		}
	})

	t.Run("remote open", func(t *testing.T) {
		previous := workflowCommandRemoteOpener
		defer func() { workflowCommandRemoteOpener = previous }()
		workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
			return config.App{}, nil, errors.New("open failed")
		}
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyRequestFailed || outcome.WorkflowID == nil || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
		}
	})

	t.Run("remote request", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		remote.getError = errors.New("get failed")
		installWorkflowCommandRemote(t, remote)
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyRequestFailed || outcome.WorkflowID == nil || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
		}
		if remote.previewCalls != 0 || remote.saveCalls != 0 {
			t.Fatalf("preview=%d save=%d, want zero", remote.previewCalls, remote.saveCalls)
		}
	})

	t.Run("preview request", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		remote.previewError = errors.New("preview failed")
		installWorkflowCommandRemote(t, remote)
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyRequestFailed ||
			outcome.WorkflowID == nil || outcome.CurrentVersion == nil || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
		}
		if remote.previewCalls != 1 || remote.saveCalls != 0 {
			t.Fatalf("preview=%d save=%d, want one/zero", remote.previewCalls, remote.saveCalls)
		}
	})

	t.Run("save request", func(t *testing.T) {
		remote := workflowGraphApplySavedRemote(t, 1)
		remote.saveError = errors.New("save failed")
		installWorkflowCommandRemote(t, remote)
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyRequestFailed ||
			outcome.WorkflowID == nil || outcome.CurrentVersion == nil || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
		}
		if remote.previewCalls != 1 || remote.saveCalls != 1 {
			t.Fatalf("preview=%d save=%d, want one/one", remote.previewCalls, remote.saveCalls)
		}
	})

	t.Run("invalid save response", func(t *testing.T) {
		remote := workflowGraphApplySavedRemote(t, 1)
		remote.saveResponse.Impact.RemovedEdgeCount = 1
		installWorkflowCommandRemote(t, remote)
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyRequestFailed ||
			outcome.WorkflowID == nil || outcome.CurrentVersion == nil || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
		}
		if remote.previewCalls != 1 || remote.saveCalls != 1 {
			t.Fatalf("preview=%d save=%d, want one/one", remote.previewCalls, remote.saveCalls)
		}
	})
}

func TestWorkflowGraphApplyProjectsBlockedConfirmationAndUnchangedWithoutSave(t *testing.T) {
	tests := []struct {
		name     string
		preview  serverapi.WorkflowGraphSavePreviewResponse
		want     workflowGraphApplyOutcomeKind
		wantExit int
	}{
		{
			name: "blocked",
			preview: workflowGraphApplyPreview(1, true, false, false, []serverapi.WorkflowGraphSaveBlocker{{
				Code: "validation_failed", Message: "invalid graph", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
			}}),
			want: workflowGraphApplyBlocked, wantExit: 1,
		},
		{
			name: "confirmation required",
			preview: workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
				Code: "confirmation_required", Message: "confirm removal", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
			}}),
			want: workflowGraphApplyConfirmationRequired, wantExit: 1,
		},
		{
			name:    "unchanged",
			preview: workflowGraphApplyPreview(1, false, true, false, []serverapi.WorkflowGraphSaveBlocker{}),
			want:    workflowGraphApplyUnchanged, wantExit: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := workflowGraphApplyUnchangedRemote(t, 1)
			remote.previewResponse = test.preview
			installWorkflowCommandRemote(t, remote)
			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, emptyWorkflowGraphDocumentJSON)
			if exitCode != test.wantExit || outcome.Outcome != test.want || stderr != "" {
				t.Fatalf("exit=%d outcome=%+v stderr=%q, want %s", exitCode, outcome, stderr, test.want)
			}
			if remote.saveCalls != 0 {
				t.Fatalf("save calls = %d, want zero", remote.saveCalls)
			}
		})
	}
}

func TestWorkflowGraphApplyJSONPreviewPreservesTypedDetails(t *testing.T) {
	impact := workflowGraphApplyConfirmationImpact()
	nodeID := "node-validation-1"
	relatedID := "edge-validation-1"
	validationCode := "workflow.validation.review"
	affected := []serverapi.WorkflowGraphEntityReference{
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge-4"},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: nodeID},
	}
	remote := workflowGraphApplyUnchangedRemote(t, 1)
	remote.previewResponse = workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
		Code:             "confirmation_required",
		Message:          "confirm removal",
		Count:            9,
		AffectedEntities: affected,
	}})
	remote.previewResponse.Impact = impact
	remote.previewResponse.ValidationResults = map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{
		serverapi.WorkflowValidationModeDraft: {
			Valid: false,
			Errors: []serverapi.WorkflowValidationError{{
				Code:       validationCode,
				Message:    "graph validation failed",
				NodeID:     nodeID,
				RelatedIDs: []string{relatedID},
			}},
		},
	}
	installWorkflowCommandRemote(t, remote)

	exitCode, outcome, stderr := runWorkflowGraphApplyCommand(
		t,
		[]string{"graph", "apply", "-", "--json"},
		emptyWorkflowGraphDocumentJSON,
	)
	if exitCode != 1 || outcome.Outcome != workflowGraphApplyConfirmationRequired || stderr != "" || remote.saveCalls != 0 {
		t.Fatalf("exit=%d outcome=%+v stderr=%q save=%d", exitCode, outcome, stderr, remote.saveCalls)
	}
	if outcome.Impact == nil ||
		outcome.Impact.RemovedNodeGroupCount != impact.RemovedNodeGroupCount ||
		outcome.Impact.RemovedNodeCount != impact.RemovedNodeCount ||
		outcome.Impact.RemovedTransitionGroupCount != impact.RemovedTransitionGroupCount ||
		outcome.Impact.RemovedEdgeCount != impact.RemovedEdgeCount ||
		outcome.Impact.NodeTaskReferenceCount != impact.NodeTaskReferenceCount ||
		outcome.Impact.EdgeTaskReferenceCount != impact.EdgeTaskReferenceCount ||
		!slices.Equal(outcome.Impact.RemovedEntities, impact.RemovedEntities) {
		t.Fatalf("outcome impact = %+v, want %+v", outcome.Impact, impact)
	}
	validation, exists := outcome.ValidationResults[serverapi.WorkflowValidationModeDraft]
	if !exists || validation.Valid || len(validation.Errors) != 1 ||
		validation.Errors[0].Code != validationCode ||
		validation.Errors[0].NodeID != nodeID ||
		!slices.Equal(validation.Errors[0].RelatedIDs, []string{relatedID}) {
		t.Fatalf("outcome validation = %+v", outcome.ValidationResults)
	}
	if len(outcome.Blockers) != 1 ||
		outcome.Blockers[0].Code != "confirmation_required" ||
		outcome.Blockers[0].Count != 9 ||
		!slices.Equal(outcome.Blockers[0].AffectedEntities, affected) {
		t.Fatalf("outcome blockers = %+v", outcome.Blockers)
	}
}

func TestWorkflowGraphApplyConfirmedRunUsesFreshPreviewCounts(t *testing.T) {
	impact := workflowGraphApplyConfirmationImpact()
	remote := workflowGraphApplySavedRemote(t, 1)
	remote.previewResponse = workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
		Code: "confirmation_required", Message: "confirm removal", Count: 9, AffectedEntities: impact.RemovedEntities[0:1],
	}})
	remote.previewResponse.Impact = impact
	remote.saveResponse.Impact = impact
	installWorkflowCommandRemote(t, remote)

	exitCode, outcome, stderr := runWorkflowGraphApplyCommand(
		t,
		[]string{"graph", "apply", "-", "--confirm", "--json"},
		emptyWorkflowGraphDocumentJSON,
	)

	if exitCode != 0 || outcome.Outcome != workflowGraphApplySaved || stderr != "" {
		t.Fatalf("exit=%d outcome=%+v stderr=%q, want saved", exitCode, outcome, stderr)
	}
	want := serverapi.WorkflowGraphSaveConfirmation{
		ExpectedRemovedNodeGroupCount:       1,
		ExpectedRemovedNodeCount:            2,
		ExpectedRemovedTransitionGroupCount: 3,
		ExpectedRemovedEdgeCount:            4,
		ExpectedNodeTaskReferenceCount:      5,
		ExpectedEdgeTaskReferenceCount:      6,
	}
	if remote.previewCalls != 1 || remote.saveCalls != 1 {
		t.Fatalf("preview=%d save=%d, want one/one", remote.previewCalls, remote.saveCalls)
	}
	if remote.saveRequest.Confirmation == nil || *remote.saveRequest.Confirmation != want {
		t.Fatalf("save confirmation = %+v, want fresh preview counts %+v", remote.saveRequest.Confirmation, want)
	}
}

func TestWorkflowGraphApplyProjectsSaveResultsWithCurrentImpactAndEntities(t *testing.T) {
	currentImpact := serverapi.WorkflowGraphSaveImpact{
		RemovedEdgeCount: 1,
		RemovedEntities: []serverapi.WorkflowGraphEntityReference{{
			EntityType: serverapi.WorkflowGraphEntityTypeEdge,
			EntityID:   "current-edge-id",
		}},
	}
	tests := []struct {
		name           string
		confirmed      bool
		preview        serverapi.WorkflowGraphSavePreviewResponse
		save           serverapi.WorkflowGraphSaveResponse
		wantOutcome    workflowGraphApplyOutcomeKind
		wantExit       int
		wantBlocker    string
		wantAffected   []serverapi.WorkflowGraphEntityReference
		wantDefinition bool
	}{
		{
			name:           "saved",
			preview:        workflowGraphApplyPreview(1, true, true, false, []serverapi.WorkflowGraphSaveBlocker{}),
			save:           workflowGraphApplySaveResponse(t, true, true, 2, serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}}, nil),
			wantOutcome:    workflowGraphApplySaved,
			wantExit:       0,
			wantDefinition: true,
		},
		{
			name:        "unchanged at save time",
			preview:     workflowGraphApplyPreview(1, true, true, false, []serverapi.WorkflowGraphSaveBlocker{}),
			save:        workflowGraphApplySaveResponse(t, true, false, 1, serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}}, nil),
			wantOutcome: workflowGraphApplyUnchanged,
			wantExit:    0,
		},
		{
			name:    "save-time version blocker",
			preview: workflowGraphApplyPreview(1, true, true, false, []serverapi.WorkflowGraphSaveBlocker{}),
			save: workflowGraphApplySaveResponse(t, false, true, 2, currentImpact, []serverapi.WorkflowGraphSaveBlocker{{
				Code: "version_changed", Message: "changed", Count: 2, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
			}}),
			wantOutcome:  workflowGraphApplyBlocked,
			wantExit:     1,
			wantBlocker:  "version_changed",
			wantAffected: []serverapi.WorkflowGraphEntityReference{},
		},
		{
			name:      "save-time impact blocker",
			confirmed: true,
			preview: workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
				Code: "confirmation_required", Message: "confirm", Count: 1, AffectedEntities: currentImpact.RemovedEntities,
			}}),
			save: workflowGraphApplySaveResponse(t, false, true, 1, currentImpact, []serverapi.WorkflowGraphSaveBlocker{{
				Code: "impact_changed", Message: "impact changed", Count: 1, AffectedEntities: currentImpact.RemovedEntities,
			}}),
			wantOutcome:  workflowGraphApplyBlocked,
			wantExit:     1,
			wantBlocker:  "impact_changed",
			wantAffected: currentImpact.RemovedEntities,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &workflowGraphApplyRemote{
				definition:      workflowGraphApplyDefinition(t, 1),
				previewResponse: test.preview,
				saveResponse:    test.save,
			}
			if test.confirmed {
				remote.previewResponse.Impact = currentImpact
			}
			installWorkflowCommandRemote(t, remote)
			args := []string{"graph", "apply", "-", "--json"}
			if test.confirmed {
				args = append(args, "--confirm")
			}

			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, args, emptyWorkflowGraphDocumentJSON)

			if exitCode != test.wantExit || outcome.Outcome != test.wantOutcome || stderr != "" {
				t.Fatalf("exit=%d outcome=%+v stderr=%q, want %s", exitCode, outcome, stderr, test.wantOutcome)
			}
			if remote.previewCalls != 1 || remote.saveCalls != 1 {
				t.Fatalf("preview=%d save=%d, want one/one", remote.previewCalls, remote.saveCalls)
			}
			if (outcome.Definition != nil) != test.wantDefinition {
				t.Fatalf("definition present = %t, want %t", outcome.Definition != nil, test.wantDefinition)
			}
			if test.wantBlocker == "" {
				return
			}
			if outcome.Impact == nil || outcome.Impact.RemovedEntities[0].EntityID != "current-edge-id" {
				t.Fatalf("outcome impact = %+v, want current save-time impact", outcome.Impact)
			}
			if len(outcome.Blockers) != 1 || outcome.Blockers[0].Code != test.wantBlocker {
				t.Fatalf("outcome blockers = %+v, want %s", outcome.Blockers, test.wantBlocker)
			}
			if !slices.Equal(outcome.Blockers[0].AffectedEntities, test.wantAffected) {
				t.Fatalf("affected entities = %+v, want %+v", outcome.Blockers[0].AffectedEntities, test.wantAffected)
			}
		})
	}
}

func runWorkflowGraphApplyCommand(t *testing.T, args []string, stdin string) (int, workflowGraphApplyOutcome, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exitCode := workflowSubcommandWithInput(args, bytes.NewBufferString(stdin), &stdout, &stderr)
	var outcome workflowGraphApplyOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome from stdout %q: %v; stderr=%q", stdout.String(), err, stderr.String())
	}
	if err := outcome.Validate(); err != nil {
		t.Fatalf("outcome validation: %v; outcome=%+v", err, outcome)
	}
	return exitCode, outcome, stderr.String()
}

func workflowGraphApplyUnchangedRemote(t *testing.T, version int64) *workflowGraphApplyRemote {
	t.Helper()
	return &workflowGraphApplyRemote{
		definition:      workflowGraphApplyDefinition(t, version),
		previewResponse: workflowGraphApplyPreview(version, false, true, false, []serverapi.WorkflowGraphSaveBlocker{}),
	}
}

func workflowGraphApplySavedRemote(t *testing.T, version int64) *workflowGraphApplyRemote {
	t.Helper()
	definition := workflowGraphApplyDefinition(t, version)
	savedDefinition := definition
	savedDefinition.Workflow.Version++
	return &workflowGraphApplyRemote{
		definition:      definition,
		previewResponse: workflowGraphApplyPreview(version, true, true, false, []serverapi.WorkflowGraphSaveBlocker{}),
		saveResponse: serverapi.WorkflowGraphSaveResponse{
			Saved:             true,
			Changed:           true,
			Definition:        &savedDefinition,
			CurrentVersion:    savedDefinition.Workflow.Version,
			ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
			Impact:            serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
			Blockers:          []serverapi.WorkflowGraphSaveBlocker{},
			CanSave:           true,
		},
	}
}

func workflowGraphApplyConfirmationImpact() serverapi.WorkflowGraphSaveImpact {
	removedEntities := []serverapi.WorkflowGraphEntityReference{
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge-1"},
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge-2"},
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge-3"},
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge-4"},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: "node-1"},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: "node-2"},
		{EntityType: serverapi.WorkflowGraphEntityTypeNodeGroup, EntityID: "node-group-1"},
		{EntityType: serverapi.WorkflowGraphEntityTypeTransitionGroup, EntityID: "transition-group-1"},
		{EntityType: serverapi.WorkflowGraphEntityTypeTransitionGroup, EntityID: "transition-group-2"},
		{EntityType: serverapi.WorkflowGraphEntityTypeTransitionGroup, EntityID: "transition-group-3"},
	}
	return serverapi.WorkflowGraphSaveImpact{
		RemovedNodeGroupCount:       1,
		RemovedNodeCount:            2,
		RemovedTransitionGroupCount: 3,
		RemovedEdgeCount:            4,
		RemovedEntities:             removedEntities,
		NodeTaskReferenceCount:      5,
		EdgeTaskReferenceCount:      6,
	}
}

func workflowGraphApplySaveResponse(
	t *testing.T,
	saved bool,
	changed bool,
	version int64,
	impact serverapi.WorkflowGraphSaveImpact,
	blockers []serverapi.WorkflowGraphSaveBlocker,
) serverapi.WorkflowGraphSaveResponse {
	t.Helper()
	var definition *serverapi.WorkflowDefinition
	if saved {
		value := workflowGraphApplyDefinition(t, version)
		definition = &value
	}
	if blockers == nil {
		blockers = []serverapi.WorkflowGraphSaveBlocker{}
	}
	return serverapi.WorkflowGraphSaveResponse{
		Saved:             saved,
		Changed:           changed,
		Definition:        definition,
		CurrentVersion:    version,
		ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
		Impact:            impact,
		Blockers:          blockers,
		CanSave:           saved,
	}
}

type workflowGraphApplyEntityKind struct {
	name       string
	graph      func(string) workflowGraphDocumentGraph
	addCurrent func(*serverapi.WorkflowDefinition, string)
}

func workflowGraphApplyEntityKinds() []workflowGraphApplyEntityKind {
	return []workflowGraphApplyEntityKind{
		{
			name: "Node Group",
			graph: func(id string) workflowGraphDocumentGraph {
				return workflowGraphDocumentGraph{
					NodeGroups:       []serverapi.WorkflowGraphDraftNodeGroup{{ID: id, Key: "group", DisplayName: "Group"}},
					Nodes:            []workflowGraphDocumentNode{},
					TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
					Edges:            []serverapi.WorkflowGraphDraftEdge{},
				}
			},
			addCurrent: func(definition *serverapi.WorkflowDefinition, id string) {
				definition.NodeGroups = []serverapi.WorkflowNodeGroup{{
					GroupID: id, WorkflowID: definition.Workflow.ID, GroupKey: "group", DisplayName: "Group",
				}}
			},
		},
		{
			name: "Node",
			graph: func(id string) workflowGraphDocumentGraph {
				return workflowGraphDocumentGraph{
					NodeGroups:       []serverapi.WorkflowGraphDraftNodeGroup{},
					Nodes:            []workflowGraphDocumentNode{{ID: id, Key: "node", Kind: "agent", DisplayName: "Node"}},
					TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
					Edges:            []serverapi.WorkflowGraphDraftEdge{},
				}
			},
			addCurrent: func(definition *serverapi.WorkflowDefinition, id string) {
				definition.Nodes = []serverapi.WorkflowNode{{
					ID: id, WorkflowID: definition.Workflow.ID, Key: "node", Kind: "agent", DisplayName: "Node",
				}}
			},
		},
		{
			name: "Transition Group",
			graph: func(id string) workflowGraphDocumentGraph {
				return workflowGraphDocumentGraph{
					NodeGroups: []serverapi.WorkflowGraphDraftNodeGroup{},
					Nodes:      []workflowGraphDocumentNode{},
					TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{{
						ID: id, SourceNodeID: "legacy-source", TransitionID: "next", DisplayName: "Next",
					}},
					Edges: []serverapi.WorkflowGraphDraftEdge{},
				}
			},
			addCurrent: func(definition *serverapi.WorkflowDefinition, id string) {
				definition.TransitionGroups = []serverapi.WorkflowTransitionGroup{{
					ID: id, WorkflowID: definition.Workflow.ID, SourceNodeID: "legacy-source", TransitionID: "next", DisplayName: "Next",
				}}
			},
		},
		{
			name: "Transition Branch",
			graph: func(id string) workflowGraphDocumentGraph {
				return workflowGraphDocumentGraph{
					NodeGroups:       []serverapi.WorkflowGraphDraftNodeGroup{},
					Nodes:            []workflowGraphDocumentNode{},
					TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
					Edges: []serverapi.WorkflowGraphDraftEdge{{
						ID:                id,
						TransitionGroupID: "legacy-transition",
						Key:               "branch",
						TargetNodeID:      "legacy-target",
						AssigneeSelection: "configured",
						ThinkingSelection: "configured",
						ContextMode:       "new_session",
						ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
					}},
				}
			},
			addCurrent: func(definition *serverapi.WorkflowDefinition, id string) {
				definition.Edges = []serverapi.WorkflowEdge{{
					ID:                id,
					WorkflowID:        definition.Workflow.ID,
					TransitionGroupID: "legacy-transition",
					Key:               "branch",
					TargetNodeID:      "legacy-target",
					AssigneeSelection: "configured",
					ThinkingSelection: "configured",
					ContextMode:       "new_session",
					ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
				}}
			},
		},
	}
}

func workflowGraphApplyDefinition(t *testing.T, version int64) serverapi.WorkflowDefinition {
	t.Helper()
	return serverapi.WorkflowDefinition{Workflow: serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: version}}
}

func workflowGraphApplyID(t *testing.T) runtimeids.WorkflowID {
	t.Helper()
	id, err := runtimeids.ParseWorkflowID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse Workflow ID: %v", err)
	}
	return id
}

func workflowGraphApplyPreview(version int64, changed bool, canSave bool, confirmationRequired bool, blockers []serverapi.WorkflowGraphSaveBlocker) serverapi.WorkflowGraphSavePreviewResponse {
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion:       version,
		Changed:              changed,
		ValidationResults:    map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
		Impact:               serverapi.WorkflowGraphSaveImpact{RemovedEntities: []serverapi.WorkflowGraphEntityReference{}},
		Blockers:             blockers,
		CanSave:              canSave,
		ConfirmationRequired: confirmationRequired,
	}
}
