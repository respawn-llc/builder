package main

import (
	"core/shared/serverapi"
	"errors"
)

func workflowRecordForCLI(record serverapi.WorkflowRecord) (serverapi.WorkflowRecord, error) {
	if record.ID.IsZero() {
		return serverapi.WorkflowRecord{}, errors.New("workflow_id is required")
	}
	return record, nil
}

func workflowRecordsForCLI(records []serverapi.WorkflowRecord) ([]serverapi.WorkflowRecord, error) {
	projected := make([]serverapi.WorkflowRecord, len(records))
	for i := range records {
		record, err := workflowRecordForCLI(records[i])
		if err != nil {
			return nil, err
		}
		projected[i] = record
	}
	return projected, nil
}

func workflowDeleteResponseForCLI(response serverapi.WorkflowDeleteResponse) (serverapi.WorkflowDeleteResponse, error) {
	if response.Impact.WorkflowID.IsZero() {
		return serverapi.WorkflowDeleteResponse{}, errors.New("workflow_id is required")
	}
	return response, nil
}

func workflowTaskSummaryForCLI(summary serverapi.WorkflowTaskSummary) (serverapi.WorkflowTaskSummary, error) {
	if summary.WorkflowID.IsZero() {
		return serverapi.WorkflowTaskSummary{}, errors.New("workflow_id is required")
	}
	return summary, nil
}

func workflowDefinitionForCLI(definition serverapi.WorkflowDefinition) (serverapi.WorkflowDefinition, error) {
	workflow, err := workflowRecordForCLI(definition.Workflow)
	if err != nil {
		return serverapi.WorkflowDefinition{}, err
	}
	projected := definition
	projected.Workflow = workflow
	projected.DerivedWiring.Diagnostics, err = workflowValidationErrorsForCLI(definition.DerivedWiring.Diagnostics)
	if err != nil {
		return serverapi.WorkflowDefinition{}, err
	}
	return projected, nil
}

func projectWorkflowLinkForCLI(link serverapi.ProjectWorkflowLink) (serverapi.ProjectWorkflowLink, error) {
	if link.WorkflowID.IsZero() {
		return serverapi.ProjectWorkflowLink{}, errors.New("workflow_id is required")
	}
	return link, nil
}

func workflowValidationForCLI(response serverapi.WorkflowValidateResponse) (serverapi.WorkflowValidateResponse, error) {
	projected := response
	errors, err := workflowValidationErrorsForCLI(response.Errors)
	if err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	projected.Errors = errors
	return projected, nil
}

func workflowValidationErrorsForCLI(errors []serverapi.WorkflowValidationError) ([]serverapi.WorkflowValidationError, error) {
	projected := append([]serverapi.WorkflowValidationError(nil), errors...)
	for i := range projected {
		if projected[i].WorkflowID == nil {
			continue
		}
	}
	return projected, nil
}

func workflowTaskDetailForCLI(detail serverapi.WorkflowTaskDetail) (serverapi.WorkflowTaskDetail, error) {
	projected := detail
	summary, err := workflowTaskSummaryForCLI(detail.Summary)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	projected.Summary = summary
	return projected, nil
}
