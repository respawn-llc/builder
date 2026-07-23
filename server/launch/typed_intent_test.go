package launch

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestPlannerCreateNewIntentCreatesWithAndWithoutValidatedParent(t *testing.T) {
	t.Run("without parent", func(t *testing.T) {
		planner, containerDir, _ := newTypedIntentPlanner(t)
		plan, err := planner.PlanSession(context.Background(), SessionRequest{
			Mode:   ModeInteractive,
			Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		})
		if err != nil {
			t.Fatalf("plan create-new session: %v", err)
		}
		meta := testStoreForPlannerPlan(t, planner, plan).Meta()
		if meta.Category == nil || *meta.Category != sessioncontract.SessionCategoryMain {
			t.Fatalf("created category = %v, want main", meta.Category)
		}
		if meta.PreviousSessionID != nil || meta.ParentAgentSessionID != nil {
			t.Fatalf("provenance = previous:%v parent-agent:%v, want absent", meta.PreviousSessionID, meta.ParentAgentSessionID)
		}
		if testStoreForPlannerPlan(t, planner, plan).Dir() == containerDir {
			t.Fatal("expected a session directory below the container")
		}
	})

	t.Run("with validated parent", func(t *testing.T) {
		planner, containerDir, persistence := newTypedIntentPlanner(t)
		parent := createTypedIntentSession(t, containerDir, sessioncontract.SessionCategoryMain, persistence)
		parentID := mustTypedIntentSessionID(t, parent.Meta().SessionID)

		plan, err := planner.PlanSession(context.Background(), SessionRequest{
			Mode:   ModeInteractive,
			Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.PreviousSessionCreateOrigin(parentID)),
		})
		if err != nil {
			t.Fatalf("plan child session: %v", err)
		}
		meta := testStoreForPlannerPlan(t, planner, plan).Meta()
		if meta.PreviousSessionID == nil || *meta.PreviousSessionID != parentID {
			t.Fatalf("previous session ID = %v, want %q", meta.PreviousSessionID, parent.Meta().SessionID)
		}
	})
}

func TestPlannerOpenExistingIntentOpensTheRequestedSession(t *testing.T) {
	planner, containerDir, persistence := newTypedIntentPlanner(t)
	target := createTypedIntentSession(t, containerDir, sessioncontract.SessionCategoryMain, persistence)
	targetID := mustTypedIntentSessionID(t, target.Meta().SessionID)

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(targetID),
	})
	if err != nil {
		t.Fatalf("plan open-existing session: %v", err)
	}
	if plan.Descriptor.SessionID().String() != target.Meta().SessionID {
		t.Fatalf("opened session ID = %q, want %q", plan.Descriptor.SessionID(), target.Meta().SessionID)
	}
}

func TestTypedLaunchIntentRejectsInvalidIdentity(t *testing.T) {
	var zeroID runtimeids.SessionID
	tests := []serverapi.SessionLaunchIntent{
		serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(zeroID)),
		serverapi.OpenExistingSessionLaunchIntent(zeroID),
	}
	for _, intent := range tests {
		if err := intent.Validate(); err == nil {
			t.Fatalf("intent %#v validated with an absent identity", intent)
		}
	}
	if _, err := runtimeids.ParseSessionID("../escape"); err == nil {
		t.Fatal("path-shaped session identity validated")
	}
}

func TestPlannerOnlyInteractiveOpenExistingPromotesSubagentToMain(t *testing.T) {
	t.Run("interactive resumes as main", func(t *testing.T) {
		planner, containerDir, persistence := newTypedIntentPlanner(t)
		target := createTypedIntentSession(t, containerDir, sessioncontract.SessionCategorySubagent, persistence)
		targetID := mustTypedIntentSessionID(t, target.Meta().SessionID)

		plan, err := planner.PlanSession(context.Background(), SessionRequest{
			Mode:   ModeInteractive,
			Intent: serverapi.OpenExistingSessionLaunchIntent(targetID),
		})
		if err != nil {
			t.Fatalf("plan interactive resume: %v", err)
		}
		meta := testStoreForPlannerPlan(t, planner, plan).Meta()
		if meta.Category == nil || *meta.Category != sessioncontract.SessionCategoryMain {
			t.Fatalf("interactive resumed category = %v, want main", meta.Category)
		}
	})

	t.Run("headless resumes as subagent", func(t *testing.T) {
		planner, containerDir, persistence := newTypedIntentPlanner(t)
		target := createTypedIntentSession(t, containerDir, sessioncontract.SessionCategorySubagent, persistence)
		targetID := mustTypedIntentSessionID(t, target.Meta().SessionID)

		plan, err := planner.PlanSession(context.Background(), SessionRequest{
			Mode:   ModeHeadless,
			Intent: serverapi.OpenExistingSessionLaunchIntent(targetID),
		})
		if err != nil {
			t.Fatalf("plan headless resume: %v", err)
		}
		meta := testStoreForPlannerPlan(t, planner, plan).Meta()
		if meta.Category == nil || *meta.Category != sessioncontract.SessionCategorySubagent {
			t.Fatalf("headless resumed category = %v, want subagent", meta.Category)
		}
	})
}

func newTypedIntentPlanner(t *testing.T) (Planner, string, *sessiontest.Persistence) {
	t.Helper()
	root := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	persistence := sessiontest.NewPersistence()
	return newPersistenceBackedTestPlanner(config.App{
		WorkspaceRoot:   "/tmp/workspace-a",
		PersistenceRoot: root,
		Settings:        config.Settings{Model: "gpt-5"},
	}, containerDir, persistence), containerDir, persistence
}

func createTypedIntentSession(t *testing.T, containerDir string, category sessioncontract.SessionCategory, persistence *sessiontest.Persistence) *session.Store {
	t.Helper()
	store, err := session.Create(containerDir, "workspace-a", "/tmp/workspace-a", category, persistence.Options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return store
}

func mustTypedIntentSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse session ID %q: %v", raw, err)
	}
	return id
}

func createNewTypedIntentWithPreviousSession(t *testing.T, raw string) serverapi.SessionLaunchIntent {
	t.Helper()
	parentID := mustTypedIntentSessionID(t, raw)
	return serverapi.CreateNewSessionLaunchIntent(serverapi.PreviousSessionCreateOrigin(parentID))
}
