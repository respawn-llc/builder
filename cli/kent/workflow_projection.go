package main

import "core/shared/serverapi"

func workflowRecordForCLI(record serverapi.WorkflowRecord) (serverapi.WorkflowRecord, error) {
	selector, err := workflowSelectorFromPersistedID(record.ID)
	if err != nil {
		return serverapi.WorkflowRecord{}, err
	}
	projected := record
	projected.ID = selector.String()
	return projected, nil
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

func workflowTaskSummaryForCLI(summary serverapi.WorkflowTaskSummary) (serverapi.WorkflowTaskSummary, error) {
	workflowID, err := workflowIDForCLI(summary.WorkflowID)
	if err != nil {
		return serverapi.WorkflowTaskSummary{}, err
	}
	projected := summary
	projected.WorkflowID = workflowID
	return projected, nil
}

func workflowDefinitionForCLI(definition serverapi.WorkflowDefinition) (serverapi.WorkflowDefinition, error) {
	workflow, err := workflowRecordForCLI(definition.Workflow)
	if err != nil {
		return serverapi.WorkflowDefinition{}, err
	}
	projected := definition
	projected.Workflow = workflow
	projected.NodeGroups = append([]serverapi.WorkflowNodeGroup(nil), definition.NodeGroups...)
	for i := range projected.NodeGroups {
		projected.NodeGroups[i].WorkflowID, err = workflowIDForCLI(projected.NodeGroups[i].WorkflowID)
		if err != nil {
			return serverapi.WorkflowDefinition{}, err
		}
	}
	projected.Nodes = append([]serverapi.WorkflowNode(nil), definition.Nodes...)
	for i := range projected.Nodes {
		projected.Nodes[i].WorkflowID, err = workflowIDForCLI(projected.Nodes[i].WorkflowID)
		if err != nil {
			return serverapi.WorkflowDefinition{}, err
		}
	}
	projected.TransitionGroups = append([]serverapi.WorkflowTransitionGroup(nil), definition.TransitionGroups...)
	for i := range projected.TransitionGroups {
		projected.TransitionGroups[i].WorkflowID, err = workflowIDForCLI(projected.TransitionGroups[i].WorkflowID)
		if err != nil {
			return serverapi.WorkflowDefinition{}, err
		}
	}
	projected.Edges = append([]serverapi.WorkflowEdge(nil), definition.Edges...)
	for i := range projected.Edges {
		projected.Edges[i].WorkflowID, err = workflowIDForCLI(projected.Edges[i].WorkflowID)
		if err != nil {
			return serverapi.WorkflowDefinition{}, err
		}
	}
	projected.DerivedWiring.Diagnostics, err = workflowValidationErrorsForCLI(definition.DerivedWiring.Diagnostics)
	if err != nil {
		return serverapi.WorkflowDefinition{}, err
	}
	return projected, nil
}

func workflowIDForCLI(persistedID string) (string, error) {
	selector, err := workflowSelectorFromPersistedID(persistedID)
	if err != nil {
		return "", err
	}
	return selector.String(), nil
}

func projectWorkflowLinkForCLI(link serverapi.ProjectWorkflowLink) (serverapi.ProjectWorkflowLink, error) {
	workflowID, err := workflowIDForCLI(link.WorkflowID)
	if err != nil {
		return serverapi.ProjectWorkflowLink{}, err
	}
	projected := link
	projected.WorkflowID = workflowID
	return projected, nil
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
		if projected[i].WorkflowID == "" {
			continue
		}
		workflowID, err := workflowIDForCLI(projected[i].WorkflowID)
		if err != nil {
			return nil, err
		}
		projected[i].WorkflowID = workflowID
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
	projected.Workflow.WorkflowID, err = workflowIDForCLI(detail.Workflow.WorkflowID)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	projected.Workflow.ValidationErrors, err = workflowValidationErrorsForCLI(detail.Workflow.ValidationErrors)
	if err != nil {
		return serverapi.WorkflowTaskDetail{}, err
	}
	projected.Attention = append([]serverapi.WorkflowAttentionItem(nil), detail.Attention...)
	for i := range projected.Attention {
		if projected.Attention[i].WorkflowID == "" {
			continue
		}
		projected.Attention[i].WorkflowID, err = workflowIDForCLI(projected.Attention[i].WorkflowID)
		if err != nil {
			return serverapi.WorkflowTaskDetail{}, err
		}
	}
	return projected, nil
}
