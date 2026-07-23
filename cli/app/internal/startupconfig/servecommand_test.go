package startupconfig

import (
	"strings"
	"testing"

	"core/shared/config"
)

func TestExecutablePathSkipsTestBinary(t *testing.T) {
	path, ok := ServeExecutablePath()
	if ok {
		t.Fatalf("expected test binary to be skipped, got %q", path)
	}
}

func TestArgsBuildServeCommand(t *testing.T) {
	args := ServeArgs()
	if len(args) != 1 || args[0] != "serve" {
		t.Fatalf("args = %#v, want [serve]", args)
	}
}

func TestEnvIncludesConfiguredServerSettings(t *testing.T) {
	env := ServeEnv(config.App{
		PersistenceRoot: "/tmp/test-persist",
		Settings: config.Settings{
			ServerHost: "127.0.0.1",
			ServerPort: 4567,
		},
	})
	if !containsEnv(env, "KENT_PERSISTENCE_ROOT=/tmp/test-persist") {
		t.Fatalf("env missing persistence root: %#v", env)
	}
	if !containsEnv(env, "KENT_SERVER_HOST=127.0.0.1") {
		t.Fatalf("env missing server host: %#v", env)
	}
	if !containsEnv(env, "KENT_SERVER_PORT=4567") {
		t.Fatalf("env missing server port: %#v", env)
	}
}

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if strings.TrimSpace(entry) == want {
			return true
		}
	}
	return false
}
