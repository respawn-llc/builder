package pty

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func BuildOrUsePrebuiltKent(ctx context.Context, outputPath string) (string, error) {
	binary, configured, err := PrebuiltExecutable(KentBinaryEnvName)
	if err != nil {
		return "", err
	}
	if configured {
		return binary, nil
	}
	buildScript, err := kentBuildScriptPath()
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, buildScript, "server", "--output", outputPath)
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build production Kent: %w output=%q", err, output)
	}
	return outputPath, nil
}

func kentBuildScriptPath() (string, error) {
	_, sourcePath, _, found := runtime.Caller(0)
	if !found {
		return "", fmt.Errorf("resolve PTY prebuild source path")
	}
	buildScript := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", "..", "scripts", "build.sh"))
	info, err := os.Stat(buildScript)
	if err != nil {
		return "", fmt.Errorf("inspect Kent build script %q: %w", buildScript, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("Kent build script %q is not executable", buildScript)
	}
	return buildScript, nil
}
