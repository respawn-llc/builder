package pty

import "core/internal/testharness/pty/assertions"

type BlankFrameAssertionError = assertions.BlankFrameAssertionError

func BlankFrame(analysis Analysis) error {
	return assertions.BlankFrame(analysis)
}

func NoWritesAbove(analysis Analysis, window OperationWindow, immutableBoundary int) error {
	return assertions.NoWritesAbove(analysis, window, immutableBoundary)
}

func ErasesOnlyWithin(analysis Analysis, window OperationWindow, allowed Region) error {
	return assertions.ErasesOnlyWithin(analysis, window, allowed)
}

func NoFullScreenReEmission(analysis Analysis, window OperationWindow) error {
	return assertions.NoFullScreenReEmission(analysis, window)
}

func NoRegionReEmission(analysis Analysis, window OperationWindow, protected Region) error {
	return assertions.NoRegionReEmission(analysis, window, protected)
}

func ContentAppendedExactlyOnce(appends []AppendOperation, content string) error {
	return assertions.ContentAppendedExactlyOnce(appends, content)
}

func NoAlternateScroll1007(analysis Analysis, windows ...OperationWindow) error {
	return assertions.NoAlternateScroll1007(analysis, windows...)
}
