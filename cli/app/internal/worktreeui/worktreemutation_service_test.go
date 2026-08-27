package worktreeui

import (
	"context"
	"io"
	"testing"
	"time"

	"core/shared/apicontract"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/proto"
)

type testWorktreeClient struct {
	listResp         *worktreepb.ListSuccess
	listErr          error
	listCtx          context.Context
	listRequests     []*worktreepb.ListRequest
	selectorCtx      context.Context
	selectorResp     *worktreepb.SelectorResolveSuccess
	selectorRequests []*worktreepb.SelectorResolveRequest
	resolveCtx       context.Context
	resolveResp      *worktreepb.CreateTargetResolveSuccess
	resolveRequests  []*worktreepb.CreateTargetResolveRequest
	createCtx        context.Context
	createResp       *worktreepb.CreateSuccess
	createRequests   []*worktreepb.CreateRequest
	enterCtx         context.Context
	enterResp        *worktreepb.ScheduledAcknowledgement
	enterRequests    []*worktreepb.EnterRequest
	deleteCtx        context.Context
	deleteResp       *worktreepb.DeleteSuccess
	deleteRequests   []*worktreepb.DeleteRequest
	errs             []error
}

func (c *testWorktreeClient) GetWorktreeStatus(context.Context, *worktreepb.StatusRequest) (*worktreepb.StatusSuccess, error) {
	return nil, c.nextErr()
}

func (c *testWorktreeClient) ListWorktrees(ctx context.Context, req *worktreepb.ListRequest) (*worktreepb.ListSuccess, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *testWorktreeClient) ListWorkspaceWorktrees(context.Context, *worktreepb.WorkspaceListRequest) (*worktreepb.WorkspaceListSuccess, error) {
	return nil, c.nextErr()
}

func (c *testWorktreeClient) ResolveWorktreeSelector(ctx context.Context, req *worktreepb.SelectorResolveRequest) (*worktreepb.SelectorResolveSuccess, error) {
	c.selectorCtx = ctx
	c.selectorRequests = append(c.selectorRequests, req)
	return c.selectorResp, c.nextErr()
}

func (c *testWorktreeClient) PreviewWorktreeDelete(context.Context, *worktreepb.DeletePreviewRequest) (*worktreepb.DeletePreviewSuccess, error) {
	return nil, c.nextErr()
}

func (c *testWorktreeClient) ResolveWorktreeCreateTarget(ctx context.Context, req *worktreepb.CreateTargetResolveRequest) (*worktreepb.CreateTargetResolveSuccess, error) {
	c.resolveCtx = ctx
	c.resolveRequests = append(c.resolveRequests, req)
	return c.resolveResp, c.nextErr()
}

func (c *testWorktreeClient) CreateWorktree(ctx context.Context, req *worktreepb.CreateRequest) (*worktreepb.CreateSuccess, error) {
	c.createCtx = ctx
	c.createRequests = append(c.createRequests, req)
	return c.createResp, c.nextErr()
}

func (c *testWorktreeClient) EnterWorktree(ctx context.Context, req *worktreepb.EnterRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	c.enterCtx = ctx
	c.enterRequests = append(c.enterRequests, req)
	return c.enterResp, c.nextErr()
}

func (c *testWorktreeClient) LeaveWorktree(context.Context, *worktreepb.LeaveRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	return nil, c.nextErr()
}

func (c *testWorktreeClient) DeleteWorktree(ctx context.Context, req *worktreepb.DeleteRequest) (*worktreepb.DeleteSuccess, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	return c.deleteResp, c.nextErr()
}

func (c *testWorktreeClient) SubscribeWorktreeSetup(ctx context.Context, req *worktreepb.SetupSubscribeRequest) (apicontract.WorktreeSetupSubscription, error) {
	return testNoopWorktreeSetupSubscription{}, nil
}

type testNoopWorktreeSetupSubscription struct{}

func (testNoopWorktreeSetupSubscription) Next(ctx context.Context) (*worktreepb.SetupEvent, error) {
	return nil, io.EOF
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
	client := &testWorktreeClient{listResp: &worktreepb.ListSuccess{Target: &worktreepb.SessionExecutionTarget{EffectiveWorkdir: "/repo"}}}
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
	if got.SessionId != "session-1" {
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
	if len(client.enterRequests) != 2 || !proto.Equal(client.enterRequests[0], client.enterRequests[1]) {
		t.Fatalf("enter requests = %+v, want identical retry", client.enterRequests)
	}
}

func TestCreateEnterDeletePopulateRequests(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)

	baseRef := "HEAD"
	branchName := "feature/a"
	if _, err := service.Create(&worktreepb.CreateRequest{Spec: &worktreepb.CreateSpec{BaseRef: &baseRef, CreateBranch: true, BranchName: &branchName}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := service.Enter(" feature/a "); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := service.Delete(" wt-3 ", true, worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	gotCreate := client.createRequests[0]
	_, setupIDErr := worktreecontract.ParseSetupOperationID(gotCreate.SetupOperationId)
	if setupIDErr != nil || gotCreate.SessionId != "session-1" || gotCreate.Spec.GetBranchName() != "feature/a" {
		t.Fatalf("create request = %+v", gotCreate)
	}
	if got := client.enterRequests[0]; got.OperationId != testWorktreeOperationID(t).String() || got.SessionId != "session-1" || got.Selector != "feature/a" {
		t.Fatalf("enter request = %+v", got)
	}
	if got := client.deleteRequests[0]; got.SessionId != "session-1" || got.Selector != "wt-3" || !got.ForceFolderRemoval || got.BranchCleanupPolicy != worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE {
		t.Fatalf("delete request = %+v", got)
	}
}

func TestMutationsUseDedicatedMutationContext(t *testing.T) {
	client := &testWorktreeClient{}
	service := newTestService(client)
	service.Runtime.MutationContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	}

	if _, err := service.Delete("wt-1", false, worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN); err != nil {
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

	baseRef := "HEAD"
	branchName := "feature/a"
	if _, err := service.Create(&worktreepb.CreateRequest{Spec: &worktreepb.CreateSpec{BaseRef: &baseRef, CreateBranch: true, BranchName: &branchName}}); err != nil {
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
	client := &testWorktreeClient{resolveResp: &worktreepb.CreateTargetResolveSuccess{Resolution: &worktreepb.CreateTargetResolution{Input: "main"}}}
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
	if got := client.resolveRequests[0]; got.SessionId != "session-1" || got.Target != "main" {
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
	if got := client.selectorRequests[0]; got.SessionId != "session-1" || got.Selector != "/wt/feature" {
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
