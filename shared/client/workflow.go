package client

import "fmt"

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
