package serverapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

const (
	workflowLabelIDAlpha = "11111111-1111-4111-8111-111111111111"
	workflowLabelIDBeta  = "22222222-2222-4222-8222-222222222222"
)

func TestWorkflowLabelPublicContractsRoundTrip(t *testing.T) {
	projectID := "project-1"
	taskID := "task-1"
	workflowID := runtimeids.NewWorkflowID()
	nodeID := "node-1"

	catalog := WorkflowProjectLabelCatalog{
		ProjectID: projectID,
		Labels: []WorkflowProjectLabel{
			{ID: workflowLabelIDAlpha, Name: "Alpha"},
			{ID: workflowLabelIDBeta, Name: "Beta"},
		},
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	var catalogShape map[string]any
	if err := json.Unmarshal(data, &catalogShape); err != nil {
		t.Fatalf("decode catalog JSON: %v", err)
	}
	if _, present := catalogShape["labels"]; !present {
		t.Fatalf("catalog JSON omits labels: %s", data)
	}
	var decodedCatalog WorkflowProjectLabelCatalog
	if err := json.Unmarshal(data, &decodedCatalog); err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}
	if !slices.EqualFunc(decodedCatalog.Labels, catalog.Labels, func(left, right WorkflowProjectLabel) bool {
		return left == right
	}) {
		t.Fatalf("catalog labels = %+v, want %+v", decodedCatalog.Labels, catalog.Labels)
	}
	assignment := WorkflowTaskAssignedLabelIDs{
		TaskID:   taskID,
		LabelIDs: []string{workflowLabelIDAlpha, workflowLabelIDBeta},
	}
	assignmentJSON, assignmentShape := marshalWorkflowJSON[map[string]any](t, WorkflowTaskLabelsUpdateResponse{
		Assignment: assignment,
	})
	assignmentObject, ok := assignmentShape["assignment"].(map[string]any)
	if !ok || assignmentObject["task_id"] != taskID || !slices.Equal(assignmentObject["label_ids"].([]any), []any{workflowLabelIDAlpha, workflowLabelIDBeta}) {
		t.Fatalf("authoritative assignment JSON = %s", assignmentJSON)
	}

	none := WorkflowTaskLabelFilter{Kind: WorkflowTaskLabelFilterKindNone}
	namedAny := WorkflowTaskLabelFilter{
		Kind: WorkflowTaskLabelFilterKindNamed,
		Named: &WorkflowTaskNamedLabelFilter{
			Mode:     WorkflowTaskNamedLabelFilterModeAny,
			LabelIDs: []string{workflowLabelIDBeta, workflowLabelIDAlpha},
		},
	}
	namedAll := WorkflowTaskLabelFilter{
		Kind: WorkflowTaskLabelFilterKindNamed,
		Named: &WorkflowTaskNamedLabelFilter{
			Mode:     WorkflowTaskNamedLabelFilterModeAll,
			LabelIDs: []string{workflowLabelIDAlpha},
		},
	}
	namedExcluded := WorkflowTaskLabelFilter{
		Kind: WorkflowTaskLabelFilterKindNamed,
		Named: &WorkflowTaskNamedLabelFilter{
			Mode:             WorkflowTaskNamedLabelFilterModeAny,
			ExcludedLabelIDs: []string{workflowLabelIDAlpha},
		},
	}
	unlabeled := WorkflowTaskLabelFilter{Kind: WorkflowTaskLabelFilterKindUnlabeled}
	for _, filter := range []WorkflowTaskLabelFilter{none, namedAny, namedAll, namedExcluded, unlabeled} {
		if err := filter.Validate(); err != nil {
			t.Fatalf("valid filter %+v rejected: %v", filter, err)
		}
		filterJSON, err := json.Marshal(filter)
		if err != nil {
			t.Fatalf("marshal filter %+v: %v", filter, err)
		}
		var decoded WorkflowTaskLabelFilter
		if err := json.Unmarshal(filterJSON, &decoded); err != nil {
			t.Fatalf("unmarshal filter %s: %v", filterJSON, err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("decoded filter %+v rejected: %v", decoded, err)
		}
	}

	create := WorkflowTaskCreateRequest{
		ProjectID:  projectID,
		WorkflowID: &workflowID,
		Title:      "Task",
		LabelIDs:   []string{workflowLabelIDAlpha, workflowLabelIDBeta},
	}
	if err := create.Validate(); err != nil {
		t.Fatalf("labeled task create rejected: %v", err)
	}
	update := WorkflowTaskLabelsUpdateRequest{
		TaskID:         taskID,
		AddLabelIDs:    []string{workflowLabelIDAlpha},
		RemoveLabelIDs: []string{workflowLabelIDBeta},
	}
	if err := update.Validate(); err != nil {
		t.Fatalf("task label update rejected: %v", err)
	}
	if err := (WorkflowTaskLabelsGetRequest{TaskID: taskID}).Validate(); err != nil {
		t.Fatalf("task label get rejected: %v", err)
	}
	if err := (WorkflowProjectLabelCatalogRequest{ProjectID: projectID}).Validate(); err != nil {
		t.Fatalf("catalog request rejected: %v", err)
	}
	if err := (WorkflowProjectLabelCreateRequest{ProjectID: projectID, Name: "Alpha"}).Validate(); err != nil {
		t.Fatalf("label create rejected: %v", err)
	}
	if err := (WorkflowProjectLabelRenameRequest{ProjectID: projectID, LabelID: workflowLabelIDAlpha, Name: "Renamed"}).Validate(); err != nil {
		t.Fatalf("label rename rejected: %v", err)
	}
	if err := (WorkflowProjectLabelDeleteRequest{ProjectID: projectID, LabelID: workflowLabelIDAlpha}).Validate(); err != nil {
		t.Fatalf("label delete rejected: %v", err)
	}

	taskList := WorkflowTaskListRequest{ProjectID: &projectID, LabelFilter: namedAny}
	if err := taskList.Validate(); err != nil {
		t.Fatalf("task list named filter rejected: %v", err)
	}
	board := WorkflowBoardRequest{ProjectID: projectID, LabelFilter: unlabeled}
	if err := board.Validate(); err != nil {
		t.Fatalf("board unlabeled filter rejected: %v", err)
	}
	cards := WorkflowBoardNodeCardsListRequest{
		ProjectID:   projectID,
		WorkflowID:  workflowID,
		NodeID:      nodeID,
		LabelFilter: namedAll,
	}
	if err := cards.Validate(); err != nil {
		t.Fatalf("board cards all filter rejected: %v", err)
	}
}

func TestWorkflowTaskListRequestRejectsZeroWorkflowID(t *testing.T) {
	projectID := "project-1"
	workflowID := runtimeids.WorkflowID{}
	request := WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
		LabelFilter: WorkflowTaskLabelFilterNone(),
	}
	if !hasWorkflowRequestError(request.Validate(), "workflow_id", WorkflowRequestErrorRequired) {
		t.Fatalf("Validate error = %v, want workflow_id required", request.Validate())
	}
}

func TestWorkflowTaskListRequestRoundTripsAndValidatesStatusKinds(t *testing.T) {
	projectID := "project-1"
	for _, statusKinds := range [][]WorkflowTaskStatusKind{
		{
			WorkflowTaskStatusKindWaitingQuestion,
			WorkflowTaskStatusKindWaitingApproval,
			WorkflowTaskStatusKindInterrupted,
			WorkflowTaskStatusKindRunning,
			WorkflowTaskStatusKindQueued,
			WorkflowTaskStatusKindActive,
		},
		{WorkflowTaskStatusKindBacklog},
		{WorkflowTaskStatusKindDone},
	} {
		request := WorkflowTaskListRequest{
			ProjectID:   &projectID,
			StatusKinds: statusKinds,
			LabelFilter: WorkflowTaskLabelFilterNone(),
		}
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal status kinds %v: %v", statusKinds, err)
		}
		var decoded WorkflowTaskListRequest
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal status kinds %v: %v", statusKinds, err)
		}
		if !slices.Equal(decoded.StatusKinds, statusKinds) {
			t.Fatalf("decoded status kinds = %v, want %v", decoded.StatusKinds, statusKinds)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("Validate status kinds %v: %v", statusKinds, err)
		}
	}

	request := WorkflowTaskListRequest{
		ProjectID:   &projectID,
		StatusKinds: []WorkflowTaskStatusKind{"future"},
		LabelFilter: WorkflowTaskLabelFilterNone(),
	}
	if !hasWorkflowRequestError(request.Validate(), "status_kinds[0]", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("Validate error = %v, want status kind invalid value", request.Validate())
	}
	group := WorkflowProjectTaskGroupActive
	request = WorkflowTaskListRequest{
		ProjectID:   &projectID,
		Group:       &group,
		LabelFilter: WorkflowTaskLabelFilterNone(),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate group: %v", err)
	}
	request.StatusKinds = []WorkflowTaskStatusKind{WorkflowTaskStatusKindActive}
	if !hasWorkflowRequestError(request.Validate(), "group", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("Validate group/status conflict = %v, want invalid group", request.Validate())
	}
}

func TestWorkflowProjectTaskGroupCountsContractIsProjectScopedAndNonPaginated(t *testing.T) {
	request := WorkflowProjectTaskGroupCountsRequest{ProjectID: "project-1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	_, requestJSON := marshalWorkflowJSON[map[string]json.RawMessage](t, request)
	if len(requestJSON) != 1 {
		t.Fatalf("group-count request JSON keys = %v, want only project_id", requestJSON)
	}
	var serializedProjectID string
	if err := json.Unmarshal(requestJSON["project_id"], &serializedProjectID); err != nil {
		t.Fatalf("decode request project_id: %v", err)
	}
	if serializedProjectID != request.ProjectID {
		t.Fatalf("request project_id = %q, want %q", serializedProjectID, request.ProjectID)
	}
	response := WorkflowProjectTaskGroupCountsResponse{
		ProjectID:   "project-1",
		Definitions: WorkflowProjectTaskGroupDefinitions(),
		Counts: WorkflowProjectTaskGroupCounts{
			Active:  3,
			Backlog: 2,
			Done:    1,
		},
		GeneratedAtUnixMs: 7,
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response Validate: %v", err)
	}
	data, responseJSON := marshalWorkflowJSON[map[string]json.RawMessage](t, response)
	for _, forbiddenKey := range []string{"tasks", "offset", "limit"} {
		if _, exists := responseJSON[forbiddenKey]; exists {
			t.Fatalf("group-count response includes forbidden key %q: %s", forbiddenKey, data)
		}
	}
	var serializedDefinitions []WorkflowProjectTaskGroupDefinition
	if err := json.Unmarshal(responseJSON["definitions"], &serializedDefinitions); err != nil {
		t.Fatalf("decode response definitions: %v", err)
	}
	if !reflect.DeepEqual(serializedDefinitions, response.Definitions) {
		t.Fatalf("response definitions = %+v, want %+v", serializedDefinitions, response.Definitions)
	}

	request.ProjectID = ""
	if !hasWorkflowRequestError(request.Validate(), "project_id", WorkflowRequestErrorRequired) {
		t.Fatalf("blank project Validate error = %v", request.Validate())
	}
	response.Counts.Active = -1
	if !hasWorkflowRequestError(response.Validate(), "counts.active", WorkflowRequestErrorInvalidValue) {
		t.Fatalf("negative count Validate error = %v", response.Validate())
	}
}

func TestWorkflowDependencyFilterRequestContractsRoundTripAndValidate(t *testing.T) {
	projectID := "project-1"
	workflowID := runtimeids.NewWorkflowID()
	nodeID := "node-1"
	for _, dependencyFilter := range []*bool{nil, boolPointerForWorkflowTest(false), boolPointerForWorkflowTest(true)} {
		t.Run(dependencyFilterName(dependencyFilter), func(t *testing.T) {
			requests := []any{
				WorkflowBoardRequest{
					ProjectID:        projectID,
					WorkflowID:       &workflowID,
					LabelFilter:      WorkflowTaskLabelFilterNone(),
					DependencyFilter: dependencyFilter,
				},
				WorkflowBoardNodeCardsListRequest{
					ProjectID:        projectID,
					WorkflowID:       workflowID,
					NodeID:           nodeID,
					LabelFilter:      WorkflowTaskLabelFilterNone(),
					DependencyFilter: dependencyFilter,
				},
				WorkflowTaskListRequest{
					ProjectID:        &projectID,
					WorkflowID:       &workflowID,
					LabelFilter:      WorkflowTaskLabelFilterNone(),
					DependencyFilter: dependencyFilter,
				},
			}
			for _, request := range requests {
				encoded, err := json.Marshal(request)
				if err != nil {
					t.Fatalf("marshal %T: %v", request, err)
				}
				var shape map[string]any
				if err := json.Unmarshal(encoded, &shape); err != nil {
					t.Fatalf("decode %T: %v", request, err)
				}
				if dependencyFilter == nil {
					if _, present := shape["dependency_filter"]; present {
						t.Fatalf("%T JSON unexpectedly includes absent dependency filter: %s", request, encoded)
					}
				} else if shape["dependency_filter"] != *dependencyFilter {
					t.Fatalf("%T dependency filter JSON = %#v, want %t", request, shape["dependency_filter"], *dependencyFilter)
				}
				decoded := reflect.New(reflect.TypeOf(request)).Elem()
				if err := json.Unmarshal(encoded, decoded.Addr().Interface()); err != nil {
					t.Fatalf("unmarshal %T: %v", request, err)
				}
				if !reflect.DeepEqual(decoded.Interface(), request) {
					t.Fatalf("%T round trip = %#v, want %#v", request, decoded.Interface(), request)
				}
				if validator, ok := request.(interface{ Validate() error }); ok {
					if err := validator.Validate(); err != nil {
						t.Fatalf("%T Validate: %v", request, err)
					}
				}
			}
		})
	}
}

func boolPointerForWorkflowTest(value bool) *bool {
	return &value
}

func dependencyFilterName(value *bool) string {
	if value == nil {
		return "all"
	}
	if *value {
		return "unblocked"
	}
	return "blocked"
}

func TestWorkflowTaskNamedLabelFilterExclusionsValidateAndMarshalAdditively(t *testing.T) {
	mixed := WorkflowTaskLabelFilter{
		Kind: WorkflowTaskLabelFilterKindNamed,
		Named: &WorkflowTaskNamedLabelFilter{
			Mode:             WorkflowTaskNamedLabelFilterModeAny,
			LabelIDs:         []string{workflowLabelIDAlpha},
			ExcludedLabelIDs: []string{workflowLabelIDBeta},
		},
	}
	if err := mixed.Validate(); err != nil {
		t.Fatalf("mixed named filter rejected: %v", err)
	}
	data, err := json.Marshal(mixed)
	if err != nil {
		t.Fatalf("marshal mixed named filter: %v", err)
	}
	var shape struct {
		Named map[string]any `json:"named"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatalf("decode mixed named filter: %v", err)
	}
	if !slices.Equal(shape.Named["excluded_label_ids"].([]any), []any{workflowLabelIDBeta}) {
		t.Fatalf("mixed named filter exclusions = %#v, want [%q]", shape.Named["excluded_label_ids"], workflowLabelIDBeta)
	}

	includeOnly := WorkflowTaskLabelFilter{
		Kind: WorkflowTaskLabelFilterKindNamed,
		Named: &WorkflowTaskNamedLabelFilter{
			Mode:     WorkflowTaskNamedLabelFilterModeAll,
			LabelIDs: []string{workflowLabelIDAlpha},
		},
	}
	includeOnlyData, err := json.Marshal(includeOnly)
	if err != nil {
		t.Fatalf("marshal include-only named filter: %v", err)
	}
	shape = struct {
		Named map[string]any `json:"named"`
	}{}
	if err := json.Unmarshal(includeOnlyData, &shape); err != nil {
		t.Fatalf("decode include-only named filter: %v", err)
	}
	if _, exists := shape.Named["excluded_label_ids"]; exists {
		t.Fatalf("include-only named filter unexpectedly carries exclusions: %s", includeOnlyData)
	}
}

func TestWorkflowLabelSuccessDTOValidation(t *testing.T) {
	label := WorkflowProjectLabel{ID: workflowLabelIDAlpha, Name: "Alpha"}
	catalog := WorkflowProjectLabelCatalog{
		ProjectID: "project-1",
		Labels:    []WorkflowProjectLabel{label},
	}
	assignment := WorkflowTaskAssignedLabelIDs{
		TaskID:   "task-1",
		LabelIDs: []string{workflowLabelIDAlpha},
	}
	testValidWorkflowRequests(t, []workflowValidRequestCase{
		{name: "project label", request: label},
		{name: "project label catalog", request: catalog},
		{name: "project label catalog response", request: WorkflowProjectLabelCatalogResponse{Catalog: catalog}},
		{name: "project label create response", request: WorkflowProjectLabelCreateResponse{Label: label}},
		{name: "project label rename response", request: WorkflowProjectLabelRenameResponse{Label: label}},
		{name: "project label delete response", request: WorkflowProjectLabelDeleteResponse{LabelID: workflowLabelIDAlpha}},
		{name: "task assignment", request: assignment},
		{name: "task label get response", request: WorkflowTaskLabelsGetResponse{Assignment: assignment}},
		{name: "task label update response", request: WorkflowTaskLabelsUpdateResponse{Assignment: assignment}},
		{name: "task detail projection", request: WorkflowTaskDetail{Summary: WorkflowTaskSummary{ID: "task-1"}, LabelIDs: []string{workflowLabelIDAlpha}, Dependencies: emptyWorkflowTaskDependenciesForTest()}},
		{name: "task list projection", request: WorkflowTaskListItem{
			TaskID: "task-1", WorkflowID: runtimeids.NewWorkflowID(),
			Status: WorkflowTaskStatus{Kind: WorkflowTaskStatusKindBacklog, NativeState: WorkflowTaskNativeStateActive},
			Labels: []WorkflowProjectLabel{label},
		}},
		{name: "board card projection", request: WorkflowBoardTaskCard{TaskID: "task-1", LabelIDs: []string{workflowLabelIDAlpha}}},
		{name: "board card projection response", request: WorkflowBoardNodeCardsListResponse{Cards: []WorkflowBoardTaskCard{{TaskID: "task-1", LabelIDs: []string{workflowLabelIDAlpha}}}}},
	})

	raw101 := make([]string, WorkflowLabelMaxIDs+1)
	for index := range raw101 {
		raw101[index] = "not-a-uuid"
	}
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{name: "label requires canonical ID", request: WorkflowProjectLabel{Name: "Alpha"}, field: "id", code: WorkflowRequestErrorInvalidValue},
		{name: "label requires name", request: WorkflowProjectLabel{ID: workflowLabelIDAlpha}, field: "name", code: WorkflowRequestErrorRequired},
		{name: "catalog requires project", request: WorkflowProjectLabelCatalog{Labels: []WorkflowProjectLabel{label}}, field: "project_id", code: WorkflowRequestErrorRequired},
		{name: "catalog raw 101 labels wins over malformed IDs", request: WorkflowProjectLabelCatalog{ProjectID: "project-1", Labels: make([]WorkflowProjectLabel, WorkflowLabelMaxIDs+1)}, field: "labels", code: WorkflowRequestErrorTooLong},
		{name: "assignment requires task", request: WorkflowTaskAssignedLabelIDs{LabelIDs: []string{workflowLabelIDAlpha}}, field: "task_id", code: WorkflowRequestErrorRequired},
		{name: "assignment raw 101 IDs wins over malformed IDs", request: WorkflowTaskAssignedLabelIDs{TaskID: "task-1", LabelIDs: raw101}, field: "label_ids", code: WorkflowRequestErrorTooLong},
		{name: "detail rejects duplicate IDs", request: WorkflowTaskDetail{Summary: WorkflowTaskSummary{ID: "task-1"}, LabelIDs: []string{workflowLabelIDAlpha, workflowLabelIDAlpha}}, field: "task.label_ids[1]", code: WorkflowRequestErrorInvalidValue},
		{name: "list item requires task ID", request: WorkflowTaskListItem{Labels: []WorkflowProjectLabel{label}}, field: "task_id", code: WorkflowRequestErrorRequired},
		{name: "board card requires task ID", request: WorkflowBoardTaskCard{LabelIDs: []string{workflowLabelIDAlpha}}, field: "task_id", code: WorkflowRequestErrorRequired},
	})
}

func TestWorkflowLabelContractsRejectInvalidCollectionsBeforeUUIDWork(t *testing.T) {
	projectID := "project-1"
	raw101 := make([]string, WorkflowLabelMaxIDs+1)
	for index := range raw101 {
		raw101[index] = "not-a-uuid"
	}
	testWorkflowFieldErrors(t, []workflowFieldErrorCase{
		{
			name:    "task create raw 101 IDs wins over malformed IDs",
			request: WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Task", LabelIDs: raw101},
			field:   "label_ids",
			code:    WorkflowRequestErrorTooLong,
		},
		{
			name: "task label add raw 101 IDs wins over malformed IDs",
			request: WorkflowTaskLabelsUpdateRequest{
				TaskID:      "task-1",
				AddLabelIDs: raw101,
			},
			field: "add_label_ids",
			code:  WorkflowRequestErrorTooLong,
		},
		{
			name: "named filter raw 101 IDs wins over malformed IDs",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{
					Mode:     WorkflowTaskNamedLabelFilterModeAny,
					LabelIDs: raw101,
				},
			},
			field: "label_filter.label_ids",
			code:  WorkflowRequestErrorTooLong,
		},
		{
			name: "named filter combined 101 IDs wins over malformed IDs",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{
					Mode:             WorkflowTaskNamedLabelFilterModeAny,
					LabelIDs:         raw101[:WorkflowLabelMaxIDs],
					ExcludedLabelIDs: raw101[WorkflowLabelMaxIDs:],
				},
			},
			field: "label_filter.excluded_label_ids",
			code:  WorkflowRequestErrorTooLong,
		},
		{
			name:    "task create rejects non-canonical label ID",
			request: WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Task", LabelIDs: []string{"11111111-1111-4111-8111-111111111111 "}},
			field:   "label_ids[0]",
			code:    WorkflowRequestErrorInvalidValue,
		},
		{
			name:    "task create rejects duplicate label ID",
			request: WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Task", LabelIDs: []string{workflowLabelIDAlpha, workflowLabelIDAlpha}},
			field:   "label_ids[1]",
			code:    WorkflowRequestErrorInvalidValue,
		},
		{
			name: "task label mutation rejects overlap",
			request: WorkflowTaskLabelsUpdateRequest{
				TaskID:         "task-1",
				AddLabelIDs:    []string{workflowLabelIDAlpha},
				RemoveLabelIDs: []string{workflowLabelIDAlpha},
			},
			field: "remove_label_ids[0]",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "task label mutation rejects duplicate remove",
			request: WorkflowTaskLabelsUpdateRequest{
				TaskID:         "task-1",
				RemoveLabelIDs: []string{workflowLabelIDAlpha, workflowLabelIDAlpha},
			},
			field: "remove_label_ids[1]",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name:    "filter kind is required",
			request: WorkflowTaskLabelFilter{},
			field:   "label_filter.kind",
			code:    WorkflowRequestErrorRequired,
		},
		{
			name: "named filter requires named payload",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
			},
			field: "label_filter.named",
			code:  WorkflowRequestErrorRequired,
		},
		{
			name: "named filter rejects empty IDs",
			request: WorkflowTaskLabelFilter{
				Kind:  WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{Mode: WorkflowTaskNamedLabelFilterModeAny},
			},
			field: "label_filter.label_ids",
			code:  WorkflowRequestErrorRequired,
		},
		{
			name: "named filter rejects malformed excluded ID",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{
					Mode:             WorkflowTaskNamedLabelFilterModeAny,
					ExcludedLabelIDs: []string{"not-a-uuid"},
				},
			},
			field: "label_filter.excluded_label_ids[0]",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "named filter rejects duplicate excluded ID",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{
					Mode:             WorkflowTaskNamedLabelFilterModeAny,
					ExcludedLabelIDs: []string{workflowLabelIDAlpha, workflowLabelIDAlpha},
				},
			},
			field: "label_filter.excluded_label_ids[1]",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "named filter rejects included and excluded overlap",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{
					Mode:             WorkflowTaskNamedLabelFilterModeAny,
					LabelIDs:         []string{workflowLabelIDAlpha},
					ExcludedLabelIDs: []string{workflowLabelIDAlpha},
				},
			},
			field: "label_filter.excluded_label_ids[0]",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "none filter rejects named sentinel",
			request: WorkflowTaskLabelFilter{
				Kind:  WorkflowTaskLabelFilterKindNone,
				Named: &WorkflowTaskNamedLabelFilter{Mode: WorkflowTaskNamedLabelFilterModeAny, LabelIDs: []string{workflowLabelIDAlpha}},
			},
			field: "label_filter.named",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "unlabeled filter rejects named sentinel",
			request: WorkflowTaskLabelFilter{
				Kind:  WorkflowTaskLabelFilterKindUnlabeled,
				Named: &WorkflowTaskNamedLabelFilter{Mode: WorkflowTaskNamedLabelFilterModeAny, LabelIDs: []string{workflowLabelIDAlpha}},
			},
			field: "label_filter.named",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name: "named filter rejects unknown mode",
			request: WorkflowTaskLabelFilter{
				Kind: WorkflowTaskLabelFilterKindNamed,
				Named: &WorkflowTaskNamedLabelFilter{
					Mode:     "either",
					LabelIDs: []string{workflowLabelIDAlpha},
				},
			},
			field: "label_filter.named.mode",
			code:  WorkflowRequestErrorInvalidValue,
		},
		{
			name:    "task list requires a tagged filter",
			request: WorkflowTaskListRequest{ProjectID: stringPointerForTest(projectID)},
			field:   "label_filter.kind",
			code:    WorkflowRequestErrorRequired,
		},
		{
			name:    "board requires a tagged filter",
			request: WorkflowBoardRequest{ProjectID: projectID},
			field:   "label_filter.kind",
			code:    WorkflowRequestErrorRequired,
		},
		{
			name:    "board cards require a tagged filter",
			request: WorkflowBoardNodeCardsListRequest{ProjectID: projectID, WorkflowID: runtimeids.NewWorkflowID(), NodeID: "node-1"},
			field:   "label_filter.kind",
			code:    WorkflowRequestErrorRequired,
		},
	})
}

func TestWorkflowLabelProjectionDTOsExposeNamesOnlyForTaskListRows(t *testing.T) {
	labelIDs := []string{workflowLabelIDAlpha, workflowLabelIDBeta}
	for _, value := range []any{
		WorkflowTaskDetail{Summary: WorkflowTaskSummary{WorkflowID: runtimeids.NewWorkflowID()}, Workflow: WorkflowTaskWorkflowSummary{WorkflowID: runtimeids.NewWorkflowID()}, LabelIDs: labelIDs, Dependencies: emptyWorkflowTaskDependenciesForTest()},
		WorkflowBoardTaskCard{WorkflowID: runtimeids.NewWorkflowID(), LabelIDs: labelIDs},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal projection %T: %v", value, err)
		}
		var shape map[string]any
		if err := json.Unmarshal(data, &shape); err != nil {
			t.Fatalf("decode projection JSON: %v", err)
		}
		if !slices.Equal(shape["label_ids"].([]any), []any{workflowLabelIDAlpha, workflowLabelIDBeta}) {
			t.Fatalf("projection %T label IDs = %#v", value, shape["label_ids"])
		}
		for key := range shape {
			if key == "label_names" || key == "labels" {
				t.Fatalf("projection %T leaks label names: %s", value, data)
			}
		}
	}

	value := WorkflowTaskListItem{
		TaskID:     "task-1",
		WorkflowID: runtimeids.NewWorkflowID(),
		Status: WorkflowTaskStatus{
			Kind:        WorkflowTaskStatusKindBacklog,
			NativeState: WorkflowTaskNativeStateActive,
		},
		Labels: []WorkflowProjectLabel{
			{ID: workflowLabelIDAlpha, Name: "Alpha"},
			{ID: workflowLabelIDBeta, Name: "Beta"},
		},
		DependencyProgress: &WorkflowTaskDependencyProgress{SatisfiedCount: 1, TotalCount: 2},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal task-list row: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatalf("decode task-list row JSON: %v", err)
	}
	if _, exists := shape["label_ids"]; exists {
		t.Fatalf("task-list row retains label_ids: %s", data)
	}
	labels, ok := shape["labels"].([]any)
	if !ok || len(labels) != 2 {
		t.Fatalf("task-list labels = %#v, want two display records", shape["labels"])
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate task-list row: %v", err)
	}
}

func TestWorkflowLabelErrorRoundTripsEveryTypedFailure(t *testing.T) {
	for _, source := range []*WorkflowLabelError{
		{Reason: WorkflowLabelErrorReasonInvalidName, ProjectID: workflowLabelStringPointer("project-1"), Field: workflowLabelStringPointer("name")},
		{Reason: WorkflowLabelErrorReasonNameConflict, ProjectID: workflowLabelStringPointer("project-1")},
		{Reason: WorkflowLabelErrorReasonCatalogLimit, ProjectID: workflowLabelStringPointer("project-1"), Limit: workflowLabelIntPointer(WorkflowLabelMaxIDs)},
		{Reason: WorkflowLabelErrorReasonProjectNotFound, ProjectID: workflowLabelStringPointer("project-1")},
		{Reason: WorkflowLabelErrorReasonLabelNotFound, LabelID: workflowLabelStringPointer(workflowLabelIDAlpha)},
		{Reason: WorkflowLabelErrorReasonTaskNotFound, TaskID: workflowLabelStringPointer("task-1")},
		{Reason: WorkflowLabelErrorReasonWrongProject, ProjectID: workflowLabelStringPointer("project-1"), LabelID: workflowLabelStringPointer(workflowLabelIDAlpha)},
		{Reason: WorkflowLabelErrorReasonInvalidFilter, Field: workflowLabelStringPointer("label_filter.label_ids")},
		{Reason: WorkflowLabelErrorReasonInvalidMutation, Field: workflowLabelStringPointer("add_label_ids")},
	} {
		decoded := DecodeWorkflowLabelError(source.RPCErrorData(), source.Error())
		var typed *WorkflowLabelError
		if !errors.As(decoded, &typed) {
			t.Fatalf("decoded error = %T %v, want WorkflowLabelError", decoded, decoded)
		}
		if !reflect.DeepEqual(typed, source) {
			t.Fatalf("decoded error = %+v, want %+v", typed, source)
		}
		if typed.RPCErrorCode() != protocol.ErrCodeWorkflowLabel {
			t.Fatalf("label error code = %d, want %d", typed.RPCErrorCode(), protocol.ErrCodeWorkflowLabel)
		}
	}

	for name, payload := range map[string]string{
		"unknown reason":        `{"type":"workflow_label_error","reason":"other"}`,
		"missing project":       `{"type":"workflow_label_error","reason":"project_not_found"}`,
		"noncanonical label":    `{"type":"workflow_label_error","reason":"label_not_found","label_id":"11111111-1111-4111-8111-111111111111 "}`,
		"wrong project missing": `{"type":"workflow_label_error","reason":"wrong_project","label_id":"` + workflowLabelIDAlpha + `"}`,
		"invalid filter field":  `{"type":"workflow_label_error","reason":"invalid_filter","field":" "}`,
		"catalog limit":         `{"type":"workflow_label_error","reason":"catalog_limit","project_id":"project-1","limit":0}`,
		"unexpected context":    `{"type":"workflow_label_error","reason":"name_conflict","project_id":"project-1","task_id":"task-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			decoded := DecodeWorkflowLabelError(json.RawMessage(payload), "fallback")
			var typed *WorkflowLabelError
			if errors.As(decoded, &typed) {
				t.Fatalf("invalid payload decoded as typed error: %+v", typed)
			}
		})
	}
}

func workflowLabelStringPointer(value string) *string {
	return &value
}

func workflowLabelIntPointer(value int) *int {
	return &value
}

func TestWorkflowLabelRPCValidationUsesTypedMutationErrors(t *testing.T) {
	raw101 := make([]string, WorkflowLabelMaxIDs+1)
	for index := range raw101 {
		raw101[index] = "not-a-uuid"
	}
	tests := []struct {
		name    string
		request interface{ ValidateRPC() error }
		reason  WorkflowLabelErrorReason
		field   string
	}{
		{
			name: "invalid create name",
			request: WorkflowProjectLabelCreateRequest{
				ProjectID: "project-1",
			},
			reason: WorkflowLabelErrorReasonInvalidName,
			field:  "name",
		},
		{
			name: "invalid rename name",
			request: WorkflowProjectLabelRenameRequest{
				ProjectID: "project-1",
				LabelID:   "11111111-1111-4111-8111-111111111111",
			},
			reason: WorkflowLabelErrorReasonInvalidName,
			field:  "name",
		},
		{
			name: "invalid delete label id",
			request: WorkflowProjectLabelDeleteRequest{
				ProjectID: "project-1",
				LabelID:   "not-a-label-id",
			},
			reason: WorkflowLabelErrorReasonInvalidMutation,
			field:  "label_id",
		},
		{
			name:    "invalid filter",
			request: WorkflowTaskLabelFilter{},
			reason:  WorkflowLabelErrorReasonInvalidFilter,
			field:   "label_filter.kind",
		},
		{
			name: "raw assignment bound",
			request: WorkflowTaskLabelsUpdateRequest{
				TaskID:      "task-1",
				AddLabelIDs: raw101,
			},
			reason: WorkflowLabelErrorReasonInvalidMutation,
			field:  "add_label_ids",
		},
		{
			name: "labeled task create",
			request: WorkflowTaskCreateRequest{
				ProjectID: "project-1",
				Title:     "Task",
				LabelIDs:  raw101,
			},
			reason: WorkflowLabelErrorReasonInvalidMutation,
			field:  "label_ids",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.ValidateRPC()
			var typed *WorkflowLabelError
			if !errors.As(err, &typed) || typed.Reason != tt.reason || typed.Field == nil || *typed.Field != tt.field {
				t.Fatalf("ValidateRPC() error = %T %+v, want reason %q field %q", err, err, tt.reason, tt.field)
			}
		})
	}
}

func TestWorkflowFilterBearingRequestRPCValidationPreservesErrorProvenance(t *testing.T) {
	projectID := "project-1"
	negativeOffset := -1
	tests := []struct {
		name       string
		request    interface{ ValidateRPC() error }
		wantField  string
		wantTyped  bool
		wantReason WorkflowLabelErrorReason
	}{
		{
			name:      "board non-label field remains generic",
			request:   WorkflowBoardRequest{LabelFilter: WorkflowTaskLabelFilterNone()},
			wantField: "project_id",
		},
		{
			name:       "board malformed filter is typed",
			request:    WorkflowBoardRequest{ProjectID: projectID},
			wantField:  "label_filter.kind",
			wantTyped:  true,
			wantReason: WorkflowLabelErrorReasonInvalidFilter,
		},
		{
			name:      "task list non-label field remains generic",
			request:   WorkflowTaskListRequest{ProjectID: &projectID, LabelFilter: WorkflowTaskLabelFilterNone(), Offset: &negativeOffset},
			wantField: "offset",
		},
		{
			name:       "task list malformed filter is typed",
			request:    WorkflowTaskListRequest{ProjectID: &projectID},
			wantField:  "label_filter.kind",
			wantTyped:  true,
			wantReason: WorkflowLabelErrorReasonInvalidFilter,
		},
		{
			name:      "board cards non-label field remains generic",
			request:   WorkflowBoardNodeCardsListRequest{LabelFilter: WorkflowTaskLabelFilterNone()},
			wantField: "project_id",
		},
		{
			name: "board cards malformed filter is typed",
			request: WorkflowBoardNodeCardsListRequest{
				ProjectID:  projectID,
				WorkflowID: runtimeids.NewWorkflowID(),
				NodeID:     "node-1",
			},
			wantField:  "label_filter.kind",
			wantTyped:  true,
			wantReason: WorkflowLabelErrorReasonInvalidFilter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.ValidateRPC()
			var typed *WorkflowLabelError
			if tt.wantTyped {
				if !errors.As(err, &typed) ||
					typed.Reason != tt.wantReason ||
					typed.Field == nil ||
					*typed.Field != tt.wantField {
					t.Fatalf("ValidateRPC() error = %T %+v, want typed reason %q field %q", err, err, tt.wantReason, tt.wantField)
				}
				return
			}
			if errors.As(err, &typed) {
				t.Fatalf("ValidateRPC() error = %+v, want generic validation error", typed)
			}
			var validationErr WorkflowRequestValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tt.wantField {
				t.Fatalf("ValidateRPC() error = %T %+v, want generic field %q", err, err, tt.wantField)
			}
		})
	}
}
