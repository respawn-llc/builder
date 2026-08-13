package scripts

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"core/shared/protoapi"
)

func TestGoAndTypeScriptReportTheSameDescriptorSet(t *testing.T) {
	root := repositoryRoot(t)
	packageRoot := filepath.Join(root, "apps", "desktop", "packages", "server-api-contract")

	build := exec.Command("pnpm", "build")
	build.Dir = packageRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build TypeScript contract package: %v\n%s", err, output)
	}

	report := exec.Command(
		"node",
		"--input-type=module",
		"--eval",
		`import { descriptorPaths } from "./dist/index.js"; process.stdout.write(JSON.stringify(descriptorPaths()));`,
	)
	report.Dir = packageRoot
	output, err := report.Output()
	if err != nil {
		t.Fatalf("report TypeScript descriptor paths: %v", err)
	}
	var typescriptPaths []string
	if err := json.Unmarshal(output, &typescriptPaths); err != nil {
		t.Fatalf("decode TypeScript descriptor paths: %v", err)
	}

	goPaths := protoapi.DescriptorPaths()
	if !slices.Equal(goPaths, typescriptPaths) {
		t.Fatalf("Go descriptor paths = %v, TypeScript descriptor paths = %v", goPaths, typescriptPaths)
	}
}
