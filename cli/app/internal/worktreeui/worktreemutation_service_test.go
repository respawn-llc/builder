package worktreeui

import (
	"context"
	"io"
	"testing"
	"time"

	"core/shared/serverapi"
	"core/shared/worktreecontract"
)

type testWorktreeClient struct {
	listResp         worktreecontract.ListResponse
	listErr          error
	listCtx          context.Context
	listRequests     []worktreecontract.ListRequest
	selectorCtx      context.Context
	selectorResp     worktreecontract.SelectorResolveResponse
	selectorRequests []worktreecontract.SelectorResolveRequest
	resolveCtx       context.Context
	resolveResp      worktreecontract.CreateTargetResolveResponse
	resolveRequests  []worktreecontract.CreateTargetResolveRequest
	createCtx        context.Context
	createResp       worktreecontract.CreateResponse
	createRequests   []worktreecontract.CreateRequest
	enterCtx         context.Context
	enterResp        worktreecontract.ScheduledAcknowledgement
	enterRequests    []worktreecontract.EnterRequest
	deleteCtx        context.Context
	deleteResp       worktreecontract.DeleteResult
	deleteRequests   []worktreecontract.DeleteRequest
	errs             []error
}

func (c *testWorktreeClient) GetWorktreeStatus(context.Context, worktreecontract.StatusRequest) (worktreecontract.StatusResponse, error) {
	return worktreecontract.StatusResponse{}, c.nextErr()
}

func (c *testWorktreeClient) ListWorktrees(ctx context.Context, req worktreecontract.ListRequest) (worktreecontract.ListResponse, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *testWorktreeClient) ListWorkspaceWorktrees(context.Context, worktreecontract.WorkspaceListRequest) (worktreecontract.WorkspaceListResponse, error) {
	return worktreecontract.WorkspaceListResponse{}, c.nextErr()
}

func (c *testWorktreeClient) ResolveWorktreeSelector(ctx context.Context, req worktreecontract.SelectorResolveRequest) (worktreecontract.SelectorResolveResponse, error) {
	c.selectorCtx = ctx
	c.selectorRequests = append(c.selectorRequests, req)
	return c.selectorResp, c.nextErr()
}

func (c *testWorktreeClient) PreviewWorktreeDelete(context.Context, worktreecontract.DeletePreviewRequest) (worktreecontract.DeletePreviewResponse, error) {
	return worktreecontract.DeletePreviewResponse{}, c.nextErr()
}

func (c *testWorktreeClient) ResolveWorktreeCreateTarget(ctx context.Context, req worktreecontract.CreateTargetResolveRequest) (worktreecontract.CreateTargetResolveResponse, error) {
	c.resolveCtx = ctx
	c.resolveRequests = append(c.resolveRequests, req)
	return c.resolveResp, c.nextErr()
}

func (c *testWorktreeClient) CreateWorktree(ctx context.Context, req worktreecontract.CreateRequest) (worktreecontract.CreateResponse, error) {
	c.createCtx = ctx
	c.createRequests = append(c.createRequests, req)
	return c.createResp, c.nextErr()
}

func (c *testWorktreeClient) EnterWorktree(ctx context.Context, req worktreecontract.EnterRequest) (worktreecontract.ScheduledAcknowledgement, error) {
	c.enterCtx = ctx
	c.enterRequests = append(c.enterRequests, req)
	return c.enterResp, c.nextErr()
}

func (c *testWorktreeClient) LeaveWorktree(context.Context, worktreecontract.LeaveRequest) (worktreecontract.ScheduledAcknowledgement, error) {
	return worktreecontract.ScheduledAcknowledgement{}, c.nextErr()
}

func (c *testWorktreeClient) DeleteWorktree(ctx context.Context, req worktreecontract.DeleteRequest) (worktreecontract.DeleteResult, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	return c.deleteResp, c.nextErr()
}

func (c *testWorktreeClient) SubscribeWorktreeSetup(ctx context.Context, req worktreecontract.SetupSubscribeRequest) (worktreecontract.SetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return testNoopWorktreeSetupSubscription{}, nil
}

type testNoopWorktreeSetupSubscription struct{}

func (testNoopWorktreeSetupSubscription) Next(ctx context.Context) (worktreecontract.SetupEvent, error) {
	return worktreecontract.SetupEvent{}, io.EOF
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
	client := &testWorktreeClient{listResp: worktreecontract.ListResponse{Target: worktreecontract.SessionExecutionTarget{EffectiveWorkdir: "/repo"}}}
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

	if _, err := service.Create(worktreecontract.CreateRequest{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := service.Enter(" feature/a "); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := service.Delete(" wt-3 ", true, worktreecontract.BranchCleanupModeDeleteSafe); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := client.createRequests[0]; got.SetupOperationID.Validate() != nil || got.SessionID != "session-1" || got.BranchName != "feature/a" {
		t.Fatalf("create request = %+v", got)
	}
	if got := client.enterRequests[0]; got.OperationID != testWorktreeOperationID(t) || got.SessionID != "session-1" || got.Selector != "feature/a" {
		t.Fatalf("enter request = %+v", got)
	}
	if got := client.deleteRequests[0]; got.OperationID != testWorktreeOperationID(t) || got.SessionID != "session-1" || got.Selector != "wt-3" || !got.ForceFolderRemoval || got.BranchCleanupPolicy != worktreecontract.BranchCleanupModeDeleteSafe {
		t.Fatalf("delete request = %+v", got)
	}
}

func TestMutationsUseDedicatedMutationContext(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)
	service.Runtime.MutationContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	}

	if _, err := service.Delete("wt-1", false, worktreecontract.BranchCleanupModeRetain); err != nil {
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

	if _, err := service.Create(worktreecontract.CreateRequest{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/a"}); err != nil {
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
	client := &testWorktreeClient{resolveResp: worktreecontract.CreateTargetResolveResponse{Resolution: worktreecontract.CreateTargetResolution{Input: "main"}}}
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
		NewOperationID: func() worktreecontract.OperationID { return testWorktreeOperationID(nil) },
	}
}

func testWorktreeOperationID(t *testing.T) worktreecontract.OperationID {
	id, err := worktreecontract.ParseOperationID("11111111-1111-4111-8111-111111111111")
	if err != nil && t != nil {
		t.Fatalf("ParseWorktreeOperationID: %v", err)
	}
	return id
}
