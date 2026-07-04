use client_contracts::auth::{
    AuthCompleteBootstrapRequest, AuthCompleteBootstrapResponse, AuthGetBootstrapStatusResponse,
    AuthStatusRequest, AuthStatusResponse,
};
use client_contracts::process::{
    ProcessInlineOutputRequest, ProcessInlineOutputResponse, ProcessKillRequest,
    ProcessKillResponse, ProcessListRequest, ProcessListResponse,
};
use client_contracts::project::{
    ProjectAttachWorkspaceRequest, ProjectAttachWorkspaceResponse, ProjectBindingPlanRequest,
    ProjectBindingPlanResponse, ProjectCreateRequest, ProjectCreateResponse,
    ProjectGetOverviewRequest, ProjectGetOverviewResponse,
};
use client_contracts::prompt::{
    ApprovalAnswerRequest, AskAnswerRequest, PromptActivitySubscribeRequest,
};
use client_contracts::protocol::{
    AttachProjectRequest, AttachResponse, AttachSessionRequest, HandshakeRequest,
    HandshakeResponse, PromptActivityEventParams, SessionActivityEventParams, SubscribeResponse,
};
use client_contracts::runtime_control::{
    RuntimeAppendCommittedEntryRequest, RuntimeCompactContextRequest,
    RuntimeDiscardQueuedUserMessageRequest, RuntimeDiscardQueuedUserMessageResponse,
    RuntimeEmptyResponse, RuntimeGoalClearRequest, RuntimeGoalSetRequest, RuntimeGoalShowRequest,
    RuntimeGoalShowResponse, RuntimeGoalStatusRequest, RuntimeHasQueuedUserWorkRequest,
    RuntimeHasQueuedUserWorkResponse, RuntimeInterruptRequest, RuntimeQueueUserMessageRequest,
    RuntimeQueueUserMessageResponse, RuntimeRecordPromptHistoryRequest,
    RuntimeSetAutoCompactionEnabledRequest, RuntimeSetAutoCompactionEnabledResponse,
    RuntimeSetFastModeEnabledRequest, RuntimeSetFastModeEnabledResponse,
    RuntimeSetQuestionsEnabledRequest, RuntimeSetQuestionsEnabledResponse,
    RuntimeSetReviewerEnabledRequest, RuntimeSetReviewerEnabledResponse,
    RuntimeSetSessionNameRequest, RuntimeSetThinkingLevelRequest,
    RuntimeSubmitQueuedUserMessagesRequest, RuntimeSubmitQueuedUserMessagesResponse,
    RuntimeSubmitUserShellCommandRequest, RuntimeSubmitUserTurnRequest,
    RuntimeSubmitUserTurnResponse,
};
use client_contracts::session::{
    SessionActivitySubscribeRequest, SessionCommittedTranscriptSuffixRequest,
    SessionCommittedTranscriptSuffixResponse, SessionInitialInputRequest,
    SessionInitialInputResponse, SessionMainViewRequest, SessionMainViewResponse,
    SessionPersistInputDraftRequest, SessionPersistInputDraftResponse, SessionPlanRequest,
    SessionPlanResponse, SessionResolveTransitionRequest, SessionResolveTransitionResponse,
    SessionRetargetWorkspaceRequest, SessionRetargetWorkspaceResponse,
    SessionRuntimeActivateRequest, SessionRuntimeActivateResponse, SessionRuntimeReleaseRequest,
    SessionRuntimeReleaseResponse, SessionTranscriptPageRequest, SessionTranscriptPageResponse,
};
use client_contracts::worktree::{
    WorktreeCreateRequest, WorktreeCreateResponse, WorktreeCreateTargetResolveRequest,
    WorktreeCreateTargetResolveResponse, WorktreeDeleteRequest, WorktreeDeleteResponse,
    WorktreeListRequest, WorktreeListResponse, WorktreeSwitchRequest, WorktreeSwitchResponse,
};
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;
use std::sync::Arc;
use std::time::Duration;

use crate::error::RpcError;
use crate::json_rpc::JsonRpcConnection;
use crate::stream::{RawSubscription, SubscriptionRoute, TypedSubscription};
use crate::transport::{ConnectionFactory, ConnectionKind, FrameConnection};

const METHOD_HANDSHAKE: &str = "protocol.handshake";
const METHOD_AUTH_GET_BOOTSTRAP_STATUS: &str = "auth.getBootstrapStatus";
const METHOD_AUTH_COMPLETE_BOOTSTRAP: &str = "auth.completeBootstrap";
const METHOD_AUTH_GET_STATUS: &str = "auth.getStatus";
const METHOD_PROCESS_INLINE_OUTPUT: &str = "process.inlineOutput";
const METHOD_PROCESS_KILL: &str = "process.kill";
const METHOD_PROCESS_LIST: &str = "process.list";
const METHOD_PROJECT_ATTACH: &str = "project.attach";
const METHOD_PROJECT_ATTACH_WORKSPACE: &str = "project.attachWorkspace";
const METHOD_PROJECT_CREATE: &str = "project.create";
const METHOD_PROJECT_GET_OVERVIEW: &str = "project.getOverview";
const METHOD_PROJECT_PLAN_WORKSPACE_BINDING: &str = "project.planWorkspaceBinding";
const METHOD_SESSION_ATTACH: &str = "session.attach";
const METHOD_SESSION_GET_COMMITTED_TRANSCRIPT_SUFFIX: &str = "session.getCommittedTranscriptSuffix";
const METHOD_SESSION_GET_INITIAL_INPUT: &str = "session.getInitialInput";
const METHOD_SESSION_GET_MAIN_VIEW: &str = "session.getMainView";
const METHOD_SESSION_GET_TRANSCRIPT_PAGE: &str = "session.getTranscriptPage";
const METHOD_SESSION_PERSIST_INPUT_DRAFT: &str = "session.persistInputDraft";
const METHOD_SESSION_PLAN: &str = "session.plan";
const METHOD_SESSION_RESOLVE_TRANSITION: &str = "session.resolveTransition";
const METHOD_SESSION_RETARGET_WORKSPACE: &str = "session.retargetWorkspace";
const METHOD_SESSION_RUNTIME_ACTIVATE: &str = "session.runtime.activate";
const METHOD_SESSION_RUNTIME_RELEASE: &str = "session.runtime.release";
const METHOD_PROMPT_SUBSCRIBE_ACTIVITY: &str = "prompt.subscribeActivity";
const METHOD_ASK_ANSWER: &str = "ask.answer";
const METHOD_APPROVAL_ANSWER: &str = "approval.answer";
const METHOD_SESSION_SUBSCRIBE_ACTIVITY: &str = "session.subscribeActivity";
const METHOD_RUNTIME_SUBMIT_USER_TURN: &str = "runtime.submitUserTurn";
const METHOD_RUNTIME_SUBMIT_USER_SHELL_COMMAND: &str = "runtime.submitUserShellCommand";
const METHOD_RUNTIME_COMPACT_CONTEXT: &str = "runtime.compactContext";
const METHOD_RUNTIME_INTERRUPT: &str = "runtime.interrupt";
const METHOD_RUNTIME_QUEUE_USER_MESSAGE: &str = "runtime.queueUserMessage";
const METHOD_RUNTIME_HAS_QUEUED_USER_WORK: &str = "runtime.hasQueuedUserWork";
const METHOD_RUNTIME_SUBMIT_QUEUED_USER_MESSAGES: &str = "runtime.submitQueuedUserMessages";
const METHOD_RUNTIME_DISCARD_QUEUED_USER_MESSAGE: &str = "runtime.discardQueuedUserMessage";
const METHOD_RUNTIME_GOAL_SHOW: &str = "runtime.goal.show";
const METHOD_RUNTIME_GOAL_SET: &str = "runtime.goal.set";
const METHOD_RUNTIME_GOAL_PAUSE: &str = "runtime.goal.pause";
const METHOD_RUNTIME_GOAL_RESUME: &str = "runtime.goal.resume";
const METHOD_RUNTIME_GOAL_CLEAR: &str = "runtime.goal.clear";
const METHOD_RUNTIME_SET_SESSION_NAME: &str = "runtime.setSessionName";
const METHOD_RUNTIME_SET_THINKING_LEVEL: &str = "runtime.setThinkingLevel";
const METHOD_RUNTIME_SET_FAST_MODE_ENABLED: &str = "runtime.setFastModeEnabled";
const METHOD_RUNTIME_SET_REVIEWER_ENABLED: &str = "runtime.setReviewerEnabled";
const METHOD_RUNTIME_SET_AUTO_COMPACTION_ENABLED: &str = "runtime.setAutoCompactionEnabled";
const METHOD_RUNTIME_SET_QUESTIONS_ENABLED: &str = "runtime.setQuestionsEnabled";
const METHOD_RUNTIME_RECORD_PROMPT_HISTORY: &str = "runtime.recordPromptHistory";
const METHOD_RUNTIME_APPEND_COMMITTED_ENTRY: &str = "runtime.appendCommittedEntry";
const METHOD_WORKTREE_LIST: &str = "worktree.list";
const METHOD_WORKTREE_SWITCH: &str = "worktree.switch";
const METHOD_WORKTREE_DELETE: &str = "worktree.delete";
const METHOD_WORKTREE_CREATE: &str = "worktree.create";
const METHOD_WORKTREE_CREATE_TARGET_RESOLVE: &str = "worktree.create_target.resolve";
const REQUEST_ID_HANDSHAKE: &str = "handshake";
const REQUEST_ID_PROJECT_ATTACH: &str = "attach-project";
const REQUEST_ID_SESSION_ATTACH: &str = "attach-session";
const REQUEST_ID_SUBSCRIBE_PROMPT_ACTIVITY: &str = "subscribe-prompt-activity";
const REQUEST_ID_SUBSCRIBE_SESSION_ACTIVITY: &str = "subscribe-session-activity";
const REQUEST_ID_RUNTIME_SUBMIT_USER_TURN: &str = "runtime-submit-user-turn";
const REQUEST_ID_RUNTIME_SUBMIT_USER_SHELL_COMMAND: &str = "runtime-submit-user-shell-command";
const REQUEST_ID_RUNTIME_COMPACT_CONTEXT: &str = "runtime-compact-context";
const REQUEST_ID_RUNTIME_SUBMIT_QUEUED_USER_MESSAGES: &str = "runtime-submit-queued-user-messages";
const REQUEST_ID_RUNTIME_INTERRUPT: &str = "runtime-interrupt";
pub const PROTOCOL_VERSION: &str = env!("KENT_PROTOCOL_VERSION");

pub trait RpcIoGuard: Send + Sync {
    fn assert_rpc_io_allowed(&self, operation: &'static str);
}

pub struct Client<C> {
    rpc: JsonRpcConnection<C>,
    unary_receive_timeout: Option<Duration>,
    io_guard: Option<Arc<dyn RpcIoGuard>>,
}

impl<C> Client<C> {
    pub fn new(connection: C) -> Self {
        Self {
            rpc: JsonRpcConnection::new(connection),
            unary_receive_timeout: None,
            io_guard: None,
        }
    }

    pub fn with_unary_receive_timeout(mut self, timeout: Duration) -> Self {
        self.unary_receive_timeout = Some(timeout);
        self
    }

    pub fn with_io_guard(mut self, guard: Arc<dyn RpcIoGuard>) -> Self {
        self.io_guard = Some(guard);
        self
    }

    pub fn into_connection(self) -> C {
        self.rpc.into_connection()
    }
}

impl<C: FrameConnection> Client<C> {
    pub fn handshake(&mut self, request: HandshakeRequest) -> Result<HandshakeResponse, RpcError> {
        self.call_fixed(REQUEST_ID_HANDSHAKE, METHOD_HANDSHAKE, &request)
    }

    pub fn get_auth_bootstrap_status(
        &mut self,
    ) -> Result<AuthGetBootstrapStatusResponse, RpcError> {
        self.call(METHOD_AUTH_GET_BOOTSTRAP_STATUS, &serde_json::json!({}))
    }

    pub fn complete_auth_bootstrap(
        &mut self,
        request: AuthCompleteBootstrapRequest,
    ) -> Result<AuthCompleteBootstrapResponse, RpcError> {
        self.call(METHOD_AUTH_COMPLETE_BOOTSTRAP, &request)
    }

    pub fn attach_project(
        &mut self,
        project_id: &str,
        workspace_id: &str,
        workspace_root: &str,
    ) -> Result<AttachResponse, RpcError> {
        self.call_fixed(
            REQUEST_ID_PROJECT_ATTACH,
            METHOD_PROJECT_ATTACH,
            &AttachProjectRequest {
                project_id: project_id.trim().to_owned(),
                workspace_id: workspace_id.trim().to_owned(),
                workspace_root: workspace_root.trim().to_owned(),
            },
        )
    }

    pub fn attach_session(&mut self, session_id: &str) -> Result<AttachResponse, RpcError> {
        self.call_fixed(
            REQUEST_ID_SESSION_ATTACH,
            METHOD_SESSION_ATTACH,
            &AttachSessionRequest {
                session_id: session_id.trim().to_owned(),
            },
        )
    }

    pub fn get_project_overview(
        &mut self,
        project_id: &str,
    ) -> Result<ProjectGetOverviewResponse, RpcError> {
        self.call(
            METHOD_PROJECT_GET_OVERVIEW,
            &ProjectGetOverviewRequest {
                project_id: project_id.trim().to_owned(),
            },
        )
    }

    pub fn plan_workspace_binding(
        &mut self,
        request: ProjectBindingPlanRequest,
    ) -> Result<ProjectBindingPlanResponse, RpcError> {
        self.call(METHOD_PROJECT_PLAN_WORKSPACE_BINDING, &request)
    }

    pub fn create_project(
        &mut self,
        request: ProjectCreateRequest,
    ) -> Result<ProjectCreateResponse, RpcError> {
        self.call(METHOD_PROJECT_CREATE, &request)
    }

    pub fn attach_workspace_to_project(
        &mut self,
        request: ProjectAttachWorkspaceRequest,
    ) -> Result<ProjectAttachWorkspaceResponse, RpcError> {
        self.call(METHOD_PROJECT_ATTACH_WORKSPACE, &request)
    }

    pub fn plan_session(
        &mut self,
        request: SessionPlanRequest,
    ) -> Result<SessionPlanResponse, RpcError> {
        self.call(METHOD_SESSION_PLAN, &request)
    }

    pub fn activate_session_runtime(
        &mut self,
        request: SessionRuntimeActivateRequest,
    ) -> Result<SessionRuntimeActivateResponse, RpcError> {
        self.call(METHOD_SESSION_RUNTIME_ACTIVATE, &request)
    }

    pub fn release_session_runtime(
        &mut self,
        request: SessionRuntimeReleaseRequest,
    ) -> Result<SessionRuntimeReleaseResponse, RpcError> {
        self.call(METHOD_SESSION_RUNTIME_RELEASE, &request)
    }

    pub fn resolve_session_transition(
        &mut self,
        request: SessionResolveTransitionRequest,
    ) -> Result<SessionResolveTransitionResponse, RpcError> {
        self.call(METHOD_SESSION_RESOLVE_TRANSITION, &request)
    }

    pub fn get_session_initial_input(
        &mut self,
        request: SessionInitialInputRequest,
    ) -> Result<SessionInitialInputResponse, RpcError> {
        self.call(METHOD_SESSION_GET_INITIAL_INPUT, &request)
    }

    pub fn persist_session_input_draft(
        &mut self,
        request: SessionPersistInputDraftRequest,
    ) -> Result<SessionPersistInputDraftResponse, RpcError> {
        self.call(METHOD_SESSION_PERSIST_INPUT_DRAFT, &request)
    }

    pub fn retarget_session_workspace(
        &mut self,
        request: SessionRetargetWorkspaceRequest,
    ) -> Result<SessionRetargetWorkspaceResponse, RpcError> {
        self.call(METHOD_SESSION_RETARGET_WORKSPACE, &request)
    }

    pub fn get_committed_transcript_suffix(
        &mut self,
        request: SessionCommittedTranscriptSuffixRequest,
    ) -> Result<SessionCommittedTranscriptSuffixResponse, RpcError> {
        self.call(METHOD_SESSION_GET_COMMITTED_TRANSCRIPT_SUFFIX, &request)
    }

    pub fn get_transcript_page(
        &mut self,
        request: SessionTranscriptPageRequest,
    ) -> Result<SessionTranscriptPageResponse, RpcError> {
        self.call(METHOD_SESSION_GET_TRANSCRIPT_PAGE, &request)
    }

    pub fn get_main_view(
        &mut self,
        request: SessionMainViewRequest,
    ) -> Result<SessionMainViewResponse, RpcError> {
        self.call(METHOD_SESSION_GET_MAIN_VIEW, &request)
    }

    pub fn answer_ask(&mut self, request: AskAnswerRequest) -> Result<(), RpcError> {
        self.call::<_, RuntimeEmptyResponse>(METHOD_ASK_ANSWER, &request)
            .map(|_| ())
    }

    pub fn answer_approval(&mut self, request: ApprovalAnswerRequest) -> Result<(), RpcError> {
        self.call::<_, RuntimeEmptyResponse>(METHOD_APPROVAL_ANSWER, &request)
            .map(|_| ())
    }

    fn call<Req, Resp>(&mut self, method: &str, request: &Req) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        self.assert_io_allowed("json-rpc control call");
        call_with_optional_timeout(&mut self.rpc, method, request, self.unary_receive_timeout)
    }

    fn call_fixed<Req, Resp>(
        &mut self,
        id: &str,
        method: &str,
        request: &Req,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        self.assert_io_allowed("json-rpc fixed control call");
        call_fixed_with_optional_timeout(
            &mut self.rpc,
            id,
            method,
            request,
            self.unary_receive_timeout,
        )
    }

    fn assert_io_allowed(&self, operation: &'static str) {
        if let Some(guard) = &self.io_guard {
            guard.assert_rpc_io_allowed(operation);
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RemoteContext {
    project_id: String,
    workspace_id: String,
    workspace_root: String,
}

impl RemoteContext {
    pub fn project(project_id: &str, workspace_id: &str, workspace_root: &str) -> Self {
        Self {
            project_id: project_id.trim().to_owned(),
            workspace_id: workspace_id.trim().to_owned(),
            workspace_root: workspace_root.trim().to_owned(),
        }
    }

    pub fn unscoped() -> Self {
        Self {
            project_id: String::new(),
            workspace_id: String::new(),
            workspace_root: String::new(),
        }
    }

    fn attach_project_request(&self) -> AttachProjectRequest {
        let workspace_id = self.workspace_id.trim().to_owned();
        let workspace_root = if workspace_id.is_empty() {
            self.workspace_root.trim().to_owned()
        } else {
            String::new()
        };
        AttachProjectRequest {
            project_id: self.project_id.clone(),
            workspace_id,
            workspace_root,
        }
    }
}

pub struct RemoteClient<F> {
    factory: F,
    context: RemoteContext,
    subscription_item_read_timeout: Option<Duration>,
    unary_receive_timeout: Option<Duration>,
    io_guard: Option<Arc<dyn RpcIoGuard>>,
}

impl<F> RemoteClient<F> {
    pub fn new(factory: F, context: RemoteContext) -> Self {
        Self {
            factory,
            context,
            subscription_item_read_timeout: None,
            unary_receive_timeout: None,
            io_guard: None,
        }
    }

    pub fn context(&self) -> &RemoteContext {
        &self.context
    }

    pub fn into_factory(self) -> F {
        self.factory
    }

    pub fn with_subscription_item_read_timeout(mut self, timeout: Duration) -> Self {
        self.subscription_item_read_timeout = Some(timeout);
        self
    }

    pub fn with_unary_receive_timeout(mut self, timeout: Duration) -> Self {
        self.unary_receive_timeout = Some(timeout);
        self
    }

    pub fn with_io_guard(mut self, guard: Arc<dyn RpcIoGuard>) -> Self {
        self.io_guard = Some(guard);
        self
    }
}

impl<F: ConnectionFactory> RemoteClient<F> {
    pub fn open_project_connection(&mut self) -> Result<F::Connection, RpcError> {
        self.assert_io_allowed("json-rpc project connection open");
        let connection = self.factory.open(ConnectionKind::Control)?;
        self.setup_connection(connection, None)
    }

    pub fn open_session_subscription<Req>(
        &mut self,
        session_id: &str,
        request_id: &str,
        method: &str,
        request: Req,
    ) -> Result<(F::Connection, String), RpcError>
    where
        Req: Serialize,
    {
        self.assert_io_allowed("json-rpc subscription open");
        let connection = self.factory.open(ConnectionKind::Subscription)?;
        let mut connection = self.setup_connection(connection, Some(session_id))?;
        let mut rpc = JsonRpcConnection::new(connection);
        match call_fixed_with_optional_timeout::<_, Req, SubscribeResponse>(
            &mut rpc,
            request_id,
            method,
            &request,
            self.unary_receive_timeout,
        ) {
            Ok(response) => {
                connection = rpc.into_connection();
                Ok((connection, response.stream))
            }
            Err(error) => {
                let _ = rpc.close();
                Err(error)
            }
        }
    }

    pub fn subscribe_raw<Req>(
        &mut self,
        session_id: &str,
        route: SubscriptionRoute,
        request: Req,
    ) -> Result<RawSubscription<F::Connection>, RpcError>
    where
        Req: Serialize,
    {
        let (connection, stream_id) =
            self.open_session_subscription(session_id, route.request_id, route.method, request)?;
        Ok(RawSubscription::new(connection, route, stream_id)
            .with_item_read_timeout(self.subscription_item_read_timeout))
    }

    pub fn subscribe_session_activity(
        &mut self,
        request: SessionActivitySubscribeRequest,
    ) -> Result<TypedSubscription<F::Connection, SessionActivityEventParams>, RpcError> {
        let session_id = request.session_id.clone();
        let raw = self.subscribe_raw(
            &session_id,
            SubscriptionRoute {
                request_id: REQUEST_ID_SUBSCRIBE_SESSION_ACTIVITY,
                method: METHOD_SESSION_SUBSCRIBE_ACTIVITY,
                event_method: "session.activity",
                complete_method: "session.activity.complete",
            },
            request,
        )?;
        Ok(TypedSubscription::new(raw))
    }

    pub fn subscribe_prompt_activity(
        &mut self,
        request: PromptActivitySubscribeRequest,
    ) -> Result<TypedSubscription<F::Connection, PromptActivityEventParams>, RpcError> {
        let session_id = request.session_id.clone();
        let raw = self.subscribe_raw(
            &session_id,
            SubscriptionRoute {
                request_id: REQUEST_ID_SUBSCRIBE_PROMPT_ACTIVITY,
                method: METHOD_PROMPT_SUBSCRIBE_ACTIVITY,
                event_method: "prompt.activity",
                complete_method: "prompt.activity.complete",
            },
            request,
        )?;
        Ok(TypedSubscription::new(raw))
    }

    pub fn get_session_main_view(
        &mut self,
        request: SessionMainViewRequest,
    ) -> Result<SessionMainViewResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_GET_MAIN_VIEW, request)
    }

    pub fn get_auth_status(&mut self) -> Result<AuthStatusResponse, RpcError> {
        self.call_unscoped_control_typed(METHOD_AUTH_GET_STATUS, AuthStatusRequest {})
    }

    pub fn list_processes(
        &mut self,
        request: ProcessListRequest,
    ) -> Result<ProcessListResponse, RpcError> {
        self.call_control_typed(METHOD_PROCESS_LIST, request)
    }

    pub fn kill_process(&mut self, request: ProcessKillRequest) -> Result<(), RpcError> {
        self.call_control_typed::<_, ProcessKillResponse>(METHOD_PROCESS_KILL, request)
            .map(|_| ())
    }

    pub fn inline_process_output(
        &mut self,
        request: ProcessInlineOutputRequest,
    ) -> Result<ProcessInlineOutputResponse, RpcError> {
        self.call_control_typed(METHOD_PROCESS_INLINE_OUTPUT, request)
    }

    pub fn activate_session_runtime(
        &mut self,
        request: SessionRuntimeActivateRequest,
    ) -> Result<SessionRuntimeActivateResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_RUNTIME_ACTIVATE, request)
    }

    pub fn plan_session(
        &mut self,
        request: SessionPlanRequest,
    ) -> Result<SessionPlanResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_PLAN, request)
    }

    pub fn release_session_runtime(
        &mut self,
        request: SessionRuntimeReleaseRequest,
    ) -> Result<SessionRuntimeReleaseResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_RUNTIME_RELEASE, request)
    }

    pub fn resolve_session_transition(
        &mut self,
        request: SessionResolveTransitionRequest,
    ) -> Result<SessionResolveTransitionResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_RESOLVE_TRANSITION, request)
    }

    pub fn get_session_initial_input(
        &mut self,
        request: SessionInitialInputRequest,
    ) -> Result<SessionInitialInputResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_GET_INITIAL_INPUT, request)
    }

    pub fn persist_session_input_draft(
        &mut self,
        request: SessionPersistInputDraftRequest,
    ) -> Result<SessionPersistInputDraftResponse, RpcError> {
        self.call_control_typed(METHOD_SESSION_PERSIST_INPUT_DRAFT, request)
    }

    pub fn answer_ask(&mut self, request: AskAnswerRequest) -> Result<(), RpcError> {
        self.call_control_typed::<_, RuntimeEmptyResponse>(METHOD_ASK_ANSWER, request)
            .map(|_| ())
    }

    pub fn answer_approval(&mut self, request: ApprovalAnswerRequest) -> Result<(), RpcError> {
        self.call_control_typed::<_, RuntimeEmptyResponse>(METHOD_APPROVAL_ANSWER, request)
            .map(|_| ())
    }

    pub fn submit_runtime_user_turn(
        &mut self,
        request: RuntimeSubmitUserTurnRequest,
    ) -> Result<RuntimeSubmitUserTurnResponse, RpcError> {
        self.call_dedicated_typed(
            REQUEST_ID_RUNTIME_SUBMIT_USER_TURN,
            METHOD_RUNTIME_SUBMIT_USER_TURN,
            request,
        )
    }

    pub fn submit_runtime_user_shell_command(
        &mut self,
        request: RuntimeSubmitUserShellCommandRequest,
    ) -> Result<(), RpcError> {
        self.call_dedicated_typed::<_, RuntimeEmptyResponse>(
            REQUEST_ID_RUNTIME_SUBMIT_USER_SHELL_COMMAND,
            METHOD_RUNTIME_SUBMIT_USER_SHELL_COMMAND,
            request,
        )
        .map(|_| ())
    }

    pub fn compact_runtime_context(
        &mut self,
        request: RuntimeCompactContextRequest,
    ) -> Result<(), RpcError> {
        self.call_dedicated_typed::<_, RuntimeEmptyResponse>(
            REQUEST_ID_RUNTIME_COMPACT_CONTEXT,
            METHOD_RUNTIME_COMPACT_CONTEXT,
            request,
        )
        .map(|_| ())
    }

    pub fn interrupt_runtime(&mut self, request: RuntimeInterruptRequest) -> Result<(), RpcError> {
        self.call_dedicated_typed::<_, RuntimeEmptyResponse>(
            REQUEST_ID_RUNTIME_INTERRUPT,
            METHOD_RUNTIME_INTERRUPT,
            request,
        )
        .map(|_| ())
    }

    pub fn queue_runtime_user_message(
        &mut self,
        request: RuntimeQueueUserMessageRequest,
    ) -> Result<RuntimeQueueUserMessageResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_QUEUE_USER_MESSAGE, request)
    }

    pub fn has_runtime_queued_user_work(
        &mut self,
        request: RuntimeHasQueuedUserWorkRequest,
    ) -> Result<RuntimeHasQueuedUserWorkResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_HAS_QUEUED_USER_WORK, request)
    }

    pub fn submit_runtime_queued_user_messages(
        &mut self,
        request: RuntimeSubmitQueuedUserMessagesRequest,
    ) -> Result<RuntimeSubmitQueuedUserMessagesResponse, RpcError> {
        self.call_dedicated_typed(
            REQUEST_ID_RUNTIME_SUBMIT_QUEUED_USER_MESSAGES,
            METHOD_RUNTIME_SUBMIT_QUEUED_USER_MESSAGES,
            request,
        )
    }

    pub fn discard_runtime_queued_user_message(
        &mut self,
        request: RuntimeDiscardQueuedUserMessageRequest,
    ) -> Result<RuntimeDiscardQueuedUserMessageResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_DISCARD_QUEUED_USER_MESSAGE, request)
    }

    pub fn show_runtime_goal(
        &mut self,
        request: RuntimeGoalShowRequest,
    ) -> Result<RuntimeGoalShowResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_GOAL_SHOW, request)
    }

    pub fn set_runtime_goal(
        &mut self,
        request: RuntimeGoalSetRequest,
    ) -> Result<RuntimeGoalShowResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_GOAL_SET, request)
    }

    pub fn pause_runtime_goal(
        &mut self,
        request: RuntimeGoalStatusRequest,
    ) -> Result<RuntimeGoalShowResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_GOAL_PAUSE, request)
    }

    pub fn resume_runtime_goal(
        &mut self,
        request: RuntimeGoalStatusRequest,
    ) -> Result<RuntimeGoalShowResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_GOAL_RESUME, request)
    }

    pub fn clear_runtime_goal(
        &mut self,
        request: RuntimeGoalClearRequest,
    ) -> Result<RuntimeGoalShowResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_GOAL_CLEAR, request)
    }

    pub fn set_runtime_session_name(
        &mut self,
        request: RuntimeSetSessionNameRequest,
    ) -> Result<(), RpcError> {
        self.call_control_typed::<_, RuntimeEmptyResponse>(METHOD_RUNTIME_SET_SESSION_NAME, request)
            .map(|_| ())
    }

    pub fn set_runtime_thinking_level(
        &mut self,
        request: RuntimeSetThinkingLevelRequest,
    ) -> Result<(), RpcError> {
        self.call_control_typed::<_, RuntimeEmptyResponse>(
            METHOD_RUNTIME_SET_THINKING_LEVEL,
            request,
        )
        .map(|_| ())
    }

    pub fn set_runtime_fast_mode_enabled(
        &mut self,
        request: RuntimeSetFastModeEnabledRequest,
    ) -> Result<RuntimeSetFastModeEnabledResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_SET_FAST_MODE_ENABLED, request)
    }

    pub fn set_runtime_reviewer_enabled(
        &mut self,
        request: RuntimeSetReviewerEnabledRequest,
    ) -> Result<RuntimeSetReviewerEnabledResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_SET_REVIEWER_ENABLED, request)
    }

    pub fn set_runtime_auto_compaction_enabled(
        &mut self,
        request: RuntimeSetAutoCompactionEnabledRequest,
    ) -> Result<RuntimeSetAutoCompactionEnabledResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_SET_AUTO_COMPACTION_ENABLED, request)
    }

    pub fn set_runtime_questions_enabled(
        &mut self,
        request: RuntimeSetQuestionsEnabledRequest,
    ) -> Result<RuntimeSetQuestionsEnabledResponse, RpcError> {
        self.call_control_typed(METHOD_RUNTIME_SET_QUESTIONS_ENABLED, request)
    }

    pub fn record_runtime_prompt_history(
        &mut self,
        request: RuntimeRecordPromptHistoryRequest,
    ) -> Result<(), RpcError> {
        self.call_control_typed::<_, RuntimeEmptyResponse>(
            METHOD_RUNTIME_RECORD_PROMPT_HISTORY,
            request,
        )
        .map(|_| ())
    }

    pub fn append_runtime_committed_entry(
        &mut self,
        request: RuntimeAppendCommittedEntryRequest,
    ) -> Result<(), RpcError> {
        self.call_control_typed::<_, RuntimeEmptyResponse>(
            METHOD_RUNTIME_APPEND_COMMITTED_ENTRY,
            request,
        )
        .map(|_| ())
    }

    pub fn list_worktrees(
        &mut self,
        request: WorktreeListRequest,
    ) -> Result<WorktreeListResponse, RpcError> {
        self.call_control_typed(METHOD_WORKTREE_LIST, request)
    }

    pub fn switch_worktree(
        &mut self,
        request: WorktreeSwitchRequest,
    ) -> Result<WorktreeSwitchResponse, RpcError> {
        self.call_control_typed(METHOD_WORKTREE_SWITCH, request)
    }

    pub fn delete_worktree(
        &mut self,
        request: WorktreeDeleteRequest,
    ) -> Result<WorktreeDeleteResponse, RpcError> {
        self.call_control_typed(METHOD_WORKTREE_DELETE, request)
    }

    pub fn create_worktree(
        &mut self,
        request: WorktreeCreateRequest,
    ) -> Result<WorktreeCreateResponse, RpcError> {
        self.call_control_typed(METHOD_WORKTREE_CREATE, request)
    }

    pub fn resolve_worktree_create_target(
        &mut self,
        request: WorktreeCreateTargetResolveRequest,
    ) -> Result<WorktreeCreateTargetResolveResponse, RpcError> {
        self.call_control_typed(METHOD_WORKTREE_CREATE_TARGET_RESOLVE, request)
    }

    pub fn call_dedicated<Req>(
        &mut self,
        request_id: &str,
        method: &str,
        request: Req,
    ) -> Result<(Value, F::Connection), RpcError>
    where
        Req: Serialize,
    {
        self.assert_io_allowed("json-rpc dedicated call");
        let connection = self.factory.open(ConnectionKind::Dedicated)?;
        let connection = self.setup_connection(connection, None)?;
        let mut rpc = JsonRpcConnection::new(connection);
        match call_fixed_with_optional_timeout(
            &mut rpc,
            request_id,
            method,
            &request,
            self.unary_receive_timeout,
        ) {
            Ok(response) => Ok((response, rpc.into_connection())),
            Err(error) => {
                let _ = rpc.close();
                Err(error)
            }
        }
    }

    pub fn call_control<Req>(
        &mut self,
        method: &str,
        request: Req,
    ) -> Result<(Value, F::Connection), RpcError>
    where
        Req: Serialize,
    {
        self.assert_io_allowed("json-rpc control call");
        let connection = self.factory.open(ConnectionKind::Control)?;
        let connection = self.setup_connection(connection, None)?;
        let mut rpc = JsonRpcConnection::new(connection);
        match call_with_optional_timeout(&mut rpc, method, &request, self.unary_receive_timeout) {
            Ok(response) => Ok((response, rpc.into_connection())),
            Err(error) => {
                let _ = rpc.close();
                Err(error)
            }
        }
    }

    fn call_dedicated_typed<Req, Resp>(
        &mut self,
        request_id: &str,
        method: &str,
        request: Req,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        let (response, _connection) = self.call_dedicated(request_id, method, request)?;
        decode_response(response)
    }

    fn call_control_typed<Req, Resp>(
        &mut self,
        method: &str,
        request: Req,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        let (response, _connection) = self.call_control(method, request)?;
        decode_response(response)
    }

    fn call_unscoped_control_typed<Req, Resp>(
        &mut self,
        method: &str,
        request: Req,
    ) -> Result<Resp, RpcError>
    where
        Req: Serialize,
        Resp: DeserializeOwned,
    {
        self.assert_io_allowed("json-rpc unscoped control call");
        let connection = self.factory.open(ConnectionKind::Control)?;
        let connection = self.setup_unscoped_connection(connection)?;
        let mut rpc = JsonRpcConnection::new(connection);
        match call_with_optional_timeout(&mut rpc, method, &request, self.unary_receive_timeout) {
            Ok(response) => decode_response(response),
            Err(error) => {
                let _ = rpc.close();
                Err(error)
            }
        }
    }

    fn setup_unscoped_connection(
        &self,
        connection: F::Connection,
    ) -> Result<F::Connection, RpcError> {
        self.assert_io_allowed("json-rpc unscoped connection setup");
        let mut rpc = JsonRpcConnection::new(connection);
        if let Err(error) = call_fixed_with_optional_timeout::<_, _, HandshakeResponse>(
            &mut rpc,
            REQUEST_ID_HANDSHAKE,
            METHOD_HANDSHAKE,
            &HandshakeRequest {
                protocol_version: PROTOCOL_VERSION.to_owned(),
            },
            self.unary_receive_timeout,
        ) {
            let _ = rpc.close();
            return Err(error);
        }
        Ok(rpc.into_connection())
    }

    fn setup_connection(
        &self,
        connection: F::Connection,
        session_id: Option<&str>,
    ) -> Result<F::Connection, RpcError> {
        self.assert_io_allowed("json-rpc scoped connection setup");
        let mut rpc = JsonRpcConnection::new(connection);
        if let Err(error) = call_fixed_with_optional_timeout::<_, _, HandshakeResponse>(
            &mut rpc,
            REQUEST_ID_HANDSHAKE,
            METHOD_HANDSHAKE,
            &HandshakeRequest {
                protocol_version: PROTOCOL_VERSION.to_owned(),
            },
            self.unary_receive_timeout,
        ) {
            let _ = rpc.close();
            return Err(error);
        }
        if !self.context.project_id.is_empty()
            && let Err(error) = call_fixed_with_optional_timeout::<_, _, AttachResponse>(
                &mut rpc,
                REQUEST_ID_PROJECT_ATTACH,
                METHOD_PROJECT_ATTACH,
                &self.context.attach_project_request(),
                self.unary_receive_timeout,
            )
        {
            let _ = rpc.close();
            return Err(error);
        }
        if let Some(session_id) = session_id
            && let Err(error) = call_fixed_with_optional_timeout::<_, _, AttachResponse>(
                &mut rpc,
                REQUEST_ID_SESSION_ATTACH,
                METHOD_SESSION_ATTACH,
                &AttachSessionRequest {
                    session_id: session_id.trim().to_owned(),
                },
                self.unary_receive_timeout,
            )
        {
            let _ = rpc.close();
            return Err(error);
        }
        Ok(rpc.into_connection())
    }

    fn assert_io_allowed(&self, operation: &'static str) {
        if let Some(guard) = &self.io_guard {
            guard.assert_rpc_io_allowed(operation);
        }
    }
}

fn decode_response<Resp>(response: Value) -> Result<Resp, RpcError>
where
    Resp: DeserializeOwned,
{
    serde_json::from_value(response).map_err(|error| RpcError::Decode(error.to_string()))
}

fn call_with_optional_timeout<C, Req, Resp>(
    rpc: &mut JsonRpcConnection<C>,
    method: &str,
    request: &Req,
    timeout: Option<Duration>,
) -> Result<Resp, RpcError>
where
    C: FrameConnection,
    Req: Serialize,
    Resp: DeserializeOwned,
{
    match timeout {
        Some(timeout) => rpc.call_with_timeout(method, request, timeout),
        None => rpc.call(method, request),
    }
}

fn call_fixed_with_optional_timeout<C, Req, Resp>(
    rpc: &mut JsonRpcConnection<C>,
    id: &str,
    method: &str,
    request: &Req,
    timeout: Option<Duration>,
) -> Result<Resp, RpcError>
where
    C: FrameConnection,
    Req: Serialize,
    Resp: DeserializeOwned,
{
    match timeout {
        Some(timeout) => rpc.call_fixed_with_timeout(id, method, request, timeout),
        None => rpc.call_fixed(id, method, request),
    }
}
