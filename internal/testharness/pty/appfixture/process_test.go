package appfixture

import (
	"path/filepath"
	"reflect"
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

func TestLifecycleProcessConfigRoundTrip(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "script.json")
	sessionID := "session-1"
	prompt := "run lifecycle scenario"
	want := LifecycleProcessConfig{
		WorkspaceRoot:   filepath.Join(t.TempDir(), "workspace"),
		PersistenceRoot: filepath.Join(t.TempDir(), "persistence"),
		ServerMode:      LifecycleServerModeLocal,
		OpeningKind:     LifecycleOpeningKindResumed,
		LocalScriptPath: &scriptPath,
		SessionID:       &sessionID,
		InitialPrompt:   &prompt,
		HookRecordPath:  filepath.Join(t.TempDir(), "hooks.jsonl"),
		HookBehavior:    LifecycleHookBehaviorSuccess,
	}
	path := filepath.Join(t.TempDir(), "lifecycle-fixture.json")
	if err := WriteLifecycleProcessConfig(path, want); err != nil {
		t.Fatalf("write lifecycle process config: %v", err)
	}
	got, err := ReadLifecycleProcessConfig(path)
	if err != nil {
		t.Fatalf("read lifecycle process config: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle process config = %#v, want %#v", got, want)
	}
}
