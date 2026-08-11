package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowDeleteStub struct {
	apicontract.WorkflowService
	preview        serverapi.WorkflowDeletePreviewResponse
	result         serverapi.WorkflowDeleteResponse
	previewRequest serverapi.WorkflowDeletePreviewRequest
	deleteRequest  *serverapi.WorkflowDeleteRequest
}

type workflowPaginationStub struct {
	apicontract.WorkflowService
	request  serverapi.WorkflowListRequest
	response serverapi.WorkflowListResponse
}

func (s *workflowPaginationStub) ListWorkflows(
	_ context.Context,
	request serverapi.WorkflowListRequest,
) (serverapi.WorkflowListResponse, error) {
	s.request = request
	return s.response, nil
}

func TestWorkflowListPaginationSuccess(t *testing.T) {
	offset, limit, nextOffset := 5, 2, 7
	stub := &workflowPaginationStub{
		response: serverapi.WorkflowListResponse{
			Workflows:  []serverapi.WorkflowRecord{},
			NextOffset: &nextOffset,
		},
	}
	response, err := listWorkflowPage(t.Context(), stub, serverapi.WorkflowListRequest{
		Offset: &offset,
		Limit:  &limit,
	})
	if err != nil ||
		stub.request.Offset == nil ||
		*stub.request.Offset != offset ||
		stub.request.Limit == nil ||
		*stub.request.Limit != limit {
		t.Fatalf("request=%+v response=%+v err=%v", stub.request, response, err)
	}

	var stdout, stderr bytes.Buffer
	if code := writeWorkflowListResponse(
		&stdout,
		&stderr,
		response,
		workflowListExpectedScope{},
		true,
	); code != 0 || stderr.Len() != 0 {
		t.Fatalf("JSON exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var output struct {
		NextOffset *int              `json:"next_offset"`
		Workflows  []json.RawMessage `json:"workflows"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil ||
		output.NextOffset == nil ||
		*output.NextOffset != nextOffset ||
		len(output.Workflows) != 0 {
		t.Fatalf("output=%+v err=%v", output, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := writeWorkflowListResponse(
		&stdout,
		&stderr,
		response,
		workflowListExpectedScope{},
		false,
	); code != 0 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func (s *workflowDeleteStub) PreviewWorkflowDelete(
	_ context.Context,
	req serverapi.WorkflowDeletePreviewRequest,
) (serverapi.WorkflowDeletePreviewResponse, error) {
	s.previewRequest = req
	return s.preview, nil
}

func (s *workflowDeleteStub) DeleteWorkflow(
	_ context.Context,
	req serverapi.WorkflowDeleteRequest,
) (serverapi.WorkflowDeleteResponse, error) {
	s.deleteRequest = &req
	return s.result, nil
}

func TestWorkflowDeleteUsesPreviewedImpactAndTypedOutcome(t *testing.T) {
	workflowID := mustWorkflowID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	impact := serverapi.WorkflowDeleteImpact{
		WorkflowID: workflowID, Version: 7, ProjectCount: 2, LinkCount: 3, TaskCount: 5,
	}
	t.Run("preview only", func(t *testing.T) {
		remote := &workflowDeleteStub{preview: serverapi.WorkflowDeletePreviewResponse{Impact: impact}}
		var stdout, stderr bytes.Buffer
		if code := runWorkflowDelete(t.Context(), remote, workflowID, false, true, &stdout, &stderr); code != 1 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if remote.deleteRequest != nil || remote.previewRequest.WorkflowID != workflowID {
			t.Fatalf("preview=%+v delete=%+v", remote.previewRequest, remote.deleteRequest)
		}
		var output serverapi.WorkflowDeleteResponse
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatal(err)
		}
		if output.Deleted || output.Impact != impact || len(output.Blockers) != 0 {
			t.Fatalf("output=%+v", output)
		}
	})

	t.Run("confirmed request copies preview counts", func(t *testing.T) {
		remote := &workflowDeleteStub{
			preview: serverapi.WorkflowDeletePreviewResponse{Impact: impact},
			result:  serverapi.WorkflowDeleteResponse{Deleted: true, Impact: impact},
		}
		var stdout, stderr bytes.Buffer
		if code := runWorkflowDelete(t.Context(), remote, workflowID, true, true, &stdout, &stderr); code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		want := serverapi.WorkflowDeleteRequest{
			WorkflowID: workflowID, Confirmed: true, ExpectedVersion: 7,
			ExpectedProjectCount: 2, ExpectedLinkCount: 3, ExpectedTaskCount: 5,
		}
		if remote.deleteRequest == nil || *remote.deleteRequest != want {
			t.Fatalf("delete request=%+v want=%+v", remote.deleteRequest, want)
		}
	})

	t.Run("typed blocker", func(t *testing.T) {
		blocker := serverapi.WorkflowDeleteBlocker{Code: "current_nodes", Message: "busy", Count: 1}
		remote := &workflowDeleteStub{
			preview: serverapi.WorkflowDeletePreviewResponse{Impact: impact},
			result:  serverapi.WorkflowDeleteResponse{Impact: impact, Blockers: []serverapi.WorkflowDeleteBlocker{blocker}},
		}
		var stdout, stderr bytes.Buffer
		if code := runWorkflowDelete(t.Context(), remote, workflowID, true, true, &stdout, &stderr); code != 1 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		var output serverapi.WorkflowDeleteResponse
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatal(err)
		}
		if output.Deleted || len(output.Blockers) != 1 || output.Blockers[0] != blocker {
			t.Fatalf("output=%+v", output)
		}
	})
}

func TestWorkflowDeleteRejectsMismatchedOrInconsistentIdentity(t *testing.T) {
	workflowID := mustWorkflowID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	otherID := mustWorkflowID(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	valid := serverapi.WorkflowDeleteImpact{WorkflowID: workflowID, Version: 1}
	for _, test := range []struct {
		name    string
		preview serverapi.WorkflowDeleteImpact
		result  serverapi.WorkflowDeleteResponse
	}{
		{name: "preview identity", preview: serverapi.WorkflowDeleteImpact{WorkflowID: otherID}},
		{name: "result identity", preview: valid, result: serverapi.WorkflowDeleteResponse{
			Deleted: true, Impact: serverapi.WorkflowDeleteImpact{WorkflowID: otherID, Version: 1},
		}},
		{name: "deleted with blocker", preview: valid, result: serverapi.WorkflowDeleteResponse{
			Deleted: true, Impact: valid,
			Blockers: []serverapi.WorkflowDeleteBlocker{{Code: "busy", Message: "busy", Count: 1}},
		}},
		{name: "not deleted without blocker", preview: valid, result: serverapi.WorkflowDeleteResponse{Impact: valid}},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := &workflowDeleteStub{
				preview: serverapi.WorkflowDeletePreviewResponse{Impact: test.preview},
				result:  test.result,
			}
			var stdout, stderr bytes.Buffer
			if code := runWorkflowDelete(t.Context(), remote, workflowID, true, true, &stdout, &stderr); code != 1 ||
				stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

type workflowGraphApplyStub struct {
	apicontract.WorkflowService
	definition serverapi.WorkflowDefinition
	saves      []serverapi.WorkflowGraphSaveResponse
	requests   []serverapi.WorkflowGraphSaveRequest
}

func (s *workflowGraphApplyStub) GetWorkflow(
	context.Context,
	serverapi.WorkflowGetRequest,
) (serverapi.WorkflowGetResponse, error) {
	return serverapi.WorkflowGetResponse{Definition: s.definition}, nil
}

func (s *workflowGraphApplyStub) SaveWorkflowGraph(
	_ context.Context,
	req serverapi.WorkflowGraphSaveRequest,
) (serverapi.WorkflowGraphSaveResponse, error) {
	s.requests = append(s.requests, req)
	response := s.saves[0]
	s.saves = s.saves[1:]
	return response, nil
}

func TestWorkflowGraphApplyTypedOutcomesAndConfirmation(t *testing.T) {
	workflowID := mustWorkflowID(t, emptyWorkflowGraphDocumentID)
	document, err := decodeWorkflowGraphDocument([]byte(emptyWorkflowGraphDocumentJSON))
	if err != nil {
		t.Fatal(err)
	}
	confirmationBlocker := serverapi.WorkflowGraphSaveBlocker{
		Code: "confirmation_required", Message: "confirm", Count: 1,
		AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
	}
	confirmationImpact := emptyGraphImpact()
	confirmationImpact.RemovedEdgeCount = 1
	confirmationImpact.RemovedEntities = []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeEdge,
		EntityID:   "edge-1",
	}}
	for _, test := range []struct {
		name      string
		confirmed bool
		saves     []serverapi.WorkflowGraphSaveResponse
		want      workflowGraphApplyOutcomeKind
	}{
		{
			name: "unchanged",
			saves: []serverapi.WorkflowGraphSaveResponse{{
				Saved: true, CanSave: true, CurrentVersion: 1,
				ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
				Impact:            emptyGraphImpact(), Blockers: []serverapi.WorkflowGraphSaveBlocker{},
			}},
			want: workflowGraphApplyUnchanged,
		},
		{
			name: "blocked",
			saves: []serverapi.WorkflowGraphSaveResponse{{
				CurrentVersion:    1,
				ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
				Impact:            emptyGraphImpact(),
				Blockers: []serverapi.WorkflowGraphSaveBlocker{{
					Code: "validation_failed", Message: "invalid", Count: 1,
					AffectedEntities: []serverapi.WorkflowGraphEntityReference{},
				}},
			}},
			want: workflowGraphApplyBlocked,
		},
		{
			name: "confirmation required",
			saves: []serverapi.WorkflowGraphSaveResponse{{
				Changed: true, CanSave: true, ConfirmationRequired: true, CurrentVersion: 1,
				ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
				Impact:            confirmationImpact, Blockers: []serverapi.WorkflowGraphSaveBlocker{confirmationBlocker},
			}},
			want: workflowGraphApplyConfirmationRequired,
		},
		{
			name:      "confirmed save",
			confirmed: true,
			saves: []serverapi.WorkflowGraphSaveResponse{
				{
					Changed: true, CanSave: true, ConfirmationRequired: true, CurrentVersion: 1,
					ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
					Impact:            confirmationImpact, Blockers: []serverapi.WorkflowGraphSaveBlocker{confirmationBlocker},
				},
				{
					Saved: true, Changed: true, CanSave: true, CurrentVersion: 2,
					Definition: &serverapi.WorkflowDefinition{
						Workflow: serverapi.WorkflowRecord{ID: workflowID, Version: 2},
					},
					ValidationResults: map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{},
					Impact:            confirmationImpact, Blockers: []serverapi.WorkflowGraphSaveBlocker{},
				},
			},
			want: workflowGraphApplySaved,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := &workflowGraphApplyStub{
				definition: serverapi.WorkflowDefinition{
					Workflow: serverapi.WorkflowRecord{ID: workflowID, Version: 1},
				},
				saves: append([]serverapi.WorkflowGraphSaveResponse(nil), test.saves...),
			}
			outcome := runWorkflowGraphApply(t.Context(), remote, document, test.confirmed)
			if outcome.Outcome != test.want {
				t.Fatalf("outcome=%+v", outcome)
			}
			if test.confirmed {
				if len(remote.requests) != 2 || remote.requests[1].Confirmation == nil ||
					remote.requests[1].Confirmation.ExpectedRemovedEdgeCount != 1 {
					t.Fatalf("requests=%+v", remote.requests)
				}
			}
		})
	}
}

func TestWorkflowGraphApplyChecksStaleVersionBeforeAddedIdentity(t *testing.T) {
	workflowID := mustWorkflowID(t, emptyWorkflowGraphDocumentID)
	remote := &workflowGraphApplyStub{definition: serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: workflowID, Version: 2},
	}}
	document := workflowGraphDocument{
		WorkflowID: workflowID, ExpectedVersion: 1,
		Graph: workflowGraphDocumentGraph{
			NodeGroups: []serverapi.WorkflowGraphDraftNodeGroup{},
			Nodes: []workflowGraphDocumentNode{{
				ID: "not-a-uuid", Key: "node", Kind: "agent", DisplayName: "Node",
			}},
			TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
			Edges:            []serverapi.WorkflowGraphDraftEdge{},
		},
	}
	outcome := runWorkflowGraphApply(t.Context(), remote, document, false)
	if outcome.Outcome != workflowGraphApplyBlocked || len(outcome.Blockers) != 1 ||
		outcome.Blockers[0].Code != "version_changed" || len(remote.requests) != 0 {
		t.Fatalf("outcome=%+v requests=%+v", outcome, remote.requests)
	}
}

func TestWorkflowGraphAddedIdentityAndDraftContracts(t *testing.T) {
	const canonical = emptyWorkflowGraphDocumentID
	if err := validateWorkflowGraphAdditionIdentities(serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{
		Nodes: []serverapi.WorkflowGraphDraftNode{{ID: canonical}},
	}); err != nil {
		t.Fatalf("canonical addition rejected: %v", err)
	}
	if err := validateWorkflowGraphAdditionIdentities(serverapi.WorkflowDefinition{}, serverapi.WorkflowGraphDraft{
		Edges: []serverapi.WorkflowGraphDraftEdge{{ID: "edge-" + canonical}},
	}); err == nil {
		t.Fatal("prefixed added identity accepted")
	}
	if err := validateWorkflowGraphAdditionIdentities(serverapi.WorkflowDefinition{
		NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "legacy"}},
	}, serverapi.WorkflowGraphDraft{
		Nodes: []serverapi.WorkflowGraphDraftNode{{ID: "legacy"}},
	}); err == nil {
		t.Fatal("legacy identity reused across entity types")
	}

	workflowID := mustWorkflowID(t, canonical)
	definition := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: workflowID, Version: 3},
		NodeGroups: []serverapi.WorkflowNodeGroup{{
			GroupID: "group-id", GroupKey: "group", DisplayName: "Group",
		}},
		Nodes: []serverapi.WorkflowNode{{
			ID: "node-id", Key: "node", Kind: "agent", DisplayName: "Node", GroupID: "group-id",
		}},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{{
			ID: "transition-group-id", SourceNodeID: "node-id", TransitionID: "next",
		}},
		Edges: []serverapi.WorkflowEdge{{
			ID: "edge-id", TransitionGroupID: "transition-group-id", Key: "branch", TargetNodeID: "node-id",
			AssigneeSelection: "previous_node", ThinkingSelection: "configured",
			Parameters: []serverapi.WorkflowParameter{{Key: "role", Purpose: "target_assignee"}},
		}},
	}
	draft := workflowGraphDraftFromDefinition(definition)
	if draft.NodeGroups[0].ID != "group-id" || draft.Nodes[0].ID != "node-id" ||
		draft.TransitionGroups[0].ID != "transition-group-id" || draft.Edges[0].ID != "edge-id" ||
		draft.Edges[0].AssigneeSelection != "previous_node" ||
		draft.Edges[0].Parameters[0].Purpose != "target_assignee" {
		t.Fatalf("draft=%+v", draft)
	}
}

func TestWorkflowEdgeSelectionAndPureMutationValidation(t *testing.T) {
	for _, valid := range []string{"configured", " previous_node "} {
		if _, err := parseWorkflowSelectionMode("assignee-selection", valid); err != nil {
			t.Fatalf("valid selection %q: %v", valid, err)
		}
	}
	if _, err := parseWorkflowSelectionMode("thinking-selection", "invalid"); err == nil {
		t.Fatal("invalid selection accepted")
	}

	parameters, err := workflowEdgeParametersForAdd(
		[]serverapi.WorkflowParameter{{Key: "ordinary", Description: "value", Purpose: "ordinary"}},
		"previous_node",
		"configured",
		nil,
		nil,
	)
	if err != nil || len(parameters) != 2 || parameters[1].Purpose != "target_assignee" {
		t.Fatalf("parameters=%+v err=%v", parameters, err)
	}
	if _, err := workflowEdgeParametersForAdd(nil, "configured", "configured", &serverapi.WorkflowParameter{
		Key: "role", Purpose: "target_assignee",
	}, nil); err == nil {
		t.Fatal("protected parameter accepted for a disabled selector")
	}

	graph := serverapi.WorkflowGraphDraft{
		Nodes: []serverapi.WorkflowGraphDraftNode{
			{ID: "source-id", Key: "source"},
			{ID: "target-id", Key: "target"},
		},
		TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
		Edges:            []serverapi.WorkflowGraphDraftEdge{},
	}
	mutate := addWorkflowEdgeDraftMutation(workflowEdgeAddDraftMutation{
		SourceNodeKey:        "source",
		TargetNodeKey:        "target",
		TransitionID:         "next",
		NewTransitionGroupID: "group-id",
		Edge: serverapi.WorkflowGraphDraftEdge{
			ID: "edge-id", Key: "branch", AssigneeSelection: "configured", ThinkingSelection: "configured",
		},
	})
	updated, result, err := mutate(graph)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.TransitionGroups) != 1 || updated.TransitionGroups[0].ID != "group-id" ||
		len(updated.Edges) != 1 || result.Edge.ID != "edge-id" ||
		result.Edge.TransitionGroupID != "group-id" || result.Edge.TargetNodeID != "target-id" {
		t.Fatalf("updated=%+v result=%+v", updated, result)
	}
}

func TestWorkflowAndTaskSearchArityAndPaginationValidation(t *testing.T) {
	if err := validateWorkflowPagination(0, workflowCommandWorkflowListLimit); err != nil {
		t.Fatalf("valid Workflow pagination: %v", err)
	}
	for _, window := range [][2]int{{-1, 1}, {0, 0}, {0, serverapi.WorkflowPaginationMaxLimit + 1}} {
		if err := validateWorkflowPagination(window[0], window[1]); err == nil {
			t.Fatalf("invalid Workflow pagination %v accepted", window)
		}
	}
	for _, test := range []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{"workflow list positional", func(stdout, stderr *bytes.Buffer) int {
			return workflowListSubcommand([]string{"extra"}, stdout, stderr)
		}},
		{"workflow list removed token", func(stdout, stderr *bytes.Buffer) int {
			return workflowListSubcommand([]string{"--page-token", "legacy"}, stdout, stderr)
		}},
		{"task search missing query", func(stdout, stderr *bytes.Buffer) int {
			return taskSearchSubcommand(nil, stdout, stderr)
		}},
		{"task search extra query", func(stdout, stderr *bytes.Buffer) int {
			return taskSearchSubcommand([]string{"needle", "extra"}, stdout, stderr)
		}},
		{"task search negative offset", func(stdout, stderr *bytes.Buffer) int {
			return taskSearchSubcommand([]string{"needle", "--offset", "-1"}, stdout, stderr)
		}},
		{"task search incompatible flags", func(stdout, stderr *bytes.Buffer) int {
			return taskSearchSubcommand([]string{"needle", "--fts5", "--case-sensitive"}, stdout, stderr)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := test.run(&stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}

	statuses, err := parseTaskSearchStatusKinds([]string{"done,active", "done"})
	if err != nil || len(statuses) != 2 ||
		statuses[0] != serverapi.WorkflowTaskStatusKindActive ||
		statuses[1] != serverapi.WorkflowTaskStatusKindDone {
		t.Fatalf("statuses=%v err=%v", statuses, err)
	}
}

func TestWorkflowDispatchHelpRemainsAvailable(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"graph", "--help"},
		{"edge", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		if code := workflowSubcommand(args, &stdout, &stderr); code != 0 || stderr.Len() == 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	if selector, err := parseWorkflowSelector("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); err != nil ||
		!strings.EqualFold(selector.String(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa") {
		t.Fatalf("selector=%v err=%v", selector, err)
	}
}

func mustWorkflowID(t *testing.T, raw string) runtimeids.WorkflowID {
	t.Helper()
	id, err := runtimeids.ParseWorkflowID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func emptyGraphImpact() serverapi.WorkflowGraphSaveImpact {
	return serverapi.WorkflowGraphSaveImpact{
		RemovedEntities: []serverapi.WorkflowGraphEntityReference{},
	}
}
