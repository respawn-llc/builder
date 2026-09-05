import type { AttentionNotificationEventHandler } from "./attentionNotifications";
import { attentionNotificationRpcHandler } from "./attentionNotificationSubscription";
import { create, operationName } from "@app/server-api-contract";
import {
  ReadinessSeverity,
  ServerService,
  type Readiness,
} from "@app/server-api-contract/gen/kent/api/server/server_pb";
import type { ApiConnectionSource, ApiService, ApiSubscription } from "./apiService";
import type { ChatApi } from "./chat";
import { createChatApi } from "./chat";
import { listSessionPage as listSessionCatalogPage } from "./clientCatalog";
import { parseRpcResponse as parse } from "./clientParse";
import * as taskLifecycle from "./clientTaskLifecycle";
import * as taskDependencies from "./clientTaskDependencies";
import * as taskDetail from "./clientTaskDetail";
import * as promptAnswers from "./clientPromptAnswers";
import * as taskSearch from "./clientTaskSearch";
import * as worktree from "./clientWorktree";
import * as project from "./clientProject";
import * as processes from "./clientProcesses";
import {
  workflowGraphDraftPayload,
  workflowGraphMetadataPayload,
  workflowGraphSaveConfirmationPayload,
} from "./clientWorkflowGraph";
import type {
  BoardNodeCardsInput,
  PromptAnswerBatchInput,
  PromptAnswerBatchResponse,
  TaskEditInput,
  TaskMoveInput,
  TaskResumeInput,
  TaskStartInput,
  TaskMutationInput,
  ProjectTaskGroupCountsInput,
  TaskListInput,
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
import { workflowPageSize } from "./clientInputs";
import { compactJsonObject, emptyJsonObject } from "./json";
import type { SetupOperationID } from "./setupOperationID";
import type * as worktreeModels from "./schemas/worktree";
import { subscribeWorktreeSetup, type WorktreeSetupEventHandler } from "./worktreeSetup";
import type {
  ActivityPage,
  AttentionPage,
  BoardNodeCardsPage,
  CommentPage,
  CreatedTaskSummary,
  PendingAsk,
  ProjectWorkflowLink,
  ProjectPage,
  ServerReadiness,
  SessionCatalogPage,
  SessionCategory,
  TaskAttention,
  TaskComment,
  TaskDetail,
  TaskDependencyDirection,
  TaskDependencyListResponse,
  TaskDependencyMutationResponse,
  TaskApproveResponse,
  TaskMoveResponse,
  TaskMovePreviewResponse,
  TaskResumeResponse,
  TaskStartResponse,
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
} from "./models";
import type {
  ProjectLabel,
  ProjectLabelCatalog,
  ProjectTaskGroupCounts,
  TaskLabelAssignment,
  TaskListPage,
} from "./workflowLabels";
import type { BoardFilter } from "./workflowBoardFilters";
import { ContractError } from "./errors";
import { requireUnarySuccess } from "./protobufRpc";
import { workflowIDSchema } from "./schemas/workflowID";
import {
  attentionPageSchema,
  projectWorkflowLinksSchema,
  taskUpdateResponseSchema,
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
import type { DescriptorRpcTransport } from "./transport";
import type { WorkflowProjectEventHandler } from "./workflowProjectEvents";
import type { TaskSearchInput, TaskSearchResponse } from "./taskSearch";
import { workflowProjectEventRpcHandler } from "./workflowProjectEvents";
import * as workflowBoard from "./clientWorkflowBoard";
import * as workflowLabels from "./clientWorkflowLabels";

export const guiTaskCommentAuthor = "user";

export class ApiClient implements ApiService {
  readonly connection: ApiConnectionSource;
  readonly #transport: DescriptorRpcTransport;

  constructor(transport: DescriptorRpcTransport) {
    this.#transport = transport;
    this.connection = transport.connection;
    this.chat = createChatApi(transport);
  }

  readonly chat: ChatApi;

  listProcesses = async (projectID: string) => processes.listProcesses(this.#transport, projectID);
  killProcess = async (processID: string) => processes.killProcess(this.#transport, processID);

  async getReadiness(): Promise<ServerReadiness> {
    const method = ServerService.method.getReadiness;
    const success = requireUnarySuccess(
      method,
      await this.#transport.callDescriptor(method, create(method.input)),
    );
    if (success.readiness === undefined) {
      throw new ContractError(`${operationName(method)} response did not match GUI contract.`);
    }
    return projectReadiness(success.readiness);
  }

  async listProjects(pageToken: string | null): Promise<ProjectPage> {
    return project.listProjectHome(this.#transport, pageToken);
  }

  async listSessionPage(
    projectID: string,
    category: SessionCategory,
    offset: number,
  ): Promise<SessionCatalogPage> {
    return listSessionCatalogPage(this.#transport, projectID, category, offset);
  }

  listWorkspaces = async (projectID: string, offset: number) =>
    project.listWorkspaces(this.#transport, projectID, offset);
  getProjectWorkspace = async (
    projectID: string,
    selector: Readonly<{ workspaceID: string } | { workspaceRoot: string }>,
  ) => project.getProjectWorkspace(this.#transport, projectID, selector);
  getProjectEdit = async (projectID: string) => project.getProjectEdit(this.#transport, projectID);
  planWorkspace = async (path: string) => project.planWorkspace(this.#transport, path);
  createProject = async (displayName: string, projectKey: string, workspaceRoot: string) =>
    project.createProject(this.#transport, displayName, projectKey, workspaceRoot);
  attachWorkspace = async (projectID: string, workspaceRoot: string) =>
    project.attachWorkspace(this.#transport, projectID, workspaceRoot);
  updateProject = async (projectID: string, displayName: string, projectKey = "") =>
    project.updateProject(this.#transport, projectID, displayName, projectKey);
  setDefaultWorkspace = async (projectID: string, workspaceID: string) =>
    project.setDefaultWorkspace(this.#transport, projectID, workspaceID);
  unlinkWorkspace = async (projectID: string, workspaceID: string) =>
    project.unlinkWorkspace(this.#transport, projectID, workspaceID);
  deleteProject = async (projectID: string) => project.deleteProject(this.#transport, projectID);

  async listProjectLabels(projectID: string): Promise<ProjectLabelCatalog> {
    return workflowLabels.listProjectLabels(this.#transport, projectID);
  }

  async createProjectLabel(projectID: string, name: string): Promise<ProjectLabel> {
    return workflowLabels.createProjectLabel(this.#transport, projectID, name);
  }

  async reorderProjectLabels(projectID: string, labelIDs: readonly string[]): Promise<ProjectLabelCatalog> {
    return workflowLabels.reorderProjectLabels(this.#transport, projectID, labelIDs);
  }

  async renameProjectLabel(projectID: string, labelID: string, name: string): Promise<ProjectLabel> {
    return workflowLabels.renameProjectLabel(this.#transport, projectID, labelID, name);
  }

  async deleteProjectLabel(projectID: string, labelID: string): Promise<string> {
    return workflowLabels.deleteProjectLabel(this.#transport, projectID, labelID);
  }

  async getTaskLabels(taskID: string): Promise<TaskLabelAssignment> {
    return workflowLabels.getTaskLabels(this.#transport, taskID);
  }

  async updateTaskLabels(
    taskID: string,
    addLabelIDs: readonly string[],
    removeLabelIDs: readonly string[],
  ): Promise<TaskLabelAssignment> {
    return workflowLabels.updateTaskLabels(this.#transport, taskID, addLabelIDs, removeLabelIDs);
  }

  async getBoard(
    projectID: string,
    workflowID: string | undefined,
    filter: BoardFilter,
  ): Promise<WorkflowBoard> {
    return workflowBoard.getBoard(this.#transport, projectID, workflowID, filter);
  }

  async getWorkflow(workflowID: string): Promise<WorkflowDefinition> {
    return parse(
      "workflow.get",
      workflowDefinitionSchema,
      await this.#transport.call("workflow.get", { workflow_id: workflowIDSchema.parse(workflowID) }),
    );
  }

  async listWorkflows(input: WorkflowListInput = {}): Promise<WorkflowPage> {
    return parse(
      "workflow.list",
      workflowListSchema,
      await this.#transport.call(
        "workflow.list",
        compactJsonObject({
          offset: input.offset ?? 0,
          limit: input.limit ?? workflowPageSize,
          project_id: input.projectID,
          query: input.query ?? "",
        }),
      ),
    );
  }

  async createWorkflow(input: WorkflowCreateInput): Promise<WorkflowRecord> {
    return parse(
      "workflow.create",
      workflowCreateSchema,
      await this.#transport.call(
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
      await this.#transport.call(
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
      await this.#transport.call(
        "workflow.linkProject",
        compactJsonObject({
          project_id: input.projectID,
          workflow_id: workflowIDSchema.parse(input.workflowID),
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
      await this.#transport.call("workflow.validate", {
        workflow_id: workflowIDSchema.parse(workflowID),
        mode,
      }),
    );
  }

  async validateWorkflowScriptPath(input: WorkflowScriptPathValidateInput): Promise<WorkflowValidation> {
    return parse(
      "workflow.scriptPath.validate",
      workflowValidationSchema,
      await this.#transport.call(
        "workflow.scriptPath.validate",
        compactJsonObject({
          workflow_id: workflowIDSchema.parse(input.workflowID),
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
      await this.#transport.call(
        "workflow.graph.validateDraft",
        compactJsonObject({
          workflow_id: workflowIDSchema.parse(input.workflowID),
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
      await this.#transport.call(
        "workflow.graph.deriveWiring",
        compactJsonObject({
          workflow_id: workflowIDSchema.parse(input.workflowID),
          graph: workflowGraphDraftPayload(input.graph),
        }),
      ),
    );
  }

  async previewWorkflowGraphSave(input: WorkflowGraphSavePreviewInput): Promise<WorkflowGraphSavePreview> {
    return parse(
      "workflow.graph.savePreview",
      workflowGraphSavePreviewSchema,
      await this.#transport.call(
        "workflow.graph.savePreview",
        compactJsonObject({
          workflow_id: workflowIDSchema.parse(input.workflowID),
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
      await this.#transport.call(
        "workflow.graph.save",
        compactJsonObject({
          workflow_id: workflowIDSchema.parse(input.workflowID),
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
      await this.#transport.call("workflow.deletePreview", {
        workflow_id: workflowIDSchema.parse(workflowID),
      }),
    );
  }

  async deleteWorkflow(input: WorkflowDeleteInput): Promise<WorkflowDeleteResponse> {
    return parse(
      "workflow.delete",
      workflowDeleteResponseSchema,
      await this.#transport.call(
        "workflow.delete",
        compactJsonObject({
          workflow_id: workflowIDSchema.parse(input.workflowID),
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
      await this.#transport.call("workflow.listProjectLinks", { project_id: projectID }),
    );
  }

  async listBoardNodeCards(input: BoardNodeCardsInput): Promise<BoardNodeCardsPage> {
    return workflowBoard.listBoardNodeCards(this.#transport, input);
  }

  async listAttention(pageToken: string): Promise<AttentionPage> {
    return parse(
      "workflow.attention.list",
      attentionPageSchema,
      await this.#transport.call(
        "workflow.attention.list",
        compactJsonObject({
          page_size: 40,
          page_token: pageToken,
        }),
      ),
    );
  }

  async listTaskAttention(taskID: string): Promise<TaskAttention> {
    return taskDetail.listTaskAttention(this.#transport, taskID);
  }

  async createTask(input: TaskMutationInput): Promise<CreatedTaskSummary> {
    return workflowLabels.createTask(this.#transport, input);
  }

  async addTaskDependency(
    blockerTaskID: string,
    blockedTaskID: string,
  ): Promise<TaskDependencyMutationResponse> {
    return taskDependencies.addTaskDependency(this.#transport, blockerTaskID, blockedTaskID);
  }

  async removeTaskDependency(
    blockerTaskID: string,
    blockedTaskID: string,
  ): Promise<TaskDependencyMutationResponse> {
    return taskDependencies.removeTaskDependency(this.#transport, blockerTaskID, blockedTaskID);
  }

  async listTaskDependencies(
    taskID: string,
    direction?: TaskDependencyDirection,
  ): Promise<TaskDependencyListResponse> {
    return taskDependencies.listTaskDependencies(this.#transport, taskID, direction);
  }

  async listTasks(input: TaskListInput): Promise<TaskListPage> {
    return workflowLabels.listTasks(this.#transport, input);
  }

  async getProjectTaskGroupCounts(input: ProjectTaskGroupCountsInput): Promise<ProjectTaskGroupCounts> {
    return workflowLabels.getProjectTaskGroupCounts(this.#transport, input);
  }

  async searchTasks(input: TaskSearchInput, signal?: AbortSignal): Promise<TaskSearchResponse> {
    return taskSearch.searchTasks(this.#transport, input, signal);
  }

  async updateTask(input: TaskEditInput): Promise<string> {
    const response = parse(
      "workflow.task.update",
      taskUpdateResponseSchema,
      await this.#transport.call(
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

  async startTask(input: TaskStartInput): Promise<TaskStartResponse> {
    return taskLifecycle.startTask(this.#transport, input);
  }

  async moveTask(input: TaskMoveInput): Promise<TaskMoveResponse> {
    return taskLifecycle.moveTask(this.#transport, input);
  }

  async previewMoveTask(taskID: string, targetNodeID: string): Promise<TaskMovePreviewResponse> {
    return taskLifecycle.previewMoveTask(this.#transport, taskID, targetNodeID);
  }

  async interruptTask(taskID: string, sessionID?: string): Promise<void> {
    await this.#transport.call(
      "workflow.task.interrupt",
      compactJsonObject({ task_id: taskID, session_id: sessionID }),
    );
  }

  async resumeTask(input: TaskResumeInput): Promise<TaskResumeResponse> {
    return taskLifecycle.resumeTask(this.#transport, input);
  }

  async approveApproval(approvalID: string): Promise<TaskApproveResponse> {
    return taskLifecycle.approveApproval(this.#transport, approvalID);
  }

  async deleteTask(taskID: string): Promise<void> {
    await this.#transport.call("workflow.task.delete", { task_id: taskID });
  }

  async getTask(taskID: string): Promise<TaskDetail> {
    return taskDetail.getTask(this.#transport, taskID);
  }

  async listTaskActivity(taskID: string, offset: number): Promise<ActivityPage> {
    return taskDetail.listTaskActivity(this.#transport, taskID, offset);
  }

  async listTaskComments(taskID: string, offset: number): Promise<CommentPage> {
    return taskDetail.listTaskComments(this.#transport, taskID, offset);
  }

  async addComment(taskID: string, body: string): Promise<TaskComment> {
    return taskDetail.addComment(this.#transport, taskID, body, guiTaskCommentAuthor);
  }

  async replaceComment(commentID: string, body: string): Promise<void> {
    await this.#transport.call("workflow.task.comment.replace", { comment_id: commentID, body });
  }

  async deleteComment(commentID: string): Promise<void> {
    await this.#transport.call("workflow.task.comment.delete", { comment_id: commentID });
  }

  async answerPromptBatch(input: PromptAnswerBatchInput): Promise<PromptAnswerBatchResponse> {
    return promptAnswers.answerPromptBatch(this.#transport, input);
  }

  async listPendingAsks(sessionID: string): Promise<readonly PendingAsk[]> {
    return taskDetail.listPendingAsks(this.#transport, sessionID);
  }

  subscribeProject(projectID: string, handler: WorkflowProjectEventHandler): ApiSubscription {
    return this.#transport.subscribe(
      "workflow.subscribeProject",
      { project_id: projectID },
      workflowProjectEventRpcHandler("workflow.project", handler),
    );
  }

  subscribeWorkflow(workflowID: string, handler: WorkflowProjectEventHandler): ApiSubscription {
    return this.#transport.subscribe(
      "workflow.subscribe",
      { workflow_id: workflowIDSchema.parse(workflowID) },
      workflowProjectEventRpcHandler("workflow.event", handler),
    );
  }

  subscribeAttentionNotifications(handler: AttentionNotificationEventHandler): ApiSubscription {
    return this.#transport.subscribe(
      "attention.notification.subscribe",
      emptyJsonObject,
      attentionNotificationRpcHandler(handler),
    );
  }

  getWorktreeStatus = async (sessionID: string) => worktree.getWorktreeStatus(this.#transport, sessionID);
  listWorktrees = async (sessionID: string) => worktree.listWorktrees(this.#transport, sessionID);
  resolveWorktreeSelector = async (sessionID: string, selector: string) =>
    worktree.resolveWorktreeSelector(this.#transport, sessionID, selector);
  resolveWorktreeCreateTarget = async (sessionID: string, target: string) =>
    worktree.resolveWorktreeCreateTarget(this.#transport, sessionID, target);
  previewWorktreeDelete = async (sessionID: string, selector: string) =>
    worktree.previewWorktreeDelete(this.#transport, sessionID, selector);
  createWorktree = async (input: worktreeModels.WorktreeCreateInput) =>
    worktree.createWorktree(this.#transport, input);
  switchWorktree = async (sessionID: string, operation: worktreeModels.WorktreeSwitch) =>
    worktree.switchWorktree(this.#transport, sessionID, operation);
  deleteWorktree = async (
    sessionID: string,
    preview: worktreeModels.WorktreeDeletePreview,
    confirmation: worktreeModels.WorktreeDeleteConfirmationChoice,
  ) => worktree.deleteWorktree(this.#transport, sessionID, preview, confirmation);
  subscribeWorktreeSetup = (setupOperationID: SetupOperationID, handler: WorktreeSetupEventHandler) =>
    subscribeWorktreeSetup(this.#transport, setupOperationID, handler);
}

function projectReadiness(readiness: Readiness): ServerReadiness {
  return {
    ready: readiness.ready,
    serverID: readiness.serverId,
    serverVersion: readiness.serverVersion,
    serverBuild: readiness.serverBuild,
    protocolVersion: readiness.protocolVersion,
    authReady: readiness.authReady,
    authRequired: readiness.authRequired,
    endpoint: readiness.endpoint,
    subagentRoles: readiness.subagentRoles.map((role) => ({ name: role.name })),
    causes: readiness.causes.map((cause) => ({
      code: cause.code,
      severity: projectReadinessSeverity(cause.severity),
      ...(cause.summary === undefined ? {} : { summary: cause.summary }),
      ...(cause.nextAction === undefined ? {} : { nextAction: cause.nextAction }),
      ...(cause.diagnosticId === undefined ? {} : { diagnosticID: cause.diagnosticId }),
    })),
  };
}

function projectReadinessSeverity(severity: ReadinessSeverity): string {
  if (severity === ReadinessSeverity.ERROR) {
    return "error";
  }
  throw new ContractError("server readiness response contained an unsupported cause severity.");
}
