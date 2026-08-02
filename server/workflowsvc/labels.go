package workflowsvc

import (
	"context"
	"errors"

	"core/server/workflow"
	"core/server/workflow/label"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func (s *Service) CreateWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelCreateRequest) (serverapi.WorkflowProjectLabelCreateResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowProjectLabelCreateResponse{}, err
	}
	record, err := s.store.CreateProjectLabel(ctx, req.ProjectID, req.Name)
	if err != nil {
		return serverapi.WorkflowProjectLabelCreateResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			projectID: &req.ProjectID,
		})
	}
	response := serverapi.WorkflowProjectLabelCreateResponse{Label: workflowProjectLabel(record)}
	s.publishProjectEvent(ctx, req.ProjectID, serverapi.WorkflowProjectEventResourceLabel, serverapi.WorkflowProjectEventActionCreated, response.Label.ID)
	return response, nil
}

func (s *Service) ListWorkflowProjectLabels(ctx context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowProjectLabelCatalogResponse{}, err
	}
	records, err := s.store.ListProjectLabels(ctx, req.ProjectID)
	if err != nil {
		return serverapi.WorkflowProjectLabelCatalogResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			projectID: &req.ProjectID,
		})
	}
	labels := make([]serverapi.WorkflowProjectLabel, 0, len(records))
	for _, record := range records {
		labels = append(labels, workflowProjectLabel(record))
	}
	return serverapi.WorkflowProjectLabelCatalogResponse{
		Catalog: serverapi.WorkflowProjectLabelCatalog{
			ProjectID: req.ProjectID,
			Labels:    labels,
		},
	}, nil
}

func (s *Service) RenameWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelRenameRequest) (serverapi.WorkflowProjectLabelRenameResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowProjectLabelRenameResponse{}, err
	}
	id, err := label.ParseID(req.LabelID)
	if err != nil {
		return serverapi.WorkflowProjectLabelRenameResponse{}, err
	}
	record, err := s.store.RenameProjectLabel(ctx, req.ProjectID, id, req.Name)
	if err != nil {
		return serverapi.WorkflowProjectLabelRenameResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			projectID: &req.ProjectID,
		})
	}
	response := serverapi.WorkflowProjectLabelRenameResponse{Label: workflowProjectLabel(record)}
	s.publishProjectEvent(ctx, req.ProjectID, serverapi.WorkflowProjectEventResourceLabel, serverapi.WorkflowProjectEventActionRenamed, response.Label.ID)
	return response, nil
}

func (s *Service) DeleteWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelDeleteRequest) (serverapi.WorkflowProjectLabelDeleteResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowProjectLabelDeleteResponse{}, err
	}
	id, err := label.ParseID(req.LabelID)
	if err != nil {
		return serverapi.WorkflowProjectLabelDeleteResponse{}, err
	}
	record, err := s.store.DeleteProjectLabel(ctx, req.ProjectID, id)
	if err != nil {
		return serverapi.WorkflowProjectLabelDeleteResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			projectID: &req.ProjectID,
		})
	}
	response := serverapi.WorkflowProjectLabelDeleteResponse{LabelID: record.ID.String()}
	s.publishProjectEvent(ctx, req.ProjectID, serverapi.WorkflowProjectEventResourceLabel, serverapi.WorkflowProjectEventActionDeleted, response.LabelID)
	return response, nil
}

func (s *Service) ReorderWorkflowProjectLabels(ctx context.Context, req serverapi.WorkflowProjectLabelReorderRequest) (serverapi.WorkflowProjectLabelReorderResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowProjectLabelReorderResponse{}, err
	}
	orderedIDs := make([]label.ID, 0, len(req.LabelIDs))
	for _, rawID := range req.LabelIDs {
		id, err := label.ParseID(rawID)
		if err != nil {
			return serverapi.WorkflowProjectLabelReorderResponse{}, err
		}
		orderedIDs = append(orderedIDs, id)
	}
	result, err := s.store.ReorderProjectLabels(ctx, req.ProjectID, orderedIDs)
	if err != nil {
		return serverapi.WorkflowProjectLabelReorderResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			projectID: &req.ProjectID,
		})
	}
	labels := make([]serverapi.WorkflowProjectLabel, 0, len(result.Labels))
	for _, record := range result.Labels {
		labels = append(labels, workflowProjectLabel(record))
	}
	response := serverapi.WorkflowProjectLabelReorderResponse{
		Catalog: serverapi.WorkflowProjectLabelCatalog{
			ProjectID: req.ProjectID,
			Labels:    labels,
		},
	}
	return response, nil
}

func (s *Service) GetWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsGetRequest) (serverapi.WorkflowTaskLabelsGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskLabelsGetResponse{}, err
	}
	ids, err := s.store.GetTaskLabelIDs(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskLabelsGetResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			taskID: &req.TaskID,
		})
	}
	return serverapi.WorkflowTaskLabelsGetResponse{
		Assignment: serverapi.WorkflowTaskAssignedLabelIDs{
			TaskID:   req.TaskID,
			LabelIDs: labelIDs(ids),
		},
	}, nil
}

func (s *Service) UpdateWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsUpdateRequest) (serverapi.WorkflowTaskLabelsUpdateResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowTaskLabelsUpdateResponse{}, err
	}
	scope, err := s.store.GetTaskLabelScope(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskLabelsUpdateResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			taskID: &req.TaskID,
		})
	}
	ids, err := s.store.UpdateTaskLabels(ctx, workflowstore.TaskLabelUpdateRequest{
		TaskID:         workflow.TaskID(req.TaskID),
		AddLabelIDs:    req.AddLabelIDs,
		RemoveLabelIDs: req.RemoveLabelIDs,
	})
	if err != nil {
		return serverapi.WorkflowTaskLabelsUpdateResponse{}, workflowLabelError(err, workflowLabelErrorScope{
			taskID: &req.TaskID,
		})
	}
	response := serverapi.WorkflowTaskLabelsUpdateResponse{
		Assignment: serverapi.WorkflowTaskAssignedLabelIDs{
			TaskID:   req.TaskID,
			LabelIDs: labelIDs(ids),
		},
	}
	s.publishProjectWorkflowEvent(ctx, scope.ProjectID, scope.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionLabelsChanged, req.TaskID)
	return response, nil
}

func workflowProjectLabel(record workflowstore.ProjectLabelRecord) serverapi.WorkflowProjectLabel {
	return serverapi.WorkflowProjectLabel{
		ID:   record.ID.String(),
		Name: record.Name.String(),
	}
}

type workflowLabelErrorScope struct {
	projectID *string
	taskID    *string
}

func workflowLabelError(err error, scope workflowLabelErrorScope) error {
	var nameErr *label.NameError
	if errors.As(err, &nameErr) {
		field := "name"
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonInvalidName,
			ProjectID: scope.projectID,
			Field:     &field,
		}
	}
	var conflictErr workflowstore.ProjectLabelNameConflictError
	if errors.As(err, &conflictErr) {
		projectID := conflictErr.ProjectID
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonNameConflict,
			ProjectID: &projectID,
		}
	}
	var limitErr workflowstore.ProjectLabelLimitError
	if errors.As(err, &limitErr) {
		projectID := limitErr.ProjectID
		limit := limitErr.Limit
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonCatalogLimit,
			ProjectID: &projectID,
			Limit:     &limit,
		}
	}
	var projectLabelNotFound workflowstore.ProjectLabelNotFoundError
	if errors.As(err, &projectLabelNotFound) {
		projectID := projectLabelNotFound.ProjectID
		labelID := projectLabelNotFound.LabelID
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonLabelNotFound,
			ProjectID: &projectID,
			LabelID:   &labelID,
		}
	}
	var orderErr workflowstore.ProjectLabelOrderError
	if errors.As(err, &orderErr) {
		projectID := orderErr.ProjectID
		field := "label_ids"
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonInvalidMutation,
			ProjectID: &projectID,
			LabelID:   orderErr.LabelID,
			Field:     &field,
		}
	}
	var taskNotFound workflowstore.TaskLabelTaskNotFoundError
	if errors.As(err, &taskNotFound) {
		taskID := taskNotFound.TaskID
		return &serverapi.WorkflowLabelError{
			Reason: serverapi.WorkflowLabelErrorReasonTaskNotFound,
			TaskID: &taskID,
		}
	}
	var labelNotFound workflowstore.TaskLabelNotFoundError
	if errors.As(err, &labelNotFound) {
		labelID := labelNotFound.LabelID
		return &serverapi.WorkflowLabelError{
			Reason:  serverapi.WorkflowLabelErrorReasonLabelNotFound,
			LabelID: &labelID,
		}
	}
	var wrongProject workflowstore.TaskLabelWrongProjectError
	if errors.As(err, &wrongProject) {
		projectID := wrongProject.TaskProjectID
		taskID := wrongProject.TaskID
		labelID := wrongProject.LabelID
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonWrongProject,
			ProjectID: &projectID,
			TaskID:    &taskID,
			LabelID:   &labelID,
		}
	}
	var mutationErr workflowstore.TaskLabelMutationError
	if errors.As(err, &mutationErr) {
		field := mutationErr.Field
		return &serverapi.WorkflowLabelError{
			Reason:  serverapi.WorkflowLabelErrorReasonInvalidMutation,
			TaskID:  scope.taskID,
			LabelID: mutationErr.LabelID,
			Field:   &field,
			Limit:   mutationErr.Limit,
		}
	}
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonProjectNotFound,
			ProjectID: scope.projectID,
		}
	}
	return err
}

func (s *Service) publishProjectEvent(ctx context.Context, projectID string, resource serverapi.WorkflowProjectEventResource, action serverapi.WorkflowProjectEventAction, primaryEntityID string, relatedIDs ...string) {
	s.publishWorkflowEvent(ctx, workflowstore.WorkflowEventRecord{
		ProjectID:       &projectID,
		Resource:        resource,
		Action:          action,
		PrimaryEntityID: primaryEntityID,
		RelatedIDs:      relatedIDs,
	})
}

func labelIDs(ids []label.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}
