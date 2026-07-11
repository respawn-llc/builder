package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/cli/app/internal/startupconfig"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
)

func testRunPromptCaller(kentSession bool, role *string) startupconfig.CallerContext {
	if !kentSession {
		return startupconfig.CallerContext{Kind: startupconfig.CallerKindHuman}
	}
	return startupconfig.CallerContext{
		Kind:      startupconfig.CallerKindKentSession,
		AgentRole: role,
	}
}

func testWorkflowRunPromptCaller(role *string) startupconfig.CallerContext {
	return startupconfig.CallerContext{
		Kind:            startupconfig.CallerKindKentSession,
		WorkflowSession: true,
		AgentRole:       role,
	}
}

func TestValidateRunPromptAgentRoleWorkflowCallability(t *testing.T) {
	worker := config.SubagentRole{Sources: map[string]string{"model": "file"}}
	tests := []struct {
		name     string
		settings config.Settings
		rawRole  string
		caller   startupconfig.CallerContext
		wantErr  error
	}{
		{
			name:     "workflow default denies explicit custom target",
			settings: config.Settings{Subagents: map[string]config.SubagentRole{"worker": worker}},
			rawRole:  "worker",
			caller:   testWorkflowRunPromptCaller(nil),
			wantErr:  errNonCallableSubagentRole,
		},
		{
			name:     "workflow global enablement permits custom target",
			settings: config.Settings{Workflow: config.WorkflowSettings{Subagents: true}, Subagents: map[string]config.SubagentRole{"worker": worker}},
			rawRole:  "worker",
			caller:   testWorkflowRunPromptCaller(nil),
		},
		{
			name: "workflow role metadata denies custom target",
			settings: config.Settings{
				Workflow: config.WorkflowSettings{Subagents: true},
				Subagents: map[string]config.SubagentRole{
					"worker": {Sources: map[string]string{"model": "file"}, WorkflowSubagent: false, WorkflowSubagentSet: true},
				},
			},
			rawRole: "worker",
			caller:  testWorkflowRunPromptCaller(nil),
			wantErr: errNonCallableSubagentRole,
		},
		{
			name:     "ordinary Kent session preserves custom target",
			settings: config.Settings{Subagents: map[string]config.SubagentRole{"worker": worker}},
			rawRole:  "worker",
			caller:   testRunPromptCaller(true, nil),
		},
		{
			name:     "human shell preserves custom target",
			settings: config.Settings{Subagents: map[string]config.SubagentRole{"worker": worker}},
			rawRole:  "worker",
			caller:   testRunPromptCaller(false, nil),
		},
		{
			name:     "workflow default caller role is absent and valid",
			settings: config.Settings{Subagents: map[string]config.SubagentRole{"worker": worker}},
			rawRole:  "default",
			caller:   testWorkflowRunPromptCaller(nil),
		},
		{
			name: "agent callable remains independently denied",
			settings: config.Settings{
				Workflow: config.WorkflowSettings{Subagents: true},
				Subagents: map[string]config.SubagentRole{
					"worker": {Sources: map[string]string{"model": "file"}, AgentCallable: false, AgentCallableSet: true},
				},
			},
			rawRole: "worker",
			caller:  testWorkflowRunPromptCaller(nil),
			wantErr: errNonCallableSubagentRole,
		},
		{
			name:     "fast caller bypasses workflow switches",
			settings: config.Settings{},
			caller:   testWorkflowRunPromptCaller(sessiontest.AgentRole(config.BuiltInSubagentRoleFast)),
		},
		{
			name: "fast caller still obeys agent callable",
			settings: config.Settings{Subagents: map[string]config.SubagentRole{
				config.BuiltInSubagentRoleFast: {AgentCallable: false, AgentCallableSet: true},
			}},
			caller:  testWorkflowRunPromptCaller(sessiontest.AgentRole(config.BuiltInSubagentRoleFast)),
			wantErr: errNonCallableSubagentRole,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRunPromptAgentRole(tt.settings, tt.rawRole, tt.caller)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("validateRunPromptAgentRole: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRunPromptAgentRoleRejectsUnknownCallerContext(t *testing.T) {
	err := validateRunPromptAgentRole(config.Settings{}, "default", startupconfig.CallerContext{})
	if err == nil {
		t.Fatal("expected unknown caller context to fail")
	}
}

func TestResolveRunPromptWorkspaceConfigPreservesCallerSessionIdentity(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	registerAppWorkspace(t, cfg.WorkspaceRoot)
	tests := []struct {
		name     string
		role     *string
		workflow bool
	}{
		{name: "ordinary custom role", role: sessiontest.AgentRole("worker")},
		{name: "workflow custom role", role: sessiontest.AgentRole("worker"), workflow: true},
		{name: "fast role", role: sessiontest.AgentRole(config.BuiltInSubagentRoleFast), workflow: true},
		{name: "default agent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
			if tt.role != nil {
				if err := parent.SetContinuationContext(session.ContinuationContext{AgentRole: tt.role}); err != nil {
					t.Fatalf("SetContinuationContext: %v", err)
				}
			}
			if tt.workflow {
				if err := parent.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
					t.Fatalf("SetWorkflowSessionState: %v", err)
				}
			}
			resolved, err := resolveRunPromptWorkspaceConfig(Options{
				WorkspaceRoot:             cfg.WorkspaceRoot,
				WorkspaceRootExplicit:     true,
				WorkspaceContextSessionID: parent.Meta().SessionID,
			})
			if err != nil {
				t.Fatalf("resolveRunPromptWorkspaceConfig: %v", err)
			}
			caller := resolved.CallerContext
			if caller.Kind != startupconfig.CallerKindKentSession {
				t.Fatalf("caller kind = %q, want Kent session", caller.Kind)
			}
			if caller.WorkflowSession != tt.workflow {
				t.Fatalf("workflow session = %t, want %t", caller.WorkflowSession, tt.workflow)
			}
			if !sessiontest.SameAgentRole(caller.AgentRole, tt.role) {
				t.Fatalf("agent role = %v, want %v", caller.AgentRole, tt.role)
			}
		})
	}
}

func TestValidateRunPromptAgentRoleBlocksNonCallableRoleForKentSession(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{
		"worker": {AgentCallable: false, AgentCallableSet: true, Sources: map[string]string{"model": "file"}},
	}}

	err := validateRunPromptAgentRole(settings, "worker", testRunPromptCaller(true, nil))
	if err == nil {
		t.Fatal("expected non-callable role to fail for Kent session")
	}
	if !errors.Is(err, errNonCallableSubagentRole) {
		t.Fatalf("error = %v, want non-callable role error", err)
	}
	if err := validateRunPromptAgentRole(settings, "worker", testRunPromptCaller(false, nil)); err != nil {
		t.Fatalf("human/no-session role validation failed: %v", err)
	}
}

func TestValidateRunPromptAgentRoleUnknownRoleListsCallableRolesForKentSession(t *testing.T) {
	settings := config.Settings{
		Model:         "gpt-5.5",
		ThinkingLevel: "medium",
		Subagents: map[string]config.SubagentRole{
			"callable":    {Settings: config.Settings{Model: "gpt-5.4-mini"}, Sources: map[string]string{"model": "file"}},
			"noncallable": {Settings: config.Settings{Model: "gpt-5.4-mini"}, Sources: map[string]string{"model": "file"}, AgentCallable: false, AgentCallableSet: true},
		},
	}

	err := validateRunPromptAgentRole(settings, "missing", testRunPromptCaller(true, nil))
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
	if !errors.Is(err, startupconfig.ErrWorkspaceContextSessionMissing) {
		t.Fatalf("error = %v, want missing workspace context session error", err)
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
	if err := parent.SetContinuationContext(session.ContinuationContext{AgentRole: sessiontest.AgentRole("blocked")}); err != nil {
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

func TestStartRunPromptClientRejectsWorkflowParentHiddenTargetBeforeDial(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"[subagents.worker]",
		"model = \"gpt-5.4-mini\"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	registerAppWorkspace(t, cfg.WorkspaceRoot)
	parent := createAuthoritativeAppSession(t, cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err := parent.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}

	_, _, err := startRunPromptClient(context.Background(), Options{
		WorkspaceRoot:             cfg.WorkspaceRoot,
		WorkspaceRootExplicit:     true,
		WorkspaceContextSessionID: parent.Meta().SessionID,
		AgentRole:                 "worker",
	})
	if !errors.Is(err, errNonCallableSubagentRole) {
		t.Fatalf("startRunPromptClient error = %v, want non-callable role", err)
	}
}

func TestValidateRunPromptAgentRoleAllowsDefaultSelector(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{}}
	if err := validateRunPromptAgentRole(settings, "default", testRunPromptCaller(true, nil)); err != nil {
		t.Fatalf("validateRunPromptAgentRole(default): %v", err)
	}
}

func TestValidateRunPromptAgentRoleRejectsRemovedDefaultAliases(t *testing.T) {
	settings := config.Settings{Subagents: map[string]config.SubagentRole{}}
	for _, alias := range []string{"none", "self"} {
		t.Run(alias, func(t *testing.T) {
			if err := validateRunPromptAgentRole(settings, alias, testRunPromptCaller(true, nil)); err == nil {
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
			err := validateRunPromptAgentRole(settings, rawRole, testRunPromptCaller(true, sessiontest.AgentRole("blocked")))
			if err == nil {
				t.Fatal("expected non-callable context role to block default invocation")
			}
			if !errors.Is(err, errNonCallableSubagentRole) {
				t.Fatalf("error = %q, want non-callable message", err.Error())
			}
		})
	}
	if err := validateRunPromptAgentRole(settings, "default", testRunPromptCaller(false, sessiontest.AgentRole("blocked"))); err != nil {
		t.Fatalf("human/no-session default alias should not enforce context role: %v", err)
	}
}

func TestStartupConfigRequestThreadsPersistenceRoot(t *testing.T) {
	req := startupConfigRequest(Options{ConfigRoot: "/tmp/iso-root"})
	if req.LoadOptions.ConfigRoot != "/tmp/iso-root" {
		t.Fatalf("LoadOptions.ConfigRoot = %q, want /tmp/iso-root", req.LoadOptions.ConfigRoot)
	}
}
