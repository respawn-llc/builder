import type { AttentionItem } from "./attention";
import type { TaskLiveSession } from "./taskDetailModels";

import type { WorkflowExecutionTarget, WorkflowExecutionTargetPolicy } from "./workflowExecutionTarget";
import type { WorkflowEdgeSelectionMode, WorkflowParameterPurpose, WorkflowSelectorApplicability } from "./workflowSelectionModels";

export { defaultWorkflowExecutionTargetPolicy } from "./workflowExecutionTarget";
export type {
  TaskApproveApplied,
  TaskApproveResponse,
  TaskMoveApplied,
  TaskMoveNoOp,
  TaskMoveResponse,
  TaskMovePreviewBlocker,
  TaskMovePreviewChoice,
  TaskMovePreviewResponse,
  TaskMoveRequiredValue,
  TaskStartApplied,
  TaskStartResponse,
  TaskResumeApplied,
  TaskResumeResponse,
  WorkflowExecutionTargetActionResponse,
  WorkflowExecutionTarget,
  WorkflowExecutionTargetMode,
  WorkflowExecutionTargetPolicy,
  WorkflowExecutionTargetProvenance,
  WorkflowExecutionTargetSelection,
  WorkflowExecutionTargetSelectionMode,
  WorkflowExecutionTargetSelectionRequirement,
  WorkflowExecutionTargetUnavailableCause,
  WorkflowManagedExecutionTarget,
  WorkflowNoManagedExecutionTarget,
} from "./workflowExecutionTarget";

export type ServerCause = Readonly<{
  code: string;
  severity: string;
  summary?: string;
  nextAction?: string;
  diagnosticID: string;
}>;

export type SubagentRoleSummary = Readonly<{
  name: string;
}>;

export type ServerReadiness = Readonly<{
  ready: boolean;
  serverID: string;
  serverVersion: string;
  serverBuild: string;
  protocolVersion: string;
  authReady: boolean;
  authRequired: boolean;
  endpoint: string;
  subagentRoles: readonly SubagentRoleSummary[];
  causes: readonly ServerCause[];
}>;

export type WorkspaceAvailability = "available" | "missing" | "inaccessible" | "unlinked";

export type WorkspaceSummary = Readonly<{
  id: string;
  name: string;
  rootPath: string;
  availability: WorkspaceAvailability;
  isPrimary: boolean;
  updatedAt: number;
}>;

export type ProjectSummary = Readonly<{
  id: string;
  key: string;
  name: string;
  primaryWorkspace: WorkspaceSummary;
  defaultWorkflowID: string | null;
  defaultWorkflowName: string;
  defaultWorkflowValid: boolean;
  updatedAt: number;
  taskCount: number;
  attentionCount: number;
  workflowCount: number;
}>;

export type ProjectPage = Readonly<{
  projects: readonly ProjectSummary[];
  nextPageToken: string;
  generatedAt: number;
}>;

export type WorkspaceList = Readonly<{
  projectID: string;
  workspaces: readonly WorkspaceSummary[];
  defaultWorkspaceID: string;
  nextPageToken: string;
}>;

export type ProjectEdit = Readonly<{
  projectID: string;
  projectKey: string;
  displayName: string;
  defaultWorkspaceID: string;
  workspaces: readonly WorkspaceSummary[];
  nextPageToken: string;
}>;

export type ProjectMutationResponse = Readonly<{
  project: ProjectSummary;
}>;

export type WorkspaceUnlinkBlocker = Readonly<{
  code: string;
  message: string;
  count: number;
}>;

export type WorkspaceUnlinkResponse = Readonly<{
  projectID: string;
  workspaceID: string;
  blockers: readonly WorkspaceUnlinkBlocker[];
  project: ProjectSummary | null;
}>;

export type ProjectDeleteBlocker = Readonly<{
  code: string;
  message: string;
  count: number;
}>;

export type ProjectDeleteResponse = Readonly<{
  projectID: string;
  deleted: boolean;
  blockers: readonly ProjectDeleteBlocker[];
}>;

export type ProjectBinding = Readonly<{
  projectID: string;
  projectKey: string;
  projectName: string;
  workspaceID: string;
  canonicalRoot: string;
  workspaceName: string;
  workspaceStatus: string;
}>;

export type BindingPlan = Readonly<{
  kind: string;
  canonicalRoot: string;
  binding: ProjectBinding | null;
}>;

export type PendingAsk = Readonly<{
  askID: string;
  sessionID: string;
  question: string;
  suggestions: readonly string[];
  recommendedOptionIndex: number | null;
  createdAt: string;
}>;

export type ApprovalDecision = "allow_once" | "allow_session" | "deny";

export type OrdinaryQuestionPrompt = Readonly<{
  kind: "ordinary";
  suggestions: readonly string[];
  recommendedOptionIndex: number | null;
}>;

export type ApprovalQuestionPrompt = Readonly<{
  kind: "approval";
  approvalDecisions: readonly ApprovalDecision[];
}>;

export type AttentionQuestionPrompt = OrdinaryQuestionPrompt | ApprovalQuestionPrompt;

export type WorkflowValidationError = Readonly<{
  code: string;
  message: string;
  workflowID: string | null;
  nodeID: string;
  transitionGroupID: string;
  edgeID: string;
  details: WorkflowValidationErrorDetails;
  relatedIDs: readonly string[];
  blocksContext: boolean;
}>;

export type WorkflowValidationErrorDetails = Readonly<{
  fieldName: string;
  inputName: string;
  placeholder: string;
  providerEdgeID: string;
  role: string | null;
  requiredTool: string | null;
}>;

export type WorkflowOutputField = Readonly<{
  name: string;
  description: string;
}>;

export type WorkflowInputField = Readonly<{
  name: string;
  description: string;
}>;

export type WorkflowParameter = Readonly<{ key: string; description: string; purpose: WorkflowParameterPurpose }>;

export type WorkflowJoinInputProvider = Readonly<{
  inputName: string;
  providerEdgeID: string;
}>;

export type WorkflowRecord = Readonly<{
  id: string;
  name: string;
  description: string;
  version: number;
  executionTargetPolicy: WorkflowExecutionTargetPolicy;
}>;

export type WorkflowPage = Readonly<{
  workflows: readonly WorkflowRecord[];
  nextOffset: number | null;
}>;

export type WorkflowNodeGroup = Readonly<{
  id: string;
  workflowID: string;
  key: string;
  name: string;
  sortOrder: number;
  nodeIDs: readonly string[];
}>;

export const workflowNodeKinds = ["agent", "join", "script", "start", "terminal"] as const;
export type WorkflowNodeKind = (typeof workflowNodeKinds)[number];

export type WorkflowNode = Readonly<{
  id: string;
  workflowID: string;
  key: string;
  kind: string;
  name: string;
  groupID: string;
  groupKey: string;
  subagentRole: string;
  completionMode?: string | undefined;
  scriptPath?: string | null | undefined;
  joinInputProviders: readonly WorkflowJoinInputProvider[];
}>;

export type WorkflowInputBinding = Readonly<{
  name: string;
  source: string;
  field: string;
}>;

export type WorkflowOutputRequirement = Readonly<{
  fieldName: string;
}>;

export type WorkflowDerivedWiring = Readonly<{
  nodes: readonly WorkflowDerivedNodeWiring[];
  transitionGroups: readonly WorkflowDerivedTransitionGroupWiring[];
  edges: readonly WorkflowDerivedEdgeWiring[];
  diagnostics: readonly WorkflowValidationError[];
}>;

export type WorkflowDerivedNodeWiring = Readonly<{
  nodeID: string;
  possibleProvisionFields: readonly WorkflowOutputField[];
  joinOutputFields: readonly WorkflowOutputField[];
}>;

export type WorkflowDerivedTransitionGroupWiring = Readonly<{
  transitionGroupID: string;
  requiredProvisionFields: readonly WorkflowOutputField[];
}>;

export type WorkflowDerivedEdgeWiring = Readonly<{
  edgeID: string;
  inputBindings: readonly WorkflowInputBinding[];
  requiredProvisionFields: readonly WorkflowOutputField[];
  requiredProviderFields: readonly WorkflowOutputField[];
  assigneeSelectionApplicability: WorkflowSelectorApplicability;
  thinkingSelectionApplicability: WorkflowSelectorApplicability;
}>;

export const emptyWorkflowDerivedWiring: WorkflowDerivedWiring = {
  diagnostics: [],
  edges: [],
  nodes: [],
  transitionGroups: [],
};

export type WorkflowContextSource = Readonly<{
  kind: string;
  nodeKey: string;
}>;

export type WorkflowTransitionGroup = Readonly<{
  id: string;
  workflowID: string;
  sourceNodeID: string;
  transitionID: string;
  name: string;
  description: string;
}>;

export type WorkflowEdge = Readonly<{
  id: string;
  workflowID: string;
  transitionGroupID: string;
  key: string;
  targetNodeID: string;
  assigneeSelection: WorkflowEdgeSelectionMode;
  thinkingSelection: WorkflowEdgeSelectionMode;
  requiresApproval: boolean;
  contextMode: string;
  contextSource: WorkflowContextSource;
  promptTemplate: string;
  parameters: readonly WorkflowParameter[];
  inputBindings: readonly WorkflowInputBinding[];
  outputRequirements: readonly WorkflowOutputRequirement[];
}>;

export type WorkflowDefinition = Readonly<{
  workflow: WorkflowRecord;
  nodeGroups: readonly WorkflowNodeGroup[];
  nodes: readonly WorkflowNode[];
  transitionGroups: readonly WorkflowTransitionGroup[];
  edges: readonly WorkflowEdge[];
  derivedWiring: WorkflowDerivedWiring;
}>;

export type WorkflowValidation = Readonly<{
  valid: boolean;
  errors: readonly WorkflowValidationError[];
}>;

export type WorkflowValidationMode = "draft" | "task_creation" | "execution";

export type WorkflowGraphDraftNodeGroup = Readonly<{
  id: string;
  key: string;
  name: string;
}>;

export type WorkflowGraphDraftNode = Readonly<{
  id: string;
  key: string;
  kind: string;
  name: string;
  groupID: string | null;
  groupKey: string;
  subagentRole: string;
  completionMode?: string | undefined;
  scriptPath?: string | null | undefined;
  joinInputProviders: readonly WorkflowJoinInputProvider[];
}>;

export type WorkflowGraphDraftTransitionGroup = Readonly<{
  id: string;
  sourceNodeID: string;
  transitionID: string;
  name: string;
  description: string;
}>;

export type WorkflowGraphDraftEdge = Readonly<{
  id: string;
  transitionGroupID: string;
  key: string;
  targetNodeID: string;
  assigneeSelection: WorkflowEdgeSelectionMode;
  thinkingSelection: WorkflowEdgeSelectionMode;
  requiresApproval: boolean;
  contextMode: string;
  contextSource: WorkflowContextSource;
  promptTemplate: string;
  parameters: readonly WorkflowParameter[];
}>;

export type WorkflowGraphDraft = Readonly<{
  nodeGroups: readonly WorkflowGraphDraftNodeGroup[];
  nodes: readonly WorkflowGraphDraftNode[];
  transitionGroups: readonly WorkflowGraphDraftTransitionGroup[];
  edges: readonly WorkflowGraphDraftEdge[];
}>;

export type WorkflowGraphValidationResults = Readonly<
  Partial<Record<WorkflowValidationMode, WorkflowValidation>>
>;

export type WorkflowGraphValidateDraftResult = WorkflowGraphValidationResults &
  Readonly<{
    derivedWiring: WorkflowDerivedWiring;
  }>;

export type WorkflowGraphMetadata = Readonly<{
  name: string;
  description: string;
  executionTargetPolicy: WorkflowExecutionTargetPolicy;
}>;

export type WorkflowGraphEntityType = "edge" | "node" | "node_group" | "transition_group";
export type WorkflowGraphEntityReference = Readonly<{ entityType: WorkflowGraphEntityType; entityID: string }>;

export type WorkflowGraphSaveImpact = Readonly<{
  removedNodeGroupCount: number;
  removedNodeCount: number;
  removedTransitionGroupCount: number;
  removedEdgeCount: number;
  removedEntities: readonly WorkflowGraphEntityReference[];
  nodeTaskReferenceCount: number;
  edgeTaskReferenceCount: number;
  activeCurrentNodeCount: number;
  pendingApprovalCount: number;
  startNodeChangeCount: number;
  lastTerminalChangeCount: number;
  taskReferencedNodeKindChangeCount: number;
}>;

export type WorkflowGraphSaveBlocker = Readonly<{
  code: string;
  message: string;
  count: number;
  affectedEntities: readonly WorkflowGraphEntityReference[];
}>;

export type WorkflowGraphSavePreview = Readonly<{
  changed: boolean;
  currentVersion: number;
  validationResults: WorkflowGraphValidationResults;
  impact: WorkflowGraphSaveImpact;
  blockers: readonly WorkflowGraphSaveBlocker[];
  canSave: boolean;
  confirmationRequired: boolean;
}>;

export type WorkflowGraphSaveConfirmation = Readonly<{
  expectedRemovedNodeGroupCount: number;
  expectedRemovedNodeCount: number;
  expectedRemovedTransitionGroupCount: number;
  expectedRemovedEdgeCount: number;
  expectedNodeTaskReferenceCount: number;
  expectedEdgeTaskReferenceCount: number;
}>;

export type WorkflowGraphSaveResult = WorkflowGraphSavePreview &
  Readonly<{
    saved: boolean;
    definition: WorkflowDefinition | null;
  }>;

export type WorkflowDeleteImpact = Readonly<{
  workflowID: string;
  version: number;
  projectCount: number;
  linkCount: number;
  defaultReplacementProjectCount: number;
  taskCount: number;
  currentNodeCount: number;
  pendingApprovalCount: number;
  blockedTaskCount: number;
}>;

export type WorkflowDeleteBlocker = Readonly<{
  code: string;
  message: string;
  count: number;
}>;

export type WorkflowDeleteResponse = Readonly<{
  deleted: boolean;
  impact: WorkflowDeleteImpact;
  blockers: readonly WorkflowDeleteBlocker[];
}>;

export type ProjectWorkflowLink = Readonly<{
  id: string;
  projectID: string;
  workflowID: string;
  isDefault: boolean;
}>;

export type WorkflowPickerItem = Readonly<{
  id: string;
  name: string;
  description: string;
  version: number;
  isProjectDefault: boolean;
  validForTaskCreation: boolean;
  validationErrors: readonly WorkflowValidationError[];
}>;

export type TaskStatusKind =
  | "done"
  | "waiting_question"
  | "waiting_approval"
  | "interrupted"
  | "running"
  | "queued"
  | "backlog"
  | "active";

export type TaskStatus = Readonly<{
  kind: TaskStatusKind;
  nativeState: string;
  nodeIDs: readonly string[];
  attentionTypes: readonly string[];
}>;

export type TaskActions = Readonly<{
  canStart: boolean;
  canInterrupt: boolean;
  canResume: boolean;
  canDelete: boolean;
}>;

export type MarkdownPreview = Readonly<{
  markdown: string;
  truncated: boolean;
}>;

export type BoardCard = Readonly<{
  id: string;
  shortID: string;
  title: string;
  preview: MarkdownPreview;
  workflowID: string;
  activeNodeIDs: readonly string[];
  sourceWorkspace: WorkspaceSummary;
  status: TaskStatus;
  actions: TaskActions;
  labelIDs: readonly string[];
  dependencyProgress: TaskDependencyProgress | null;
  updatedAt: number;
}>;

export type TaskDependencyProgress = Readonly<{
  satisfiedCount: number;
  totalCount: number;
}>;

export type TaskDependencyDirection = "blocked-by" | "blocks";
export type TaskDependencySatisfaction = "satisfied" | "unsatisfied";

export type TaskDependencyAddAvailability =
  Readonly<{ kind: "available"; remainingCapacity: number }> | Readonly<{ kind: "limit_reached" }>;

export type TaskDependencyItem = Readonly<{
  taskID: string;
  shortID: string;
  title: string;
  workflowID: string;
  status: TaskStatus;
  satisfaction: TaskDependencySatisfaction | null;
}>;

export type TaskDependencyDirectionProjection = Readonly<{
  direction: TaskDependencyDirection;
  totalCount: number;
  unsatisfiedCount: number | null;
  items: readonly TaskDependencyItem[];
  addAvailability: TaskDependencyAddAvailability;
}>;

export type TaskDependencies = Readonly<{
  blockerCount: number;
  unsatisfiedBlockerCount: number;
  directlyBlockedTaskCount: number;
  directions: readonly TaskDependencyDirectionProjection[];
}>;

export type TaskDependencyMutationOutcome = "added" | "already_present" | "removed" | "already_absent";

export type TaskDependencyMutationResponse = Readonly<{
  outcome: TaskDependencyMutationOutcome;
  blockerTaskID: string;
  blockerShortID: string;
  blockedTaskID: string;
  blockedShortID: string;
}>;

export type TaskDependencyListDirection = Omit<TaskDependencyDirectionProjection, "addAvailability">;

export type TaskDependencyListResponse = Readonly<{
  taskID: string;
  shortID: string;
  directions: readonly TaskDependencyListDirection[];
}>;

export type BoardColumn = Readonly<{
  id: string;
  key: string;
  kind: string;
  name: string;
  assigneeRole: string;
  outputFields: readonly WorkflowOutputField[];
  groupID: string;
  sortOrder: number;
  isBacklog: boolean;
  isDone: boolean;
  taskCount: number;
}>;

export type BoardGroup = Readonly<{
  id: string;
  key: string;
  name: string;
  sortOrder: number;
  nodeIDs: readonly string[];
}>;

export type WorkflowBoard = Readonly<{
  projectID: string;
  projectKey: string;
  projectName: string;
  defaultWorkspaceID: string;
  attachedWorkspaceCount: number;
  selectedWorkflow: WorkflowPickerItem | null;
  workflows: readonly WorkflowPickerItem[];
  groups: readonly BoardGroup[];
  columns: readonly BoardColumn[];
  generatedAt: number;
}>;

export type SelectedWorkflowBoard = Omit<WorkflowBoard, "selectedWorkflow"> &
  Readonly<{ selectedWorkflow: WorkflowPickerItem }>;

export function hasSelectedWorkflow(board: WorkflowBoard): board is SelectedWorkflowBoard {
  return board.selectedWorkflow !== null;
}

export type BoardNodeCardsPage = Readonly<{
  projectID: string;
  workflowID: string;
  nodeID: string;
  cards: readonly BoardCard[];
  nextOffset: number | null;
  generatedAt: number;
}>;

export type ApprovalSnapshot = Readonly<{
  sourceNodeName: string;
  targets: readonly Readonly<{ displayName: string }>[];
  commentary: string;
  outputValues: Readonly<Record<string, string>>;
  version: number;
}>;

export type AttentionPage = Readonly<{
  items: readonly AttentionItem[];
  nextPageToken: string;
  generatedAt: number;
}>;

export type TaskAttention = Readonly<{
  items: readonly AttentionItem[];
  generatedAt: number;
}>;

export type TaskCommentAuthorKind = "agent" | "user";

export type TaskComment = Readonly<{
  id: string;
  taskID: string;
  body: string;
  authorKind: TaskCommentAuthorKind;
  authorID: string | null;
  createdAt: number;
  updatedAt: number;
}>;

export type CommentPage = Readonly<{
  comments: readonly TaskComment[];
  nextOffset: number | null;
}>;

export type TaskCurrentNode = Readonly<{
  nodeID: string;
  transitionBranchKey: string | null;
  sessionID: string | null;
  effectiveAssignee: string | null;
  effectiveThinking: string | null;
}>;

export type TaskScriptCurrentNode = Readonly<{
  nodeID: string;
  transitionBranchKey: string | null;
  sessionID: null;
}>;

export type TaskDetail = Readonly<{
  id: string;
  shortID: string;
  projectID: string;
  projectName: string;
  workflowID: string;
  workflowName: string;
  workflowVersion: number;
  title: string;
  body: string;
  sourceURL: string;
  sourceWorkspace: WorkspaceSummary;
  status: TaskStatus;
  actions: TaskActions;
  labelIDs: readonly string[];
  attentionCount: number;
  dependencies: TaskDependencies;
  executionTarget: WorkflowExecutionTarget | null;
  worktreePath: string | null;
  currentNodes: readonly TaskCurrentNode[];
  liveSessions: readonly TaskLiveSession[];
  currentScripts: readonly Readonly<{ currentNode: TaskScriptCurrentNode; path: string }>[];
  retainedSessionCount: number;
  createdAt: number;
  updatedAt: number;
  done: boolean;
}>;

export type CommentActivityItem = Readonly<{
  id: string;
  type: "comment";
  taskID: string;
  occurredAt: number;
  updatedAt: number;
  comment: TaskComment;
}>;

export type SessionStartedActivityItem = Readonly<{
  id: string;
  type: "session_started";
  taskID: string;
  occurredAt: number;
  updatedAt: number;
  sessionID: string;
  sessionName: string;
}>;

export type ActivityItem = CommentActivityItem | SessionStartedActivityItem;

export type ActivityPage = Readonly<{
  items: readonly ActivityItem[];
  nextPageToken: string;
  generatedAt: number;
}>;
