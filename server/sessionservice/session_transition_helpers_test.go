package sessionservice

import (
	"testing"

	"core/shared/serverapi"
)

func requireSessionLifecycleLaunch(t *testing.T, result serverapi.SessionDirective) (serverapi.SessionLaunchIntent, serverapi.SessionLaunchPreparation) {
	t.Helper()
	if result.Kind() != serverapi.SessionDirectiveLaunch {
		t.Fatalf("result kind = %q, want launch", result.Kind())
	}
	intent, ok := result.LaunchIntent()
	if !ok {
		t.Fatal("launch result omitted intent")
	}
	preparation, ok := result.LaunchPreparation()
	if !ok {
		t.Fatal("launch result omitted preparation")
	}
	return intent, preparation
}
