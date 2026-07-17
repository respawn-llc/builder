package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func newConfigTestEnv(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	return home, workspace
}

func newConfigTestFile(t *testing.T) (string, string, string) {
	t.Helper()
	home, workspace := newConfigTestEnv(t)
	configPath := filepath.Join(home, ConfigDirName, "config.toml")
	ensureConfigTestDir(t, configPath)
	return home, workspace, configPath
}

func ensureConfigTestDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func writeConfigTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	ensureConfigTestDir(t, path)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func loadConfigTestFileApp(t *testing.T, contents string, opts LoadOptions) (string, string, App) {
	t.Helper()
	home, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, contents)
	return home, workspace, loadConfigTestApp(t, workspace, opts)
}

func loadConfigTestFileError(t *testing.T, contents string, opts LoadOptions) error {
	t.Helper()
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, contents)
	_, err := Load(workspace, opts)
	return err
}

func loadConfigTestApp(t *testing.T, workspace string, opts LoadOptions) App {
	t.Helper()
	cfg, err := Load(workspace, opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// unknownSettingsKeyReported reports whether err is an UnknownSettingsKeysError
// that names the given key among its offending keys.
func unknownSettingsKeyReported(err error, key string) bool {
	var unknownErr *UnknownSettingsKeysError
	if !errors.As(err, &unknownErr) {
		return false
	}
	return slices.Contains(unknownErr.Keys, key)
}

func assertConfigSource(t *testing.T, cfg App, key string, want string) {
	t.Helper()
	if got := cfg.Source.Sources[key]; got != want {
		t.Fatalf("expected %s source %s, got %q", key, want, got)
	}
}

type configPrecedenceCase[T comparable] struct {
	fileContents string
	sourceKey    string
	fileWant     T
	envName      string
	envValue     string
	envWant      T
	read         func(Settings) T
}

func assertConfigPrecedence[T comparable](t *testing.T, tc configPrecedenceCase[T]) string {
	t.Helper()
	_, workspace, cfg := loadConfigTestFileApp(t, tc.fileContents, LoadOptions{})
	if got := tc.read(cfg.Settings); got != tc.fileWant {
		t.Fatalf("expected file %s=%v, got %v", tc.sourceKey, tc.fileWant, got)
	}
	assertConfigSource(t, cfg, tc.sourceKey, "file")

	t.Setenv(tc.envName, tc.envValue)
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if got := tc.read(cfg.Settings); got != tc.envWant {
		t.Fatalf("expected env %s=%v, got %v", tc.sourceKey, tc.envWant, got)
	}
	assertConfigSource(t, cfg, tc.sourceKey, "env")
	return workspace
}

func assertConfigEnvRejected(t *testing.T, workspace string, envName string, envValue string) {
	t.Helper()
	t.Setenv(envName, envValue)
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatalf("expected invalid %s=%q to be rejected", envName, envValue)
	}
}
