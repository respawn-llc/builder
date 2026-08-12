package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"core/shared/serverapi"
)

const (
	emptyWorkflowGraphDocumentID   = "11111111-1111-4111-8111-111111111111"
	emptyWorkflowGraphDocumentJSON = `{"workflow_id":"` + emptyWorkflowGraphDocumentID + `","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`
)

func TestWorkflowGraphDocumentRequiresPublicShape(t *testing.T) {
	contract, err := prepareWorkflowGraphDocumentContract()
	if err != nil {
		t.Fatalf("prepare Workflow graph document contract: %v", err)
	}
	invalid := []string{
		`{`,
		emptyWorkflowGraphDocumentJSON + `{}`,
		`null`,
		`[]`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":-1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":[]}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":{},"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"11111111-1111-4111-8111-111111111112","kind":"agent","display_name":"Node","group_id":null}],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node","key":"node","kind":"agnt","display_name":"Node","group_id":null}],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node","key":"node","kind":"agent","display_name":"Node","group_id":null,"group_key":"group"}],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node","key":"node","kind":"agent","display_name":"Node"}],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node","key":"node","kind":"agent","display_name":"Node","group_id":7}],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":null,"nodes":[],"transition_groups":[],"edges":[]}}`,
		`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[{"id":"edge","transition_group_id":"group","key":"edge","target_node_id":"node","assignee_selection":"configured","thinking_selection":"configured","requires_approval":null,"context_mode":"new_session","context_source":null}]}}`,
	}
	for _, data := range invalid {
		document, err := contract.Decode([]byte(data))
		if err == nil {
			_, err = document.WorkflowGraphDraft()
		}
		if err == nil {
			t.Fatalf("invalid document accepted: %s", data)
		}
	}

	if document, err := contract.Decode([]byte(`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"expected_version":2,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`)); err != nil || document.ExpectedVersion != 2 {
		t.Fatalf("library duplicate-field semantics: document=%+v err=%v", document, err)
	}

	document, err := contract.Decode([]byte(`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"future":{"":true},"graph":{"node_groups":[{"id":"group","key":"group","display_name":"Group"}],"nodes":[{"id":"node","key":"node","kind":"agent","display_name":"Node","group_id":"group","script_path":null}],"transition_groups":[],"edges":[],"future":[]}}`))
	if err != nil {
		t.Fatalf("valid document: %v", err)
	}
	graph, err := document.WorkflowGraphDraft()
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].GroupID == nil || *graph.Nodes[0].GroupID != "group" || graph.Nodes[0].GroupKey != "" {
		t.Fatalf("Node membership = %+v", graph.Nodes)
	}
}

func TestWorkflowGraphDocumentEmitsExplicitArraysAndPreservesNestedOrder(t *testing.T) {
	contract, err := prepareWorkflowGraphDocumentContract()
	if err != nil {
		t.Fatalf("prepare Workflow graph document contract: %v", err)
	}
	document, err := workflowGraphDocumentFromDraft(mustWorkflowID(t, emptyWorkflowGraphDocumentID), 1, serverapi.WorkflowGraphDraft{
		NodeGroups:       []serverapi.WorkflowGraphDraftNodeGroup{},
		Nodes:            []serverapi.WorkflowGraphDraftNode{{ID: "node", Key: "node", Kind: "join", DisplayName: "Node", JoinInputProviders: []serverapi.WorkflowJoinInputProvider{{InputName: "second", ProviderEdgeID: "edge-2"}, {InputName: "first", ProviderEdgeID: "edge-1"}}}},
		TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
		Edges:            []serverapi.WorkflowGraphDraftEdge{},
	})
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := contract.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Graph.NodeGroups) != 0 || decoded.Graph.NodeGroups == nil ||
		len(decoded.Graph.TransitionGroups) != 0 || decoded.Graph.TransitionGroups == nil ||
		len(decoded.Graph.Edges) != 0 || decoded.Graph.Edges == nil ||
		decoded.Graph.Nodes[0].GroupID != nil ||
		decoded.Graph.Nodes[0].JoinInputProviders[0].InputName != "second" {
		t.Fatalf("round trip = %+v", decoded.Graph)
	}
}

func TestWorkflowGraphApplyRejectsInvalidDocumentBeforeRemoteComposition(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := workflowGraphApplySubcommand(
		[]string{"--json", "-"},
		strings.NewReader(`{"workflow_id":`),
		&stdout,
		&stderr,
	)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", exitCode, stderr.String())
	}
	var outcome workflowGraphApplyOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatalf("decode graph apply outcome: %v; stdout=%q", err, stdout.String())
	}
	if outcome.Outcome != workflowGraphApplyInvalidDocument {
		t.Fatalf("graph apply outcome = %+v, want invalid_document", outcome)
	}
}
