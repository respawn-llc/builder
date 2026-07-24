package pty

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const (
	KentBinaryEnvName             = "KENT_PTY_KENT_BINARY"
	AnsiWriterBinaryEnvName       = "KENT_PTY_ANSI_WRITER_BINARY"
	PhaseInputWriterBinaryEnvName = "KENT_PTY_PHASE_INPUT_WRITER_BINARY"
	PhaseWriterBinaryEnvName      = "KENT_PTY_PHASE_WRITER_BINARY"
)

func PrebuiltExecutable(environmentName string) (string, bool, error) {
	path, configured := os.LookupEnv(environmentName)
	if !configured {
		return "", false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", false, fmt.Errorf("%q is not an executable regular file", path)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve %q: %w", path, err)
	}
	return absolutePath, true, nil
}

func BuildOrUsePrebuiltPackage(
	ctx context.Context,
	environmentName string,
	packagePath string,
	outputPath string,
) (string, error) {
	binary, configured, err := PrebuiltExecutable(environmentName)
	if err != nil {
		return "", err
	}
	if configured {
		return binary, nil
	}
	if err := BuildPackage(ctx, packagePath, outputPath); err != nil {
		return "", err
	}
	return outputPath, nil
}
