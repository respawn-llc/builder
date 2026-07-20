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
		return serverapi.WorkflowProjectLabelCreateResponse{}, workflowLabelError(err, req.ProjectID, "")
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
		return serverapi.WorkflowProjectLabelCatalogResponse{}, workflowLabelError(err, req.ProjectID, "")
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
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowProjectLabelRenameResponse{}, err
	}
	id, err := label.ParseID(req.LabelID)
	if err != nil {
		return serverapi.WorkflowProjectLabelRenameResponse{}, &serverapi.WorkflowLabelError{
			Reason:  serverapi.WorkflowLabelErrorReasonInvalidMutation,
			LabelID: req.LabelID,
			Field:   "label_id",
		}
	}
	record, err := s.store.RenameProjectLabel(ctx, req.ProjectID, id, req.Name)
	if err != nil {
		return serverapi.WorkflowProjectLabelRenameResponse{}, workflowLabelError(err, req.ProjectID, "")
	}
	response := serverapi.WorkflowProjectLabelRenameResponse{Label: workflowProjectLabel(record)}
	s.publishProjectEvent(ctx, req.ProjectID, serverapi.WorkflowProjectEventResourceLabel, serverapi.WorkflowProjectEventActionRenamed, response.Label.ID)
	return response, nil
}

func (s *Service) DeleteWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelDeleteRequest) (serverapi.WorkflowProjectLabelDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowProjectLabelDeleteResponse{}, err
	}
	id, err := label.ParseID(req.LabelID)
	if err != nil {
		return serverapi.WorkflowProjectLabelDeleteResponse{}, &serverapi.WorkflowLabelError{
			Reason:  serverapi.WorkflowLabelErrorReasonInvalidMutation,
			LabelID: req.LabelID,
			Field:   "label_id",
		}
	}
	record, err := s.store.DeleteProjectLabel(ctx, req.ProjectID, id)
	if err != nil {
		return serverapi.WorkflowProjectLabelDeleteResponse{}, workflowLabelError(err, req.ProjectID, "")
	}
	response := serverapi.WorkflowProjectLabelDeleteResponse{LabelID: record.ID.String()}
	s.publishProjectEvent(ctx, req.ProjectID, serverapi.WorkflowProjectEventResourceLabel, serverapi.WorkflowProjectEventActionDeleted, response.LabelID)
	return response, nil
}

func (s *Service) GetWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsGetRequest) (serverapi.WorkflowTaskLabelsGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskLabelsGetResponse{}, err
	}
	ids, err := s.store.GetTaskLabelIDs(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskLabelsGetResponse{}, workflowLabelError(err, "", req.TaskID)
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
		return serverapi.WorkflowTaskLabelsUpdateResponse{}, workflowLabelError(err, "", req.TaskID)
	}
	ids, err := s.store.UpdateTaskLabels(ctx, workflowstore.TaskLabelUpdateRequest{
		TaskID:         workflow.TaskID(req.TaskID),
		AddLabelIDs:    req.AddLabelIDs,
		RemoveLabelIDs: req.RemoveLabelIDs,
	})
	if err != nil {
		return serverapi.WorkflowTaskLabelsUpdateResponse{}, workflowLabelError(err, "", req.TaskID)
	}
	response := serverapi.WorkflowTaskLabelsUpdateResponse{
		Assignment: serverapi.WorkflowTaskAssignedLabelIDs{
			TaskID:   req.TaskID,
			LabelIDs: labelIDs(ids),
		},
	}
	s.publishProjectWorkflowEvent(ctx, scope.ProjectID, string(scope.WorkflowID), serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionLabelsChanged, req.TaskID)
	return response, nil
}

func workflowProjectLabel(record workflowstore.ProjectLabelRecord) serverapi.WorkflowProjectLabel {
	return serverapi.WorkflowProjectLabel{
		ID:   record.ID.String(),
		Name: record.Name.String(),
	}
}

func workflowLabelError(err error, projectID string, taskID string) error {
	var nameErr *label.NameError
	if errors.As(err, &nameErr) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonInvalidName,
			ProjectID: projectID,
			Field:     "name",
		}
	}
	var conflictErr workflowstore.ProjectLabelNameConflictError
	if errors.As(err, &conflictErr) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonNameConflict,
			ProjectID: conflictErr.ProjectID,
		}
	}
	var limitErr workflowstore.ProjectLabelLimitError
	if errors.As(err, &limitErr) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonCatalogLimit,
			ProjectID: limitErr.ProjectID,
			Limit:     limitErr.Limit,
		}
	}
	var projectLabelNotFound workflowstore.ProjectLabelNotFoundError
	if errors.As(err, &projectLabelNotFound) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonLabelNotFound,
			ProjectID: projectLabelNotFound.ProjectID,
			LabelID:   projectLabelNotFound.LabelID,
		}
	}
	var taskNotFound workflowstore.TaskLabelTaskNotFoundError
	if errors.As(err, &taskNotFound) {
		return &serverapi.WorkflowLabelError{
			Reason: serverapi.WorkflowLabelErrorReasonTaskNotFound,
			TaskID: taskNotFound.TaskID,
		}
	}
	var labelNotFound workflowstore.TaskLabelNotFoundError
	if errors.As(err, &labelNotFound) {
		return &serverapi.WorkflowLabelError{
			Reason:  serverapi.WorkflowLabelErrorReasonLabelNotFound,
			LabelID: labelNotFound.LabelID,
		}
	}
	var wrongProject workflowstore.TaskLabelWrongProjectError
	if errors.As(err, &wrongProject) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonWrongProject,
			ProjectID: wrongProject.TaskProjectID,
			TaskID:    wrongProject.TaskID,
			LabelID:   wrongProject.LabelID,
		}
	}
	var mutationErr workflowstore.TaskLabelMutationError
	if errors.As(err, &mutationErr) {
		return &serverapi.WorkflowLabelError{
			Reason:  serverapi.WorkflowLabelErrorReasonInvalidMutation,
			TaskID:  taskID,
			LabelID: mutationErr.LabelID,
			Field:   mutationErr.Field,
			Limit:   mutationErr.Limit,
		}
	}
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		return &serverapi.WorkflowLabelError{
			Reason:    serverapi.WorkflowLabelErrorReasonProjectNotFound,
			ProjectID: projectID,
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
