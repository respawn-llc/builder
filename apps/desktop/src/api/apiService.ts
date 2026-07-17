import type { AttentionNotificationEventHandler } from "./attentionNotifications";
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
  WorkflowListInput,
  WorkflowProjectLinkInput,
  WorkflowScriptPathValidateInput,
} from "./clientInputs";
import type { ConnectionSnapshot } from "./connectionStore";
import type {
  ActivityPage,
  AttentionPage,
  BindingPlan,
  BoardNodeCardsPage,
  CommentPage,
  PendingAsk,
  ProjectBinding,
  ProjectDeleteResponse,
  ProjectEdit,
  ProjectMutationResponse,
  ProjectPage,
  ProjectWorkflowLink,
  ServerReadiness,
  TaskApproveResponse,
  TaskComment,
  TaskDetail,
  TaskMoveResponse,
  TaskStartResponse,
  WorkflowBoard,
  WorkflowDefinition,
  WorkflowDeleteImpact,
  WorkflowDeleteResponse,
  WorkflowDerivedWiring,
  WorkflowExecutionTargetSelection,
  WorkflowGraphSavePreview,
  WorkflowGraphSaveResult,
  WorkflowGraphValidateDraftResult,
  WorkflowPage,
  WorkflowRecord,
  WorkflowValidation,
  WorkspaceList,
  WorkspaceUnlinkResponse,
} from "./models";
import type { SetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEventHandler } from "./worktreeSetup";

export type ApiConnectionSource = Readonly<{
  snapshot(): ConnectionSnapshot;
  subscribe(listener: () => void): () => void;
}>;

export type ApiSubscription = Readonly<{
  close(): void;
}>;

export type ApiEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(params: unknown): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

export interface ApiService {
  readonly connection: ApiConnectionSource;

  getReadiness(): Promise<ServerReadiness>;
  listProjects(pageToken: string): Promise<ProjectPage>;
  listWorkspaces(projectID: string, pageToken?: string): Promise<WorkspaceList>;
  getProjectEdit(projectID: string, pageToken?: string): Promise<ProjectEdit>;
  planWorkspace(path: string): Promise<BindingPlan>;
  createProject(displayName: string, projectKey: string, workspaceRoot: string): Promise<ProjectBinding>;
  attachWorkspace(projectID: string, workspaceRoot: string): Promise<ProjectBinding>;
  updateProject(
    projectID: string,
    displayName: string,
    projectKey?: string,
  ): Promise<ProjectMutationResponse>;
  setDefaultWorkspace(projectID: string, workspaceID: string): Promise<ProjectMutationResponse>;
  unlinkWorkspace(projectID: string, workspaceID: string): Promise<WorkspaceUnlinkResponse>;
  deleteProject(projectID: string): Promise<ProjectDeleteResponse>;
  getBoard(projectID: string, workflowID: string | undefined): Promise<WorkflowBoard>;
  getWorkflow(workflowID: string): Promise<WorkflowDefinition>;
  listWorkflows(input?: WorkflowListInput): Promise<WorkflowPage>;
  createWorkflow(input: WorkflowCreateInput): Promise<WorkflowRecord>;
  createAndLinkWorkflowToProject(
    input: WorkflowCreateAndLinkInput,
  ): Promise<Readonly<{ workflow: WorkflowRecord; link: ProjectWorkflowLink }>>;
  linkWorkflowToProject(input: WorkflowProjectLinkInput): Promise<ProjectWorkflowLink>;
  validateWorkflow(
    workflowID: string,
    mode: "draft" | "task_creation" | "execution",
  ): Promise<WorkflowValidation>;
  validateWorkflowScriptPath(input: WorkflowScriptPathValidateInput): Promise<WorkflowValidation>;
  validateWorkflowGraphDraft(
    input: WorkflowGraphValidateDraftInput,
  ): Promise<WorkflowGraphValidateDraftResult>;
  deriveWorkflowGraphWiring(input: WorkflowGraphDeriveWiringInput): Promise<WorkflowDerivedWiring>;
  previewWorkflowGraphSave(input: WorkflowGraphSavePreviewInput): Promise<WorkflowGraphSavePreview>;
  saveWorkflowGraph(input: WorkflowGraphSaveInput): Promise<WorkflowGraphSaveResult>;
  previewWorkflowDelete(workflowID: string): Promise<WorkflowDeleteImpact>;
  deleteWorkflow(input: WorkflowDeleteInput): Promise<WorkflowDeleteResponse>;
  listProjectWorkflowLinks(projectID: string): Promise<readonly ProjectWorkflowLink[]>;
  listBoardNodeCards(
    projectID: string,
    workflowID: string,
    nodeID: string,
    pageToken?: string | null,
  ): Promise<BoardNodeCardsPage>;
  listAttention(projectID: string, pageToken: string): Promise<AttentionPage>;
  createTask(input: TaskMutationInput): Promise<string>;
  updateTask(input: TaskEditInput): Promise<string>;
  startTask(
    taskID: string,
    setupOperationID?: SetupOperationID,
    executionTarget?: WorkflowExecutionTargetSelection,
  ): Promise<TaskStartResponse>;
  moveTask(input: TaskMoveInput): Promise<TaskMoveResponse>;
  interruptTask(taskID: string, sessionID?: string): Promise<void>;
  resumeTask(taskID: string): Promise<void>;
  approveTransition(
    taskTransitionID: string,
    setupOperationID?: SetupOperationID,
    executionTarget?: WorkflowExecutionTargetSelection,
  ): Promise<TaskApproveResponse>;
  cancelTask(taskID: string): Promise<void>;
  deleteTask(taskID: string): Promise<void>;
  getTask(taskID: string): Promise<TaskDetail>;
  listTaskActivity(taskID: string, pageToken: string): Promise<ActivityPage>;
  listTaskComments(taskID: string, pageToken: string): Promise<CommentPage>;
  addComment(taskID: string, body: string): Promise<TaskComment>;
  replaceComment(commentID: string, body: string): Promise<void>;
  deleteComment(commentID: string): Promise<void>;
  answerQuestion(input: QuestionAnswerInput): Promise<void>;
  listPendingAsks(sessionID: string): Promise<readonly PendingAsk[]>;
  subscribeProject(projectID: string, handler: ApiEventHandler): ApiSubscription;
  subscribeWorkflow(workflowID: string, handler: ApiEventHandler): ApiSubscription;
  subscribeAttentionNotifications(handler: AttentionNotificationEventHandler): ApiSubscription;
  subscribeWorktreeSetup(
    setupOperationID: SetupOperationID,
    handler: WorktreeSetupEventHandler,
  ): ApiSubscription;
}
