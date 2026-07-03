package assertions

import (
	"fmt"

	"core/internal/testharness/pty/analyzer"
)

func validateWindow(analysis analyzer.Analysis, window analyzer.OperationWindow) error {
	if window.Start < 0 || window.End < window.Start || window.End > len(analysis.Operations) {
		return fmt.Errorf("invalid operation window: window=%+v operation_count=%d", window, len(analysis.Operations))
	}
	return nil
}

func regionContains(container, target analyzer.Region) bool {
	return container.Top <= target.Top &&
		container.Bottom >= target.Bottom &&
		container.Left <= target.Left &&
		container.Right >= target.Right
}

func assertionError(reason string, operation analyzer.Operation) error {
	return fmt.Errorf("%s: operation_sequence=%d kind=%d region=%+v byte_range=%+v", reason, operation.Sequence, operation.Kind, operation.Region, operation.ByteRange)
}
