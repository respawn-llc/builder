package client

import "fmt"

type workflowResponse interface {
	Validate() error
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
