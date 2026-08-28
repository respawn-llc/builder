package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/worktreeui"
	"core/shared/apicontract"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type worktreeCommandTestClient struct {
	listResp          *worktreepb.ListSuccess
	listErr           error
	listCtx           context.Context
	listRequests      []*worktreepb.ListRequest
	selectorCtx       context.Context
	selectorResp      *worktreepb.SelectorResolveSuccess
	selectorErr       error
	selectorRequests  []*worktreepb.SelectorResolveRequest
	resolveCtx        context.Context
	resolveResp       *worktreepb.CreateTargetResolveSuccess
	resolveErr        error
	createCtx         context.Context
	createResp        *worktreepb.CreateSuccess
	createErr         error
	enterRequests     []*worktreepb.EnterRequest
	enterErr          error
	deleteCtx         context.Context
	deleteResp        *worktreepb.DeleteSuccess
	deleteErr         error
	resolveRequests   []*worktreepb.CreateTargetResolveRequest
	createRequests    []*worktreepb.CreateRequest
	deleteRequests    []*worktreepb.DeleteRequest
	reconnectFailures map[string]int
}

func (c *worktreeCommandTestClient) GetWorktreeStatus(context.Context, *worktreepb.StatusRequest) (*worktreepb.StatusSuccess, error) {
	return &worktreepb.StatusSuccess{}, nil
}

func (c *worktreeCommandTestClient) ListWorktrees(ctx context.Context, req *worktreepb.ListRequest) (*worktreepb.ListSuccess, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *worktreeCommandTestClient) ListWorkspaceWorktrees(context.Context, *worktreepb.WorkspaceListRequest) (*worktreepb.WorkspaceListSuccess, error) {
	return nil, errors.New("unexpected workspace worktree list request")
}

func (c *worktreeCommandTestClient) ResolveWorktreeSelector(ctx context.Context, req *worktreepb.SelectorResolveRequest) (*worktreepb.SelectorResolveSuccess, error) {
	c.selectorCtx = ctx
	c.selectorRequests = append(c.selectorRequests, req)
	return c.selectorResp, c.selectorErr
}

func (c *worktreeCommandTestClient) PreviewWorktreeDelete(context.Context, *worktreepb.DeletePreviewRequest) (*worktreepb.DeletePreviewSuccess, error) {
	return nil, errors.New("unexpected worktree delete preview request")
}

func (c *worktreeCommandTestClient) ResolveWorktreeCreateTarget(ctx context.Context, req *worktreepb.CreateTargetResolveRequest) (*worktreepb.CreateTargetResolveSuccess, error) {
	c.resolveCtx = ctx
	c.resolveRequests = append(c.resolveRequests, req)
	if c.resolveErr != nil {
		return nil, c.resolveErr
	}
	if c.resolveResp != nil && c.resolveResp.GetResolution().GetKind() != worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_UNSPECIFIED {
		return c.resolveResp, nil
	}
	return &worktreepb.CreateTargetResolveSuccess{Resolution: &worktreepb.CreateTargetResolution{Input: req.Target, Kind: worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}}, nil
}

func (c *worktreeCommandTestClient) CreateWorktree(ctx context.Context, req *worktreepb.CreateRequest) (*worktreepb.CreateSuccess, error) {
	c.createCtx = ctx
	c.createRequests = append(c.createRequests, req)
	if c.consumeReconnectFailure("create") {
		return nil, serverapi.ErrRuntimeUnavailable
	}
	return c.createResp, c.createErr
}

func (c *worktreeCommandTestClient) EnterWorktree(_ context.Context, req *worktreepb.EnterRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	c.enterRequests = append(c.enterRequests, req)
	if c.consumeReconnectFailure("enter") {
		return nil, serverapi.ErrRuntimeUnavailable
	}
	return &worktreepb.ScheduledAcknowledgement{OperationId: req.OperationId}, c.enterErr
}

func (c *worktreeCommandTestClient) LeaveWorktree(context.Context, *worktreepb.LeaveRequest) (*worktreepb.ScheduledAcknowledgement, error) {
	return &worktreepb.ScheduledAcknowledgement{}, nil
}

func (c *worktreeCommandTestClient) DeleteWorktree(ctx context.Context, req *worktreepb.DeleteRequest) (*worktreepb.DeleteSuccess, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	if c.consumeReconnectFailure("delete") {
		return nil, serverapi.ErrRuntimeUnavailable
	}
	return c.deleteResp, c.deleteErr
}

func (c *worktreeCommandTestClient) SubscribeWorktreeSetup(ctx context.Context, req *worktreepb.SetupSubscribeRequest) (apicontract.WorktreeSetupSubscription, error) {
	return worktreeCommandNoopSetupSubscription{}, nil
}

type worktreeCommandNoopSetupSubscription struct{}

func (worktreeCommandNoopSetupSubscription) Next(ctx context.Context) (*worktreepb.SetupEvent, error) {
	return nil, io.EOF
}

func (worktreeCommandNoopSetupSubscription) Close() error { return nil }

func (c *worktreeCommandTestClient) consumeReconnectFailure(kind string) bool {
	if c == nil || c.reconnectFailures == nil {
		return false
	}
	remaining := c.reconnectFailures[kind]
	if remaining <= 0 {
		return false
	}
	c.reconnectFailures[kind] = remaining - 1
	return true
}

func newWorktreeTestRuntimeClient(sessionID string) *sessionRuntimeClient {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: sessionID}}}
	return newUIRuntimeClientWithReads(sessionID, reads, &reconnectRetryRuntimeControlClient{}, nil).(*sessionRuntimeClient)
}

func newWorktreeTestModel(t *testing.T, client *worktreeCommandTestClient, opts ...UIOption) *uiModel {
	t.Helper()
	originalDebounce := worktreeCreateResolveDebounce
	worktreeCreateResolveDebounce = time.Millisecond
	t.Cleanup(func() { worktreeCreateResolveDebounce = originalDebounce })

	allOpts := []UIOption{WithUIWorktreeClient(client), WithUISessionID("session-1")}
	allOpts = append(allOpts, opts...)
	model := newProjectedTestUIModel(newWorktreeTestRuntimeClient("session-1"), allOpts...)
	if runtimeClient, ok := model.runtimeClient().(*sessionRuntimeClient); ok && strings.TrimSpace(model.sessionName) != "" {
		runtimeClient.storeMainView(clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: model.sessionID, SessionName: model.sessionName}})
	}
	return model
}

func applyWorktreeCmdMessages(t *testing.T, model *uiModel, cmd tea.Cmd) *uiModel {
	t.Helper()
	for _, msg := range collectCmdMessages(t, cmd) {
		switch msg.(type) {
		case worktreeListDoneMsg, worktreeDeleteTargetResolvedMsg, worktreeCreateDoneMsg, worktreeSwitchDoneMsg, worktreeDeleteDoneMsg, worktreeCreateTargetResolveDebounceMsg, worktreeCreateTargetResolveDoneMsg:
			next, nextCmd := model.Update(msg)
			model = next.(*uiModel)
			model = applyWorktreeCmdMessages(t, model, nextCmd)
		}
	}
	return model
}

func testMainWorktreeListResponse() *worktreepb.ListSuccess {
	return &worktreepb.ListSuccess{
		Target: &worktreepb.SessionExecutionTarget{
			WorkspaceId:      "workspace-1",
			WorkspaceRoot:    "/repo",
			EffectiveWorkdir: "/repo",
		},
		Worktrees: []*worktreepb.ListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, true, true, false),
		},
	}
}

func testLinkedWorktreeListResponse() *worktreepb.ListSuccess {
	return &worktreepb.ListSuccess{
		Target: &worktreepb.SessionExecutionTarget{
			WorkspaceId:      "workspace-1",
			WorkspaceRoot:    "/repo",
			Worktree:         &worktreepb.SessionExecutionWorktreeTarget{Id: "wt-feature", Root: "/wt/feature-a"},
			EffectiveWorkdir: "/wt/feature-a/pkg",
		},
		Worktrees: []*worktreepb.ListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, false, true, false),
			testRegisteredWorktreeListEntry("wt-feature", "feature-a", "/wt/feature-a", "feature/a", false, true, true, true),
		},
	}
}

func testRegisteredWorktreeListEntry(id, name, root, branch string, main, current, managed, createdBranch bool) *worktreepb.ListEntry {
	branchRef := "refs/heads/" + branch
	branchName := branch
	originSessionID := "session-1"
	kent := &worktreepb.KentFacts{
		WorktreeId:    id,
		CanonicalRoot: root,
		DisplayName:   name,
		Managed:       managed,
		CreatedBranch: createdBranch,
	}
	if createdBranch {
		kent.OriginSessionId = &originSessionID
	}
	return &worktreepb.ListEntry{
		Topology: &worktreepb.TopologyEntry{
			Topology: &worktreepb.TopologyEntry_Registered{
				Registered: &worktreepb.RegisteredFacts{
					Git: &worktreepb.GitFacts{
						CanonicalRoot: root,
						HeadObject:    "deadbeef",
						BranchRef:     &branchRef,
						BranchName:    &branchName,
						IsMain:        main,
						PathAvailable: true,
					},
					Kent: kent,
				},
			},
		},
		Projection: &worktreepb.ListProjection{Selector: branch, IsCurrent: current},
	}
}

func testExternalWorktreeListEntry(root string, selector string, current bool) *worktreepb.ListEntry {
	fallbackIdentity := filepath.Base(root)
	return &worktreepb.ListEntry{
		Topology: &worktreepb.TopologyEntry{
			Topology: &worktreepb.TopologyEntry_External{
				External: &worktreepb.ExternalFacts{
					Git: &worktreepb.GitFacts{
						CanonicalRoot: root,
						HeadObject:    "deadbeef",
						Detached:      true,
						PathAvailable: true,
					},
				},
			},
		},
		Projection: &worktreepb.ListProjection{
			Selector:         selector,
			IsCurrent:        current,
			FallbackIdentity: &fallbackIdentity,
		},
	}
}

func mustProjectWorktreeItem(t *testing.T, entry *worktreepb.ListEntry) worktreeui.Item {
	t.Helper()
	item, err := worktreeui.ProjectItem(entry)
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}
	return item
}
