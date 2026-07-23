package runtime

import (
	"errors"
	"testing"
	"time"

	"core/server/tools"
)

func TestEnvironmentContextRequiresModel(t *testing.T) {
	if _, err := environmentContextMessage(t.TempDir(), "", time.Unix(0, 0).UTC()); !errors.Is(err, errEnvironmentContextModelRequired) {
		t.Fatalf("environment context error = %v", err)
	}
}

func TestEngineNewRequiresModel(t *testing.T) {
	store := mustCreateTestSession(t)
	_, err := New(
		store,
		mustMaterializeTestEventLog(t, store),
		&fakeClient{},
		tools.NewRegistry(),
		Config{},
	)
	if !errors.Is(err, ErrModelRequired) {
		t.Fatalf("engine construction error = %v", err)
	}
}
