import type { AttentionNotificationEventHandler } from "./attentionNotifications";
import type {
  BoardNodeCardsInput,
  QuestionAnswerInput,
  TaskEditInput,
  TaskMoveInput,
  TaskStartInput,
  TaskMutationInput,
  TaskListInput,
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
  TaskAttention,
  TaskApproveResponse,
  TaskComment,
  TaskDetail,
  TaskDependencyDirection,
  TaskDependencyListResponse,
  TaskDependencyMutationResponse,
  TaskMoveResponse,
  TaskStartResponse,
  WorkflowBoard,
  WorkflowDefinition,
  WorkflowDeleteImpact,
  WorkflowDeleteResponse,
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
import type {
  ProjectLabel,
  ProjectLabelCatalog,
  TaskLabelAssignment,
  TaskLabelFilter,
  TaskListPage,
} from "./workflowLabels";
import type { SetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEventHandler } from "./worktreeSetup";
import type { WorkflowProjectEventHandler } from "./workflowProjectEvents";

export type ApiConnectionSource = Readonly<{
  snapshot(): ConnectionSnapshot;
  subscribe(listener: () => void): () => void;
}>;

export type ApiSubscription = Readonly<{
  close(): void;
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
  listProjectLabels(projectID: string): Promise<ProjectLabelCatalog>;
  createProjectLabel(projectID: string, name: string): Promise<ProjectLabel>;
  renameProjectLabel(projectID: string, labelID: string, name: string): Promise<ProjectLabel>;
  deleteProjectLabel(projectID: string, labelID: string): Promise<string>;
  reorderProjectLabels(projectID: string, labelIDs: readonly string[]): Promise<ProjectLabelCatalog>;
  getTaskLabels(taskID: string): Promise<TaskLabelAssignment>;
  updateTaskLabels(
    taskID: string,
    addLabelIDs: readonly string[],
    removeLabelIDs: readonly string[],
  ): Promise<TaskLabelAssignment>;
  getBoard(
    projectID: string,
    workflowID: string | undefined,
    labelFilter: TaskLabelFilter,
  ): Promise<WorkflowBoard>;
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
  listBoardNodeCards(input: BoardNodeCardsInput): Promise<BoardNodeCardsPage>;
  listAttention(pageToken: string): Promise<AttentionPage>;
  listTaskAttention(taskID: string): Promise<TaskAttention>;
  createTask(input: TaskMutationInput): Promise<string>;
  addTaskDependency(blockerTaskID: string, blockedTaskID: string): Promise<TaskDependencyMutationResponse>;
  removeTaskDependency(blockerTaskID: string, blockedTaskID: string): Promise<TaskDependencyMutationResponse>;
  listTaskDependencies(
    taskID: string,
    direction?: TaskDependencyDirection,
  ): Promise<TaskDependencyListResponse>;
  listTasks(input: TaskListInput): Promise<TaskListPage>;
  updateTask(input: TaskEditInput): Promise<string>;
  startTask(input: TaskStartInput): Promise<TaskStartResponse>;
  moveTask(input: TaskMoveInput): Promise<TaskMoveResponse>;
  interruptTask(taskID: string, sessionID?: string): Promise<void>;
  resumeTask(taskID: string): Promise<void>;
  approveApproval(approvalID: string): Promise<TaskApproveResponse>;
  deleteTask(taskID: string): Promise<void>;
  getTask(taskID: string): Promise<TaskDetail>;
  listTaskActivity(taskID: string, pageToken: string): Promise<ActivityPage>;
  listTaskComments(taskID: string, offset: number): Promise<CommentPage>;
  addComment(taskID: string, body: string): Promise<TaskComment>;
  replaceComment(commentID: string, body: string): Promise<void>;
  deleteComment(commentID: string): Promise<void>;
  answerQuestion(input: QuestionAnswerInput): Promise<void>;
  listPendingAsks(sessionID: string): Promise<readonly PendingAsk[]>;
  subscribeProject(projectID: string, handler: WorkflowProjectEventHandler): ApiSubscription;
  subscribeWorkflow(workflowID: string, handler: WorkflowProjectEventHandler): ApiSubscription;
  subscribeAttentionNotifications(handler: AttentionNotificationEventHandler): ApiSubscription;
  subscribeWorktreeSetup(
    setupOperationID: SetupOperationID,
    handler: WorktreeSetupEventHandler,
  ): ApiSubscription;
}
