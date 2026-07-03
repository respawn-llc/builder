package analyzer

import (
	"errors"
	"fmt"
)

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
		if isAppendWrite(analysis.Dimensions, operation, immutableBoundary) {
			appends = append(appends, AppendOperation{Operation: operation})
		}
	}
	return appends
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

func phaseKindFromProtocol(raw string) (PhaseKind, error) {
	switch raw {
	case "ScenarioStart":
		return PhaseScenarioStart, nil
	case "WindowStart":
		return PhaseWindowStart, nil
	case "WindowEnd":
		return PhaseWindowEnd, nil
	case "ReadyForQuit":
		return PhaseReadyForQuit, nil
	case "ScenarioComplete":
		return PhaseScenarioComplete, nil
	default:
		return 0, fmt.Errorf("unknown phase %q", raw)
	}
}

func validateWindowEventID(kind PhaseKind, id *WindowID) error {
	if (kind == PhaseWindowStart || kind == PhaseWindowEnd) && id == nil {
		return errors.New("window phase requires window_id")
	}
	return nil
}
