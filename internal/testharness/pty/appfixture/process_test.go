package appfixture

import (
	"path/filepath"
	"testing"
)

func TestProcessConfigRoundTrip(t *testing.T) {
	want := ProcessConfig{
		WorkspaceRoot:   filepath.Join(t.TempDir(), "workspace"),
		PersistenceRoot: filepath.Join(t.TempDir(), "persistence"),
		ScriptPath:      filepath.Join(t.TempDir(), "script.json"),
		ObservationPath: filepath.Join(t.TempDir(), "observations.json"),
	}
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := WriteProcessConfig(path, want); err != nil {
		t.Fatalf("write process config: %v", err)
	}
	got, err := ReadProcessConfig(path)
	if err != nil {
		t.Fatalf("read process config: %v", err)
	}
	if got != want {
		t.Fatalf("process config = %#v, want %#v", got, want)
	}
}
