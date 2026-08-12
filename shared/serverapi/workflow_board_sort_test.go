package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowBoardNodeCardsListRequestAcceptsBoardSortFieldsAndDirections(t *testing.T) {
	projectID := "project-1"
	workflowID := runtimeids.NewWorkflowID()
	offset := 25
	for _, field := range []WorkflowTaskListSortField{
		WorkflowTaskListSortFieldUpdated,
		WorkflowTaskListSortFieldCreated,
		WorkflowTaskListSortFieldLabels,
		WorkflowTaskListSortFieldShortID,
	} {
		for _, direction := range []WorkflowTaskListSortDirection{
			WorkflowTaskListSortDirectionAsc,
			WorkflowTaskListSortDirectionDesc,
		} {
			request := WorkflowBoardNodeCardsListRequest{
				ProjectID:  projectID,
				WorkflowID: workflowID,
				NodeID:     runtimeids.NewGraphEntityID(),
				LabelFilter: WorkflowTaskLabelFilter{
					Kind: WorkflowTaskLabelFilterKindNone,
				},
				Sort: &WorkflowTaskListSort{
					Field:     field,
					Direction: direction,
				},
				Offset: &offset,
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("%s %s rejected: %v", field, direction, err)
			}
		}
	}
}

func TestWorkflowBoardNodeCardsListRequestRejectsUnsupportedSortAndOffset(t *testing.T) {
	projectID := "project-1"
	workflowID := runtimeids.NewWorkflowID()
	base := WorkflowBoardNodeCardsListRequest{
		ProjectID:  projectID,
		WorkflowID: workflowID,
		NodeID:     runtimeids.NewGraphEntityID(),
		LabelFilter: WorkflowTaskLabelFilter{
			Kind: WorkflowTaskLabelFilterKindNone,
		},
	}
	negativeOffset := -1
	invalidCases := []struct {
		name    string
		request WorkflowBoardNodeCardsListRequest
	}{
		{
			name: "title sort",
			request: func() WorkflowBoardNodeCardsListRequest {
				request := base
				request.Sort = &WorkflowTaskListSort{
					Field:     WorkflowTaskListSortFieldTitle,
					Direction: WorkflowTaskListSortDirectionAsc,
				}
				return request
			}(),
		},
		{
			name: "invalid direction",
			request: func() WorkflowBoardNodeCardsListRequest {
				request := base
				request.Sort = &WorkflowTaskListSort{
					Field:     WorkflowTaskListSortFieldUpdated,
					Direction: WorkflowTaskListSortDirection("sideways"),
				}
				return request
			}(),
		},
		{
			name: "negative offset",
			request: func() WorkflowBoardNodeCardsListRequest {
				request := base
				request.Offset = &negativeOffset
				return request
			}(),
		},
	}
	for _, testCase := range invalidCases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.request.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly accepted invalid board cards request")
			}
		})
	}
}

func TestWorkflowBoardNodeCardsListContractUsesOffsetAndNextOffset(t *testing.T) {
	request := WorkflowBoardNodeCardsListRequest{
		ProjectID:  "project-1",
		WorkflowID: runtimeids.NewWorkflowID(),
		NodeID:     runtimeids.NewGraphEntityID(),
		LabelFilter: WorkflowTaskLabelFilter{
			Kind: WorkflowTaskLabelFilterKindNone,
		},
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var requestShape map[string]any
	if err := json.Unmarshal(encodedRequest, &requestShape); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	for _, removedField := range []string{"page_token", "previous_page_token", "next_page_token"} {
		if _, present := requestShape[removedField]; present {
			t.Fatalf("request contains removed board pagination field %q: %s", removedField, encodedRequest)
		}
	}

	nextOffset := 50
	response := WorkflowBoardNodeCardsListResponse{
		WorkflowID: runtimeids.NewWorkflowID(),
		NextOffset: &nextOffset,
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var responseShape map[string]any
	if err := json.Unmarshal(encodedResponse, &responseShape); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if responseShape["next_offset"] != float64(nextOffset) {
		t.Fatalf("next_offset = %#v, want %d", responseShape["next_offset"], nextOffset)
	}
	for _, removedField := range []string{"previous_page_token", "next_page_token"} {
		if _, present := responseShape[removedField]; present {
			t.Fatalf("response contains removed board pagination field %q: %s", removedField, encodedResponse)
		}
	}
}
