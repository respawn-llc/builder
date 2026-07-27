package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestWorkflowTaskListPageTokenUsesTypedModeInvariants(t *testing.T) {
	const tokenWorkflowID = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	base := workflowTaskListPageTokenPayload{
		Version:                     workflowTaskListPageTokenVersion,
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityMultiple,
		StatusModelVersion:          workflowTaskStatusModelVersion,
		Fingerprint:                 "fingerprint",
		Cursor:                      workflowTaskListCursor{TaskID: "task-1"},
	}
	projectWide := base
	projectWide.Scope = workflowTaskListPageTokenScope{
		ProjectID:   "project-1",
		ProjectWide: &workflowTaskListProjectWidePageTokenInvariants{},
	}
	parsed, ok, err := parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, projectWide))
	if err != nil || !ok || parsed.Scope.ProjectWide == nil || parsed.Scope.Narrowed != nil {
		t.Fatalf("parse project-wide token = %+v/%t/%v", parsed, ok, err)
	}
	raw, err := json.Marshal(projectWide)
	if err != nil {
		t.Fatalf("marshal project-wide token: %v", err)
	}
	var envelope struct {
		Scope map[string]json.RawMessage `json:"scope"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode project-wide token shape: %v", err)
	}
	if _, exists := envelope.Scope["narrowed"]; exists {
		t.Fatalf("project-wide token scope = %s, want no narrowed invariant block", raw)
	}

	narrowed := base
	narrowed.MatchingWorkflowCardinality = serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne
	narrowed.Scope = workflowTaskListPageTokenScope{
		ProjectID: "project-1",
		Narrowed: &workflowTaskListNarrowedPageTokenInvariants{
			WorkflowID:          tokenWorkflowID,
			WorkflowVersion:     2,
			ColumnStructureHash: "columns",
		},
	}
	parsed, ok, err = parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, narrowed))
	if err != nil || !ok || parsed.Scope.ProjectWide != nil || parsed.Scope.Narrowed == nil {
		t.Fatalf("parse narrowed token = %+v/%t/%v", parsed, ok, err)
	}
	raw, err = json.Marshal(narrowed)
	if err != nil {
		t.Fatalf("marshal narrowed token: %v", err)
	}
	envelope = struct {
		Scope map[string]json.RawMessage `json:"scope"`
	}{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode narrowed token shape: %v", err)
	}
	if _, exists := envelope.Scope["project_wide"]; exists {
		t.Fatalf("narrowed token scope = %s, want no project-wide invariant block", raw)
	}
}

func TestWorkflowTaskListPageTokenRejectsMalformedModeAndCardinality(t *testing.T) {
	const tokenWorkflowID = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	valid := workflowTaskListPageTokenPayload{
		Version: workflowTaskListPageTokenVersion,
		Scope: workflowTaskListPageTokenScope{
			ProjectID:   "project-1",
			ProjectWide: &workflowTaskListProjectWidePageTokenInvariants{},
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		StatusModelVersion:          workflowTaskStatusModelVersion,
		Fingerprint:                 "fingerprint",
		Cursor:                      workflowTaskListCursor{TaskID: "task-1"},
	}
	neitherMode := valid
	neitherMode.Scope.ProjectWide = nil
	bothModes := valid
	bothModes.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:          tokenWorkflowID,
		WorkflowVersion:     1,
		ColumnStructureHash: "columns",
	}
	invalidCardinality := valid
	invalidCardinality.MatchingWorkflowCardinality = serverapi.WorkflowTaskListMatchingWorkflowCardinalityNone
	missingWorkflowID := valid
	missingWorkflowID.Scope.ProjectWide = nil
	missingWorkflowID.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowVersion:     1,
		ColumnStructureHash: "columns",
	}
	missingWorkflowVersion := valid
	missingWorkflowVersion.Scope.ProjectWide = nil
	missingWorkflowVersion.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:          tokenWorkflowID,
		ColumnStructureHash: "columns",
	}
	missingColumnStructure := valid
	missingColumnStructure.Scope.ProjectWide = nil
	missingColumnStructure.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:      tokenWorkflowID,
		WorkflowVersion: 1,
	}
	paddedProjectID := valid
	paddedProjectID.Scope.ProjectID = " project-1"
	malformedWorkflowID := valid
	malformedWorkflowID.Scope.ProjectWide = nil
	malformedWorkflowID.Scope.Narrowed = &workflowTaskListNarrowedPageTokenInvariants{
		WorkflowID:          "workflow-1",
		WorkflowVersion:     1,
		ColumnStructureHash: "columns",
	}
	for name, payload := range map[string]workflowTaskListPageTokenPayload{
		"neither mode":             neitherMode,
		"both modes":               bothModes,
		"none cardinality":         invalidCardinality,
		"missing workflow id":      missingWorkflowID,
		"missing workflow version": missingWorkflowVersion,
		"missing column structure": missingColumnStructure,
		"padded project id":        paddedProjectID,
		"malformed workflow id":    malformedWorkflowID,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, payload)); !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("parse malformed token error = %v, want ErrInvalidPageToken", err)
			}
		})
	}
}

func TestWorkflowTaskListPageTokenRejectsPriorStatusModelVersion(t *testing.T) {
	payload := workflowTaskListPageTokenPayload{
		Version: workflowTaskListPageTokenVersion,
		Scope: workflowTaskListPageTokenScope{
			ProjectID:   "project-1",
			ProjectWide: &workflowTaskListProjectWidePageTokenInvariants{},
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		StatusModelVersion:          1,
		Fingerprint:                 "fingerprint",
		Cursor:                      workflowTaskListCursor{TaskID: "task-1"},
	}
	if _, _, err := parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, payload)); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("parse version-1 status token error = %v, want ErrInvalidPageToken", err)
	}
}

func TestWorkflowTaskListPageTokenRoundTripsCurrentStatusModelVersion(t *testing.T) {
	payload := workflowTaskListPageTokenPayload{
		Version: workflowTaskListPageTokenVersion,
		Scope: workflowTaskListPageTokenScope{
			ProjectID:   "project-1",
			ProjectWide: &workflowTaskListProjectWidePageTokenInvariants{},
		},
		MatchingWorkflowCardinality: serverapi.WorkflowTaskListMatchingWorkflowCardinalityOne,
		StatusModelVersion:          workflowTaskStatusModelVersion,
		Fingerprint:                 "fingerprint",
		Cursor:                      workflowTaskListCursor{TaskID: "task-1"},
	}
	parsed, present, err := parseWorkflowTaskListPageToken(workflowTaskListPageTokenForTest(t, payload))
	if err != nil || !present {
		t.Fatalf("parse current-version status token = %+v/%t/%v", parsed, present, err)
	}
	if parsed.StatusModelVersion != workflowTaskStatusModelVersion {
		t.Fatalf("current-version status token = %d, want %d", parsed.StatusModelVersion, workflowTaskStatusModelVersion)
	}
}

func workflowTaskListPageTokenForTest(t *testing.T, payload workflowTaskListPageTokenPayload) string {
	t.Helper()
	token, err := workflowTaskListPageToken(payload)
	if err != nil {
		t.Fatalf("workflowTaskListPageToken: %v", err)
	}
	return token
}

func TestWorkflowTaskListRequestFingerprintCanonicalizesSetFilters(t *testing.T) {
	sortSelectors := []serverapi.WorkflowTaskListSort{
		{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
	}
	first, err := workflowTaskListRequestFingerprint(serverapi.WorkflowTaskListRequest{
		LabelFilter:    serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ColumnKeys:     []string{"done", "backlog", "done"},
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindRunning, serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindRunning},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion, serverapi.WorkflowTaskAttentionKindApproval},
	}, workflowTaskLabelFilterFacts{
		Kind:     serverapi.WorkflowTaskLabelFilterKindNone,
		LabelIDs: []string{},
	}, sortSelectors, workflowTaskListFingerprintScope{
		Narrowed: &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: "columns"},
	})
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	second, err := workflowTaskListRequestFingerprint(serverapi.WorkflowTaskListRequest{
		LabelFilter:    serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ColumnKeys:     []string{"backlog", "done"},
		StatusKinds:    []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog, serverapi.WorkflowTaskStatusKindRunning},
		AttentionKinds: []serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval, serverapi.WorkflowTaskAttentionKindQuestion},
	}, workflowTaskLabelFilterFacts{
		Kind:     serverapi.WorkflowTaskLabelFilterKindNone,
		LabelIDs: []string{},
	}, sortSelectors, workflowTaskListFingerprintScope{
		Narrowed: &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: "columns"},
	})
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if first != second {
		t.Fatalf("canonical fingerprints differ: %q != %q", first, second)
	}
}

func TestWorkflowTaskListRequestFingerprintIncludesExcludedConditions(t *testing.T) {
	const (
		alphaID = "00000000-0000-4000-8000-000000000001"
		betaID  = "00000000-0000-4000-8000-000000000002"
	)
	mode := serverapi.WorkflowTaskNamedLabelFilterModeAny
	scope := workflowTaskListFingerprintScope{
		ProjectWide: &workflowTaskListProjectWideFingerprintInvariants{},
	}
	sortSelectors := normalizeWorkflowTaskListSort(nil)
	includedAlphaExcludedBeta, err := workflowTaskListRequestFingerprint(
		serverapi.WorkflowTaskListRequest{LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:             mode,
				LabelIDs:         []string{alphaID},
				ExcludedLabelIDs: []string{betaID},
			},
		}},
		workflowTaskLabelFilterFacts{
			Kind:             serverapi.WorkflowTaskLabelFilterKindNamed,
			Mode:             &mode,
			LabelIDs:         []string{alphaID},
			ExcludedLabelIDs: []string{betaID},
		},
		sortSelectors,
		scope,
	)
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	includedBetaExcludedAlpha, err := workflowTaskListRequestFingerprint(
		serverapi.WorkflowTaskListRequest{LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:             mode,
				LabelIDs:         []string{betaID},
				ExcludedLabelIDs: []string{alphaID},
			},
		}},
		workflowTaskLabelFilterFacts{
			Kind:             serverapi.WorkflowTaskLabelFilterKindNamed,
			Mode:             &mode,
			LabelIDs:         []string{betaID},
			ExcludedLabelIDs: []string{alphaID},
		},
		sortSelectors,
		scope,
	)
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if includedAlphaExcludedBeta == includedBetaExcludedAlpha {
		t.Fatalf("fingerprints collide across label-condition polarity: %q", includedAlphaExcludedBeta)
	}
}

func TestWorkflowTaskListRequestFingerprintRequiresOneTypedScope(t *testing.T) {
	request := serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}}
	sortSelectors := normalizeWorkflowTaskListSort(nil)
	for name, scope := range map[string]workflowTaskListFingerprintScope{
		"missing mode": {},
		"both modes": {
			ProjectWide: &workflowTaskListProjectWideFingerprintInvariants{},
			Narrowed:    &workflowTaskListNarrowedFingerprintInvariants{ColumnStructureHash: "columns"},
		},
		"blank narrowed hash": {
			Narrowed: &workflowTaskListNarrowedFingerprintInvariants{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflowTaskListRequestFingerprint(
				request,
				workflowTaskLabelFilterFacts{
					Kind:     serverapi.WorkflowTaskLabelFilterKindNone,
					LabelIDs: []string{},
				},
				sortSelectors,
				scope,
			); err == nil {
				t.Fatalf("workflowTaskListRequestFingerprint accepted %s", name)
			}
		})
	}
}

func TestCanceledBoardTerminalNodeIDUsesTypedAbsence(t *testing.T) {
	if got := canceledBoardTerminalNodeID(serverapi.WorkflowDefinition{}); got != nil {
		t.Fatalf("canceledBoardTerminalNodeID without terminal = %v, want nil", got)
	}
	def := serverapi.WorkflowDefinition{Nodes: []serverapi.WorkflowNode{
		{ID: "node-terminal", Key: "archive", Kind: string(workflow.NodeKindTerminal)},
		{ID: "node-done", Key: "done", Kind: string(workflow.NodeKindTerminal)},
	}}
	got := canceledBoardTerminalNodeID(def)
	if got == nil || *got != "node-done" {
		t.Fatalf("canceledBoardTerminalNodeID = %v, want done terminal", got)
	}
}

func TestTaskListInfersScopeFromContinuationToken(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	for index := 0; index < 2; index++ {
		if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"}); err != nil {
			t.Fatalf("CreateTask %d: %v", index, err)
		}
	}
	projectID, workflowIDValue := binding.ProjectID, string(workflowID)
	firstPage, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &projectID, WorkflowID: &workflowIDValue, PageSize: 1})
	if err != nil {
		t.Fatalf("ListTasks first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("expected continuation token")
	}
	nextPage, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, PageToken: *firstPage.NextPageToken, PageSize: 1})
	if err != nil {
		t.Fatalf("ListTasks token-only continuation: %v", err)
	}
	if nextPage.Scope.ProjectID != binding.ProjectID || nextPage.Scope.WorkflowID == nil || *nextPage.Scope.WorkflowID != string(workflowID) || len(nextPage.Tasks) != 1 || nextPage.Tasks[0].TaskID == firstPage.Tasks[0].TaskID {
		t.Fatalf("token-only continuation = %+v, want resolved second page", nextPage)
	}
	otherProjectID := "project-conflict"
	_, err = view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &otherProjectID, PageToken: *firstPage.NextPageToken, PageSize: 1})
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("ListTasks conflicting token scope error = %v, want ErrInvalidPageToken", err)
	}
}

func createTwoLinkedWorkflowViewWorkflows(t *testing.T, ctx context.Context, store *workflowstore.Store, projectID string) (workflow.WorkflowID, workflow.WorkflowID) {
	t.Helper()
	firstWorkflowID := createWorkflowViewValidWorkflow(t, ctx, store)
	secondWorkflowID := createWorkflowViewValidWorkflow(t, ctx, store)
	if _, err := store.LinkWorkflow(ctx, projectID, firstWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow first: %v", err)
	}
	if _, err := store.LinkWorkflow(ctx, projectID, secondWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow second: %v", err)
	}
	return firstWorkflowID, secondWorkflowID
}

func scopeIDSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func taskStatusKinds(items []serverapi.WorkflowTaskListItem) []serverapi.WorkflowTaskStatusKind {
	kinds := make([]serverapi.WorkflowTaskStatusKind, 0, len(items))
	for _, item := range items {
		kinds = append(kinds, item.Status.Kind)
	}
	return kinds
}
