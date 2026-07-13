import type { AttentionNotificationEventHandler } from "./attentionNotifications";
import { attentionNotificationRpcHandler } from "./attentionNotificationSubscription";
import { parseRpcResponse as parse } from "./clientParse";
import * as taskLifecycle from "./clientTaskLifecycle";
import {
  workflowGraphDraftPayload,
  workflowGraphMetadataPayload,
  workflowGraphSaveConfirmationPayload,
} from "./clientWorkflowGraph";
import type {
  QuestionAnswerInput,
  TaskEditInput,
  TaskMoveInput,
  TaskMutationInput,
  WorkflowCreateAndLinkInput,
  WorkflowCreateInput,
  WorkflowDeleteInput,
  WorkflowGraphDeriveWiringInput,
  WorkflowGraphSaveInput,
  WorkflowGraphSavePreviewInput,
  WorkflowGraphValidateDraftInput,
  WorkflowScriptPathValidateInput,
  WorkflowListInput,
  WorkflowProjectLinkInput,
} from "./clientInputs";
import { compactJsonObject, emptyJsonObject } from "./json";
import type { SetupOperationID } from "./setupOperationID";
import {
  worktreeSetupRpcHandler,
  type WorktreeSetupEventHandler,
} from "./worktreeSetup";
import type {
  ActivityPage,
  AttentionPage,
  BindingPlan,
  BoardNodeCardsPage,
  CommentPage,
  PendingAsk,
  ProjectWorkflowLink,
  ProjectBinding,
  ProjectEdit,
  ProjectDeleteResponse,
  ProjectMutationResponse,
  ProjectPage,
  ServerReadiness,
  TaskComment,
  TaskDetail,
  TaskMoveResponse,
  WorkflowBoard,
  WorkflowDeleteImpact,
  WorkflowDeleteResponse,
  WorkflowDefinition,
  WorkflowDerivedWiring,
  WorkflowGraphSavePreview,
  WorkflowGraphSaveResult,
  WorkflowGraphValidateDraftResult,
  WorkflowPage,
  WorkflowRecord,
  WorkflowValidation,
  WorkspaceList,
  WorkspaceUnlinkResponse,
} from "./models";
import {
  bindingPlanSchema,
  projectCreateSchema,
  projectDeleteResponseSchema,
  projectEditSchema,
  projectMutationResponseSchema,
  projectPageSchema,
  workspaceListSchema,
  workspaceUnlinkResponseSchema,
} from "./schemas/project";
import { readinessSchema } from "./schemas/status";
import {
  activityPageSchema,
  attentionPageSchema,
  boardNodeCardsPageSchema,
  commentAddResponseSchema,
  commentPageSchema,
  pendingAskListSchema,
  projectWorkflowLinksSchema,
  taskCreateResponseSchema,
  taskDetailSchema,
  taskUpdateResponseSchema,
  workflowBoardSchema,
} from "./schemas/workflowBoard";
import {
  workflowCreateAndLinkSchema,
  workflowCreateSchema,
  workflowDeletePreviewSchema,
  workflowDeleteResponseSchema,
  workflowDefinitionSchema,
  workflowGraphDeriveWiringSchema,
  workflowGraphSavePreviewSchema,
  workflowGraphSaveSchema,
  workflowGraphValidateDraftSchema,
  workflowLinkProjectSchema,
  workflowListSchema,
  workflowValidationSchema,
} from "./schemas/workflow";
import type { RpcEventHandler, RpcSubscription, RpcTransport } from "./transport";

export const guiTaskCommentAuthor = "user";

export class ApiClient {
  readonly transport: RpcTransport;

  constructor(transport: RpcTransport) {
    this.transport = transport;
  }

  async getReadiness(): Promise<ServerReadiness> {
    return parse(
      "server.readiness.get",
      readinessSchema,
      await this.transport.call("server.readiness.get", emptyJsonObject),
    );
  }

  async listProjects(pageToken: string): Promise<ProjectPage> {
    return parse(
      "project.home.list",
      projectPageSchema,
      await this.transport.call("project.home.list", { page_size: 40, page_token: pageToken }),
    );
  }

  async listWorkspaces(projectID: string, pageToken = ""): Promise<WorkspaceList> {
    return parse(
      "project.workspace.list",
      workspaceListSchema,
      await this.transport.call("project.workspace.list", {
        project_id: projectID,
        page_size: 100,
        page_token: pageToken,
      }),
    );
  }

  async getProjectEdit(projectID: string, pageToken = ""): Promise<ProjectEdit> {
    return parse(
      "project.edit.get",
      projectEditSchema,
      await this.transport.call("project.edit.get", {
        project_id: projectID,
        page_size: 100,
        page_token: pageToken,
      }),
    );
  }

  async planWorkspace(path: string): Promise<BindingPlan> {
    return parse(
      "project.planWorkspaceBinding",
      bindingPlanSchema,
      await this.transport.call("project.planWorkspaceBinding", { path, mode: "interactive" }),
    );
  }

  async createProject(
    displayName: string,
    projectKey: string,
    workspaceRoot: string,
  ): Promise<ProjectBinding> {
    return parse(
      "project.create",
      projectCreateSchema,
      await this.transport.call("project.create", {
        display_name: displayName,
        project_key: projectKey,
        workspace_root: workspaceRoot,
      }),
    );
  }

  async attachWorkspace(projectID: string, workspaceRoot: string): Promise<ProjectBinding> {
    return parse(
      "project.attachWorkspace",
      projectCreateSchema,
      await this.transport.call("project.attachWorkspace", {
        project_id: projectID,
        workspace_root: workspaceRoot,
      }),
    );
  }

  async updateProject(
    projectID: string,
    displayName: string,
    projectKey = "",
  ): Promise<ProjectMutationResponse> {
    return parse(
      "project.update",
      projectMutationResponseSchema,
      await this.transport.call("project.update", {
        project_id: projectID,
        display_name: displayName,
        project_key: projectKey,
      }),
    );
  }

  async setDefaultWorkspace(projectID: string, workspaceID: string): Promise<ProjectMutationResponse> {
    return parse(
      "project.defaultWorkspace.set",
      projectMutationResponseSchema,
      await this.transport.call("project.defaultWorkspace.set", {
        project_id: projectID,
        workspace_id: workspaceID,
      }),
    );
  }

  async unlinkWorkspace(projectID: string, workspaceID: string): Promise<WorkspaceUnlinkResponse> {
    return parse(
      "project.unlinkWorkspace",
      workspaceUnlinkResponseSchema,
      await this.transport.call("project.unlinkWorkspace", {
        project_id: projectID,
        workspace_id: workspaceID,
      }),
    );
  }

  async deleteProject(projectID: string): Promise<ProjectDeleteResponse> {
    return parse(
      "project.delete",
      projectDeleteResponseSchema,
      await this.transport.call("project.delete", { project_id: projectID }),
    );
  }

  async getBoard(projectID: string, workflowID: string): Promise<WorkflowBoard> {
    return parse(
      "workflow.board.get",
      workflowBoardSchema,
      await this.transport.call(
        "workflow.board.get",
        compactJsonObject({
          project_id: projectID,
          workflow_id: workflowID.length > 0 ? workflowID : undefined,
        }),
      ),
    );
  }

  async getWorkflow(workflowID: string): Promise<WorkflowDefinition> {
    return parse(
      "workflow.get",
      workflowDefinitionSchema,
      await this.transport.call("workflow.get", { workflow_id: workflowID }),
    );
  }

  async listWorkflows(input: WorkflowListInput = {}): Promise<WorkflowPage> {
    return parse(
      "workflow.list",
      workflowListSchema,
      await this.transport.call(
        "workflow.list",
        compactJsonObject({
          page_size: input.pageSize ?? 40,
          page_token: input.pageToken ?? "",
          query: input.query ?? "",
        }),
      ),
    );
  }

  async createWorkflow(input: WorkflowCreateInput): Promise<WorkflowRecord> {
    return parse(
      "workflow.create",
      workflowCreateSchema,
      await this.transport.call(
        "workflow.create",
        compactJsonObject({
          name: input.name,
          description: input.description,
        }),
      ),
    );
  }

  async createAndLinkWorkflowToProject(
    input: WorkflowCreateAndLinkInput,
  ): Promise<Readonly<{ workflow: WorkflowRecord; link: ProjectWorkflowLink }>> {
    return parse(
      "workflow.createAndLinkProject",
      workflowCreateAndLinkSchema,
      await this.transport.call(
        "workflow.createAndLinkProject",
        compactJsonObject({
          name: input.name,
          description: input.description,
          project_id: input.projectID,
          default_policy: "if_project_has_none",
        }),
      ),
    );
  }

  async linkWorkflowToProject(input: WorkflowProjectLinkInput): Promise<ProjectWorkflowLink> {
    return parse(
      "workflow.linkProject",
      workflowLinkProjectSchema,
      await this.transport.call(
        "workflow.linkProject",
        compactJsonObject({
          project_id: input.projectID,
          workflow_id: input.workflowID,
          default_policy: "if_project_has_none",
        }),
      ),
    );
  }

  async validateWorkflow(
    workflowID: string,
    mode: "draft" | "task_creation" | "execution",
  ): Promise<WorkflowValidation> {
    return parse(
      "workflow.validate",
      workflowValidationSchema,
      await this.transport.call("workflow.validate", { workflow_id: workflowID, mode }),
    );
  }

  async validateWorkflowScriptPath(input: WorkflowScriptPathValidateInput): Promise<WorkflowValidation> {
    return parse(
      "workflow.scriptPath.validate",
      workflowValidationSchema,
      await this.transport.call(
        "workflow.scriptPath.validate",
        compactJsonObject({
          workflow_id: input.workflowID,
          node_id: input.nodeID,
          script_path: input.scriptPath,
        }),
      ),
    );
  }

  async validateWorkflowGraphDraft(
    input: WorkflowGraphValidateDraftInput,
  ): Promise<WorkflowGraphValidateDraftResult> {
    return parse(
      "workflow.graph.validateDraft",
      workflowGraphValidateDraftSchema,
      await this.transport.call(
        "workflow.graph.validateDraft",
        compactJsonObject({
          workflow_id: input.workflowID,
          metadata: workflowGraphMetadataPayload(input.metadata),
          graph: workflowGraphDraftPayload(input.graph),
          modes: input.modes,
        }),
      ),
    );
  }

  async deriveWorkflowGraphWiring(input: WorkflowGraphDeriveWiringInput): Promise<WorkflowDerivedWiring> {
    return parse(
      "workflow.graph.deriveWiring",
      workflowGraphDeriveWiringSchema,
      await this.transport.call(
        "workflow.graph.deriveWiring",
        compactJsonObject({
          workflow_id: input.workflowID,
          graph: workflowGraphDraftPayload(input.graph),
        }),
      ),
    );
  }

  async previewWorkflowGraphSave(input: WorkflowGraphSavePreviewInput): Promise<WorkflowGraphSavePreview> {
    return parse(
      "workflow.graph.savePreview",
      workflowGraphSavePreviewSchema,
      await this.transport.call(
        "workflow.graph.savePreview",
        compactJsonObject({
          workflow_id: input.workflowID,
          expected_version: input.expectedVersion,
          metadata: workflowGraphMetadataPayload(input.metadata),
          graph: workflowGraphDraftPayload(input.graph),
        }),
      ),
    );
  }

  async saveWorkflowGraph(input: WorkflowGraphSaveInput): Promise<WorkflowGraphSaveResult> {
    return parse(
      "workflow.graph.save",
      workflowGraphSaveSchema,
      await this.transport.call(
        "workflow.graph.save",
        compactJsonObject({
          workflow_id: input.workflowID,
          expected_version: input.expectedVersion,
          metadata: workflowGraphMetadataPayload(input.metadata),
          graph: workflowGraphDraftPayload(input.graph),
          confirmation: workflowGraphSaveConfirmationPayload(input.confirmation),
        }),
      ),
    );
  }

  async previewWorkflowDelete(workflowID: string): Promise<WorkflowDeleteImpact> {
    return parse(
      "workflow.deletePreview",
      workflowDeletePreviewSchema,
      await this.transport.call("workflow.deletePreview", { workflow_id: workflowID }),
    );
  }

  async deleteWorkflow(input: WorkflowDeleteInput): Promise<WorkflowDeleteResponse> {
    return parse(
      "workflow.delete",
      workflowDeleteResponseSchema,
      await this.transport.call(
        "workflow.delete",
        compactJsonObject({
          workflow_id: input.workflowID,
          confirmed: input.confirmed,
          expected_version: input.expectedVersion,
          expected_project_count: input.expectedProjectCount,
          expected_link_count: input.expectedLinkCount,
          expected_task_count: input.expectedTaskCount,
          cleanup_artifacts: input.cleanupArtifacts,
        }),
      ),
    );
  }

  async listProjectWorkflowLinks(projectID: string): Promise<readonly ProjectWorkflowLink[]> {
    return parse(
      "workflow.listProjectLinks",
      projectWorkflowLinksSchema,
      await this.transport.call("workflow.listProjectLinks", { project_id: projectID }),
    );
  }

  async listBoardNodeCards(
    projectID: string,
    workflowID: string,
    nodeID: string,
    pageToken = "",
  ): Promise<BoardNodeCardsPage> {
    return parse(
      "workflow.board.nodeCards.list",
      boardNodeCardsPageSchema,
      await this.transport.call(
        "workflow.board.nodeCards.list",
        compactJsonObject({
          project_id: projectID,
          workflow_id: workflowID,
          node_id: nodeID,
          page_size: 100,
          page_token: pageToken,
        }),
      ),
    );
  }

  async listAttention(projectID: string, pageToken: string): Promise<AttentionPage> {
    return parse(
      "workflow.attention.list",
      attentionPageSchema,
      await this.transport.call(
        "workflow.attention.list",
        compactJsonObject({
          project_id: projectID.length > 0 ? projectID : undefined,
          page_size: 40,
          page_token: pageToken,
        }),
      ),
    );
  }

  async createTask(input: TaskMutationInput): Promise<string> {
    const response = parse(
      "workflow.task.create",
      taskCreateResponseSchema,
      await this.transport.call(
        "workflow.task.create",
        compactJsonObject({
          project_id: input.projectID,
          workflow_id: input.workflowID,
          title: input.title,
          body: input.body,
          source_workspace_id: input.sourceWorkspaceID,
        }),
      ),
    );
    return response.task.id;
  }

  async updateTask(input: TaskEditInput): Promise<string> {
    const response = parse(
      "workflow.task.update",
      taskUpdateResponseSchema,
      await this.transport.call(
        "workflow.task.update",
        compactJsonObject({
          task_id: input.taskID,
          title: input.title,
          body: input.body,
          source_workspace_id: input.sourceWorkspaceID,
        }),
      ),
    );
    return response.task.id;
  }

  async startTask(taskID: string, setupOperationID?: SetupOperationID): Promise<void> {
    await taskLifecycle.startTask(this.transport, taskID, setupOperationID);
  }

  async moveTask(input: TaskMoveInput): Promise<TaskMoveResponse> {
    return taskLifecycle.moveTask(this.transport, input);
  }

  async interruptTask(taskID: string, sessionID?: string): Promise<void> {
    await this.transport.call(
      "workflow.task.interrupt",
      compactJsonObject({ task_id: taskID, session_id: sessionID }),
    );
  }

  async resumeTask(taskID: string): Promise<void> {
    await this.transport.call("workflow.task.resume", compactJsonObject({ task_id: taskID }));
  }

  async approveTransition(
    taskTransitionID: string,
    setupOperationID?: SetupOperationID,
  ): Promise<void> {
    await taskLifecycle.approveTransition(this.transport, taskTransitionID, setupOperationID);
  }

  async cancelTask(taskID: string): Promise<void> {
    await this.transport.call("workflow.task.cancel", { task_id: taskID });
  }

  async deleteTask(taskID: string): Promise<void> {
    await this.transport.call("workflow.task.delete", { task_id: taskID });
  }

  async getTask(taskID: string): Promise<TaskDetail> {
    return parse(
      "workflow.task.get",
      taskDetailSchema,
      await this.transport.call("workflow.task.get", { task_id: taskID }),
    );
  }

  async listTaskActivity(taskID: string, pageToken: string): Promise<ActivityPage> {
    return parse(
      "workflow.task.activity.list",
      activityPageSchema,
      await this.transport.call("workflow.task.activity.list", {
        task_id: taskID,
        page_size: 40,
        page_token: pageToken,
      }),
    );
  }

  async listTaskComments(taskID: string, pageToken: string): Promise<CommentPage> {
    return parse(
      "workflow.task.comment.list",
      commentPageSchema,
      await this.transport.call("workflow.task.comment.list", {
        task_id: taskID,
        page_size: 40,
        page_token: pageToken,
      }),
    );
  }

  async addComment(taskID: string, body: string): Promise<TaskComment> {
    return parse(
      "workflow.task.comment.add",
      commentAddResponseSchema,
      await this.transport.call("workflow.task.comment.add", {
        task_id: taskID,
        body,
        author: guiTaskCommentAuthor,
      }),
    ).comment;
  }

  async replaceComment(commentID: string, body: string): Promise<void> {
    await this.transport.call("workflow.task.comment.replace", { comment_id: commentID, body });
  }

  async deleteComment(commentID: string): Promise<void> {
    await this.transport.call("workflow.task.comment.delete", { comment_id: commentID });
  }

  async answerQuestion(input: QuestionAnswerInput): Promise<void> {
    const answer =
      input.kind === "approval"
        ? {
            approval: {
              decision: input.decision,
              commentary: input.commentary,
            },
          }
        : {
            selected_option_number: input.selectedOptionNumber,
            freeform_answer: input.freeformAnswer,
          };
    await this.transport.call(
      "workflow.task.question.answer",
      compactJsonObject({
        client_request_id: input.clientRequestID,
        task_id: input.taskID,
        run_id: input.runID,
        ask_id: input.askID,
        ...answer,
      }),
    );
  }

  async listPendingAsks(sessionID: string): Promise<readonly PendingAsk[]> {
    return parse(
      "ask.listPendingBySession",
      pendingAskListSchema,
      await this.transport.call("ask.listPendingBySession", { SessionID: sessionID }),
    );
  }

  subscribeProject(projectID: string, handler: RpcEventHandler): RpcSubscription {
    return this.transport.subscribe("workflow.subscribeProject", { project_id: projectID }, handler);
  }

  subscribeWorkflow(workflowID: string, handler: RpcEventHandler): RpcSubscription {
    return this.transport.subscribe("workflow.subscribe", { workflow_id: workflowID }, handler);
  }

  subscribeAttentionNotifications(handler: AttentionNotificationEventHandler): RpcSubscription {
    return this.transport.subscribe(
      "attention.notification.subscribe",
      emptyJsonObject,
      attentionNotificationRpcHandler(handler),
    );
  }

  subscribeWorktreeSetup(setupOperationID: SetupOperationID, handler: WorktreeSetupEventHandler): RpcSubscription {
    return this.transport.subscribe(
      "worktree.setup.subscribe",
      { setup_operation_id: setupOperationID.toJSONValue() },
      worktreeSetupRpcHandler(handler),
    );
  }
}
