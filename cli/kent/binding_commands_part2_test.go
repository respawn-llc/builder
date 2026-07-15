package main

import (
	"context"
	"core/shared/serverapi"
	"errors"
	"testing"
	"time"
)

func TestResolveWorkspaceBindingAppliesRPCTimeout(t *testing.T) {
	originalTimeout := bindingCommandRPCTimeout
	bindingCommandRPCTimeout = 20 * time.Millisecond
	t.Cleanup(func() { bindingCommandRPCTimeout = originalTimeout })

	stub := bindingCommandTimeoutProjectViewStub{
		resolveProjectPath: func(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
			<-ctx.Done()
			return serverapi.ProjectResolvePathResponse{}, ctx.Err()
		},
	}
	start := time.Now()
	_, err := resolveWorkspaceBinding(context.Background(), stub, "/tmp/workspace")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolveWorkspaceBinding error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("resolveWorkspaceBinding timeout took too long: %v", elapsed)
	}
}

func TestBindingCommandProjectRPCWrappersApplyTimeout(t *testing.T) {
	originalTimeout := bindingCommandRPCTimeout
	bindingCommandRPCTimeout = 20 * time.Millisecond
	t.Cleanup(func() { bindingCommandRPCTimeout = originalTimeout })

	deadlineErrAfterCancel := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	stub := bindingCommandTimeoutProjectViewStub{
		listProjects: func(ctx context.Context, req serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
			return serverapi.ProjectListResponse{}, deadlineErrAfterCancel(ctx)
		},
		createProject: func(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
			return serverapi.ProjectCreateResponse{}, deadlineErrAfterCancel(ctx)
		},
		attachWorkspace: func(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
			return serverapi.ProjectAttachWorkspaceResponse{}, deadlineErrAfterCancel(ctx)
		},
		rebindWorkspace: func(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
			return serverapi.ProjectRebindWorkspaceResponse{}, deadlineErrAfterCancel(ctx)
		},
	}

	assertDeadlineExceeded := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s error = %v, want deadline exceeded", name, err)
		}
	}

	_, err := listProjectsWithTimeout(context.Background(), stub)
	assertDeadlineExceeded("listProjectsWithTimeout", err)
	_, err = createProjectWithTimeout(context.Background(), stub, "project", "/tmp/workspace")
	assertDeadlineExceeded("createProjectWithTimeout", err)
	_, err = attachWorkspaceToProject(context.Background(), stub, "project-1", "/tmp/workspace")
	assertDeadlineExceeded("attachWorkspaceToProject", err)
	_, err = rebindWorkspaceWithTimeout(context.Background(), stub, "/tmp/old", "/tmp/new")
	assertDeadlineExceeded("rebindWorkspaceWithTimeout", err)
}

func TestRetargetSessionWorkspaceWaitsBeyondGenericBindingTimeout(t *testing.T) {
	originalTimeout := bindingCommandRPCTimeout
	bindingCommandRPCTimeout = 20 * time.Millisecond
	t.Cleanup(func() { bindingCommandRPCTimeout = originalTimeout })

	stub := bindingCommandTimeoutSessionLifecycleStub{retargetSessionWorkspace: func(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
		select {
		case <-time.After(60 * time.Millisecond):
			return serverapi.SessionRetargetWorkspaceResponse{}, nil
		case <-ctx.Done():
			return serverapi.SessionRetargetWorkspaceResponse{}, ctx.Err()
		}
	}}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := retargetSessionWorkspace(ctx, stub, "session-1", "/tmp/workspace", nil)
	if err != nil {
		t.Fatalf("retargetSessionWorkspace: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("retargetSessionWorkspace returned before maintenance wait: %v", elapsed)
	}
}
