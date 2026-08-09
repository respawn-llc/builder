package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestWorkflowGraphApplyReadsFileAndDashStdin(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		args  func(t *testing.T) []string
	}{
		{
			name:  "dash stdin",
			input: emptyWorkflowGraphDocumentJSON,
			args:  func(*testing.T) []string { return []string{"graph", "apply", "-", "--json"} },
		},
		{
			name: "file",
			args: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "workflow.json")
				if err := os.WriteFile(path, []byte(emptyWorkflowGraphDocumentJSON), 0o600); err != nil {
					t.Fatalf("write input: %v", err)
				}
				return []string{"graph", "apply", path, "--json"}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := workflowGraphApplyUnchangedRemote(t, 1)
			installWorkflowCommandRemote(t, remote)
			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, test.args(t), test.input)
			if exitCode != 0 || outcome.Outcome != workflowGraphApplyUnchanged {
				t.Fatalf("exit=%d outcome=%+v stderr=%q, want unchanged", exitCode, outcome, stderr)
			}
			if remote.getCalls != 1 || remote.previewCalls != 1 || remote.saveCalls != 0 {
				t.Fatalf("calls get=%d preview=%d save=%d", remote.getCalls, remote.previewCalls, remote.saveCalls)
			}
		})
	}
}

func TestWorkflowGraphApplyProjectsInvalidDocumentAndRequestFailures(t *testing.T) {
	t.Run("invalid document", func(t *testing.T) {
		previous := workflowCommandRemoteOpener
		defer func() { workflowCommandRemoteOpener = previous }()
		opened := 0
		workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
			opened++
			return config.App{}, nil, errors.New("unexpected open")
		}
		exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, `{`)
		if exitCode != 1 || outcome.Outcome != workflowGraphApplyInvalidDocument || opened != 0 || stderr != "" {
			t.Fatalf("exit=%d outcome=%+v opened=%d stderr=%q", exitCode, outcome, opened, stderr)
		}
	})

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

func TestWorkflowGraphApplyHumanWriterUsesOutcomeSpecificStreams(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		remote     *workflowGraphApplyRemote
		wantExit   int
		wantStdout bool
		wantStderr bool
	}{
		{name: "invalid document", input: `{`, wantExit: 1, wantStderr: true},
		{
			name:  "request failed",
			input: emptyWorkflowGraphDocumentJSON,
			remote: &workflowGraphApplyRemote{
				definition: workflowGraphApplyDefinition(t, 1),
				getError:   errors.New("get failed"),
			},
			wantExit: 1, wantStderr: true,
		},
		{
			name:  "blocked",
			input: emptyWorkflowGraphDocumentJSON,
			remote: &workflowGraphApplyRemote{
				definition: workflowGraphApplyDefinition(t, 1),
				previewResponse: workflowGraphApplyPreview(1, true, false, false, []serverapi.WorkflowGraphSaveBlocker{{
					Code: "validation_failed", Message: "invalid graph", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
				}}),
			},
			wantExit: 1, wantStderr: true,
		},
		{
			name:  "confirmation required",
			input: emptyWorkflowGraphDocumentJSON,
			remote: &workflowGraphApplyRemote{
				definition: workflowGraphApplyDefinition(t, 1),
				previewResponse: workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
					Code: "confirmation_required", Message: "confirm removal", Count: 1, AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
				}}),
			},
			wantExit: 1, wantStderr: true,
		},
		{
			name:       "unchanged",
			input:      emptyWorkflowGraphDocumentJSON,
			remote:     workflowGraphApplyUnchangedRemote(t, 1),
			wantExit:   0,
			wantStdout: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.remote != nil {
				installWorkflowCommandRemote(t, test.remote)
			}
			var stdout, stderr bytes.Buffer
			exitCode := workflowSubcommandWithInput(
				[]string{"graph", "apply", "-"},
				bytes.NewBufferString(test.input),
				&stdout,
				&stderr,
			)
			if exitCode != test.wantExit || (stdout.Len() > 0) != test.wantStdout || (stderr.Len() > 0) != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			if test.remote != nil && test.remote.saveCalls != 0 {
				t.Fatalf("save calls = %d, want zero", test.remote.saveCalls)
			}
		})
	}
}

func TestWorkflowGraphApplyHumanConfirmationReportsTypedPreviewDetails(t *testing.T) {
	impact := workflowGraphApplyConfirmationImpact()
	affected := []serverapi.WorkflowGraphEntityReference{
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: "edge-4"},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: "node-2"},
	}
	remote := workflowGraphApplyUnchangedRemote(t, 1)
	remote.previewResponse = workflowGraphApplyPreview(1, true, false, true, []serverapi.WorkflowGraphSaveBlocker{{
		Code:             "confirmation_required",
		Message:          "confirm removal",
		Count:            9,
		AffectedEntities: affected,
	}})
	remote.previewResponse.Impact = impact
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := workflowSubcommandWithInput(
		[]string{"graph", "apply", "-"},
		bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
		&stdout,
		&stderr,
	)
	if exitCode != 1 || stdout.Len() != 0 || remote.saveCalls != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q save=%d", exitCode, stdout.String(), stderr.String(), remote.saveCalls)
	}
	for _, value := range []string{
		fmt.Sprint(impact.RemovedNodeGroupCount),
		fmt.Sprint(impact.RemovedNodeCount),
		fmt.Sprint(impact.RemovedTransitionGroupCount),
		fmt.Sprint(impact.RemovedEdgeCount),
		fmt.Sprint(impact.NodeTaskReferenceCount),
		fmt.Sprint(impact.EdgeTaskReferenceCount),
	} {
		if !strings.Contains(stderr.String(), value) {
			t.Fatalf("human confirmation output omitted aggregate count %q: %q", value, stderr.String())
		}
	}
	for _, entity := range append(append([]serverapi.WorkflowGraphEntityReference{}, impact.RemovedEntities...), affected...) {
		if !strings.Contains(stderr.String(), string(entity.EntityType)) ||
			!strings.Contains(stderr.String(), entity.EntityID) {
			t.Fatalf("human confirmation output omitted entity %+v: %q", entity, stderr.String())
		}
	}
}

func TestWorkflowGraphApplyHumanBlockedReportsValidationAndBlockerEntities(t *testing.T) {
	nodeID := "node-validation-1"
	relatedID := "edge-validation-1"
	validationCode := "workflow.validation.review"
	affected := []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeNode,
		EntityID:   nodeID,
	}}
	remote := workflowGraphApplyUnchangedRemote(t, 1)
	remote.previewResponse = workflowGraphApplyPreview(1, true, false, false, []serverapi.WorkflowGraphSaveBlocker{{
		Code:             "validation_failed",
		Message:          "invalid graph",
		Count:            1,
		AffectedEntities: affected,
	}})
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

	var stdout, stderr bytes.Buffer
	exitCode := workflowSubcommandWithInput(
		[]string{"graph", "apply", "-"},
		bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
		&stdout,
		&stderr,
	)
	if exitCode != 1 || stdout.Len() != 0 || remote.saveCalls != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q save=%d", exitCode, stdout.String(), stderr.String(), remote.saveCalls)
	}
	for _, value := range []string{
		string(serverapi.WorkflowValidationModeDraft),
		validationCode,
		nodeID,
		relatedID,
		string(affected[0].EntityType),
		affected[0].EntityID,
	} {
		if !strings.Contains(stderr.String(), value) {
			t.Fatalf("human blocked output omitted typed value %q: %q", value, stderr.String())
		}
	}
}

func TestWorkflowGraphAdditionIdentityRejectsUnsupportedEntityTypeWithoutPanicking(t *testing.T) {
	currentIDs := workflowGraphCurrentEntityIDs{}

	err := currentIDs.validateAddition(
		"11111111-1111-4111-8111-111111111111",
		workflowGraphIdentityType(255),
		"graph.nodes",
		0,
	)

	var typeError workflowGraphIdentityTypeError
	if !errors.As(err, &typeError) {
		t.Fatalf("validateAddition error = %v, want workflowGraphIdentityTypeError", err)
	}
	if typeError.EntityType != workflowGraphIdentityType(255) {
		t.Fatalf("entity type = %d, want 255", typeError.EntityType)
	}
}

func TestWorkflowGraphApplyStaleVersionPrecedesLegacyAndInvalidIdentityClassification(t *testing.T) {
	tests := []struct {
		name            string
		expectedVersion int64
		current         serverapi.WorkflowDefinition
		graph           string
	}{
		{
			name:            "lower version deleted legacy Node",
			expectedVersion: 6,
			current:         workflowGraphApplyDefinition(t, 7),
			graph:           `{"node_groups":[],"nodes":[{"id":"node-deleted-legacy","key":"old","kind":"agent","display_name":"Old"}],"transition_groups":[],"edges":[]}`,
		},
		{
			name:            "lower version legacy ID changed type",
			expectedVersion: 6,
			current: serverapi.WorkflowDefinition{
				Workflow:   serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 7},
				NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "legacy-cross-type", WorkflowID: workflowGraphApplyID(t), GroupKey: "group", DisplayName: "Group"}},
			},
			graph: `{"node_groups":[],"nodes":[{"id":"legacy-cross-type","key":"old","kind":"agent","display_name":"Old"}],"transition_groups":[],"edges":[]}`,
		},
		{
			name:            "future version invalid submitted-only ID",
			expectedVersion: 8,
			current:         workflowGraphApplyDefinition(t, 7),
			graph:           `{"node_groups":[],"nodes":[{"id":"not-a-uuid","key":"new","kind":"agent","display_name":"New"}],"transition_groups":[],"edges":[]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &workflowGraphApplyRemote{definition: test.current}
			installWorkflowCommandRemote(t, remote)
			document := `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":` +
				fmt.Sprint(test.expectedVersion) + `,"graph":` + test.graph + `}`
			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, document)
			if exitCode != 1 || outcome.Outcome != workflowGraphApplyBlocked || stderr != "" {
				t.Fatalf("exit=%d outcome=%+v stderr=%q", exitCode, outcome, stderr)
			}
			if len(outcome.Blockers) != 1 || outcome.Blockers[0].Code != "version_changed" ||
				outcome.CurrentVersion == nil || *outcome.CurrentVersion != 7 {
				t.Fatalf("stale outcome = %+v, want version_changed at 7", outcome)
			}
			if remote.previewCalls != 0 || remote.saveCalls != 0 {
				t.Fatalf("preview=%d save=%d, want zero", remote.previewCalls, remote.saveCalls)
			}
		})
	}
}

func TestWorkflowGraphApplyClassifiesAdditionIdentitiesStrictlyForEveryEntityType(t *testing.T) {
	const canonical = "a8098c1a-f86e-4ea2-9c16-2d8f3a4b5c6d"
	identities := []struct {
		name      string
		id        string
		wantSaved bool
	}{
		{name: "canonical lowercase UUID v4", id: canonical, wantSaved: true},
		{name: "prefixed ID", id: "node-" + canonical},
		{name: "UUID v1", id: "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
		{name: "uppercase noncanonical UUID", id: "A8098C1A-F86E-4EA2-9C16-2D8F3A4B5C6D"},
		{name: "hyphenless noncanonical UUID", id: "a8098c1af86e4ea29c162d8f3a4b5c6d"},
		{name: "leading whitespace", id: " " + canonical},
		{name: "trailing whitespace", id: canonical + " "},
	}
	for _, entityType := range workflowGraphApplyEntityKinds() {
		for _, identity := range identities {
			t.Run(entityType.name+"/"+identity.name, func(t *testing.T) {
				document := workflowGraphDocument{
					WorkflowID:      workflowGraphApplyID(t),
					ExpectedVersion: 1,
					Graph:           entityType.graph(identity.id),
				}
				data, err := json.Marshal(document)
				if err != nil {
					t.Fatalf("marshal document: %v", err)
				}
				remote := workflowGraphApplySavedRemote(t, 1)
				installWorkflowCommandRemote(t, remote)

				exitCode, outcome, stderr := runWorkflowGraphApplyCommand(
					t,
					[]string{"graph", "apply", "-", "--json"},
					string(data),
				)

				if identity.wantSaved {
					if exitCode != 0 || outcome.Outcome != workflowGraphApplySaved || stderr != "" {
						t.Fatalf("exit=%d outcome=%+v stderr=%q, want saved", exitCode, outcome, stderr)
					}
					if remote.previewCalls != 1 || remote.saveCalls != 1 {
						t.Fatalf("preview=%d save=%d, want one/one", remote.previewCalls, remote.saveCalls)
					}
					return
				}
				if exitCode != 1 || outcome.Outcome != workflowGraphApplyInvalidDocument || stderr != "" {
					t.Fatalf("exit=%d outcome=%+v stderr=%q, want invalid_document", exitCode, outcome, stderr)
				}
				if remote.previewCalls != 0 || remote.saveCalls != 0 {
					t.Fatalf("preview=%d save=%d, want zero/zero", remote.previewCalls, remote.saveCalls)
				}
			})
		}
	}
}

func TestWorkflowGraphApplyPreservesSameTypeLegacyIDsAndRejectsCrossTypeMatches(t *testing.T) {
	const legacyID = "legacy-graph-entity"
	const crossTypeID = "a8098c1a-f86e-4ea2-9c16-2d8f3a4b5c6d"
	entityTypes := workflowGraphApplyEntityKinds()
	for index, entityType := range entityTypes {
		t.Run(entityType.name+"/same type legacy ID", func(t *testing.T) {
			current := workflowGraphApplyDefinition(t, 1)
			entityType.addCurrent(&current, legacyID)
			remote := workflowGraphApplyUnchangedRemote(t, 1)
			remote.definition = current
			installWorkflowCommandRemote(t, remote)
			document := workflowGraphDocument{
				WorkflowID:      workflowGraphApplyID(t),
				ExpectedVersion: 1,
				Graph:           entityType.graph(legacyID),
			}
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("marshal document: %v", err)
			}

			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(
				t,
				[]string{"graph", "apply", "-", "--json"},
				string(data),
			)

			if exitCode != 0 || outcome.Outcome != workflowGraphApplyUnchanged || stderr != "" {
				t.Fatalf("exit=%d outcome=%+v stderr=%q, want unchanged", exitCode, outcome, stderr)
			}
			if remote.previewCalls != 1 || remote.saveCalls != 0 {
				t.Fatalf("preview=%d save=%d, want one/zero", remote.previewCalls, remote.saveCalls)
			}
		})

		t.Run(entityType.name+"/cross type match", func(t *testing.T) {
			current := workflowGraphApplyDefinition(t, 1)
			entityTypes[(index+1)%len(entityTypes)].addCurrent(&current, crossTypeID)
			remote := workflowGraphApplySavedRemote(t, 1)
			remote.definition = current
			installWorkflowCommandRemote(t, remote)
			document := workflowGraphDocument{
				WorkflowID:      workflowGraphApplyID(t),
				ExpectedVersion: 1,
				Graph:           entityType.graph(crossTypeID),
			}
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("marshal document: %v", err)
			}

			exitCode, outcome, stderr := runWorkflowGraphApplyCommand(
				t,
				[]string{"graph", "apply", "-", "--json"},
				string(data),
			)

			if exitCode != 1 || outcome.Outcome != workflowGraphApplyInvalidDocument || stderr != "" {
				t.Fatalf("exit=%d outcome=%+v stderr=%q, want invalid_document", exitCode, outcome, stderr)
			}
			if remote.previewCalls != 0 || remote.saveCalls != 0 {
				t.Fatalf("preview=%d save=%d, want zero/zero", remote.previewCalls, remote.saveCalls)
			}
		})
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

func TestWorkflowGraphApplyUsageErrorsExitTwoWithoutJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := workflowSubcommandWithInput([]string{"graph", "apply", "--json"}, bytes.NewBuffer(nil), &stdout, &stderr)
	if exitCode != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestWorkflowGraphApplyReportsCloseAndJSONWriterFailures(t *testing.T) {
	t.Run("close after unchanged", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		remote.closeError = errors.New("close failed")
		installWorkflowCommandRemote(t, remote)
		var stdout, stderr bytes.Buffer
		exitCode := workflowSubcommandWithInput(
			[]string{"graph", "apply", "-", "--json"},
			bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
			&stdout,
			&stderr,
		)
		if exitCode != 0 || stdout.Len() == 0 || stderr.Len() == 0 || remote.closeCalls != 1 {
			t.Fatalf("exit=%d stdout=%q stderr=%q close=%d", exitCode, stdout.String(), stderr.String(), remote.closeCalls)
		}
	})

	t.Run("JSON writer", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		installWorkflowCommandRemote(t, remote)
		var stderr bytes.Buffer
		exitCode := workflowSubcommandWithInput(
			[]string{"graph", "apply", "-", "--json"},
			bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
			bindingMutationFailingWriter{},
			&stderr,
		)
		if exitCode != 1 || stderr.Len() == 0 || remote.saveCalls != 0 || remote.closeCalls != 1 {
			t.Fatalf("exit=%d stderr=%q save=%d close=%d", exitCode, stderr.String(), remote.saveCalls, remote.closeCalls)
		}
	})

	t.Run("human writer", func(t *testing.T) {
		remote := workflowGraphApplyUnchangedRemote(t, 1)
		installWorkflowCommandRemote(t, remote)
		var stderr bytes.Buffer
		exitCode := workflowSubcommandWithInput(
			[]string{"graph", "apply", "-"},
			bytes.NewBufferString(emptyWorkflowGraphDocumentJSON),
			bindingMutationFailingWriter{},
			&stderr,
		)
		if exitCode != 1 || stderr.Len() == 0 || remote.saveCalls != 0 || remote.closeCalls != 1 {
			t.Fatalf("exit=%d stderr=%q save=%d close=%d", exitCode, stderr.String(), remote.saveCalls, remote.closeCalls)
		}
	})
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
