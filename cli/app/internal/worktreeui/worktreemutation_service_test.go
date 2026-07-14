package worktreeui

import (
	"context"
	"io"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type testWorktreeClient struct {
	listResp         serverapi.WorktreeListResponse
	listErr          error
	listCtx          context.Context
	listRequests     []serverapi.WorktreeListRequest
	selectorCtx      context.Context
	selectorResp     serverapi.WorktreeSelectorPreviewResponse
	selectorRequests []serverapi.WorktreeSelectorPreviewRequest
	resolveCtx       context.Context
	resolveResp      serverapi.WorktreeCreateTargetResolveResponse
	resolveRequests  []serverapi.WorktreeCreateTargetResolveRequest
	createCtx        context.Context
	createResp       serverapi.WorktreeCreateResponse
	createRequests   []serverapi.WorktreeCreateRequest
	enterCtx         context.Context
	enterResp        serverapi.WorktreeScheduledAcknowledgement
	enterRequests    []serverapi.WorktreeEnterRequest
	deleteCtx        context.Context
	deleteResp       serverapi.WorktreeDeleteResult
	deleteRequests   []serverapi.WorktreeDeleteRequest
	errs             []error
}

func (c *testWorktreeClient) GetWorktreeStatus(context.Context, serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	return serverapi.WorktreeStatusResponse{}, c.nextErr()
}

func (c *testWorktreeClient) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *testWorktreeClient) ListWorkspaceWorktrees(context.Context, serverapi.WorktreeWorkspaceListRequest) (serverapi.WorktreeWorkspaceListResponse, error) {
	return serverapi.WorktreeWorkspaceListResponse{}, c.nextErr()
}

func (c *testWorktreeClient) ResolveWorktreeSelector(ctx context.Context, req serverapi.WorktreeSelectorPreviewRequest) (serverapi.WorktreeSelectorPreviewResponse, error) {
	c.selectorCtx = ctx
	c.selectorRequests = append(c.selectorRequests, req)
	return c.selectorResp, c.nextErr()
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

func (c *testWorktreeClient) EnterWorktree(ctx context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	c.enterCtx = ctx
	c.enterRequests = append(c.enterRequests, req)
	return c.enterResp, c.nextErr()
}

func (c *testWorktreeClient) LeaveWorktree(context.Context, serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	return serverapi.WorktreeScheduledAcknowledgement{}, c.nextErr()
}

func (c *testWorktreeClient) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	return c.deleteResp, c.nextErr()
}

func (c *testWorktreeClient) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return testNoopWorktreeSetupSubscription{}, nil
}

type testNoopWorktreeSetupSubscription struct{}

func (testNoopWorktreeSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	return serverapi.WorktreeSetupEvent{}, io.EOF
}

func (testNoopWorktreeSetupSubscription) Close() error { return nil }

func (c *testWorktreeClient) nextErr() error {
	if len(c.errs) == 0 {
		return nil
	}
	err := c.errs[0]
	c.errs = c.errs[1:]
	return err
}

func TestListUsesSession(t *testing.T) {
	client := &testWorktreeClient{listResp: serverapi.WorktreeListResponse{Target: clientui.SessionExecutionTarget{EffectiveWorkdir: "/repo"}}}
	service := newTestService(client)

	resp, err := service.List()
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
	if got.SessionID != "session-1" {
		t.Fatalf("list request = %+v, want session", got)
	}
}

func TestMutationRetriesAfterRecoverableError(t *testing.T) {
	client := &testWorktreeClient{
		errs: []error{serverapi.ErrRuntimeUnavailable, nil},
	}
	recoverCalls := 0
	service := newTestService(client)
	service.Runtime.RecoverRuntimeConnection = func(context.Context, error, bool) error {
		recoverCalls++
		return nil
	}

	_, err := service.Enter("feature")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if recoverCalls != 1 {
		t.Fatalf("recover calls = %d, want 1", recoverCalls)
	}
	if len(client.enterRequests) != 2 || client.enterRequests[0] != client.enterRequests[1] {
		t.Fatalf("enter requests = %+v, want identical retry", client.enterRequests)
	}
}

func TestCreateEnterDeletePopulateRequests(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)

	if _, err := service.Create(serverapi.WorktreeCreateRequest{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := service.Enter(" feature/a "); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := service.Delete(" wt-3 ", true, serverapi.WorktreeBranchCleanupModeDeleteSafe); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := client.createRequests[0]; got.ClientRequestID != "request-1" || got.SessionID != "session-1" || got.BranchName != "feature/a" {
		t.Fatalf("create request = %+v", got)
	}
	if got := client.enterRequests[0]; got.OperationID != testWorktreeOperationID(t) || got.SessionID != "session-1" || got.Selector != "feature/a" {
		t.Fatalf("enter request = %+v", got)
	}
	if got := client.deleteRequests[0]; got.OperationID != testWorktreeOperationID(t) || got.SessionID != "session-1" || got.Selector != "wt-3" || !got.ForceFolderRemoval || got.BranchCleanupPolicy != serverapi.WorktreeBranchCleanupModeDeleteSafe {
		t.Fatalf("delete request = %+v", got)
	}
}

func TestMutationsUseDedicatedMutationContext(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)
	service.Runtime.MutationContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	}

	if _, err := service.Delete("wt-1", false, serverapi.WorktreeBranchCleanupModeRetain); err != nil {
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

func TestCreateDoesNotInstallFixedMutationDeadline(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)
	service.Runtime.MutationContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Millisecond)
	}

	if _, err := service.Create(serverapi.WorktreeCreateRequest{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if client.createCtx == nil {
		t.Fatal("expected create context recorded")
	}
	if _, ok := client.createCtx.Deadline(); ok {
		t.Fatal("create context has a deadline")
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

func TestResolveSelectorUsesBoundedSessionScopedRequest(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)

	if _, err := service.ResolveSelector(" /wt/feature "); err != nil {
		t.Fatalf("ResolveSelector: %v", err)
	}
	if client.selectorCtx == nil {
		t.Fatal("expected selector context recorded")
	}
	if _, ok := client.selectorCtx.Deadline(); !ok {
		t.Fatal("expected bounded selector context")
	}
	if got := client.selectorRequests[0]; got.SessionID != "session-1" || got.Selector != "/wt/feature" {
		t.Fatalf("selector request = %+v, want trimmed session-scoped selector", got)
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
		NewOperationID:     func() serverapi.WorktreeOperationID { return testWorktreeOperationID(nil) },
	}
}

func testWorktreeOperationID(t *testing.T) serverapi.WorktreeOperationID {
	id, err := serverapi.ParseWorktreeOperationID("11111111-1111-4111-8111-111111111111")
	if err != nil && t != nil {
		t.Fatalf("ParseWorktreeOperationID: %v", err)
	}
	return id
}
