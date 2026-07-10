package driver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"core/internal/testharness/pty/analyzer"

	creackpty "github.com/creack/pty"
)

// SessionStart prepares a generic PTY child with an exact environment. The
// event reactor is introduced separately; keeping startup isolated prevents
// the legacy overlay facade from leaking into black-box launches.
func sessionStart(spec SessionSpec) (*exec.Cmd, *os.File, error) {
	if _, err := analyzer.NewDimensions(spec.Dimensions.Rows, spec.Dimensions.Cols); err != nil {
		return nil, nil, err
	}
	if spec.Path == "" {
		return nil, nil, errors.New("session command path is required")
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = append([]string(nil), spec.Env...)
	cmd.Dir = spec.Dir
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Rows: uint16(spec.Dimensions.Rows),
		Cols: uint16(spec.Dimensions.Cols),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start PTY session path=%s args=%v: %w", spec.Path, spec.Args, err)
	}
	return cmd, ptmx, nil
}
