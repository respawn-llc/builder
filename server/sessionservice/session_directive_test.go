package sessionservice

import (
	"bytes"
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func requireSessionDirectiveWireEqual(t *testing.T, got serverapi.SessionDirective, want serverapi.SessionDirective) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal directive: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal expected directive: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("directive JSON = %s, want %s", gotJSON, wantJSON)
	}
}

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

func assertSessionLifecycleAuth(t *testing.T, result serverapi.SessionDirective, want serverapi.SessionAuthPreparation) {
	t.Helper()
	authPreparation, ok := result.AuthPreparation()
	if !ok || authPreparation != want {
		t.Fatalf("auth preparation = %q/%v, want %q", authPreparation, ok, want)
	}
}

func assertSessionLaunchPreparation(
	t *testing.T,
	preparation serverapi.SessionLaunchPreparation,
	wantPrompt *serverapi.SessionInitialPromptMetadata,
	wantInputKind serverapi.SessionDraftDispositionKind,
	wantOverride string,
	wantOverridePresent bool,
	wantAuth serverapi.SessionAuthPreparation,
) {
	t.Helper()
	prompt, hasPrompt := preparation.InitialPrompt()
	if wantPrompt == nil {
		if hasPrompt {
			t.Fatalf("unexpected initial prompt %+v", prompt)
		}
	} else if !hasPrompt || prompt != *wantPrompt {
		t.Fatalf("initial prompt = %+v/%v, want %+v", prompt, hasPrompt, *wantPrompt)
	}
	inputPolicy := preparation.DraftDisposition()
	if inputPolicy.Kind() != wantInputKind {
		t.Fatalf("input policy kind = %q, want %q", inputPolicy.Kind(), wantInputKind)
	}
	override, hasOverride := inputPolicy.OverrideText()
	if hasOverride != wantOverridePresent || override != wantOverride {
		t.Fatalf("input override = %q/%v, want %q/%v", override, hasOverride, wantOverride, wantOverridePresent)
	}
	if preparation.AuthPreparation() != wantAuth {
		t.Fatalf("auth preparation = %q, want %q", preparation.AuthPreparation(), wantAuth)
	}
}

func mustSessionLifecycleResultID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
