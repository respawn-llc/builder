package blackbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunnerExecutesDeclaredGoModelBoundaryScenario(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "kent")
	build := exec.CommandContext(context.Background(), "./scripts/build.sh", "server", "--output", binary)
	build.Dir = filepath.Join("..", "..", "..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Logf("build output:\n%s", output)
		t.Fatalf("build compiled Kent client: %v", err)
	}
	// The build output is a newly materialized macOS executable. Execute its
	// lightweight version path before timing the harness's fixed 500 ms server
	// readiness contract so first-exec loader work is not attributed to server
	// readiness.
	if output, err := exec.CommandContext(context.Background(), binary, "--version").CombinedOutput(); err != nil {
		t.Logf("version output:\n%s", output)
		t.Fatalf("preflight compiled Kent client: %v", err)
	}
	scenario, err := LoadScenario("testdata/go-model-boundary.json")
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	result := (Runner{}).Run(RunRequest{
		Scenario:     scenario,
		Profile:      GoProfile,
		ClientBinary: binary,
		ServerBinary: binary,
	})
	if result.Err != nil {
		t.Fatalf("Run: %v; run_root=%s; artifacts=%s", result.Err, result.RunRoot, result.ArtifactDir)
	}
	if !result.Observation.Model.RequiredConsumed() {
		t.Fatalf("declared Responses proof was not consumed: %#v", result.Observation.Model)
	}
	if !result.Observation.ClientExited {
		t.Fatal("client did not exit after declared termination action")
	}
}
