package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"core/shared/serverapi"
)

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
