package workflowview

import "errors"

var (
	ErrTaskIDRequired   = errors.New("task_id is required")
	ErrInvalidPageToken = errors.New("page_token is invalid")
)
