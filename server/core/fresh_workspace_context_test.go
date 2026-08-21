package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/shared/config"
	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"
)

func TestCoreWorkspaceChatContextAndFollowingPlanReloadPrimaryAndSecondaryFromStartupRoot(t *testing.T) {
	configRoot := t.TempDir()
	primary := t.TempDir()
	secondary := t.TempDir()
	writeCoreContextConfig(t, configRoot, 120_000, 90_000, config.CompactionModeLocal)
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: primary,
		LoadOptions:   config.LoadOptions{ConfigRoot: configRoot},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	bindingPrimary, err := metadata.RegisterBinding(t.Context(), configRoot, primary)
	if err != nil {
		t.Fatalf("RegisterBinding primary: %v", err)
	}
	bindingSecondary, err := metadata.RegisterBinding(t.Context(), configRoot, secondary)
	if err != nil {
		t.Fatalf("RegisterBinding secondary: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())

	for _, test := range []struct {
		name      string
		binding   metadata.Binding
		root      string
		window    int
		threshold int
	}{
		{name: "primary", binding: bindingPrimary, root: primary, window: 140_000, threshold: 100_000},
		{name: "secondary", binding: bindingSecondary, root: secondary, window: 80_000, threshold: 60_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeCoreWorkspaceContextConfig(t, test.root, test.window, test.threshold, config.CompactionModeNone)
			owner, err := appCore.WorkspaceChatContextOwnerForProjectWorkspace(
				t.Context(),
				test.binding.ProjectID,
				test.root,
			)
			if err != nil {
				t.Fatalf("WorkspaceChatContextOwnerForProjectWorkspace: %v", err)
			}
			contextFacts, err := owner.ReadWorkspaceChatContext(t.Context())
			if err != nil {
				t.Fatalf("ReadWorkspaceChatContext: %v", err)
			}
			if contextFacts.ContextWindowTokens != int64(test.window) ||
				contextFacts.AutomaticThresholdTokens != int64(test.threshold) ||
				contextFacts.CompactionMode != serverapi.ChatContextCompactionModeDisabled {
				t.Fatalf("fresh Context = %+v", contextFacts)
			}

			client, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), test.binding.ProjectID, test.root)
			if err != nil {
				t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
			}
			intent, err := protoapi.SessionLaunchIntentToProto(
				serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
			)
			if err != nil {
				t.Fatalf("convert Session launch intent: %v", err)
			}
			plan, err := client.PlanSession(context.Background(), &sessionlaunchpb.SessionPlanRequest{
				Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_HEADLESS,
				Intent: intent,
			})
			if err != nil {
				t.Fatalf("PlanSession: %v", err)
			}
			if plan.Plan.ActiveSettings.ModelContextWindow != int32(test.window) ||
				plan.Plan.ActiveSettings.ContextCompactionThresholdTokens != int32(test.threshold) ||
				plan.Plan.ActiveSettings.CompactionMode != sessionlaunchpb.CompactionMode_COMPACTION_MODE_NONE {
				t.Fatalf("following plan settings = %+v", plan.Plan.ActiveSettings)
			}
		})
	}
}

func writeCoreContextConfig(t *testing.T, root string, window int, threshold int, mode config.CompactionMode) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(root, "config.toml"),
		[]byte(coreContextConfigText(window, threshold, mode)),
		0o600,
	); err != nil {
		t.Fatalf("write root config: %v", err)
	}
}

func writeCoreWorkspaceContextConfig(t *testing.T, workspace string, window int, threshold int, mode config.CompactionMode) {
	t.Helper()
	dir := filepath.Join(workspace, ".kent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create workspace config dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "config.toml"),
		[]byte(coreContextConfigText(window, threshold, mode)),
		0o600,
	); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
}

func coreContextConfigText(window int, threshold int, mode config.CompactionMode) string {
	return "model = \"gpt-5.6-sol\"\n" +
		"model_context_window = " + formatCoreContextInt(window) + "\n" +
		"context_compaction_threshold_tokens = " + formatCoreContextInt(threshold) + "\n" +
		"pre_submit_compaction_lead_tokens = 10000\n" +
		"compaction_mode = \"" + string(mode) + "\"\n"
}

func formatCoreContextInt(value int) string {
	return fmt.Sprintf("%d", value)
}
