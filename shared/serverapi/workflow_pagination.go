package serverapi

type WorkflowTaskOffsetPageRequest struct {
	TaskID string `json:"task_id"`
	Offset *int   `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

func (r WorkflowTaskOffsetPageRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	_, err := ResolveWorkflowOffsetWindow(r.Offset, r.Limit)
	return err
}

type WorkflowOffsetPage[T any] struct {
	Items      []T  `json:"items"`
	NextOffset *int `json:"next_offset,omitempty"`
}

func FinalizeWorkflowOffsetPage[T any](window WorkflowOffsetWindow, items []T) WorkflowOffsetPage[T] {
	items, nextOffset := TrimOffsetLookahead(window, items)
	return WorkflowOffsetPage[T]{Items: items, NextOffset: nextOffset}
}
