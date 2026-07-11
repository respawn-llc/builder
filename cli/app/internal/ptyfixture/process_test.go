package ptyfixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/appfixture"
)

type ptyFixtureBinaryBuild struct {
	root   *string
	binary *string
	err    error
}

const ptyFixtureBuildTimeout = 2 * time.Minute

var (
	ptyFixtureBinaryBuildOnce sync.Once
	ptyFixtureCachedBuild     ptyFixtureBinaryBuild
)

func buildPTYFixtureBinary(t *testing.T, _ context.Context) string {
	t.Helper()

	ptyFixtureBinaryBuildOnce.Do(func() {
		root, err := os.MkdirTemp("", "kent-pty-fixture-")
		if err != nil {
			ptyFixtureCachedBuild.err = fmt.Errorf("create fixture build root: %w", err)
			return
		}
		ptyFixtureCachedBuild.root = &root

		binary := filepath.Join(root, "kent-pty-fixture.test")
		ptyFixtureCachedBuild.binary = &binary

		buildCtx, cancel := context.WithTimeout(context.Background(), ptyFixtureBuildTimeout)
		defer cancel()
		ptyFixtureCachedBuild.err = pty.BuildTestBinary(buildCtx, "core/cli/app", binary)
	})

	if err := ptyFixtureCachedBuild.err; err != nil {
		t.Fatalf("build app test fixture: %v", err)
	}
	if ptyFixtureCachedBuild.binary == nil {
		t.Fatal("PTY fixture build succeeded without a binary path")
	}
	return *ptyFixtureCachedBuild.binary
}

func TestMain(m *testing.M) {
	code := m.Run()
	if err := cleanupPTYFixtureBinaryBuild(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up PTY fixture binary cache: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func cleanupPTYFixtureBinaryBuild() error {
	if ptyFixtureCachedBuild.root == nil {
		return nil
	}
	if err := os.RemoveAll(*ptyFixtureCachedBuild.root); err != nil {
		return fmt.Errorf("remove fixture build root %q: %w", *ptyFixtureCachedBuild.root, err)
	}
	return nil
}

func ptyFixtureProcessEnv(
	t *testing.T,
	root string,
	workspaceRoot string,
	persistenceRoot string,
	scriptPath string,
	observationPath string,
) string {
	t.Helper()
	configPath := filepath.Join(root, "process.json")
	if err := appfixture.WriteProcessConfig(configPath, appfixture.ProcessConfig{
		WorkspaceRoot:   workspaceRoot,
		PersistenceRoot: persistenceRoot,
		ScriptPath:      scriptPath,
		ObservationPath: observationPath,
	}); err != nil {
		t.Fatalf("write fixture process config: %v", err)
	}
	return appfixture.ProcessConfigEnvName + "=" + configPath
}
