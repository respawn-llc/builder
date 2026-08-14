package workflowview

import "core/shared/serverapi"

func workflowOffsetWindowFromValidated(offset *int, limit *int) serverapi.WorkflowOffsetWindow {
	window := serverapi.WorkflowOffsetWindow{Limit: serverapi.OffsetPaginationMaxLimit}
	if offset != nil {
		window.Offset = *offset
	}
	if limit != nil {
		window.Limit = *limit
	}
	return window
}
