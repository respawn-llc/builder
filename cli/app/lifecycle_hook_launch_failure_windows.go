//go:build windows

package app

import (
	"errors"
	"io/fs"
	"os/exec"

	"golang.org/x/sys/windows"
)

func classifyLifecycleHookLaunchFailure(
	err error,
) (lifecycleHookLaunchFailureKind, bool) {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return lifecycleHookLaunchUnavailable, true
	case errors.Is(err, fs.ErrPermission),
		errors.Is(err, windows.ERROR_BAD_EXE_FORMAT),
		errors.Is(err, windows.ERROR_EXE_MACHINE_TYPE_MISMATCH):
		return lifecycleHookLaunchNonExecutable, true
	default:
		return 0, false
	}
}
