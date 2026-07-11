//go:build windows

package driver

import (
	"context"

	"core/internal/testharness/pty/analyzer"
)

// RunCommand is unavailable until the harness has a ConPTY backend.
func RunCommand(context.Context, CommandSpec) (analyzer.Capture, error) {
	return analyzer.Capture{}, errConPTYUnavailable
}
