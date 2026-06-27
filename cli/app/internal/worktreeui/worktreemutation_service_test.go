package worktreeui

import (
	"context"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type testWorktreeClient struct {
	listResp        serverapi.WorktreeListResponse
	listErr         error
	listCtx         context.Context
	listRequests    []serverapi.WorktreeListRequest
	resolveCtx      context.Context
	resolveResp     serverapi.WorktreeCreateTargetResolveResponse
	resolveRequests []serverapi.WorktreeCreateTargetResolveRequest
	createCtx       context.Context
	createResp      serverapi.WorktreeCreateResponse
	createRequests  []serverapi.WorktreeCreateRequest
	switchCtx       context.Context
	switchResp      serverapi.WorktreeSwitchResponse
	switchRequests  []serverapi.WorktreeSwitchRequest
	deleteCtx       context.Context
	deleteResp      serverapi.WorktreeDeleteResponse
	deleteRequests  []serverapi.WorktreeDeleteRequest
	errs            []error
}

func (c *testWorktreeClient) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *testWorktreeClient) ResolveWorktreeCreateTarget(ctx context.Context, req serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	c.resolveCtx = ctx
	c.resolveRequests = append(c.resolveRequests, req)
	return c.resolveResp, c.nextErr()
}

func (c *testWorktreeClient) CreateWorktree(ctx context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	c.createCtx = ctx
	c.createRequests = append(c.createRequests, req)
	return c.createResp, c.nextErr()
}

func (c *testWorktreeClient) SwitchWorktree(ctx context.Context, req serverapi.WorktreeSwitchRequest) (serverapi.WorktreeSwitchResponse, error) {
	c.switchCtx = ctx
	c.switchRequests = append(c.switchRequests, req)
	return c.switchResp, c.nextErr()
}

func (c *testWorktreeClient) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResponse, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	return c.deleteResp, c.nextErr()
}

func (c *testWorktreeClient) nextErr() error {
	if len(c.errs) == 0 {
		return nil
	}
	err := c.errs[0]
	c.errs = c.errs[1:]
	return err
}

func TestListUsesSessionAndDirtyFlag(t *testing.T) {
	client := &testWorktreeClient{listResp: serverapi.WorktreeListResponse{Target: clientui.SessionExecutionTarget{EffectiveWorkdir: "/repo"}}}
	service := newTestService(client)

	resp, err := service.List(true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Target.EffectiveWorkdir != "/repo" {
		t.Fatalf("target = %+v, want /repo", resp.Target)
	}
	if client.listCtx == nil {
		t.Fatal("expected context recorded")
	}
	if _, ok := client.listCtx.Deadline(); !ok {
		t.Fatal("expected bounded control context")
	}
	if len(client.listRequests) != 1 {
		t.Fatalf("list requests = %+v, want one", client.listRequests)
	}
	got := client.listRequests[0]
	if got.SessionID != "session-1" || !got.IncludeDirtyCount {
		t.Fatalf("list request = %+v, want session/dirty", got)
	}
}

func TestMutationRetriesAfterRecoverableError(t *testing.T) {
	client := &testWorktreeClient{
		errs:       []error{serverapi.ErrRuntimeUnavailable, nil},
		switchResp: serverapi.WorktreeSwitchResponse{Worktree: serverapi.WorktreeView{WorktreeID: "wt-1"}},
	}
	recoverCalls := 0
	service := newTestService(client)
	service.Runtime.RecoverRuntimeConnection = func(context.Context, error, bool) error {
		recoverCalls++
		return nil
	}

	resp, err := service.Switch("wt-1")
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if resp.Worktree.WorktreeID != "wt-1" {
		t.Fatalf("worktree id = %q, want wt-1", resp.Worktree.WorktreeID)
	}
	if recoverCalls != 1 {
		t.Fatalf("recover calls = %d, want 1", recoverCalls)
	}
	if len(client.switchRequests) != 2 {
		t.Fatalf("switch requests = %+v, want retry", client.switchRequests)
	}
}

func TestCreateSwitchDeletePopulateRequests(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)

	if _, err := service.Create(serverapi.WorktreeCreateRequest{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := service.Switch(" wt-2 "); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if _, err := service.Delete(" wt-3 ", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := client.createRequests[0]; got.ClientRequestID != "request-1" || got.SessionID != "session-1" || got.BranchName != "feature/a" {
		t.Fatalf("create request = %+v", got)
	}
	if got := client.switchRequests[0]; got.ClientRequestID != "request-1" || got.SessionID != "session-1" || got.WorktreeID != "wt-2" {
		t.Fatalf("switch request = %+v", got)
	}
	if got := client.deleteRequests[0]; got.ClientRequestID != "request-1" || got.SessionID != "session-1" || got.WorktreeID != "wt-3" || !got.DeleteBranch {
		t.Fatalf("delete request = %+v", got)
	}
}

func TestMutationsUseDedicatedMutationContext(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)
	service.Runtime.MutationContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	}

	if _, err := service.Delete("wt-1", false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if client.deleteCtx == nil {
		t.Fatal("expected delete context recorded")
	}
	deadline, ok := client.deleteCtx.Deadline()
	if !ok {
		t.Fatal("expected delete context deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 8*time.Second {
		t.Fatalf("delete context remaining = %v, want dedicated mutation timeout", remaining)
	}
}

func TestResolveCreateTargetUsesBoundedContext(t *testing.T) {
	client := &testWorktreeClient{resolveResp: serverapi.WorktreeCreateTargetResolveResponse{Resolution: serverapi.WorktreeCreateTargetResolution{Input: "main"}}}
	service := newTestService(client)

	if _, err := service.ResolveCreateTarget("main"); err != nil {
		t.Fatalf("ResolveCreateTarget: %v", err)
	}
	if client.resolveCtx == nil {
		t.Fatal("expected resolve context recorded")
	}
	if _, ok := client.resolveCtx.Deadline(); !ok {
		t.Fatal("expected bounded resolve context")
	}
	if got := client.resolveRequests[0]; got.SessionID != "session-1" || got.Target != "main" {
		t.Fatalf("resolve request = %+v", got)
	}
}

func newTestService(client *testWorktreeClient) Service {
	return Service{
		Client:    client,
		SessionID: "session-1",
		Runtime: RuntimeControl{
			Context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Second)
			},
			RecoverRuntimeConnection: func(context.Context, error, bool) error { return nil },
		},
		NewClientRequestID: func() string { return "request-1" },
	}
}
