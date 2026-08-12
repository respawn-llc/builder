package main

import (
	"context"
	"errors"

	"core/shared/apicontract"
	"core/shared/serverapi"
)

type workflowGraphPreview struct {
	Graph    serverapi.WorkflowGraphDraft
	Response serverapi.WorkflowGraphSavePreviewResponse
}

func previewWorkflowGraphDraft(
	ctx context.Context,
	remote apicontract.WorkflowService,
	current serverapi.WorkflowDefinition,
	submitted serverapi.WorkflowGraphDraft,
) (workflowGraphPreview, error) {
	if remote == nil {
		return workflowGraphPreview{}, errors.New("workflow service is required")
	}
	response, err := remote.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      current.Workflow.ID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           submitted,
	})
	if err != nil {
		return workflowGraphPreview{}, err
	}
	return workflowGraphPreview{Graph: submitted, Response: response}, nil
}
