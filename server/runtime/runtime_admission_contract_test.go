package runtime

import (
	"errors"
	"testing"
	"time"
)

func TestEnvironmentContextRequiresModel(t *testing.T) {
	t.Parallel()
	if _, err := environmentContextMessage(t.TempDir(), "", time.Unix(0, 0).UTC()); !errors.Is(err, errEnvironmentContextModelRequired) {
		t.Fatalf("environment context error = %v", err)
	}
}

func TestEngineNewRequiresModel(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	_, err := New(
		store,
		mustMaterializeTestEventLog(t, store),
		&fakeClient{},
		newTestToolRegistry(t),
		Config{},
	)
	if !errors.Is(err, ErrModelRequired) {
		t.Fatalf("engine construction error = %v", err)
	}
}
