//go:build windows

package blackbox

import (
	"errors"
	"os/exec"
)

var errConPTYUnavailable = errors.New("black-box PTY harness is unavailable on Windows: ConPTY support is not implemented")

func requirePTYPlatform() error {
	return errConPTYUnavailable
}

func configureServerProcessGroup(*exec.Cmd) error {
	return errConPTYUnavailable
}

func terminateServerProcessGroup(*exec.Cmd) error {
	return errConPTYUnavailable
}

func killServerProcessGroup(*exec.Cmd) error {
	return errConPTYUnavailable
}
