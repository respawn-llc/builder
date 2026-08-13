package core

import (
	"context"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/chatcontext"
	"core/server/metadata"
)

func TestCoreExposesTargetLocalChatContextOwners(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if _, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, workspace); err != nil {
		t.Fatalf("register binding: %v", err)
	}
	appCore := newCoreTestApp(t, resolved.Config, auth.EmptyState())
	ctx := context.Background()

	workspaceOwner, err := appCore.WorkspaceChatContextOwnerForProjectWorkspace(
		ctx,
		appCore.ProjectID(),
		appCore.Config().WorkspaceRoot,
	)
	if err != nil {
		t.Fatalf("workspace Chat Context owner: %v", err)
	}
	if workspaceOwner == nil {
		t.Fatal("workspace Chat Context owner is nil")
	}
	if appCore.SessionChatContextOwner() == nil {
		t.Fatal("Session Chat Context owner is nil")
	}

	var _ chatcontext.WorkspaceOwner = workspaceOwner
	var _ chatcontext.SessionOwner = appCore.SessionChatContextOwner()
}
