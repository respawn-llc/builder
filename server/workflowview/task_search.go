package workflowview

import (
	"context"
	"errors"

	"core/shared/serverapi"
)

type TaskSearch struct{}

func NewTaskSearch() (*TaskSearch, error) {
	return &TaskSearch{}, nil
}

func (s *TaskSearch) Search(ctx context.Context, req serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	if s == nil {
		return serverapi.TaskSearchResponse{}, errors.New("task search is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	return serverapi.TaskSearchResponse{
		Mode:   req.Mode,
		Groups: []serverapi.TaskSearchGroup{},
	}, nil
}
