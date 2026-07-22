package scriptedllm_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptedLLMImportReachability(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list: %v\n%s", err, string(exitErr.Stderr))
		}
		t.Fatalf("go list: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode package: %v", err)
		}
		if allowedScriptedLLMImporter(pkg.ImportPath) {
			continue
		}
		for _, imported := range pkg.Imports {
			if imported == "core/internal/testharness/scriptedllm" {
				t.Fatalf("%s imports scriptedllm from non-harness production code", pkg.ImportPath)
			}
		}
	}
}

type listedPackage struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
}

func allowedScriptedLLMImporter(importPath string) bool {
	return importPath == "core/internal/testharness/scriptedllm" ||
		strings.HasPrefix(importPath, "core/internal/testharness/") ||
		strings.HasPrefix(importPath, "core/cli/app/internal/ptyfixture")
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
