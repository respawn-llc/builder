package client

import (
	"fmt"

	"core/shared/serverapi"
)

type workflowResponse interface {
	Validate() error
}

type workflowTaskBoundResponse interface {
	ValidateForTask(string) error
}

func validateWorkflowResponse[T workflowResponse](operation string, response T, err error) (T, error) {
	if err != nil {
		return response, err
	}
	if validationErr := response.Validate(); validationErr != nil {
		var zero T
		return zero, fmt.Errorf("%s returned an invalid response: %w", operation, validationErr)
	}
	return response, nil
}

func validateWorkflowTaskListResponse(
	operation string,
	request serverapi.WorkflowTaskListRequest,
	response serverapi.WorkflowTaskListResponse,
	err error,
) (serverapi.WorkflowTaskListResponse, error) {
	if err != nil {
		return response, err
	}
	if validationErr := response.ValidateForRequest(request); validationErr != nil {
		return serverapi.WorkflowTaskListResponse{}, fmt.Errorf("%s returned an invalid response: %w", operation, validationErr)
	}
	return response, nil
}

func validateWorkflowTaskBoundResponse[T workflowTaskBoundResponse](operation string, taskID string, response T, err error) (T, error) {
	if err != nil {
		return response, err
	}
	if validationErr := response.ValidateForTask(taskID); validationErr != nil {
		var zero T
		return zero, fmt.Errorf("%s returned an invalid response: %w", operation, validationErr)
	}
	return response, nil
}
