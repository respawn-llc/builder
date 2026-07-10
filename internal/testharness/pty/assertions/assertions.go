package assertions

import (
	"errors"
	"fmt"

	"core/internal/testharness/pty/analyzer"
)

type BlankFrameAssertionError struct {
	analyzer.BlankFrameDiagnostic
}

func (e *BlankFrameAssertionError) Error() string {
	return fmt.Sprintf(
		"terminal frame is not blank: dimensions=%dx%d first_occupied=row:%d col:%d content=%q",
		e.Dimensions.Rows,
		e.Dimensions.Cols,
		e.Position.Row,
		e.Position.Col,
		e.Content,
	)
}

func BlankFrame(analysis analyzer.Analysis) error {
	diagnostic := analysis.Screen.BlankFrameDiagnostic()
	if diagnostic == nil {
		return nil
	}
	return &BlankFrameAssertionError{BlankFrameDiagnostic: *diagnostic}
}

func NoWritesAbove(analysis analyzer.Analysis, window analyzer.OperationWindow, immutableBoundary int) error {
	if err := validateWindow(analysis, window); err != nil {
		return err
	}
	for _, operation := range analysis.Operations[window.Start:window.End] {
		if operation.Kind == analyzer.OperationWrite && operation.Region.Top < immutableBoundary {
			return assertionError("write above immutable boundary", operation)
		}
	}
	return nil
}

func ErasesOnlyWithin(analysis analyzer.Analysis, window analyzer.OperationWindow, allowed analyzer.Region) error {
	if err := validateWindow(analysis, window); err != nil {
		return err
	}
	for _, operation := range analysis.Operations[window.Start:window.End] {
		if operation.Kind == analyzer.OperationErase && !regionContains(allowed, operation.Region) {
			return assertionError("erase outside allowed region", operation)
		}
	}
	return nil
}

func NoFullScreenReEmission(analysis analyzer.Analysis, window analyzer.OperationWindow) error {
	return NoRegionReEmission(analysis, window, analyzer.Region{Top: 0, Bottom: analysis.Dimensions.Rows, Left: 0, Right: analysis.Dimensions.Cols})
}

func NoRegionReEmission(analysis analyzer.Analysis, window analyzer.OperationWindow, protected analyzer.Region) error {
	if err := validateWindow(analysis, window); err != nil {
		return err
	}
	erasedCells := map[int]map[int]struct{}{}
	rewrittenAfterErase := map[int]map[int]struct{}{}
	for _, operation := range analysis.Operations[window.Start:window.End] {
		if operation.Kind == analyzer.OperationErase {
			recordWriteCoverage(erasedCells, intersection(operation.Region, protected))
			continue
		}
		if operation.Kind == analyzer.OperationWrite {
			recordRewriteCoverage(rewrittenAfterErase, erasedCells, intersection(operation.Region, protected))
		}
	}
	if !regionCovered(rewrittenAfterErase, protected) {
		return nil
	}
	return errors.New("protected-region re-emission detected: erase followed by complete write coverage")
}

func ContentAppendedExactlyOnce(appends []analyzer.AppendOperation, content string) error {
	count := 0
	for _, appendOperation := range appends {
		if appendOperation.Operation.Kind == analyzer.OperationWrite && appendOperation.Operation.Write == nil {
			return fmt.Errorf("append write operation missing payload: sequence=%d byte_range=%+v", appendOperation.Operation.Sequence, appendOperation.Operation.ByteRange)
		}
		if appendOperation.Operation.Write != nil && appendOperation.Operation.Write.Text() == content {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("content append count for %q = %d, want exactly 1", content, count)
	}
	return nil
}

func NoAlternateScroll1007(analysis analyzer.Analysis, windows ...analyzer.OperationWindow) error {
	ranges := []analyzer.OperationWindow{{Start: 0, End: len(analysis.Operations)}}
	if len(windows) > 0 {
		ranges = windows
	}
	for _, window := range ranges {
		if err := validateWindow(analysis, window); err != nil {
			return err
		}
		for _, operation := range analysis.Operations[window.Start:window.End] {
			if operation.Kind != analyzer.OperationModeChange || operation.PrivateMode == nil {
				continue
			}
			if operation.PrivateMode.Mode == 1007 && operation.PrivateMode.Enabled {
				return fmt.Errorf("forbidden private mode ?1007 enabled at chunk=%d byte_range=%+v", operation.ChunkIndex, operation.ByteRange)
			}
		}
	}
	return nil
}

func recordWriteCoverage(coverage map[int]map[int]struct{}, region analyzer.Region) {
	if region.Empty() || region.Bottom < region.Top || region.Right < region.Left {
		return
	}
	for row := region.Top; row < region.Bottom; row++ {
		if coverage[row] == nil {
			coverage[row] = map[int]struct{}{}
		}
		for col := region.Left; col < region.Right; col++ {
			coverage[row][col] = struct{}{}
		}
	}
}

func regionCovered(coverage map[int]map[int]struct{}, region analyzer.Region) bool {
	if region.Empty() {
		return false
	}
	for row := region.Top; row < region.Bottom; row++ {
		for col := region.Left; col < region.Right; col++ {
			if _, ok := coverage[row][col]; !ok {
				return false
			}
		}
	}
	return true
}

func recordRewriteCoverage(rewritten map[int]map[int]struct{}, erased map[int]map[int]struct{}, region analyzer.Region) {
	if region.Empty() || region.Bottom < region.Top || region.Right < region.Left {
		return
	}
	for row := region.Top; row < region.Bottom; row++ {
		for col := region.Left; col < region.Right; col++ {
			if _, ok := erased[row][col]; !ok {
				continue
			}
			if rewritten[row] == nil {
				rewritten[row] = map[int]struct{}{}
			}
			rewritten[row][col] = struct{}{}
		}
	}
}

func intersection(a analyzer.Region, b analyzer.Region) analyzer.Region {
	return analyzer.Region{
		Top:    max(a.Top, b.Top),
		Bottom: min(a.Bottom, b.Bottom),
		Left:   max(a.Left, b.Left),
		Right:  min(a.Right, b.Right),
	}
}
