package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/server/session"
	"core/shared/config"
)

func TestValidateRunPromptAgentRoleBlocksNonCallableRoleForKentSession(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{
		"worker": {AgentCallable: false, AgentCallableSet: true, Sources: map[string]string{"model": "file"}},
	}}

	err := validateRunPromptAgentRole(settings, "worker", true, "")
	if err == nil {
		t.Fatal("expected non-callable role to fail for Kent session")
	}
	if !errors.Is(err, errNonCallableSubagentRole) {
		t.Fatalf("error = %v, want non-callable role error", err)
	}
	if err := validateRunPromptAgentRole(settings, "worker", false, ""); err != nil {
		t.Fatalf("human/no-session role validation failed: %v", err)
	}
}

func TestValidateRunPromptAgentRoleUnknownRoleListsCallableRolesForKentSession(t *testing.T) {
	settings := config.Settings{
		Model:         "gpt-5.6-sol",
		ThinkingLevel: "medium",
		Subagents: map[string]config.SubagentRole{
			"callable":    {Settings: config.Settings{Model: "gpt-5.4-mini"}, Sources: map[string]string{"model": "file"}},
			"noncallable": {Settings: config.Settings{Model: "gpt-5.4-mini"}, Sources: map[string]string{"model": "file"}, AgentCallable: false, AgentCallableSet: true},
		},
	}

	err := validateRunPromptAgentRole(settings, "missing", true, "")
	if err == nil {
		t.Fatal("expected unknown role to fail")
	}
	if !errors.Is(err, errUnrecognizedSubagentRole) {
		t.Fatalf("error = %v, want unrecognized role error", err)
	}
	available := config.AvailableSubagentRoleNames(settings, true)
	if got, want := strings.Join(available, ","), "fast,callable"; got != want {
		t.Fatalf("kent-session available roles = %q, want %q (non-callable omitted)", got, want)
	}
}

func TestStartRunPromptClientUnknownRoleKentSessionErrorUsesCallableAvailableRoles(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	contents := strings.Join([]string{
		"[subagents.worker]",
		"model = \"gpt-5.4-mini\"",
		"",
		"[subagents.blocked]",
		"model = \"gpt-5.4-mini\"",
		"agent_callable = false",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, _, err := startRunPromptClient(context.Background(), Options{
		WorkspaceRoot:             workspace,
		WorkspaceRootExplicit:     true,
		WorkspaceContextSessionID: "session-from-env",
		AgentRole:                 "missing",
	})
	if err == nil {
		t.Fatal("expected unknown role error")
	}
	if !errors.Is(err, errUnrecognizedSubagentRole) {
		t.Fatalf("error = %v, want unrecognized role error", err)
	}
}

func TestStartRunPromptClientDefaultAliasBlocksNonCallableContextRole(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	contents := strings.Join([]string{
		"[subagents.blocked]",
		"model = \"gpt-5.4-mini\"",
		"agent_callable = false",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	registerAppWorkspace(t, cfg.WorkspaceRoot)
	parent := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err := parent.SetContinuationContext(session.ContinuationContext{AgentRole: "blocked"}); err != nil {
		t.Fatalf("SetContinuationContext: %v", err)
	}

	_, _, err := startRunPromptClient(context.Background(), Options{
		WorkspaceRoot:             cfg.WorkspaceRoot,
		WorkspaceRootExplicit:     true,
		WorkspaceContextSessionID: parent.Meta().SessionID,
		AgentRole:                 "default",
	})
	if err == nil {
		t.Fatal("expected default alias to fail from non-callable context role")
	}
	if !errors.Is(err, errNonCallableSubagentRole) {
		t.Fatalf("error = %v, want non-callable role error", err)
	}
}

func TestValidateRunPromptAgentRoleAllowsDefaultSelector(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{}}
	if err := validateRunPromptAgentRole(settings, "default", true, ""); err != nil {
		t.Fatalf("validateRunPromptAgentRole(default): %v", err)
	}
}

func TestValidateRunPromptAgentRoleRejectsRemovedDefaultAliases(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{}}
	for _, alias := range []string{"none", "self"} {
		t.Run(alias, func(t *testing.T) {
			if err := validateRunPromptAgentRole(settings, alias, true, ""); err == nil {
				t.Fatalf("expected validateRunPromptAgentRole(%q) to fail", alias)
			}
		})
	}
}

func TestValidateRunPromptAgentRoleBlocksDefaultAliasFromNonCallableContextRole(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{
		"blocked": {AgentCallable: false, AgentCallableSet: true, Sources: map[string]string{"model": "file"}},
	}}
	for _, rawRole := range []string{"", "default"} {
		t.Run(rawRole, func(t *testing.T) {
			err := validateRunPromptAgentRole(settings, rawRole, true, "blocked")
			if err == nil {
				t.Fatal("expected non-callable context role to block default invocation")
			}
			if !errors.Is(err, errNonCallableSubagentRole) {
				t.Fatalf("error = %q, want non-callable message", err.Error())
			}
		})
	}
	if err := validateRunPromptAgentRole(settings, "default", false, "blocked"); err != nil {
		t.Fatalf("human/no-session default alias should not enforce context role: %v", err)
	}
}

func TestStartupConfigRequestThreadsPersistenceRoot(t *testing.T) {
	req := startupConfigRequest(Options{ConfigRoot: "/tmp/iso-root"})
	if req.LoadOptions.ConfigRoot != "/tmp/iso-root" {
		t.Fatalf("LoadOptions.ConfigRoot = %q, want /tmp/iso-root", req.LoadOptions.ConfigRoot)
	}
}
