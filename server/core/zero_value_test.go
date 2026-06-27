package core

import (
	"context"
	"testing"

	"core/server/runtime"
	askquestion "core/server/tools"
)

func TestCoreZeroValueMethodsDoNotPanic(t *testing.T) {
	testCases := []struct {
		name string
		run  func(*Core)
	}{
		{name: "ProjectExists", run: func(c *Core) { _ = c.ProjectExists(context.Background(), "project") }},
		{name: "SessionBelongsToProject", run: func(c *Core) { _ = c.SessionBelongsToProject(context.Background(), "session", "project") }},
		{name: "SessionLaunchClientForProject", run: func(c *Core) { _, _ = c.SessionLaunchClientForProject(context.Background(), "project") }},
		{name: "SessionLaunchClientForProjectWorkspaceID", run: func(c *Core) {
			_, _ = c.SessionLaunchClientForProjectWorkspaceID(context.Background(), "project", "workspace")
		}},
		{name: "SessionLaunchClientForProjectWorkspace", run: func(c *Core) {
			_, _ = c.SessionLaunchClientForProjectWorkspace(context.Background(), "project", "workspace")
		}},
		{name: "RunPromptClientForProject", run: func(c *Core) { _, _ = c.RunPromptClientForProject(context.Background(), "project") }},
		{name: "RunPromptClientForProjectWorkspaceID", run: func(c *Core) {
			_, _ = c.RunPromptClientForProjectWorkspaceID(context.Background(), "project", "workspace")
		}},
		{name: "RunPromptClientForProjectWorkspace", run: func(c *Core) {
			_, _ = c.RunPromptClientForProjectWorkspace(context.Background(), "project", "workspace")
		}},
		{name: "Close", run: func(c *Core) { _ = c.Close() }},
		{name: "Config", run: func(c *Core) { _ = c.Config() }},
		{name: "ContainerDir", run: func(c *Core) { _ = c.ContainerDir() }},
		{name: "MetadataStore", run: func(c *Core) { _ = c.MetadataStore() }},
		{name: "OAuthOptions", run: func(c *Core) { _ = c.OAuthOptions() }},
		{name: "AuthManager", run: func(c *Core) { _ = c.AuthManager() }},
		{name: "FastModeState", run: func(c *Core) { _ = c.FastModeState() }},
		{name: "Background", run: func(c *Core) { _ = c.Background() }},
		{name: "BackgroundRouter", run: func(c *Core) { _ = c.BackgroundRouter() }},
		{name: "SessionViewClient", run: func(c *Core) { _ = c.SessionViewClient() }},
		{name: "ProjectID", run: func(c *Core) { _ = c.ProjectID() }},
		{name: "ProjectViewClient", run: func(c *Core) { _ = c.ProjectViewClient() }},
		{name: "AuthBootstrapClient", run: func(c *Core) { _ = c.AuthBootstrapClient() }},
		{name: "AuthStatusClient", run: func(c *Core) { _ = c.AuthStatusClient() }},
		{name: "AskViewClient", run: func(c *Core) { _ = c.AskViewClient() }},
		{name: "ApprovalViewClient", run: func(c *Core) { _ = c.ApprovalViewClient() }},
		{name: "ProcessViewClient", run: func(c *Core) { _ = c.ProcessViewClient() }},
		{name: "RuntimeControlClient", run: func(c *Core) { _ = c.RuntimeControlClient() }},
		{name: "PromptControlClient", run: func(c *Core) { _ = c.PromptControlClient() }},
		{name: "PromptActivityClient", run: func(c *Core) { _ = c.PromptActivityClient() }},
		{name: "ProcessControlClient", run: func(c *Core) { _ = c.ProcessControlClient() }},
		{name: "ProcessOutputClient", run: func(c *Core) { _ = c.ProcessOutputClient() }},
		{name: "SessionActivityClient", run: func(c *Core) { _ = c.SessionActivityClient() }},
		{name: "SessionLaunchClient", run: func(c *Core) { _ = c.SessionLaunchClient() }},
		{name: "SessionRuntimeClient", run: func(c *Core) { _ = c.SessionRuntimeClient() }},
		{name: "SessionLifecycleClient", run: func(c *Core) { _ = c.SessionLifecycleClient() }},
		{name: "WorktreeClient", run: func(c *Core) { _ = c.WorktreeClient() }},
		{name: "RegisterSessionStore", run: func(c *Core) { c.RegisterSessionStore(nil) }},
		{name: "ResolveSessionStore", run: func(c *Core) { _, _ = c.ResolveSessionStore("session") }},
		{name: "RegisterRuntime", run: func(c *Core) { c.RegisterRuntime("session", nil) }},
		{name: "UnregisterRuntime", run: func(c *Core) { c.UnregisterRuntime("session", nil) }},
		{name: "PublishRuntimeEvent", run: func(c *Core) { c.PublishRuntimeEvent("session", runtime.Event{}) }},
		{name: "BeginPendingPrompt", run: func(c *Core) { c.BeginPendingPrompt("session", askquestion.AskQuestionRequest{}) }},
		{name: "CompletePendingPrompt", run: func(c *Core) { c.CompletePendingPrompt("session", "request") }},
		{name: "AwaitPromptResponse", run: func(c *Core) {
			_, _ = c.AwaitPromptResponse(context.Background(), "session", askquestion.AskQuestionRequest{})
		}},
		{name: "RunPromptClient", run: func(c *Core) { _ = c.RunPromptClient() }},
	}

	for _, appCore := range []*Core{nil, {}} {
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				assertNoPanic(t, func() { tc.run(appCore) })
			})
		}
	}
}

func assertNoPanic(t *testing.T, run func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("panic: %v", recovered)
		}
	}()
	run()
}
