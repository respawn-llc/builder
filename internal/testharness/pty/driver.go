package pty

import (
	"context"

	"core/internal/testharness/pty/driver"
)

type CommandSpec = driver.CommandSpec

type InputEvent = driver.InputEvent

type PhaseInputEvent = driver.PhaseInputEvent

type FrameInputSequence = driver.FrameInputSequence

type FrameInput = driver.FrameInput

type FrameResizeEvent = driver.FrameResizeEvent

type ParseableInputEvent = driver.ParseableInputEvent

type DriverResizeEvent = driver.ResizeEvent

type TimeoutError = driver.TimeoutError

func RunCommand(ctx context.Context, spec CommandSpec) (Capture, error) {
	return driver.RunCommand(ctx, spec)
}

func BuildPackage(ctx context.Context, packagePath, outputPath string) error {
	return driver.BuildPackage(ctx, packagePath, outputPath)
}

func BuildTestBinary(ctx context.Context, packagePath, outputPath string) error {
	return driver.BuildTestBinary(ctx, packagePath, outputPath)
}
