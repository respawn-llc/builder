package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"core/shared/serverjsoncontract"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/types/known/emptypb"
)

type InvalidResponseError struct {
	Operation string
	Cause     error
}

func (e *InvalidResponseError) Error() string {
	return fmt.Sprintf("validate %s response: %v", e.Operation, e.Cause)
}
func (e *InvalidResponseError) Unwrap() error { return e.Cause }
func invalidResponseError(operation string, cause error) error {
	return &InvalidResponseError{Operation: operation, Cause: cause}
}

func validateRuntimeLiveResponseSession(operation string, requestedSessionID string, responseSessionID string) error {
	if responseSessionID != requestedSessionID {
		return invalidResponseError(operation, fmt.Errorf(
			"response session ID %q does not match requested session %q",
			responseSessionID, requestedSessionID,
		))
	}
	return nil
}

type Remote struct {
	plan                             remoteDialPlan
	transport                        rpcwire.ClientTransport
	mu                               sync.Mutex
	control                          *remoteControlConn
	identity                         protocol.ServerIdentity
	attachIntent                     *remoteAttachmentIntent
	attachment                       *remoteAttachment
	sessionExecutionResponseContract serverjsoncontract.SessionExecutionEnvironmentResponse
	expectedRootID                   atomic.Value // string; empty disables root validation
	noAuthAck                        atomic.Bool
	closed                           atomic.Bool
}

func DialRemoteURL(ctx context.Context, rpcURL string) (*Remote, error) {
	return dialRemoteURL(ctx, rpcURL, nil)
}

func DialRemoteURLForProject(ctx context.Context, rpcURL string, projectID string) (*Remote, error) {
	intent, err := newRemoteDefaultProjectAttachmentIntent(projectID)
	if err != nil {
		return nil, err
	}
	return dialRemoteURL(ctx, rpcURL, intent)
}

func DialRemoteURLForProjectWorkspace(ctx context.Context, rpcURL string, projectID string, workspaceRoot string) (*Remote, error) {
	intent, err := newRemoteProjectWorkspaceRootAttachmentIntent(projectID, workspaceRoot)
	if err != nil {
		return nil, err
	}
	return dialRemoteURL(ctx, rpcURL, intent)
}

func DialRemoteURLForSession(ctx context.Context, rpcURL string, sessionID string) (*Remote, error) {
	intent, err := newRemoteSessionAttachmentIntent(sessionID)
	if err != nil {
		return nil, err
	}
	return dialRemoteURL(ctx, rpcURL, intent)
}

func DialConfiguredRemote(ctx context.Context, cfg config.App) (*Remote, error) {
	return dialConfiguredRemote(ctx, cfg, nil)
}

func DialConfiguredRemoteForProjectWorkspace(ctx context.Context, cfg config.App, projectID string, workspaceRoot string) (*Remote, error) {
	intent, err := newRemoteProjectWorkspaceRootAttachmentIntent(projectID, workspaceRoot)
	if err != nil {
		return nil, err
	}
	return dialConfiguredRemote(ctx, cfg, intent)
}

func DialConfiguredRemoteForProjectWorkspaceID(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*Remote, error) {
	intent, err := newRemoteProjectWorkspaceIDAttachmentIntent(projectID, workspaceID)
	if err != nil {
		return nil, err
	}
	return dialConfiguredRemote(ctx, cfg, intent)
}

func DialConfiguredRemoteForSession(ctx context.Context, cfg config.App, sessionID string) (*Remote, error) {
	intent, err := newRemoteSessionAttachmentIntent(sessionID)
	if err != nil {
		return nil, err
	}
	return dialConfiguredRemote(ctx, cfg, intent)
}

func (c *Remote) Close() error {
	if c == nil {
		return nil
	}
	c.closed.Store(true)
	c.mu.Lock()
	control := c.control
	c.control = nil
	c.mu.Unlock()
	if control == nil {
		return nil
	}
	return control.Close()
}

func (c *Remote) Identity() protocol.ServerIdentity {
	if c == nil {
		return protocol.ServerIdentity{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identity
}

// RequireRoot pins the persistence-root id that every (re)connect handshake must
// report. It validates the current identity immediately and returns an error on
// mismatch so an initial attach to the wrong instance is rejected; the pinned id
// then guards reconnects, where the dial plan may resolve a different server on
// the fallback TCP endpoint after the original socket disappears. An empty rootID
// disables root validation (default-root behavior is unchanged).
func (c *Remote) RequireRoot(rootID string) error {
	if c == nil {
		return errors.New("remote client is required")
	}
	c.expectedRootID.Store(rootID)
	c.mu.Lock()
	identity := c.identity
	c.mu.Unlock()
	return validateIdentityRoot(rootID, identity)
}

func (c *Remote) rootID() string {
	if c == nil {
		return ""
	}
	if value, ok := c.expectedRootID.Load().(string); ok {
		return value
	}
	return ""
}

func (c *Remote) EnableNoAuthBootstrapAcknowledgement(ctx context.Context) error {
	if c == nil {
		return errors.New("remote client is required")
	}
	resp, err := c.AcknowledgeNoAuth(ctx, &emptypb.Empty{})
	if err != nil {
		c.noAuthAck.Store(false)
		return err
	}
	if resp.NoAuthSelected {
		c.noAuthAck.Store(true)
		return nil
	}
	c.noAuthAck.Store(false)
	if resp.AuthReady {
		return nil
	}
	return serverapi.ErrServerAuthRequired
}

func (c *Remote) DisableNoAuthBootstrapAcknowledgement() {
	if c != nil {
		c.noAuthAck.Store(false)
	}
}

func (c *Remote) NoAuthBootstrapAcknowledgementEnabled() bool {
	return c != nil && c.noAuthAck.Load()
}

func (c *Remote) acknowledgeNoAuthOnConn(ctx context.Context, conn rpcwire.Conn) error {
	if c == nil || !c.noAuthAck.Load() {
		return nil
	}
	result := &authpb.AcknowledgeNoAuthResult{}
	if err := callBinaryRPC(
		ctx,
		conn,
		"auth-acknowledge-no-auth",
		bootstrapMethod(authpb.File_kent_api_auth_auth_proto, "AuthService", "AcknowledgeNoAuth"),
		&emptypb.Empty{},
		result,
	); err != nil {
		if errors.Is(err, serverapi.ErrServerAuthRequired) {
			c.noAuthAck.Store(false)
		}
		return err
	}
	if result.GetError() != nil {
		err := authGeneratedError(result.GetError().Code, result.GetError().GetInternalFailure())
		if errors.Is(err, serverapi.ErrServerAuthRequired) {
			c.noAuthAck.Store(false)
		}
		return err
	}
	resp := result.GetSuccess()
	if resp.NoAuthSelected {
		return nil
	}
	c.noAuthAck.Store(false)
	if resp.AuthReady {
		return nil
	}
	return serverapi.ErrServerAuthRequired
}

func (c *Remote) GetReadiness(ctx context.Context, req *emptypb.Empty) (*serverpb.GetReadinessSuccess, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(serverpb.File_kent_api_server_server_proto, "ServerService", "GetReadiness"),
		req,
		&serverpb.GetReadinessResult{},
		func(failure *serverpb.GetReadinessError) error {
			return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
		})
}

func (c *Remote) GetUpdateStatus(ctx context.Context, req *emptypb.Empty) (*serverpb.GetUpdateStatusSuccess, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(serverpb.File_kent_api_server_server_proto, "ServerService", "GetUpdateStatus"),
		req,
		&serverpb.GetUpdateStatusResult{},
		func(failure *serverpb.GetUpdateStatusError) error {
			switch failure.Code {
			case "auth_required":
				return serverapi.ErrServerAuthRequired
			case "server_not_ready":
				return protoapi.ServerNotReadyFromProto(failure.GetServerNotReady())
			case "internal_failure":
				return protoapi.InternalFailureFromProto(failure.GetInternalFailure())
			default:
				return generatedOperationFailure(failure.Code)
			}
		})
}

func (c *Remote) ProjectID() string {
	if binding, present := c.projectBinding(); present {
		return binding.ProjectID
	}
	return ""
}

func (c *Remote) WorkspaceRoot() string {
	if binding, present := c.projectBinding(); present {
		return binding.WorkspaceRoot
	}
	return ""
}

func (c *Remote) WorkspaceID() string {
	if binding, present := c.projectBinding(); present {
		return binding.WorkspaceID
	}
	return ""
}

func (c *Remote) ProjectBinding() (ProjectAttachment, bool) {
	return c.projectBinding()
}

func (c *Remote) projectBinding() (ProjectAttachment, bool) {
	if c == nil {
		return ProjectAttachment{}, false
	}
	return remoteAttachmentProjectBinding(c.attachment)
}

func callUnscopedRPC[Req any, Resp any](c *Remote, ctx context.Context, method string, req Req) (Resp, error) {
	var resp Resp
	return resp, c.callUnscoped(ctx, method, req, &resp)
}

func callControlRPC[Req any, Resp any](c *Remote, ctx context.Context, method string, req Req) (Resp, error) {
	var resp Resp
	return resp, c.call(ctx, method, req, &resp)
}

func callDedicatedRPC[Req any, Resp any](c *Remote, ctx context.Context, requestID string, method string, req Req) (Resp, error) {
	var resp Resp
	return resp, c.callDedicated(ctx, requestID, method, req, &resp)
}

func (c *Remote) GetBootstrapStatus(ctx context.Context, req *emptypb.Empty) (*authpb.BootstrapStatus, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(authpb.File_kent_api_auth_auth_proto, "AuthService", "GetBootstrapStatus"),
		req,
		&authpb.GetBootstrapStatusResult{},
		func(failure *authpb.GetBootstrapStatusError) error {
			return authGeneratedError(failure.Code, failure.GetInternalFailure())
		})
}

func (c *Remote) CompleteBootstrap(ctx context.Context, req *authpb.CompleteBootstrapRequest) (*authpb.BootstrapCompletion, error) {
	resp, err := callGeneratedBinary(c, ctx,
		bootstrapMethod(authpb.File_kent_api_auth_auth_proto, "AuthService", "CompleteBootstrap"),
		req,
		&authpb.CompleteBootstrapResult{},
		func(failure *authpb.CompleteBootstrapError) error {
			return authGeneratedError(failure.Code, failure.GetInternalFailure())
		})
	if err != nil {
		return nil, err
	}
	if resp.GetNoAuthSelected() {
		c.noAuthAck.Store(true)
	} else if resp.GetAuthReady() {
		c.noAuthAck.Store(false)
	}
	return resp, nil
}

func (c *Remote) AcknowledgeNoAuth(ctx context.Context, req *emptypb.Empty) (*authpb.NoAuthAcknowledgement, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(authpb.File_kent_api_auth_auth_proto, "AuthService", "AcknowledgeNoAuth"),
		req,
		&authpb.AcknowledgeNoAuthResult{},
		func(failure *authpb.AcknowledgeNoAuthError) error {
			return authGeneratedError(failure.Code, failure.GetInternalFailure())
		})
}

func (c *Remote) GetStatus(ctx context.Context, req *authpb.GetStatusRequest) (*authpb.Status, error) {
	return callGeneratedBinary(c, ctx,
		bootstrapMethod(authpb.File_kent_api_auth_auth_proto, "AuthService", "GetStatus"),
		req,
		&authpb.GetStatusResult{},
		func(failure *authpb.GetStatusError) error {
			return authGeneratedError(failure.Code, failure.GetInternalFailure())
		})
}

func (c *Remote) GetChatContext(ctx context.Context, req serverapi.ChatContextRequest) (serverapi.ChatContextResponse, error) {
	return callValidatedControlRPC[serverapi.ChatContextRequest, serverapi.ChatContextResponse](c, ctx, protocol.MethodChatContextGet, req)
}

func (c *Remote) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](c, ctx, protocol.MethodWorkflowCreate, req)
}

func (c *Remote) CreateAndLinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowCreateAndLinkProjectRequest) (serverapi.WorkflowCreateAndLinkProjectResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](c, ctx, protocol.MethodWorkflowCreateAndLinkProject, req)
}

func (c *Remote) UpdateWorkflow(ctx context.Context, req serverapi.WorkflowUpdateRequest) (serverapi.WorkflowGetResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](c, ctx, protocol.MethodWorkflowUpdate, req)
}

func (c *Remote) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](c, ctx, protocol.MethodWorkflowList, req)
}

func (c *Remote) GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](c, ctx, protocol.MethodWorkflowGet, req)
}

func (c *Remote) LinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](c, ctx, protocol.MethodWorkflowLinkProject, req)
}

func (c *Remote) ListProjectWorkflowLinks(ctx context.Context, req serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](c, ctx, protocol.MethodWorkflowListProjectLinks, req)
}

func (c *Remote) SetDefaultProjectWorkflowLink(ctx context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](c, ctx, protocol.MethodWorkflowSetDefaultProjectLink, req)
}

func (c *Remote) UnlinkWorkflowFromProject(ctx context.Context, req serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](c, ctx, protocol.MethodWorkflowUnlinkProject, req)
}

func (c *Remote) PreviewWorkflowDelete(ctx context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](c, ctx, protocol.MethodWorkflowDeletePreview, req)
}

func (c *Remote) DeleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](c, ctx, protocol.MethodWorkflowDelete, req)
}

func (c *Remote) ValidateWorkflow(ctx context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](c, ctx, protocol.MethodWorkflowValidate, req)
}

func (c *Remote) ValidateWorkflowScriptPath(ctx context.Context, req serverapi.WorkflowScriptPathValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowScriptPathValidateRequest, serverapi.WorkflowValidateResponse](c, ctx, protocol.MethodWorkflowScriptPathValidate, req)
}

func (c *Remote) ValidateWorkflowGraphDraft(ctx context.Context, req serverapi.WorkflowGraphValidateDraftRequest) (serverapi.WorkflowGraphValidateDraftResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](c, ctx, protocol.MethodWorkflowGraphValidateDraft, req)
}

func (c *Remote) DeriveWorkflowGraphWiring(ctx context.Context, req serverapi.WorkflowGraphDeriveWiringRequest) (serverapi.WorkflowGraphDeriveWiringResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](c, ctx, protocol.MethodWorkflowGraphDeriveWiring, req)
}

func (c *Remote) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](c, ctx, protocol.MethodWorkflowGraphSavePreview, req)
}

func (c *Remote) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](c, ctx, protocol.MethodWorkflowGraphSave, req)
}

func (c *Remote) CreateWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelCreateRequest) (serverapi.WorkflowProjectLabelCreateResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowProjectLabelCreateRequest, serverapi.WorkflowProjectLabelCreateResponse](c, ctx, protocol.MethodWorkflowProjectLabelCreate, req)
	return validateWorkflowResponse("create workflow project label", response, err)
}

func (c *Remote) ListWorkflowProjectLabels(ctx context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowProjectLabelCatalogRequest, serverapi.WorkflowProjectLabelCatalogResponse](c, ctx, protocol.MethodWorkflowProjectLabelList, req)
	return validateWorkflowResponse("list workflow project labels", response, err)
}

func (c *Remote) RenameWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelRenameRequest) (serverapi.WorkflowProjectLabelRenameResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowProjectLabelRenameRequest, serverapi.WorkflowProjectLabelRenameResponse](c, ctx, protocol.MethodWorkflowProjectLabelRename, req)
	return validateWorkflowResponse("rename workflow project label", response, err)
}

func (c *Remote) DeleteWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelDeleteRequest) (serverapi.WorkflowProjectLabelDeleteResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowProjectLabelDeleteRequest, serverapi.WorkflowProjectLabelDeleteResponse](c, ctx, protocol.MethodWorkflowProjectLabelDelete, req)
	return validateWorkflowResponse("delete workflow project label", response, err)
}

func (c *Remote) ReorderWorkflowProjectLabels(ctx context.Context, req serverapi.WorkflowProjectLabelReorderRequest) (serverapi.WorkflowProjectLabelReorderResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowProjectLabelReorderRequest, serverapi.WorkflowProjectLabelReorderResponse](c, ctx, protocol.MethodWorkflowProjectLabelReorder, req)
	return validateWorkflowResponse("reorder workflow project labels", response, err)
}

func (c *Remote) GetWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsGetRequest) (serverapi.WorkflowTaskLabelsGetResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskLabelsGetRequest, serverapi.WorkflowTaskLabelsGetResponse](c, ctx, protocol.MethodWorkflowTaskLabelsGet, req)
	return validateWorkflowResponse("get workflow task labels", response, err)
}

func (c *Remote) UpdateWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsUpdateRequest) (serverapi.WorkflowTaskLabelsUpdateResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskLabelsUpdateRequest, serverapi.WorkflowTaskLabelsUpdateResponse](c, ctx, protocol.MethodWorkflowTaskLabelsUpdate, req)
	return validateWorkflowResponse("update workflow task labels", response, err)
}

func (c *Remote) CreateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](c, ctx, protocol.MethodWorkflowTaskCreate, req)
}

func (c *Remote) AddWorkflowTaskDependency(ctx context.Context, req serverapi.WorkflowTaskDependencyAddRequest) (serverapi.WorkflowTaskDependencyAddResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskDependencyAddRequest, serverapi.WorkflowTaskDependencyAddResponse](c, ctx, protocol.MethodWorkflowTaskDependencyAdd, req)
	return validateWorkflowResponse("add workflow task dependency", response, err)
}

func (c *Remote) RemoveWorkflowTaskDependency(ctx context.Context, req serverapi.WorkflowTaskDependencyRemoveRequest) (serverapi.WorkflowTaskDependencyRemoveResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskDependencyRemoveRequest, serverapi.WorkflowTaskDependencyRemoveResponse](c, ctx, protocol.MethodWorkflowTaskDependencyRemove, req)
	return validateWorkflowResponse("remove workflow task dependency", response, err)
}

func (c *Remote) ListWorkflowTaskDependencies(ctx context.Context, req serverapi.WorkflowTaskDependencyListRequest) (serverapi.WorkflowTaskDependencyListResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskDependencyListRequest, serverapi.WorkflowTaskDependencyListResponse](c, ctx, protocol.MethodWorkflowTaskDependencyList, req)
	return validateWorkflowResponse("list workflow task dependencies", response, err)
}

func (c *Remote) UpdateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskUpdateRequest) (serverapi.WorkflowTaskUpdateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](c, ctx, protocol.MethodWorkflowTaskUpdate, req)
}

func (c *Remote) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](c, ctx, protocol.MethodWorkflowTaskStart, req)
	return validateWorkflowResponse("start workflow task", response, err)
}

func (c *Remote) InterruptWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](c, ctx, protocol.MethodWorkflowTaskInterrupt, req)
}

func (c *Remote) ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](c, ctx, protocol.MethodWorkflowTaskResume, req)
	return validateWorkflowResponse("resume workflow task", response, err)
}

func (c *Remote) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](c, ctx, protocol.MethodWorkflowTaskApprove, req)
	return validateWorkflowResponse("approve workflow task", response, err)
}

func (c *Remote) PreviewWorkflowTaskMove(ctx context.Context, req serverapi.WorkflowTaskMovePreviewRequest) (serverapi.WorkflowTaskMovePreviewResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskMovePreviewRequest, serverapi.WorkflowTaskMovePreviewResponse](c, ctx, protocol.MethodWorkflowTaskMovePreview, req)
	return validateWorkflowResponse("preview workflow task move", response, err)
}

func (c *Remote) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](c, ctx, protocol.MethodWorkflowTaskMove, req)
	return validateWorkflowResponse("move workflow task", response, err)
}

func (c *Remote) CompleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](c, ctx, protocol.MethodWorkflowTaskComplete, req)
}

func (c *Remote) DeleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskDeleteRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskDelete, req, &struct{}{})
}

func (c *Remote) ListWorkflowAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](c, ctx, protocol.MethodWorkflowAttentionList, req)
	return validateWorkflowResponse("list workflow attention", response, err)
}

func (c *Remote) ListWorkflowTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](c, ctx, protocol.MethodWorkflowTaskAttentionList, req)
	return validateWorkflowTaskBoundResponse("list workflow task attention", strings.TrimSpace(req.TaskID), response, err)
}

func (c *Remote) AddWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](c, ctx, protocol.MethodWorkflowTaskCommentAdd, req)
}

func (c *Remote) ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskCommentListResponse](c, ctx, protocol.MethodWorkflowTaskCommentList, req)
}

func (c *Remote) ReplaceWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentReplaceRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskCommentReplace, req, &struct{}{})
}

func (c *Remote) DeleteWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentDeleteRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskCommentDelete, req, &struct{}{})
}

func (c *Remote) ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskActivityListResponse](c, ctx, protocol.MethodWorkflowTaskActivityList, req)
	return validateWorkflowTaskBoundResponse("list workflow task activity", strings.TrimSpace(req.TaskID), response, err)
}

func (c *Remote) ListWorkflowTaskSessions(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskSessionListResponse](c, ctx, protocol.MethodWorkflowTaskSessionList, req)
}

func (c *Remote) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](c, ctx, protocol.MethodWorkflowTaskList, req)
	return validateWorkflowResponse("list workflow tasks", response, err)
}

func (c *Remote) GetWorkflowProjectTaskGroupCounts(ctx context.Context, req serverapi.WorkflowProjectTaskGroupCountsRequest) (serverapi.WorkflowProjectTaskGroupCountsResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowProjectTaskGroupCountsRequest, serverapi.WorkflowProjectTaskGroupCountsResponse](c, ctx, protocol.MethodWorkflowProjectTaskGroupCounts, req)
	return validateWorkflowResponse("get workflow project task group counts", response, err)
}

func (c *Remote) SearchWorkflowTasks(ctx context.Context, req serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	response, err := callDedicatedRPC[serverapi.TaskSearchRequest, serverapi.TaskSearchResponse](
		c,
		ctx,
		apicontract.TaskSearchDedicatedRequestID,
		protocol.MethodWorkflowTaskSearch,
		req,
	)
	response, err = validateWorkflowResponse("search workflow tasks", response, err)
	if err != nil {
		return response, err
	}
	if response.Mode != req.Mode {
		return serverapi.TaskSearchResponse{}, fmt.Errorf(
			"search workflow tasks returned mode %q for request mode %q",
			response.Mode,
			req.Mode,
		)
	}
	return response, nil
}

func (c *Remote) GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](c, ctx, protocol.MethodWorkflowBoardGet, req)
}

func (c *Remote) ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](c, ctx, protocol.MethodWorkflowBoardNodeCardsList, req)
	return validateWorkflowResponse("list workflow board node cards", response, err)
}

func (c *Remote) GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](c, ctx, protocol.MethodWorkflowTaskGet, req)
	return validateWorkflowResponse("get workflow task", response, err)
}

func (c *Remote) ObserveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, error) {
	response, err := callUnscopedRPC[serverapi.WorkflowTaskObservationRequest, serverapi.WorkflowTaskObservationResponse](c, ctx, protocol.MethodWorkflowTaskObserve, req)
	if err = normalizeWorkflowTaskObservationRPCError(err); err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return serverapi.WorkflowTaskObservationResponse{}, invalidResponseError("workflow task observation", err)
	}
	return response, nil
}

func normalizeWorkflowTaskObservationRPCError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: workflow task observation RPC stream closed: %v", serverapi.ErrStreamFailed, err)
	}
	return err
}

func (c *Remote) ReadChatSettings(
	ctx context.Context,
	req serverapi.ChatSettingsReadRequest,
) (serverapi.ChatSettingsReadResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	var response serverapi.ChatSettingsReadResponse
	if err := c.call(ctx, protocol.MethodChatSettingsRead, req, &response); err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	if err := response.ValidateForTarget(req.Target); err != nil {
		return serverapi.ChatSettingsReadResponse{}, invalidResponseError(
			"Chat settings",
			err,
		)
	}
	return response, nil
}

func (c *Remote) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	return callValidatedControlRPC[serverapi.SessionMainViewRequest, serverapi.SessionMainViewResponse](c, ctx, protocol.MethodSessionGetMainView, req)
}

func (c *Remote) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	var resp serverapi.SessionTranscriptPageResponse
	if err := c.call(ctx, protocol.MethodSessionGetTranscriptPage, req, &resp); err != nil {
		return resp, err
	}
	if err := resp.Validate(); err != nil {
		return serverapi.SessionTranscriptPageResponse{}, fmt.Errorf("validate session transcript page response: %w", err)
	}
	return resp, nil
}

func (c *Remote) GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	var resp serverapi.SessionLatestCommittedAssistantFinalAnswerResponse
	return resp, c.call(ctx, protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer, req, &resp)
}

func (c *Remote) GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	var raw json.RawMessage
	if err := c.call(ctx, protocol.MethodSessionGetExecutionEnvironment, req, &raw); err != nil {
		return serverapi.SessionExecutionEnvironmentResponse{}, err
	}
	return c.sessionExecutionResponseContract.Decode(raw)
}

func (c *Remote) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	var resp serverapi.SessionInitialInputResponse
	return resp, c.call(ctx, protocol.MethodSessionGetInitialInput, req, &resp)
}

func (c *Remote) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	var resp serverapi.SessionPersistInputDraftResponse
	return resp, c.call(ctx, protocol.MethodSessionPersistInputDraft, req, &resp)
}

func (c *Remote) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return callUnscopedRPC[serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](c, ctx, protocol.MethodSessionRetargetWorkspace, req)
}

func (c *Remote) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	var resp serverapi.SessionResolveTransitionResponse
	return resp, c.call(ctx, protocol.MethodSessionResolveTransition, req, &resp)
}

func (c *Remote) ListWorktrees(ctx context.Context, req worktreecontract.ListRequest) (worktreecontract.ListResponse, error) {
	return callValidatedRPC[worktreecontract.ListRequest, worktreecontract.ListResponse](c, ctx, protocol.MethodWorktreeList, req)
}

func (c *Remote) ListWorkspaceWorktrees(ctx context.Context, req worktreecontract.WorkspaceListRequest) (worktreecontract.WorkspaceListResponse, error) {
	return callValidatedRPC[worktreecontract.WorkspaceListRequest, worktreecontract.WorkspaceListResponse](c, ctx, protocol.MethodWorktreeWorkspaceList, req)
}

func (c *Remote) GetWorktreeStatus(ctx context.Context, req worktreecontract.StatusRequest) (worktreecontract.StatusResponse, error) {
	var resp worktreecontract.StatusResponse
	return resp, c.call(ctx, protocol.MethodWorktreeStatus, req, &resp)
}

func (c *Remote) ResolveWorktreeSelector(ctx context.Context, req worktreecontract.SelectorResolveRequest) (worktreecontract.SelectorResolveResponse, error) {
	return callValidatedRPC[worktreecontract.SelectorResolveRequest, worktreecontract.SelectorResolveResponse](c, ctx, protocol.MethodWorktreeSelectorResolve, req)
}

func (c *Remote) PreviewWorktreeDelete(ctx context.Context, req worktreecontract.DeletePreviewRequest) (worktreecontract.DeletePreviewResponse, error) {
	var resp worktreecontract.DeletePreviewResponse
	if err := c.call(ctx, protocol.MethodWorktreeDeletePreview, req, &resp); err != nil {
		return worktreecontract.DeletePreviewResponse{}, err
	}
	if err := resp.Validate(); err != nil {
		return worktreecontract.DeletePreviewResponse{}, fmt.Errorf("validate worktree delete preview response: %w", err)
	}
	return resp, nil
}

func (c *Remote) ResolveWorktreeCreateTarget(ctx context.Context, req worktreecontract.CreateTargetResolveRequest) (worktreecontract.CreateTargetResolveResponse, error) {
	var resp worktreecontract.CreateTargetResolveResponse
	return resp, c.call(ctx, protocol.MethodWorktreeCreateTargetResolve, req, &resp)
}

func (c *Remote) CreateWorktree(ctx context.Context, req worktreecontract.CreateRequest) (worktreecontract.CreateResponse, error) {
	return callValidatedRPC[worktreecontract.CreateRequest, worktreecontract.CreateResponse](c, ctx, protocol.MethodWorktreeCreate, req)
}

func (c *Remote) EnterWorktree(ctx context.Context, req worktreecontract.EnterRequest) (worktreecontract.ScheduledAcknowledgement, error) {
	var resp worktreecontract.ScheduledAcknowledgement
	return resp, c.call(ctx, protocol.MethodWorktreeEnter, req, &resp)
}

func (c *Remote) LeaveWorktree(ctx context.Context, req worktreecontract.LeaveRequest) (worktreecontract.ScheduledAcknowledgement, error) {
	var resp worktreecontract.ScheduledAcknowledgement
	return resp, c.call(ctx, protocol.MethodWorktreeLeave, req, &resp)
}

func (c *Remote) DeleteWorktree(ctx context.Context, req worktreecontract.DeleteRequest) (worktreecontract.DeleteResult, error) {
	var resp worktreecontract.DeleteResult
	return resp, c.call(ctx, protocol.MethodWorktreeDelete, req, &resp)
}

func (c *Remote) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	var resp serverapi.SessionRuntimeActivateResponse
	return resp, c.call(ctx, protocol.MethodSessionRuntimeActivate, req, &resp)
}

func (c *Remote) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	var resp serverapi.SessionRuntimeReleaseResponse
	return resp, c.call(ctx, protocol.MethodSessionRuntimeRelease, req, &resp)
}

func (c *Remote) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	return c.call(ctx, protocol.MethodRuntimeSetSessionName, req, nil)
}

func (c *Remote) SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error {
	return c.call(ctx, protocol.MethodRuntimeSetThinkingLevel, req, nil)
}

func (c *Remote) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](c, ctx, protocol.MethodRuntimeSetFastModeEnabled, req)
}

func (c *Remote) SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](c, ctx, protocol.MethodRuntimeSetReviewerEnabled, req)
}

func (c *Remote) SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](c, ctx, protocol.MethodRuntimeSetAutoCompactionEnabled, req)
}

func (c *Remote) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](c, ctx, protocol.MethodRuntimeSetQuestionsEnabled, req)
}

func (c *Remote) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	return c.call(ctx, protocol.MethodRuntimeAppendCommittedEntry, req, nil)
}

func (c *Remote) ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	return callControlRPC[serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](c, ctx, protocol.MethodRuntimeShouldCompactBeforeUserMessage, req)
}

func (c *Remote) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	response, err := callDedicatedRPC[serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](c, ctx, "runtime-submit-user-turn", protocol.MethodRuntimeSubmitUserTurn, req)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, fmt.Errorf("validate runtime submit user turn response: %w", err)
	}
	return response, nil
}

func (c *Remote) SubmitUserShellCommand(ctx context.Context, req serverapi.RuntimeSubmitUserShellCommandRequest) error {
	return c.callDedicated(ctx, "runtime-submit-user-shell-command", protocol.MethodRuntimeSubmitUserShellCommand, req, nil)
}

func (c *Remote) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	return c.callDedicated(ctx, "runtime-compact-context", protocol.MethodRuntimeCompactContext, req, nil)
}

func (c *Remote) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error) {
	return callDedicatedRPC[serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](c, ctx, "runtime-interrupt", protocol.MethodRuntimeInterrupt, req)
}

func (c *Remote) LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error) {
	return callControlRPC[serverapi.RuntimeLiveSteerRequest, serverapi.RuntimeLiveSteerResponse](c, ctx, protocol.MethodRuntimeLiveSteer, req)
}

func (c *Remote) LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error) {
	return callDedicatedRPC[serverapi.RuntimeLiveStopRequest, serverapi.RuntimeLiveStopResponse](c, ctx, "runtime-live-stop", protocol.MethodRuntimeLiveStop, req)
}

func (c *Remote) LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error) {
	response, err := callDedicatedRPC[serverapi.RuntimeLiveWaitRequest, serverapi.RuntimeLiveWaitResponse](c, ctx, "runtime-live-wait", protocol.MethodRuntimeLiveWait, req)
	if err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, invalidResponseError("runtime live wait", err)
	}
	if err := validateRuntimeLiveResponseSession("runtime live wait", req.SessionID, response.SessionID); err != nil {
		return serverapi.RuntimeLiveWaitResponse{}, err
	}
	return response, nil
}

func (c *Remote) LiveWatch(ctx context.Context, req serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error) {
	response, err := callDedicatedRPC[serverapi.RuntimeLiveWatchRequest, serverapi.RuntimeLiveWatchResponse](c, ctx, "runtime-live-watch", protocol.MethodRuntimeLiveWatch, req)
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, invalidResponseError("runtime live watch", err)
	}
	if err := validateRuntimeLiveResponseSession("runtime live watch", req.SessionID, response.SessionID); err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	return response, nil
}

func (c *Remote) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	return callControlRPC[serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](c, ctx, protocol.MethodRuntimeDiscardQueuedUserMessage, req)
}

func (c *Remote) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	return c.call(ctx, protocol.MethodRuntimeRecordPromptHistory, req, nil)
}

func (c *Remote) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callValidatedControlRPC[serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalShow, req)
}

func (c *Remote) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callValidatedControlRPC[serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalSet, req)
}

func (c *Remote) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callValidatedControlRPC[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalPause, req)
}

func (c *Remote) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callValidatedControlRPC[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalResume, req)
}

func (c *Remote) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callValidatedControlRPC[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalComplete, req)
}

func (c *Remote) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callValidatedControlRPC[serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalClear, req)
}

func callValidatedControlRPC[Request any, Response interface{ Validate() error }](c *Remote, ctx context.Context, method string, req Request) (Response, error) {
	response, err := callControlRPC[Request, Response](c, ctx, method, req)
	if err != nil {
		return response, err
	}
	if err := response.Validate(); err != nil {
		var zero Response
		return zero, invalidResponseError(method, err)
	}
	return response, nil
}

func (c *Remote) ListProcesses(ctx context.Context, req serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	var resp serverapi.ProcessListResponse
	return resp, c.call(ctx, protocol.MethodProcessList, req, &resp)
}

func (c *Remote) GetProcess(ctx context.Context, req serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	var resp serverapi.ProcessGetResponse
	return resp, c.call(ctx, protocol.MethodProcessGet, req, &resp)
}

func (c *Remote) KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	var resp serverapi.ProcessKillResponse
	return resp, c.call(ctx, protocol.MethodProcessKill, req, &resp)
}

func (c *Remote) GetInlineOutput(ctx context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	var resp serverapi.ProcessInlineOutputResponse
	return resp, c.call(ctx, protocol.MethodProcessInlineOutput, req, &resp)
}

func (c *Remote) ListPendingAsksBySession(ctx context.Context, req serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	var resp serverapi.AskListPendingBySessionResponse
	return resp, c.call(ctx, protocol.MethodAskListPending, req, &resp)
}

func (c *Remote) AnswerPromptBatch(ctx context.Context, req serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error) {
	response, err := callControlRPC[serverapi.PromptAnswerBatchRequest, serverapi.PromptAnswerBatchResponse](c, ctx, protocol.MethodPromptAnswerBatch, req)
	if err != nil {
		return serverapi.PromptAnswerBatchResponse{}, err
	}
	if err := serverapi.ValidatePromptAnswerBatchResponse(req, response); err != nil {
		return serverapi.PromptAnswerBatchResponse{}, fmt.Errorf("validate prompt answer batch response: %w", err)
	}
	return response, nil
}

func (c *Remote) ListPendingApprovalsBySession(ctx context.Context, req serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	var resp serverapi.ApprovalListPendingBySessionResponse
	return resp, c.call(ctx, protocol.MethodApprovalListPending, req, &resp)
}

func (c *Remote) ensureOpen() error {
	if c == nil {
		return errors.New("remote client is required")
	}
	if c.closed.Load() {
		return errors.New("remote client is closed")
	}
	return nil
}

func (c *Remote) call(ctx context.Context, method string, params any, out any) error {
	return c.callUnscoped(ctx, method, params, out)
}

func callValidatedRPC[Req any, Resp interface{ Validate() error }](c *Remote, ctx context.Context, method string, req Req) (Resp, error) {
	var resp Resp
	if err := c.call(ctx, method, req, &resp); err != nil {
		return resp, err
	}
	if err := resp.Validate(); err != nil {
		return resp, fmt.Errorf("validate %s response: %w", method, err)
	}
	return resp, nil
}

func (c *Remote) callUnscoped(ctx context.Context, method string, params any, out any) error {
	control, err := c.ensureControl(ctx)
	if err != nil {
		return err
	}
	return control.call(ctx, method, params, out)
}

func (c *Remote) ensureControl(ctx context.Context) (*remoteControlConn, error) {
	if err := c.ensureOpen(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil, errors.New("remote client is closed")
	}
	if c.control != nil && !c.control.IsDone() {
		return c.control, nil
	}
	if c.control != nil {
		_ = c.control.Close()
		c.control = nil
	}
	conn, cleanup, state, err := c.openSetupRPCConn(ctx, nil)
	if err != nil {
		return nil, err
	}
	if c.closed.Load() {
		cleanup()
		return nil, errors.New("remote client is closed")
	}
	control := newRemoteControlConn(conn)
	c.control = control
	c.identity = state.identity
	return control, nil
}

func dialRemoteURL(ctx context.Context, rpcURL string, intent *remoteAttachmentIntent) (*Remote, error) {
	endpoint, err := rpcwire.ParseWebSocketEndpoint(strings.TrimSpace(rpcURL))
	if err != nil {
		return nil, err
	}
	return dialRemoteWithTransport(ctx, remoteDialPlan{endpoints: []rpcwire.Endpoint{endpoint}}, rpcwire.NewWebSocketTransport(), intent)
}

func dialConfiguredRemote(ctx context.Context, cfg config.App, intent *remoteAttachmentIntent) (*Remote, error) {
	plan, err := configuredRemoteDialPlan(cfg)
	if err != nil {
		return nil, err
	}
	return dialRemoteWithTransport(ctx, plan, rpcwire.NewWebSocketTransport(), intent)
}

var _ apicontract.ProjectViewService = (*Remote)(nil)
var _ apicontract.AuthStatusService = (*Remote)(nil)
var _ apicontract.ChatContextService = (*Remote)(nil)
var _ apicontract.SessionLaunchService = (*Remote)(nil)
var _ apicontract.SessionViewService = (*Remote)(nil)
var _ apicontract.SessionLifecycleService = (*Remote)(nil)
var _ apicontract.SessionRuntimeService = (*Remote)(nil)
var _ apicontract.RuntimeControlService = (*Remote)(nil)
var _ apicontract.RuntimeLiveControlService = (*Remote)(nil)
var _ apicontract.ProcessViewService = (*Remote)(nil)
var _ apicontract.ProcessControlService = (*Remote)(nil)
var _ apicontract.SessionTranscriptService = (*Remote)(nil)
var _ apicontract.AttentionNotificationService = (*Remote)(nil)
var _ apicontract.RunPromptService = (*Remote)(nil)
var _ apicontract.AskViewService = (*Remote)(nil)
var _ apicontract.PromptControlService = (*Remote)(nil)
var _ apicontract.ApprovalViewService = (*Remote)(nil)
