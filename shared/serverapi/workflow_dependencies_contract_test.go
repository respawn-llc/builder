package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"core/shared/protocol"
)

func TestWorkflowTaskDependencyMutationContractsRoundTrip(t *testing.T) {
	request := WorkflowTaskCreateRequest{
		ProjectID: "project-1",
		Title:     "new task",
		LabelIDs:  []string{},
		DependencyIntents: []WorkflowTaskDependencyCreateIntent{
			{RelatedTaskID: "task-blocked", NewTaskRole: WorkflowTaskDependencyRoleBlocker},
			{RelatedTaskID: "task-blocker", NewTaskRole: WorkflowTaskDependencyRoleBlocked},
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("create request Validate: %v", err)
	}
	data, decodedRequest := marshalWorkflowJSON[WorkflowTaskCreateRequest](t, request)
	if len(decodedRequest.DependencyIntents) != 2 {
		t.Fatalf("decoded dependency intents = %+v, want two", decodedRequest.DependencyIntents)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode create request JSON: %v", err)
	}
	if _, ok := envelope["dependency_intents"]; !ok {
		t.Fatalf("create request JSON = %s, want dependency_intents", data)
	}
	mutation := WorkflowTaskDependencyMutationResponse{
		Outcome:        WorkflowTaskDependencyOutcomeAdded,
		BlockerTaskID:  "task-blocker",
		BlockerShortID: "PROJ-1",
		BlockedTaskID:  "task-blocked",
		BlockedShortID: "PROJ-2",
	}
	data, decoded := marshalWorkflowJSON[WorkflowTaskDependencyMutationResponse](t, mutation)
	if err := decoded.Validate(); err != nil {
		t.Fatalf("mutation response Validate: %v", err)
	}
	if string(data) == "" {
		t.Fatal("mutation response JSON is empty")
	}
}

func TestWorkflowTaskCreateDependencyIntentsAreBoundedPerRole(t *testing.T) {
	request := WorkflowTaskCreateRequest{
		ProjectID: "project-1",
		Title:     "new task",
		LabelIDs:  []string{},
	}
	for index := 0; index < 50; index++ {
		request.DependencyIntents = append(request.DependencyIntents,
			WorkflowTaskDependencyCreateIntent{
				RelatedTaskID: fmt.Sprintf("task-blocked-%d", index),
				NewTaskRole:   WorkflowTaskDependencyRoleBlocker,
			},
			WorkflowTaskDependencyCreateIntent{
				RelatedTaskID: fmt.Sprintf("task-blocker-%d", index),
				NewTaskRole:   WorkflowTaskDependencyRoleBlocked,
			},
		)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("50 intents per role Validate: %v", err)
	}

	request.DependencyIntents = append(request.DependencyIntents, WorkflowTaskDependencyCreateIntent{
		RelatedTaskID: "task-extra",
		NewTaskRole:   WorkflowTaskDependencyRoleBlocker,
	})
	if err := request.Validate(); err == nil {
		t.Fatal("51 blocker-role intents Validate succeeded, want bounded collection error")
	}
}

func TestWorkflowTaskCreateDependencyIntentsRejectDuplicateRoleAndRelatedTask(t *testing.T) {
	request := WorkflowTaskCreateRequest{
		ProjectID: "project-1",
		Title:     "new task",
		LabelIDs:  []string{},
		DependencyIntents: []WorkflowTaskDependencyCreateIntent{
			{RelatedTaskID: "task-related", NewTaskRole: WorkflowTaskDependencyRoleBlocker},
			{RelatedTaskID: "task-related", NewTaskRole: WorkflowTaskDependencyRoleBlocker},
		},
	}

	err := request.Validate()
	var validationErr WorkflowRequestValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Validate error = %T %v, want WorkflowRequestValidationError", err, err)
	}
	if validationErr.Code != WorkflowRequestErrorInvalidValue {
		t.Fatalf("Validate error code = %q, want %q", validationErr.Code, WorkflowRequestErrorInvalidValue)
	}
	if validationErr.Field != "dependency_intents[1].related_task_id" {
		t.Fatalf("Validate error field = %q, want duplicate related Task field", validationErr.Field)
	}

	request.DependencyIntents[1].NewTaskRole = WorkflowTaskDependencyRoleBlocked
	if err := request.Validate(); err != nil {
		t.Fatalf("same related Task in opposite roles Validate: %v", err)
	}
}

func TestWorkflowTaskDependencyListContractRejectsMalformedDirections(t *testing.T) {
	satisfied := WorkflowTaskDependencySatisfied
	tests := []struct {
		name string
		resp WorkflowTaskDependencyListResponse
	}{
		{
			name: "nil directions",
			resp: WorkflowTaskDependencyListResponse{
				TaskID:  "task",
				ShortID: "PROJ-1",
			},
		},
		{
			name: "count does not match items",
			resp: WorkflowTaskDependencyListResponse{
				TaskID:  "task",
				ShortID: "PROJ-1",
				Directions: []WorkflowTaskDependencyListDirectionProjection{{
					Direction:        WorkflowTaskDependencyDirectionBlockedBy,
					TotalCount:       1,
					Items:            []WorkflowTaskDependencyItem{},
					UnsatisfiedCount: intPointer(0),
				}},
			},
		},
		{
			name: "blocks carries satisfaction",
			resp: WorkflowTaskDependencyListResponse{
				TaskID:  "task",
				ShortID: "PROJ-1",
				Directions: []WorkflowTaskDependencyListDirectionProjection{{
					Direction:  WorkflowTaskDependencyDirectionBlocks,
					TotalCount: 1,
					Items: []WorkflowTaskDependencyItem{{
						TaskID:       "related",
						ShortID:      "PROJ-2",
						Title:        "related",
						WorkflowID:   "workflow",
						Satisfaction: &satisfied,
					}},
				}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.resp.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestWorkflowTaskDependencyListJSONOmitsAddAvailability(t *testing.T) {
	response := WorkflowTaskDependencyListResponse{
		TaskID:  "task",
		ShortID: "PROJ-1",
		Directions: []WorkflowTaskDependencyListDirectionProjection{{
			Direction: WorkflowTaskDependencyDirectionBlocks,
			Items:     []WorkflowTaskDependencyItem{},
		}},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) == "" || containsJSONField(data, "add_availability") {
		t.Fatalf("list JSON unexpectedly exposes add availability: %s", data)
	}
}

func TestWorkflowTaskDependencyDetailContractRequiresAvailabilityAndConsistentCounts(t *testing.T) {
	detail := WorkflowTaskDetail{
		Summary:  WorkflowTaskSummary{ID: "task"},
		LabelIDs: []string{},
		Dependencies: WorkflowTaskDependencies{
			Directions: []WorkflowTaskDependencyDirectionProjection{{
				Direction:       WorkflowTaskDependencyDirectionBlocks,
				TotalCount:      0,
				Items:           []WorkflowTaskDependencyItem{},
				AddAvailability: nil,
			}},
		},
	}
	if err := detail.Validate(); err == nil {
		t.Fatal("Validate() error = nil for missing detail availability")
	}

	available := &WorkflowTaskDependencyAddAvailability{
		Available: &WorkflowTaskDependencyAvailable{RemainingCapacity: 50},
	}
	detail.Dependencies = WorkflowTaskDependencies{
		BlockerCount:             1,
		UnsatisfiedBlockerCount:  1,
		DirectlyBlockedTaskCount: 0,
		Directions: []WorkflowTaskDependencyDirectionProjection{
			{
				Direction:        WorkflowTaskDependencyDirectionBlockedBy,
				TotalCount:       1,
				UnsatisfiedCount: intPointer(1),
				Items: []WorkflowTaskDependencyItem{{
					TaskID:       "blocker",
					ShortID:      "PROJ-2",
					Title:        "blocker",
					WorkflowID:   "workflow",
					Satisfaction: satisfactionPointer(WorkflowTaskDependencyUnsatisfied),
				}},
				AddAvailability: available,
			},
			{
				Direction:  WorkflowTaskDependencyDirectionBlocks,
				TotalCount: 0,
				Items:      []WorkflowTaskDependencyItem{},
				AddAvailability: &WorkflowTaskDependencyAddAvailability{
					LimitReached: &WorkflowTaskDependencyLimitReached{},
				},
			},
		},
	}
	if err := detail.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestWorkflowTaskDependencyAvailabilityRejectsMalformedUnion(t *testing.T) {
	remaining := &WorkflowTaskDependencyAvailable{RemainingCapacity: 1}
	tests := []WorkflowTaskDependencyAddAvailability{
		{},
		{Available: remaining, LimitReached: &WorkflowTaskDependencyLimitReached{}},
		{Available: &WorkflowTaskDependencyAvailable{RemainingCapacity: 0}},
	}
	for _, availability := range tests {
		if err := availability.Validate(); err == nil {
			t.Fatalf("Validate() error = nil for %#v", availability)
		}
	}
}

func TestWorkflowTaskDependencyBoardProgressRejectsZeroAndInconsistentCounts(t *testing.T) {
	tests := []WorkflowTaskDependencyProgress{
		{SatisfiedCount: 0, TotalCount: 0},
		{SatisfiedCount: 2, TotalCount: 1},
		{SatisfiedCount: -1, TotalCount: 1},
	}
	for _, progress := range tests {
		if err := progress.Validate(); err == nil {
			t.Fatalf("Validate() error = nil for %#v", progress)
		}
	}
	if err := (WorkflowTaskDependencyProgress{SatisfiedCount: 1, TotalCount: 2}).Validate(); err != nil {
		t.Fatalf("valid progress Validate: %v", err)
	}
}

func TestWorkflowTaskDependencyErrorRoundTripAndDecoderRejectsMalformedData(t *testing.T) {
	currentCount := 50
	limit := 50
	source := &WorkflowTaskDependencyError{
		Reason:        WorkflowTaskDependencyErrorReasonBlockerLimit,
		BlockerTaskID: "task-blocker",
		BlockedTaskID: "task-blocked",
		CurrentCount:  &currentCount,
		Limit:         &limit,
	}
	if source.RPCErrorCode() != protocol.ErrCodeWorkflowTaskDependency {
		t.Fatalf("RPCErrorCode() = %d", source.RPCErrorCode())
	}
	decoded := DecodeWorkflowTaskDependencyError(source.RPCErrorData(), source.Error())
	var typed *WorkflowTaskDependencyError
	if !errors.As(decoded, &typed) {
		t.Fatalf("decoded error type = %T, want *WorkflowTaskDependencyError", decoded)
	}
	if typed.Reason != source.Reason || typed.BlockerTaskID != source.BlockerTaskID {
		t.Fatalf("decoded error = %#v, want %#v", typed, source)
	}
	if got := DecodeWorkflowTaskDependencyError(json.RawMessage(`{"type":"wrong"}`), "fallback"); got.Error() != "fallback" {
		t.Fatalf("malformed decoder error = %v", got)
	}
}

func intPointer(value int) *int {
	return &value
}

func satisfactionPointer(value WorkflowTaskDependencySatisfaction) *WorkflowTaskDependencySatisfaction {
	return &value
}

func emptyWorkflowTaskDependenciesForTest() WorkflowTaskDependencies {
	return WorkflowTaskDependencies{
		Directions: []WorkflowTaskDependencyDirectionProjection{
			{
				Direction:        WorkflowTaskDependencyDirectionBlockedBy,
				Items:            []WorkflowTaskDependencyItem{},
				AddAvailability:  &WorkflowTaskDependencyAddAvailability{Available: &WorkflowTaskDependencyAvailable{RemainingCapacity: 50}},
				UnsatisfiedCount: intPointer(0),
			},
			{
				Direction:       WorkflowTaskDependencyDirectionBlocks,
				Items:           []WorkflowTaskDependencyItem{},
				AddAvailability: &WorkflowTaskDependencyAddAvailability{Available: &WorkflowTaskDependencyAvailable{RemainingCapacity: 50}},
			},
		},
	}
}

func containsJSONField(data []byte, field string) bool {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return false
	}
	if _, exists := value[field]; exists {
		return true
	}
	if directions, ok := value["directions"].([]any); ok {
		for _, raw := range directions {
			direction, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if _, exists := direction[field]; exists {
				return true
			}
		}
	}
	return false
}
