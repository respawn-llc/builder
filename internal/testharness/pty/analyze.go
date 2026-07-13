package pty

import "core/internal/testharness/pty/analyzer"

func Analyze(capture Capture) (Analysis, error) {
	return analyzer.Analyze(capture)
}

type ReplayCheckpoint = analyzer.ReplayCheckpoint

func ReplayCheckpointScreens(capture Capture, checkpoints []ReplayCheckpoint) ([]ScreenSnapshot, error) {
	return analyzer.ReplayCheckpointScreens(capture, checkpoints)
}
