package pty

import "core/internal/testharness/pty/analyzer"

func Analyze(capture Capture) (Analysis, error) {
	return analyzer.Analyze(capture)
}
