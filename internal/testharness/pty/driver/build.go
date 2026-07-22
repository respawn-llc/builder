package driver

import (
	"context"
	"os/exec"
)

func BuildPackage(ctx context.Context, packagePath, outputPath string) error {
	return exec.CommandContext(ctx, "go", "build", "-o", outputPath, packagePath).Run()
}

func BuildTestBinary(ctx context.Context, packagePath, outputPath string) error {
	return exec.CommandContext(ctx, "go", "test", "-c", "-o", outputPath, packagePath).Run()
}
