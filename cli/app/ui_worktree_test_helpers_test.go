package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	sharedclient "core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type worktreeCommandTestClient struct {
	listResp          serverapi.WorktreeListResponse
	listErr           error
	listCtx           context.Context
	listRequests      []serverapi.WorktreeListRequest
	resolveCtx        context.Context
	resolveResp       serverapi.WorktreeCreateTargetResolveResponse
	resolveErr        error
	createCtx         context.Context
	createResp        serverapi.WorktreeCreateResponse
	createErr         error
	deleteCtx         context.Context
	deleteResp        serverapi.WorktreeDeleteResponse
	deleteErr         error
	switchCtx         context.Context
	switchResp        serverapi.WorktreeSwitchResponse
	switchErr         error
	resolveRequests   []serverapi.WorktreeCreateTargetResolveRequest
	createRequests    []serverapi.WorktreeCreateRequest
	deleteRequests    []serverapi.WorktreeDeleteRequest
	switchRequests    []serverapi.WorktreeSwitchRequest
	reconnectFailures map[string]int
}

func (c *worktreeCommandTestClient) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
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

func (c *worktreeCommandTestClient) SwitchWorktree(ctx context.Context, req serverapi.WorktreeSwitchRequest) (serverapi.WorktreeSwitchResponse, error) {
	c.switchCtx = ctx
	c.switchRequests = append(c.switchRequests, req)
	if c.consumeReconnectFailure("switch") {
		return serverapi.WorktreeSwitchResponse{}, serverapi.ErrRuntimeUnavailable
	}
	return c.switchResp, c.switchErr
}

func (c *worktreeCommandTestClient) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResponse, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	if c.consumeReconnectFailure("delete") {
		return serverapi.WorktreeDeleteResponse{}, serverapi.ErrRuntimeUnavailable
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
	return newUIRuntimeClientWithReads(sessionID, reads, sharedclient.NewLoopbackRuntimeControlClient(nil)).(*sessionRuntimeClient)
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
		case worktreeListDoneMsg, worktreeCreateDoneMsg, worktreeSwitchDoneMsg, worktreeDeleteDoneMsg, worktreeCreateTargetResolveDebounceMsg, worktreeCreateTargetResolveDoneMsg:
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
		Worktrees: []serverapi.WorktreeView{{
			WorktreeID:    "wt-main",
			DisplayName:   "main",
			CanonicalRoot: "/repo",
			BranchName:    "main",
			IsMain:        true,
			IsCurrent:     true,
		}},
	}
}

func worktreeListResponseForRoots(mainRoot string, featureRoot string) serverapi.WorktreeListResponse {
	resp := serverapi.WorktreeListResponse{
		Target: clientui.SessionExecutionTarget{
			WorkspaceID:      "workspace-1",
			WorkspaceRoot:    mainRoot,
			EffectiveWorkdir: mainRoot,
		},
		Worktrees: []serverapi.WorktreeView{{
			WorktreeID:    "wt-main",
			DisplayName:   "main",
			CanonicalRoot: mainRoot,
			BranchName:    "main",
			IsMain:        true,
			IsCurrent:     featureRoot == "",
		}},
	}
	if strings.TrimSpace(featureRoot) != "" {
		resp.Target.Worktree = &clientui.SessionExecutionWorktreeTarget{ID: "wt-feature", Root: featureRoot}
		resp.Target.EffectiveWorkdir = featureRoot
		resp.Worktrees[0].IsCurrent = false
		resp.Worktrees = append(resp.Worktrees, serverapi.WorktreeView{
			WorktreeID:      "wt-feature",
			DisplayName:     "feature",
			CanonicalRoot:   featureRoot,
			BranchName:      "feature",
			IsCurrent:       true,
			Managed:         true,
			CreatedBranch:   true,
			OriginSessionID: "session-1",
		})
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
		Worktrees: []serverapi.WorktreeView{
			{
				WorktreeID:    "wt-main",
				DisplayName:   "main",
				CanonicalRoot: "/repo",
				BranchName:    "main",
				IsMain:        true,
			},
			{
				WorktreeID:      "wt-feature",
				DisplayName:     "feature-a",
				CanonicalRoot:   "/wt/feature-a",
				BranchName:      "feature/a",
				IsCurrent:       true,
				Managed:         true,
				CreatedBranch:   true,
				OriginSessionID: "session-1",
			},
		},
	}
}
