package pty

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	KentBinaryEnvName             = "KENT_PTY_KENT_BINARY"
	AnsiWriterBinaryEnvName       = "KENT_PTY_ANSI_WRITER_BINARY"
	PhaseInputWriterBinaryEnvName = "KENT_PTY_PHASE_INPUT_WRITER_BINARY"
	PhaseWriterBinaryEnvName      = "KENT_PTY_PHASE_WRITER_BINARY"
)

func PrebuiltExecutable(environmentName string) (*string, error) {
	path, configured := os.LookupEnv(environmentName)
	if !configured {
		return nil, nil
	}
	absolutePath, err := executablePath(path)
	if err != nil {
		return nil, err
	}
	return &absolutePath, nil
}

func executablePath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %q: %w", path, err)
	}
	if !executableMode(info.Mode(), runtime.GOOS) {
		return "", fmt.Errorf("%q is not an executable regular file", path)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	return absolutePath, nil
}

func executableMode(mode os.FileMode, operatingSystem string) bool {
	return mode.IsRegular() && (operatingSystem == "windows" || mode&0o111 != 0)
}

func BuildOrUsePrebuiltPackage(
	ctx context.Context,
	environmentName string,
	packagePath string,
	outputPath string,
) (string, error) {
	binary, err := PrebuiltExecutable(environmentName)
	if err != nil {
		return "", err
	}
	if binary != nil {
		return *binary, nil
	}
	if err := BuildPackage(ctx, packagePath, outputPath); err != nil {
		return "", err
	}
	builtBinary, err := executablePath(outputPath)
	if err != nil {
		return "", fmt.Errorf("validate built package %q: %w", packagePath, err)
	}
	return builtBinary, nil
}

func BuildOrUsePrebuiltKent(ctx context.Context, outputPath string) (string, error) {
	return BuildOrUsePrebuiltPackage(ctx, KentBinaryEnvName, "core/cli/kent", outputPath)
}
