package analyzer

import "fmt"

func ResolveOperationWindows(analysis Analysis) (map[WindowID]OperationWindow, error) {
	starts := map[WindowID]PhaseEvent{}
	windows := map[WindowID]OperationWindow{}
	for _, event := range analysis.PhaseEvents {
		if event.Phase != PhaseWindowStart && event.Phase != PhaseWindowEnd {
			continue
		}
		if event.WindowID == nil {
			return nil, fmt.Errorf("phase %v missing window ID", event.Phase)
		}
		windowID := *event.WindowID
		switch event.Phase {
		case PhaseWindowStart:
			if _, exists := starts[windowID]; exists {
				return nil, fmt.Errorf("duplicate window start for %s", windowID)
			}
			starts[windowID] = event
		case PhaseWindowEnd:
			start, exists := starts[windowID]
			if !exists {
				return nil, fmt.Errorf("window end without start for %s", windowID)
			}
			if _, exists := windows[windowID]; exists {
				return nil, fmt.Errorf("duplicate window end for %s", windowID)
			}
			windows[windowID] = OperationWindow{
				Start: firstOperationAfter(analysis.Operations, start.ByteRange.End),
				End:   firstOperationAtOrAfter(analysis.Operations, event.ByteRange.Start),
			}
		}
	}
	for windowID := range starts {
		if _, exists := windows[windowID]; !exists {
			return nil, fmt.Errorf("window start without end for %s", windowID)
		}
	}
	return windows, nil
}

func ClassifyAppends(analysis Analysis, window OperationWindow, immutableBoundary int) []AppendOperation {
	if window.Start < 0 || window.End < window.Start || window.End > len(analysis.Operations) {
		panic(fmt.Sprintf("invalid operation window: window=%+v operation_count=%d", window, len(analysis.Operations)))
	}
	appends := make([]AppendOperation, 0)
	for _, operation := range analysis.Operations[window.Start:window.End] {
		for _, record := range OperationRecords(operation) {
			if isAppendWrite(analysis.Dimensions, record, immutableBoundary) {
				appends = append(appends, AppendOperation{Operation: record})
			}
		}
	}
	return appends
}

func CoalesceAppendRows(appends []AppendOperation) []AppendOperation {
	out := make([]AppendOperation, 0, len(appends))
	for _, appendOperation := range appends {
		current := appendOperation.Operation
		if current.Kind != OperationWrite || current.Write == nil {
			out = append(out, appendOperation)
			continue
		}
		if len(out) == 0 {
			out = append(out, appendOperation)
			continue
		}
		previous := &out[len(out)-1].Operation
		if previous.Kind != OperationWrite ||
			previous.Write == nil ||
			previous.Write.Faint != current.Write.Faint ||
			previous.Write.Bold != current.Write.Bold ||
			previous.Write.Italic != current.Write.Italic ||
			previous.Write.Underline != current.Write.Underline ||
			previous.Write.Foreground != current.Write.Foreground ||
			previous.Write.Background != current.Write.Background ||
			previous.Region.Top != current.Region.Top ||
			previous.Region.Bottom != current.Region.Bottom ||
			previous.Region.Right != current.Region.Left {
			out = append(out, appendOperation)
			continue
		}
		previous.Region.Right = current.Region.Right
		previous.ByteRange.End = current.ByteRange.End
		previous.After = current.After
		previous.CapturedAt = current.CapturedAt
		payload := MustWritePayload(previous.Write.Text() + current.Write.Text())
		payload.Faint = previous.Write.Faint
		payload.Bold = previous.Write.Bold
		payload.Italic = previous.Write.Italic
		payload.Underline = previous.Write.Underline
		payload.Foreground = previous.Write.Foreground
		payload.Background = previous.Write.Background
		previous.Write = &payload
	}
	return out
}

func isAppendWrite(dimensions Dimensions, operation Operation, immutableBoundary int) bool {
	if operation.Kind != OperationWrite {
		return false
	}
	if operation.Write == nil {
		panic(fmt.Sprintf("write operation missing payload: sequence=%d byte_range=%+v", operation.Sequence, operation.ByteRange))
	}
	if operation.Region.Top < immutableBoundary {
		return false
	}
	return operation.Region.Top == immutableBoundary || operation.Region.Bottom >= dimensions.Rows
}

func firstOperationAfter(operations []Operation, byteOffset int64) int {
	for index, operation := range operations {
		if operation.ByteRange.Start >= byteOffset {
			return index
		}
	}
	return len(operations)
}

func firstOperationAtOrAfter(operations []Operation, byteOffset int64) int {
	for index, operation := range operations {
		if operation.ByteRange.Start >= byteOffset {
			return index
		}
	}
	return len(operations)
}
