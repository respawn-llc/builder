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
	page := WorkflowOffsetPage[T]{Items: items}
	if len(items) <= window.Limit {
		return page
	}
	page.Items = items[:window.Limit]
	nextOffset := window.Offset + window.Limit
	page.NextOffset = &nextOffset
	return page
}
