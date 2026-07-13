package core_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInstallScriptEmbedsAuthoritativeChecksumLibrary(t *testing.T) {
	repoRoot := findRepoRoot(t)
	generatorPath := filepath.Join(repoRoot, "scripts", "generate-install.sh")
	command := exec.Command("bash", generatorPath, "--check")
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("check generated install script: %v\n%s", err, output)
	}
}
