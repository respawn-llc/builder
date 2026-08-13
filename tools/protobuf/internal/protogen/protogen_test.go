package protogen

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureGeneratesOnlyWhenInputsOrOutputsChange(t *testing.T) {
	manager, target, generations := newFixtureManager(t)

	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generations.Load(); got != 1 {
		t.Fatalf("unchanged ensure generation count = %d, want 1", got)
	}

	writeFile(t, manager.RepositoryRoot, "api/proto/fixture.proto", "changed schema\n")
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generations.Load(); got != 2 {
		t.Fatalf("changed-input generation count = %d, want 2", got)
	}

	writeFile(t, manager.RepositoryRoot, filepath.Join(target.OutputPath, "contract.generated"), "edited output\n")
	if err := manager.Ensure([]Target{target}); err != nil {
		t.Fatal(err)
	}
	if got := generations.Load(); got != 3 {
		t.Fatalf("changed-output generation count = %d, want 3", got)
	}
}

func TestEnsureSerializesConcurrentGeneration(t *testing.T) {
	manager, target, generations := newFixtureManager(t)
	manager.Generate = func(target Target, destination string) error {
		generations.Add(1)
		time.Sleep(100 * time.Millisecond)
		return writeGeneratedOutput(target, destination)
	}

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
	if got := generations.Load(); got != 1 {
		t.Fatalf("concurrent generation count = %d, want 1", got)
	}
}

func TestFailedReplacementRestoresPreviousOutput(t *testing.T) {
	manager, target, _ := newFixtureManager(t)
	writeFile(t, manager.RepositoryRoot, filepath.Join(target.OutputPath, "contract.generated"), "previous\n")
	manager.BeforeReplace = func(Target, string) error {
		return errors.New("replacement interrupted")
	}

	if err := manager.GenerateTargets([]Target{target}); err == nil {
		t.Fatal("interrupted replacement unexpectedly succeeded")
	}
	content, err := os.ReadFile(filepath.Join(manager.RepositoryRoot, target.OutputPath, "contract.generated"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "previous\n" {
		t.Fatalf("previous output = %q", content)
	}
}

func TestVerifyRejectsNondeterministicGeneration(t *testing.T) {
	manager, target, generations := newFixtureManager(t)
	manager.Generate = func(target Target, destination string) error {
		value := generations.Add(1)
		return writeGeneratedOutputWithContent(target, destination, string(rune('0'+value))+"\n")
	}

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

func newFixtureManager(t *testing.T) (*Manager, Target, *atomic.Int32) {
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
		"tools/protobuf/internal/registrygen/x": "registry\n",
		"tools/protobuf/generator/main.go":      "generator\n",
	} {
		writeFile(t, root, path, content)
	}
	var generations atomic.Int32
	manager := NewManager(root)
	manager.Generate = func(target Target, destination string) error {
		generations.Add(1)
		return writeGeneratedOutput(target, destination)
	}
	return manager, target, &generations
}

func writeGeneratedOutput(target Target, destination string) error {
	return writeGeneratedOutputWithContent(target, destination, "generated\n")
}

func writeGeneratedOutputWithContent(target Target, destination string, content string) error {
	path := filepath.Join(destination, target.OutputPath, "contract.generated")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
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
