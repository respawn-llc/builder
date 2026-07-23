package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Homebrew kent formula must install prebuilt binaries (release archives /
// bottles), never compile from source. A source build in the formula means
// `brew install kent` silently falls back to a full toolchain build whenever a
// bottle is missing, which has regressed releases before. This guard fails the
// build if scripts/update-brew-tap.sh ever reintroduces a source-build path into
// the generated formula. The tap repo enforces the same invariant on the
// committed Formula/kent.rb.
func TestBrewTapGeneratorEmitsPrebuiltBinaryFormula(t *testing.T) {
	repoRoot := findRepoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "update-brew-tap.sh")
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	script := string(content)

	forbidden := []string{
		"scripts/build.sh",
		"go build",
		"=> :build",
		`system "go"`,
	}
	for _, marker := range forbidden {
		if strings.Contains(script, marker) {
			t.Errorf("update-brew-tap.sh reintroduces a source build (found %q); the kent formula must install prebuilt binaries only", marker)
		}
	}

	if !strings.Contains(script, "bin.install") {
		t.Error("update-brew-tap.sh no longer installs a prebuilt binary (missing bin.install); the kent formula must install prebuilt binaries only")
	}
}
