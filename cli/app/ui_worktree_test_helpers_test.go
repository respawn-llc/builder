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
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	tea "github.com/charmbracelet/bubbletea"
)

type worktreeCommandTestClient struct {
	listResp          worktreecontract.ListResponse
	listErr           error
	listCtx           context.Context
	listRequests      []worktreecontract.ListRequest
	selectorCtx       context.Context
	selectorResp      worktreecontract.SelectorResolveResponse
	selectorErr       error
	selectorRequests  []worktreecontract.SelectorResolveRequest
	resolveCtx        context.Context
	resolveResp       worktreecontract.CreateTargetResolveResponse
	resolveErr        error
	createCtx         context.Context
	createResp        worktreecontract.CreateResponse
	createErr         error
	enterRequests     []worktreecontract.EnterRequest
	enterErr          error
	deleteCtx         context.Context
	deleteResp        worktreecontract.DeleteResult
	deleteErr         error
	resolveRequests   []worktreecontract.CreateTargetResolveRequest
	createRequests    []worktreecontract.CreateRequest
	deleteRequests    []worktreecontract.DeleteRequest
	reconnectFailures map[string]int
}

func (c *worktreeCommandTestClient) GetWorktreeStatus(context.Context, worktreecontract.StatusRequest) (worktreecontract.StatusResponse, error) {
	return worktreecontract.StatusResponse{}, nil
}

func (c *worktreeCommandTestClient) ListWorktrees(ctx context.Context, req worktreecontract.ListRequest) (worktreecontract.ListResponse, error) {
	c.listCtx = ctx
	c.listRequests = append(c.listRequests, req)
	return c.listResp, c.listErr
}

func (c *worktreeCommandTestClient) ListWorkspaceWorktrees(context.Context, worktreecontract.WorkspaceListRequest) (worktreecontract.WorkspaceListResponse, error) {
	return worktreecontract.WorkspaceListResponse{}, errors.New("unexpected workspace worktree list request")
}

func (c *worktreeCommandTestClient) ResolveWorktreeSelector(ctx context.Context, req worktreecontract.SelectorResolveRequest) (worktreecontract.SelectorResolveResponse, error) {
	c.selectorCtx = ctx
	c.selectorRequests = append(c.selectorRequests, req)
	return c.selectorResp, c.selectorErr
}

func (c *worktreeCommandTestClient) PreviewWorktreeDelete(context.Context, worktreecontract.DeletePreviewRequest) (worktreecontract.DeletePreviewResponse, error) {
	return worktreecontract.DeletePreviewResponse{}, errors.New("unexpected worktree delete preview request")
}

func (c *worktreeCommandTestClient) ResolveWorktreeCreateTarget(ctx context.Context, req worktreecontract.CreateTargetResolveRequest) (worktreecontract.CreateTargetResolveResponse, error) {
	c.resolveCtx = ctx
	c.resolveRequests = append(c.resolveRequests, req)
	if c.resolveErr != nil {
		return worktreecontract.CreateTargetResolveResponse{}, c.resolveErr
	}
	if c.resolveResp.Resolution.Kind != "" {
		return c.resolveResp, nil
	}
	return worktreecontract.CreateTargetResolveResponse{Resolution: worktreecontract.CreateTargetResolution{Input: req.Target, Kind: worktreecontract.CreateTargetResolutionKindNewBranch}}, nil
}

func (c *worktreeCommandTestClient) CreateWorktree(ctx context.Context, req worktreecontract.CreateRequest) (worktreecontract.CreateResponse, error) {
	c.createCtx = ctx
	c.createRequests = append(c.createRequests, req)
	if c.consumeReconnectFailure("create") {
		return worktreecontract.CreateResponse{}, serverapi.ErrRuntimeUnavailable
	}
	return c.createResp, c.createErr
}

func (c *worktreeCommandTestClient) EnterWorktree(_ context.Context, req worktreecontract.EnterRequest) (worktreecontract.ScheduledAcknowledgement, error) {
	c.enterRequests = append(c.enterRequests, req)
	if c.consumeReconnectFailure("enter") {
		return worktreecontract.ScheduledAcknowledgement{}, serverapi.ErrRuntimeUnavailable
	}
	return worktreecontract.ScheduledAcknowledgement{OperationID: req.OperationID}, c.enterErr
}

func (c *worktreeCommandTestClient) LeaveWorktree(context.Context, worktreecontract.LeaveRequest) (worktreecontract.ScheduledAcknowledgement, error) {
	return worktreecontract.ScheduledAcknowledgement{}, nil
}

func (c *worktreeCommandTestClient) DeleteWorktree(ctx context.Context, req worktreecontract.DeleteRequest) (worktreecontract.DeleteResult, error) {
	c.deleteCtx = ctx
	c.deleteRequests = append(c.deleteRequests, req)
	if c.consumeReconnectFailure("delete") {
		return worktreecontract.DeleteResult{}, serverapi.ErrRuntimeUnavailable
	}
	return c.deleteResp, c.deleteErr
}

func (c *worktreeCommandTestClient) SubscribeWorktreeSetup(ctx context.Context, req worktreecontract.SetupSubscribeRequest) (worktreecontract.SetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return worktreeCommandNoopSetupSubscription{}, nil
}

type worktreeCommandNoopSetupSubscription struct{}

func (worktreeCommandNoopSetupSubscription) Next(ctx context.Context) (worktreecontract.SetupEvent, error) {
	return worktreecontract.SetupEvent{}, io.EOF
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

func testMainWorktreeListResponse() worktreecontract.ListResponse {
	return worktreecontract.ListResponse{
		Target: worktreecontract.SessionExecutionTarget{
			WorkspaceID:      "workspace-1",
			WorkspaceRoot:    "/repo",
			EffectiveWorkdir: "/repo",
		},
		Worktrees: []worktreecontract.ListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, true, true, false),
		},
	}
}

func testLinkedWorktreeListResponse() worktreecontract.ListResponse {
	return worktreecontract.ListResponse{
		Target: worktreecontract.SessionExecutionTarget{
			WorkspaceID:      "workspace-1",
			WorkspaceRoot:    "/repo",
			Worktree:         &worktreecontract.SessionExecutionWorktreeTarget{ID: "wt-feature", Root: "/wt/feature-a"},
			EffectiveWorkdir: "/wt/feature-a/pkg",
		},
		Worktrees: []worktreecontract.ListEntry{
			testRegisteredWorktreeListEntry("wt-main", "main", "/repo", "main", true, false, true, false),
			testRegisteredWorktreeListEntry("wt-feature", "feature-a", "/wt/feature-a", "feature/a", false, true, true, true),
		},
	}
}

func testRegisteredWorktreeListEntry(id, name, root, branch string, main, current, managed, createdBranch bool) worktreecontract.ListEntry {
	branchRef := "refs/heads/" + branch
	branchName := branch
	originSessionID := "session-1"
	kent := worktreecontract.KentFacts{
		WorktreeID:    id,
		CanonicalRoot: root,
		DisplayName:   name,
		Managed:       managed,
		CreatedBranch: createdBranch,
	}
	if createdBranch {
		kent.OriginSessionID = &originSessionID
	}
	return worktreecontract.ListEntry{
		Topology: worktreecontract.TopologyEntry{
			Variant: worktreecontract.TopologyVariantRegistered,
			Registered: &worktreecontract.RegisteredFacts{
				Git: worktreecontract.GitFacts{
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
		Projection: worktreecontract.ListProjection{Selector: branch, IsCurrent: current},
	}
}

func testExternalWorktreeListEntry(root string, selector string, current bool) worktreecontract.ListEntry {
	fallbackIdentity := filepath.Base(root)
	return worktreecontract.ListEntry{
		Topology: worktreecontract.TopologyEntry{
			Variant: worktreecontract.TopologyVariantExternal,
			External: &worktreecontract.ExternalFacts{
				Git: worktreecontract.GitFacts{
					CanonicalRoot: root,
					HeadObject:    "deadbeef",
					Detached:      true,
					PathAvailable: true,
				},
			},
		},
		Projection: worktreecontract.ListProjection{
			Selector:         selector,
			IsCurrent:        current,
			FallbackIdentity: &fallbackIdentity,
		},
	}
}

func mustProjectWorktreeItem(t *testing.T, entry worktreecontract.ListEntry) worktreeui.Item {
	t.Helper()
	item, err := worktreeui.ProjectItem(entry)
	if err != nil {
		t.Fatalf("ProjectItem: %v", err)
	}
	return item
}
