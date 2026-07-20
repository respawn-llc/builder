//go:build !windows

package app

import (
	"errors"
	"io/fs"
	"os/exec"
)

func classifyLifecycleHookLaunchFailure(
	err error,
) (lifecycleHookLaunchFailureKind, bool) {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return lifecycleHookLaunchUnavailable, true
	case errors.Is(err, fs.ErrPermission):
		return lifecycleHookLaunchNonExecutable, true
	default:
		return 0, false
	}
}
