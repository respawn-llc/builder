package driver

import (
	"context"
	"os/exec"
)

func BuildPackage(ctx context.Context, packagePath, outputPath string) error {
	return exec.CommandContext(ctx, "go", "build", "-o", outputPath, packagePath).Run()
}
