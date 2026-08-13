package protogen

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureGeneratesOnlyWhenInputsOrOutputsChange(t *testing.T) {
	manager, target, generations := newFixtureManager(t)

	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generationCount(t, generations); got != 1 {
		t.Fatalf("unchanged ensure generation count = %d, want 1", got)
	}

	writeFile(t, manager.RepositoryRoot, "api/proto/fixture.proto", "changed schema\n")
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generationCount(t, generations); got != 2 {
		t.Fatalf("changed-input generation count = %d, want 2", got)
	}

	writeFile(t, manager.RepositoryRoot, "tools/protobuf/internal/protogen/generate.go", "changed orchestration\n")
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generationCount(t, generations); got != 3 {
		t.Fatalf("changed-orchestration generation count = %d, want 3", got)
	}

	writeFile(t, manager.RepositoryRoot, filepath.Join(target.OutputPath, "contract.generated"), "edited output\n")
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generationCount(t, generations); got != 4 {
		t.Fatalf("changed-output generation count = %d, want 4", got)
	}
}

func TestEnsureSerializesConcurrentGeneration(t *testing.T) {
	manager, target, generations := newFixtureManager(t)
	t.Setenv("KENT_PROTOBUF_TEST_GENERATION_DELAY", "0.1")

	var waitGroup sync.WaitGroup
	errorsByCall := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errorsByCall <- manager.Ensure([]Target{target})
		}()
	}
	waitGroup.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := generationCount(t, generations); got != 1 {
		t.Fatalf("concurrent generation count = %d, want 1", got)
	}
}

func TestVerifyRejectsNondeterministicGeneration(t *testing.T) {
	manager, target, _ := newFixtureManager(t)
	t.Setenv("KENT_PROTOBUF_TEST_NONDETERMINISTIC", "1")

	if err := manager.Verify([]Target{target}); err == nil {
		t.Fatal("verify accepted nondeterministic generation")
	}
}

func TestCleanRemovesGeneratedOutputAndManifest(t *testing.T) {
	manager, target, _ := newFixtureManager(t)
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Clean([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(manager.RepositoryRoot, target.OutputPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated output stat error = %v", err)
	}
	if _, err := os.Stat(manager.manifestPath(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest stat error = %v", err)
	}
}

func TestOutputsReadyMatchesTheRequiredTarget(t *testing.T) {
	if !OutputsReady("all", []Target{Targets["go"]}) {
		t.Fatal("all outputs did not satisfy Go")
	}
	if !OutputsReady("go", []Target{Targets["go"]}) {
		t.Fatal("Go outputs did not satisfy Go")
	}
	if OutputsReady("go", []Target{Targets["ts"]}) {
		t.Fatal("Go outputs satisfied TypeScript")
	}
	if OutputsReady("go", []Target{Targets["go"], Targets["ts"]}) {
		t.Fatal("Go outputs satisfied all targets")
	}
}

func newFixtureManager(t *testing.T) (*Manager, Target, string) {
	t.Helper()
	root := t.TempDir()
	target := Target{
		Name:       "fixture",
		Template:   "buf.gen.fixture.yaml",
		OutputPath: "generated/fixture",
		Inputs:     []string{"tools/protobuf/generator"},
	}
	for path, content := range map[string]string{
		"api/proto/fixture.proto":               "schema\n",
		"buf.yaml":                              "buf\n",
		"buf.lock":                              "lock\n",
		"buf.gen.fixture.yaml":                  "template\n",
		"tools/protobuf/go.mod":                 "module fixture\n",
		"tools/protobuf/go.sum":                 "sum\n",
		"tools/protobuf/internal/protogen/x":    "orchestration\n",
		"tools/protobuf/internal/registrygen/x": "registry\n",
		"tools/protobuf/generator/main.go":      "generator\n",
	} {
		writeFile(t, root, path, content)
	}
	binRoot := filepath.Join(root, "bin")
	generations := filepath.Join(root, "generation-count")
	writeFile(t, root, "bin/go", `#!/usr/bin/env bash
set -euo pipefail
count=0
if [[ -f "$KENT_PROTOBUF_TEST_GENERATION_COUNT" ]]; then
	read -r count < "$KENT_PROTOBUF_TEST_GENERATION_COUNT"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$KENT_PROTOBUF_TEST_GENERATION_COUNT"
if [[ -n "${KENT_PROTOBUF_TEST_GENERATION_DELAY:-}" ]]; then
	sleep "$KENT_PROTOBUF_TEST_GENERATION_DELAY"
fi
content=generated
if [[ "${KENT_PROTOBUF_TEST_NONDETERMINISTIC:-0}" == "1" ]]; then
	content="generated-$count"
fi
output=
while [[ $# -gt 0 ]]; do
	case "$1" in
	--output)
		output="$2"
		shift 2
		;;
	*)
		shift
		;;
	esac
done
mkdir -p "$output/generated/fixture"
printf '%s\n' "$content" > "$output/generated/fixture/contract.generated"
`)
	if err := os.Chmod(filepath.Join(binRoot, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KENT_PROTOBUF_GO_COMMAND", filepath.Join(binRoot, "go"))
	t.Setenv("KENT_PROTOBUF_TEST_GENERATION_COUNT", generations)
	manager := NewManager(root)
	return manager, target, generations
}

func generationCount(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	switch string(content) {
	case "1\n":
		return 1
	case "2\n":
		return 2
	case "3\n":
		return 3
	case "4\n":
		return 4
	default:
		t.Fatalf("unexpected generation count %q", content)
		return 0
	}
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	absolute := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
