package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"core/cli/app/internal/worktreeui"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type worktreeCommandTestClient struct {
	listResp          serverapi.WorktreeListResponse
	listErr           error
	listCtx           context.Context
	listRequests      []serverapi.WorktreeListRequest
	selectorCtx       context.Context
	selectorResp      serverapi.WorktreeSelectorPreviewResponse
	selectorErr       error
	selectorRequests  []serverapi.WorktreeSelectorPreviewRequest
	resolveCtx        context.Context
	resolveResp       serverapi.WorktreeCreateTargetResolveResponse
	resolveErr        error
	createCtx         context.Context
	createResp        serverapi.WorktreeCreateResponse
	createErr         error
	enterRequests     []serverapi.WorktreeEnterRequest
	enterErr          error
	deleteCtx         context.Context
	deleteResp        serverapi.WorktreeDeleteResult
	deleteErr         error
	resolveRequests   []serverapi.WorktreeCreateTargetResolveRequest
	createRequests    []serverapi.WorktreeCreateRequest
	deleteRequests    []serverapi.WorktreeDeleteRequest
	reconnectFailures map[string]int
}

func (c *worktreeCommandTestClient) GetWorktreeStatus(context.Context, serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error) {
	return serverapi.WorktreeStatusResponse{}, nil
}

func (c *worktreeCommandTestClient) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *worktreeCommandTestClient) ListWorkspaceWorktrees(context.Context, serverapi.WorktreeWorkspaceListRequest) (serverapi.WorktreeWorkspaceListResponse, error) {
	return serverapi.WorktreeWorkspaceListResponse{}, errors.New("unexpected workspace worktree list request")
}

func (c *worktreeCommandTestClient) ResolveWorktreeSelector(ctx context.Context, req serverapi.WorktreeSelectorPreviewRequest) (serverapi.WorktreeSelectorPreviewResponse, error) {
	c.selectorCtx = ctx
	c.selectorRequests = append(c.selectorRequests, req)
	return c.selectorResp, c.selectorErr
}

func (c *worktreeCommandTestClient) ResolveWorktreeCreateTarget(ctx context.Context, req serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	c.resolveCtx = ctx
	c.resolveRequests = append(c.resolveRequests, req)
	if c.resolveErr != nil {
		return serverapi.WorktreeCreateTargetResolveResponse{}, c.resolveErr
	}
	if c.resolveResp.Resolution.Kind != "" {
		return c.resolveResp, nil
	}
	return serverapi.WorktreeCreateTargetResolveResponse{Resolution: serverapi.WorktreeCreateTargetResolution{Input: req.Target, Kind: serverapi.WorktreeCreateTargetResolutionKindNewBranch}}, nil
}

func (c *worktreeCommandTestClient) CreateWorktree(ctx context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	c.createCtx = ctx
	c.createRequests = append(c.createRequests, req)
	if c.consumeReconnectFailure("create") {
		return serverapi.WorktreeCreateResponse{}, serverapi.ErrRuntimeUnavailable
	}
	return c.createResp, c.createErr
}

func (c *worktreeCommandTestClient) EnterWorktree(_ context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	c.enterRequests = append(c.enterRequests, req)
	if c.consumeReconnectFailure("enter") {
		return serverapi.WorktreeScheduledAcknowledgement{}, serverapi.ErrRuntimeUnavailable
	}
	return serverapi.WorktreeScheduledAcknowledgement{OperationID: req.OperationID}, c.enterErr
}

func (c *worktreeCommandTestClient) LeaveWorktree(context.Context, serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error) {
	return serverapi.WorktreeScheduledAcknowledgement{}, nil
}

func (c *worktreeCommandTestClient) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	if c.consumeReconnectFailure("delete") {
		return serverapi.WorktreeDeleteResult{}, serverapi.ErrRuntimeUnavailable
	}
	return c.deleteResp, c.deleteErr
}

func (c *worktreeCommandTestClient) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return worktreeCommandNoopSetupSubscription{}, nil
}

type worktreeCommandNoopSetupSubscription struct{}

func (worktreeCommandNoopSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	return serverapi.WorktreeSetupEvent{}, io.EOF
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
	return newUIRuntimeClientWithReads(sessionID, reads, &reconnectRetryRuntimeControlClient{}).(*sessionRuntimeClient)
}

func newWorktreeTestModel(t *testing.T, client *worktreeCommandTestClient, opts ...UIOption) *uiModel {
	t.Helper()
	originalDebounce := worktreeCreateResolveDebounce
	worktreeCreateResolveDebounce = time.Millisecond
	t.Cleanup(func() { worktreeCreateResolveDebounce = originalDebounce })

	allOpts := []UIOption{WithUIWorktreeClient(client), WithUISessionID("session-1")}
	allOpts = append(allOpts, opts...)
	model := newProjectedTestUIModel(newWorktreeTestRuntimeClient("session-1"), nil, nil, allOpts...)
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

func worktreeStatusLine(model *uiModel) string {
	return stripANSIAndTrimRight(model.layout().renderStatusLine(120, uiThemeStyles("dark")))
}

func testMainWorktreeListResponse() serverapi.WorktreeListResponse {
	return serverapi.WorktreeListResponse{
		Target: clientui.SessionExecutionTarget{
			WorkspaceID:      "workspace-1",
			WorkspaceRoot:    "/repo",
			EffectiveWorkdir: "/repo",
		},
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, true, true, false),
		},
	}
}

func worktreeListResponseForRoots(mainRoot string, featureRoot string) serverapi.WorktreeListResponse {
	resp := serverapi.WorktreeListResponse{
		Target: clientui.SessionExecutionTarget{
			WorkspaceID:      "workspace-1",
			WorkspaceRoot:    mainRoot,
			EffectiveWorkdir: mainRoot,
		},
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", mainRoot, "main", true, featureRoot == "", true, false),
		},
	}
	if strings.TrimSpace(featureRoot) != "" {
		resp.Target.Worktree = &clientui.SessionExecutionWorktreeTarget{ID: "wt-feature", Root: featureRoot}
		resp.Target.EffectiveWorkdir = featureRoot
		resp.Worktrees[0].Projection.IsCurrent = false
		resp.Worktrees = append(resp.Worktrees, testRegisteredWorktreeListEntry(
			"wt-feature", "feature", featureRoot, "feature", false, true, true, true,
		))
	}
	return resp
}

func testLinkedWorktreeListResponse() serverapi.WorktreeListResponse {
	return serverapi.WorktreeListResponse{
		Target: clientui.SessionExecutionTarget{
			WorkspaceID:      "workspace-1",
			WorkspaceRoot:    "/repo",
			Worktree:         &clientui.SessionExecutionWorktreeTarget{ID: "wt-feature", Root: "/wt/feature-a"},
			EffectiveWorkdir: "/wt/feature-a/pkg",
		},
		Worktrees: []serverapi.WorktreeListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, false, true, false),
			testRegisteredWorktreeListEntry("wt-feature", "feature-a", "/wt/feature-a", "feature/a", false, true, true, true),
		},
	}
}

func testRegisteredWorktreeListEntry(id, name, root, branch string, main, current, managed, createdBranch bool) serverapi.WorktreeListEntry {
	branchRef := "refs/heads/" + branch
	branchName := branch
	originSessionID := "session-1"
	kent := serverapi.WorktreeKentFacts{
		WorktreeID:    id,
		CanonicalRoot: root,
		DisplayName:   name,
		Managed:       managed,
		CreatedBranch: createdBranch,
	}
	if createdBranch {
		kent.OriginSessionID = &originSessionID
	}
	return serverapi.WorktreeListEntry{
		Topology: serverapi.WorktreeTopologyEntry{
			Variant: serverapi.WorktreeTopologyVariantRegistered,
			Registered: &serverapi.WorktreeRegisteredFacts{
				Git: serverapi.WorktreeGitFacts{
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
		Projection: serverapi.WorktreeListProjection{Selector: branch, IsCurrent: current},
	}
}

func testExternalWorktreeListEntry(root string, selector string, current bool) serverapi.WorktreeListEntry {
	return serverapi.WorktreeListEntry{
		Topology: serverapi.WorktreeTopologyEntry{
			Variant: serverapi.WorktreeTopologyVariantExternal,
			External: &serverapi.WorktreeExternalFacts{
				Git: serverapi.WorktreeGitFacts{
					CanonicalRoot: root,
					HeadObject:    "deadbeef",
					Detached:      true,
					PathAvailable: true,
				},
			},
		},
		Projection: serverapi.WorktreeListProjection{
			Selector:  selector,
			IsCurrent: current,
		},
	}
}

func mustProjectWorktreeItem(t *testing.T, entry serverapi.WorktreeListEntry) worktreeui.Item {
	t.Helper()
	item, err := worktreeui.ProjectItem(entry)
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}
	return item
}
