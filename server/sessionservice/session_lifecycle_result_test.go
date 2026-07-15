package sessionservice

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/auth"
	"core/server/llm"
	"core/server/requestmemo"
	"core/server/session"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestSessionTransitionMapsEveryActionToTypedLifecycleResult(t *testing.T) {
	parentID := mustSessionLifecycleResultID(t, "parent-session")
	targetID := mustSessionLifecycleResultID(t, "target-session")
	service := newTestSessionLifecycleService(t.TempDir(), nil)

	tests := []struct {
		name       string
		transition serverapi.SessionTransition
		wantKind   serverapi.SessionLifecycleResultKind
		assert     func(t *testing.T, result serverapi.SessionLifecycleResult)
	}{
		{
			name:       "none stops",
			transition: serverapi.SessionTransition{Action: serverapi.SessionTransitionActionNone},
			wantKind:   serverapi.SessionLifecycleResultStop,
		},
		{
			name:       "resume selects with current auth",
			transition: serverapi.SessionTransition{Action: serverapi.SessionTransitionActionResume},
			wantKind:   serverapi.SessionLifecycleResultSelectSession,
			assert: func(t *testing.T, result serverapi.SessionLifecycleResult) {
				assertSessionLifecycleAuth(t, result, serverapi.SessionAuthPreparationKeepCurrent)
			},
		},
		{
			name: "new session launches create with parent and prompt",
			transition: serverapi.SessionTransition{
				Action:                       serverapi.SessionTransitionActionNewSession,
				InitialPrompt:                "seed prompt",
				InitialPromptHistoryRecorded: true,
				ParentSessionID:              parentID.String(),
			},
			wantKind: serverapi.SessionLifecycleResultLaunch,
			assert: func(t *testing.T, result serverapi.SessionLifecycleResult) {
				intent, preparation := requireSessionLifecycleLaunch(t, result)
				if intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
					t.Fatalf("intent kind = %q, want create new", intent.Kind())
				}
				parent, ok := intent.ParentID()
				if !ok || parent != parentID {
					t.Fatalf("parent = %q/%v, want %q", parent.String(), ok, parentID.String())
				}
				assertSessionLaunchPreparation(
					t,
					preparation,
					&serverapi.SessionInitialPromptMetadata{Text: "seed prompt", HistoryRecorded: true},
					serverapi.SessionInitialInputPolicyRestoreStoredDraft,
					"",
					false,
					serverapi.SessionAuthPreparationKeepCurrent,
				)
			},
		},
		{
			name: "open session launches target with empty input override",
			transition: serverapi.SessionTransition{
				Action:          serverapi.SessionTransitionActionOpenSession,
				TargetSessionID: targetID.String(),
				InitialInput:    "",
			},
			wantKind: serverapi.SessionLifecycleResultLaunch,
			assert: func(t *testing.T, result serverapi.SessionLifecycleResult) {
				intent, preparation := requireSessionLifecycleLaunch(t, result)
				if intent.Kind() != serverapi.SessionLaunchIntentOpenExisting {
					t.Fatalf("intent kind = %q, want open existing", intent.Kind())
				}
				target, ok := intent.SessionID()
				if !ok || target != targetID {
					t.Fatalf("target = %q/%v, want %q", target.String(), ok, targetID.String())
				}
				assertSessionLaunchPreparation(
					t,
					preparation,
					nil,
					serverapi.SessionInitialInputPolicyOverrideStoredDraft,
					"",
					true,
					serverapi.SessionAuthPreparationKeepCurrent,
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
				ClientRequestID: "transition-" + test.name,
				Transition:      test.transition,
			})
			if err != nil {
				t.Fatalf("ResolveTransition: %v", err)
			}
			if result.Kind() != test.wantKind {
				t.Fatalf("result kind = %q, want %q", result.Kind(), test.wantKind)
			}
			if test.assert != nil {
				test.assert(t, result)
			}
		})
	}
}

func TestSessionTransitionRollbackLaunchesCreatedFork(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	service := newTestSessionLifecycleService(containerDir, nil)
	result, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "rollback-result",
		SessionID:       store.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:                       serverapi.SessionTransitionActionForkRollback,
			InitialPrompt:                "edited prompt",
			InitialPromptHistoryRecorded: true,
			ForkRollbackTargetID:         rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 1)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	intent, preparation := requireSessionLifecycleLaunch(t, result)
	if intent.Kind() != serverapi.SessionLaunchIntentOpenExisting {
		t.Fatalf("intent kind = %q, want open existing", intent.Kind())
	}
	forkID, ok := intent.SessionID()
	if !ok {
		t.Fatal("rollback launch omitted fork session ID")
	}
	if forkID.String() == store.Meta().SessionID {
		t.Fatal("rollback launch targeted the parent")
	}
	if _, err := session.Open(filepath.Join(containerDir, forkID.String()), sessionServiceTestPersistence.Options()...); err != nil {
		t.Fatalf("open forked session: %v", err)
	}
	assertSessionLaunchPreparation(
		t,
		preparation,
		&serverapi.SessionInitialPromptMetadata{Text: "edited prompt", HistoryRecorded: true},
		serverapi.SessionInitialInputPolicyRestoreStoredDraft,
		"",
		false,
		serverapi.SessionAuthPreparationKeepCurrent,
	)
}

func TestSessionTransitionLogoutResultDependsOnCurrentSession(t *testing.T) {
	manager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "sk-before"},
		},
	}), nil, time.Now)
	service := newTestSessionLifecycleService(t.TempDir(), manager)
	currentID := mustSessionLifecycleResultID(t, "current-session")

	withCurrent, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "logout-current",
		SessionID:       currentID.String(),
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionLogout},
	})
	if err != nil {
		t.Fatalf("ResolveTransition with current session: %v", err)
	}
	intent, preparation := requireSessionLifecycleLaunch(t, withCurrent)
	target, ok := intent.SessionID()
	if intent.Kind() != serverapi.SessionLaunchIntentOpenExisting || !ok || target != currentID {
		t.Fatalf("logout launch intent = kind %q target %q/%v", intent.Kind(), target.String(), ok)
	}
	assertSessionLaunchPreparation(
		t,
		preparation,
		nil,
		serverapi.SessionInitialInputPolicyRestoreStoredDraft,
		"",
		false,
		serverapi.SessionAuthPreparationReauthenticate,
	)

	withoutCurrent, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "logout-no-current",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionLogout},
	})
	if err != nil {
		t.Fatalf("ResolveTransition without current session: %v", err)
	}
	if withoutCurrent.Kind() != serverapi.SessionLifecycleResultSelectSession {
		t.Fatalf("result kind = %q, want select session", withoutCurrent.Kind())
	}
	assertSessionLifecycleAuth(t, withoutCurrent, serverapi.SessionAuthPreparationReauthenticate)
}

func TestSessionTransitionMemoizationUsesTypedLifecycleResult(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	req := serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "typed-result-replay",
		Transition:      serverapi.SessionTransition{Action: serverapi.SessionTransitionActionResume},
	}
	first, err := service.ResolveTransition(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveTransition first: %v", err)
	}
	second, err := service.ResolveTransition(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveTransition replay: %v", err)
	}
	if !first.Equal(second) {
		t.Fatalf("memoized result = %+v, want %+v", second, first)
	}

	req.Transition.Action = serverapi.SessionTransitionActionNone
	if _, err := service.ResolveTransition(context.Background(), req); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("changed transition error = %v, want request ID reuse", err)
	}
}

func requireSessionLifecycleLaunch(t *testing.T, result serverapi.SessionLifecycleResult) (serverapi.SessionLaunchIntent, serverapi.SessionLaunchPreparation) {
	t.Helper()
	if result.Kind() != serverapi.SessionLifecycleResultLaunch {
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

func assertSessionLifecycleAuth(t *testing.T, result serverapi.SessionLifecycleResult, want serverapi.SessionAuthPreparation) {
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
	wantInputKind serverapi.SessionInitialInputPolicyKind,
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
	inputPolicy := preparation.InitialInputPolicy()
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
