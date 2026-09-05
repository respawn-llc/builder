package sessionlaunch

import (
	"testing"

	"core/server/auth"
	"core/server/session"
	"core/shared/config"
	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"
)

func TestOpenSessionUsesCurrentAgentBudgetAcrossConfigChanges(t *testing.T) {
	workspace := t.TempDir()
	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.ModelContextWindow = 400_000
	cfg.Settings.ContextCompactionThresholdTokens = 372_000
	containerDir := t.TempDir()
	store := createLaunchTestSession(t, containerDir, "evals", workspace)
	role := "pm"
	if err := store.SetContinuationContext(session.ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{Model: cfg.Settings.Model}); err != nil {
		t.Fatal(err)
	}
	intent, err := protoapi.SessionLaunchIntentToProto(serverapi.OpenExistingSessionLaunchIntent(mustSessionLaunchIntentID(t, store.Meta().SessionID)))
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []struct {
		window    int
		threshold int
	}{
		{window: 872_000, threshold: 828_400},
		{window: 400_000, threshold: 372_000},
	} {
		roleSettings := cfg.Settings
		roleSettings.Subagents = nil
		roleSettings.ModelContextWindow = budget.window
		roleSettings.ContextCompactionThresholdTokens = budget.threshold
		cfg.Settings.Subagents = map[string]config.SubagentRole{
			role: {
				Settings: roleSettings,
				Sources: map[string]string{
					"model_context_window":                "file",
					"context_compaction_threshold_tokens": "file",
				},
			},
			"code_review": {
				Settings: cfg.Settings,
				Sources:  map[string]string{"model": "file"},
			},
		}
		service := newSessionLaunchTestService(cfg, containerDir).WithAuthStateReader(&nonRefreshingAuthStateReader{
			loaded:  auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
			current: auth.State{Method: auth.Method{Type: auth.MethodOAuth}},
		})
		result, err := service.PlanSession(t.Context(), &sessionlaunchpb.SessionPlanRequest{
			Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
			Intent: intent,
		})
		if err != nil {
			t.Fatalf("open existing Session with window %d: %v", budget.window, err)
		}
		settings, err := protoapi.SessionSettingsFromProto(result.Plan.ActiveSettings)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range []config.Settings{settings, settings.Subagents["code_review"].Settings} {
			if got.ModelContextWindow != budget.window || got.ContextCompactionThresholdTokens != budget.threshold {
				t.Fatalf("effective budget = %d/%d, want %d/%d",
					got.ContextCompactionThresholdTokens, got.ModelContextWindow, budget.threshold, budget.window)
			}
		}
	}
}
