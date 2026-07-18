package remoteattach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/client"
	"core/shared/config"
)

func validateSuccessor(context.Context, *client.Remote) error { return nil }

func TestBindProjectWorkspaceDialsWorkspaceRootWithTrimmedProject(t *testing.T) {
	var gotProjectID string
	var gotWorkspaceRoot string
	next := &client.Remote{}
	bound, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		Config:            config.App{WorkspaceRoot: "/workspace"},
		ProjectID:         " project-1 ",
		WorkspaceID:       " ",
		ValidateSuccessor: validateSuccessor,
		DialWorkspaceRoot: func(_ context.Context, _ config.App, projectID string, workspaceRoot string) (*client.Remote, error) {
			gotProjectID = projectID
			gotWorkspaceRoot = workspaceRoot
			return next, nil
		},
	})
	if err != nil {
		t.Fatalf("BindProjectWorkspace: %v", err)
	}
	if bound.Remote != next {
		t.Fatal("expected bound remote from workspace-root dialer")
	}
	if gotProjectID != "project-1" {
		t.Fatalf("project id = %q, want trimmed project id", gotProjectID)
	}
	if gotWorkspaceRoot != "/workspace" {
		t.Fatalf("workspace root = %q, want config workspace root", gotWorkspaceRoot)
	}
}

func TestBindProjectWorkspaceDialsWorkspaceIDWithoutReadingUnavailableWorkspaceRoot(t *testing.T) {
	var rootDialed bool
	var gotWorkspaceID string
	unavailableRoot := filepath.Join(t.TempDir(), "server-host-only-workspace")
	if _, err := os.Stat(unavailableRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable workspace root precondition: %v", err)
	}
	next := &client.Remote{}
	bound, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		Config:            config.App{WorkspaceRoot: unavailableRoot},
		ProjectID:         "project-1",
		WorkspaceID:       " workspace-id ",
		ValidateSuccessor: validateSuccessor,
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			rootDialed = true
			return nil, nil
		},
		DialWorkspaceID: func(_ context.Context, _ config.App, _ string, workspaceID string) (*client.Remote, error) {
			gotWorkspaceID = workspaceID
			return next, nil
		},
	})
	if err != nil {
		t.Fatalf("BindProjectWorkspace: %v", err)
	}
	if bound.Remote != next {
		t.Fatal("expected bound remote from workspace-id dialer")
	}
	if rootDialed {
		t.Fatal("workspace root dialer should not be used for workspace id binding")
	}
	if gotWorkspaceID != "workspace-id" {
		t.Fatalf("workspace id = %q, want trimmed workspace id", gotWorkspaceID)
	}
}

func TestBindProjectWorkspaceRejectsMissingInputsBeforeDial(t *testing.T) {
	dialed := false
	_, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		ProjectID:         " ",
		ValidateSuccessor: validateSuccessor,
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			dialed = true
			return &client.Remote{}, nil
		},
	})
	if err == nil {
		t.Fatal("expected missing project id error")
	}
	if dialed {
		t.Fatal("dialer should not be called for missing project id")
	}
	_, err = BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{ProjectID: "project-1", ValidateSuccessor: validateSuccessor})
	if err == nil {
		t.Fatal("expected missing current remote error")
	}
}

func TestBindProjectWorkspacePinsRootOnReboundRemote(t *testing.T) {
	// The rebound remote must validate the expected persistence root just like the
	// initially attached one. The zero-value remote reports an empty root id, so a
	// non-empty required RootID must reject it on mismatch rather than handing back
	// an unpinned remote that could reconnect to a different root.
	next := &client.Remote{}
	_, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		ProjectID:         "project-1",
		RootID:            "root-iso",
		ValidateSuccessor: validateSuccessor,
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return next, nil
		},
	})
	if !errors.Is(err, client.ErrServerRootMismatch) {
		t.Fatalf("error = %v, want ErrServerRootMismatch from rebound-remote root pin", err)
	}
}

func TestBindProjectWorkspaceKeepsCurrentRemoteOpenWhenDialFails(t *testing.T) {
	dialErr := errors.New("dial failed")
	_, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		ProjectID:         "project-1",
		ValidateSuccessor: validateSuccessor,
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return nil, dialErr
		},
	})
	if !errors.Is(err, dialErr) {
		t.Fatalf("error = %v, want %v", err, dialErr)
	}
}

func TestBindProjectWorkspaceRejectsSuccessorBeforeRetiringCurrent(t *testing.T) {
	current := &client.Remote{}
	next := &client.Remote{}
	want := errors.New("startup validation failed")
	_, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:   current,
		ProjectID: "project-1",
		ValidateSuccessor: func(context.Context, *client.Remote) error {
			return want
		},
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return next, nil
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if err := current.RequireRoot(""); err != nil {
		t.Fatalf("current remote was retired after successor validation failure: %v", err)
	}
}

func TestBindProjectWorkspaceRequiresSuccessorValidatorBeforeDial(t *testing.T) {
	dialed := false
	_, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:   &client.Remote{},
		ProjectID: "project-1",
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			dialed = true
			return &client.Remote{}, nil
		},
	})
	if err == nil {
		t.Fatal("expected missing successor validator error")
	}
	if dialed {
		t.Fatal("must not dial before required successor validator is supplied")
	}
}

func TestBindProjectWorkspaceOwnedCloseClosesBoundAndOwnedServer(t *testing.T) {
	next := &client.Remote{}
	ownedCloseCalled := false
	bound, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		ProjectID:         "project-1",
		OwnsServer:        true,
		ValidateSuccessor: validateSuccessor,
		OwnedClose: func() error {
			ownedCloseCalled = true
			return nil
		},
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return next, nil
		},
	})
	if err != nil {
		t.Fatalf("BindProjectWorkspace: %v", err)
	}
	if bound.CloseFn == nil {
		t.Fatal("expected owned close fn")
	}
	if err := bound.CloseFn(); err != nil {
		t.Fatalf("CloseFn: %v", err)
	}
	if !ownedCloseCalled {
		t.Fatal("expected owned server close")
	}
}

func TestBindProjectWorkspaceDoesNotPromoteCloseFnToOwnership(t *testing.T) {
	next := &client.Remote{}
	bound, err := BindProjectWorkspace(context.Background(), ProjectWorkspaceBindingRequest{
		Current:           &client.Remote{},
		ProjectID:         "project-1",
		ValidateSuccessor: validateSuccessor,
		OwnedClose: func() error {
			return nil
		},
		DialWorkspaceRoot: func(context.Context, config.App, string, string) (*client.Remote, error) {
			return next, nil
		},
	})
	if err != nil {
		t.Fatalf("BindProjectWorkspace: %v", err)
	}
	if bound.CloseFn != nil {
		t.Fatal("expected non-owned binding to avoid owned close fn")
	}
}
