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

export type WorkspaceSummary = Readonly<{
  id: string;
  name: string;
  rootPath: string;
  availability: string;
  isPrimary: boolean;
  updatedAt: number;
}>;

export type ProjectSummary = Readonly<{
  id: string;
  key: string;
  name: string;
  primaryWorkspace: WorkspaceSummary;
  defaultWorkflowID: string;
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
  unlinked: boolean;
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
  recommendedOptionIndex: number;
  createdAt: string;
}>;

export type ApprovalDecision = "allow_once" | "allow_session" | "deny";

export type OrdinaryQuestionPrompt = Readonly<{
  kind: "ordinary";
  suggestions: readonly string[];
  recommendedOptionIndex: number;
}>;

export type ApprovalQuestionPrompt = Readonly<{
  kind: "approval";
  approvalDecisions: readonly ApprovalDecision[];
}>;

export type AttentionQuestionPrompt = OrdinaryQuestionPrompt | ApprovalQuestionPrompt;

export type WorkflowValidationError = Readonly<{
  code: string;
  message: string;
  workflowID: string;
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
}>;

export type WorkflowOutputField = Readonly<{
  name: string;
  description: string;
}>;

export type WorkflowInputField = Readonly<{
  name: string;
  description: string;
}>;

export type WorkflowParameter = Readonly<{
  key: string;
  description: string;
}>;

export type WorkflowJoinInputProvider = Readonly<{
  inputName: string;
  providerEdgeID: string;
}>;

export type WorkflowRecord = Readonly<{
  id: string;
  name: string;
  description: string;
  version: number;
}>;

export type WorkflowPage = Readonly<{
  workflows: readonly WorkflowRecord[];
  nextPageToken: string;
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
  kind: WorkflowNodeKind;
  name: string;
  groupID: string;
  groupKey: string;
  subagentRole: string;
  promptTemplate: string;
  completionMode?: string | undefined;
  scriptPath?: string | null | undefined;
  inputFields: readonly WorkflowInputField[];
  joinInputProviders: readonly WorkflowJoinInputProvider[];
  outputFields: readonly WorkflowOutputField[];
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
  groupID: string;
  groupKey: string;
  subagentRole: string;
  promptTemplate: string;
  completionMode?: string | undefined;
  scriptPath?: string | null | undefined;
  inputFields: readonly WorkflowInputField[];
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
}>;

export type WorkflowGraphSaveImpact = Readonly<{
  removedNodeCount: number;
  removedTransitionGroupCount: number;
  removedEdgeCount: number;
  nodeTaskReferenceCount: number;
  edgeTaskReferenceCount: number;
  activeNodePlacementCount: number;
  pendingApprovalCount: number;
  activeRunCount: number;
  runnableRunCount: number;
  startNodeChangeCount: number;
  lastTerminalChangeCount: number;
  taskReferencedNodeKindChangeCount: number;
}>;

export type WorkflowGraphSaveBlocker = Readonly<{
  code: string;
  message: string;
  count: number;
}>;

export type WorkflowGraphSavePreview = Readonly<{
  currentVersion: number;
  validationResults: WorkflowGraphValidationResults;
  impact: WorkflowGraphSaveImpact;
  blockers: readonly WorkflowGraphSaveBlocker[];
  canSave: boolean;
  confirmationRequired: boolean;
}>;

export type WorkflowGraphSaveConfirmation = Readonly<{
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
  activeRunCount: number;
  runnableRunCount: number;
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
  | "canceled"
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
  runIDs: readonly string[];
  attentionTypes: readonly string[];
}>;

export type TaskActions = Readonly<{
  canStart: boolean;
  canInterrupt: boolean;
  canResume: boolean;
  canCancel: boolean;
  manualMoveTargetNodeIDs: readonly string[];
}>;

export type TaskMoveResponse = Readonly<{
  transitionID: string;
  state: string;
  placementIDs: readonly string[];
  runIDs: readonly string[];
  approvalError: string;
}>;

export type BoardCard = Readonly<{
  id: string;
  shortID: string;
  title: string;
  bodyPreview: string;
  workflowID: string;
  activeNodeIDs: readonly string[];
  sourceWorkspace: WorkspaceSummary;
  status: TaskStatus;
  actions: TaskActions;
  updatedAt: number;
}>;

export type BoardColumn = Readonly<{
  id: string;
  key: string;
  kind: string;
  name: string;
  assigneeRole: string;
  outputFields: readonly WorkflowOutputField[];
  transitionOutputFields: readonly WorkflowOutputField[];
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
  selectedWorkflow: WorkflowPickerItem;
  workflows: readonly WorkflowPickerItem[];
  groups: readonly BoardGroup[];
  columns: readonly BoardColumn[];
  generatedAt: number;
}>;

export type BoardNodeCardsPage = Readonly<{
  projectID: string;
  workflowID: string;
  nodeID: string;
  cards: readonly BoardCard[];
  nextPageToken: string;
  generatedAt: number;
}>;

export type AttentionItem = Readonly<{
  id: string;
  kind: string;
  projectID: string;
  workflowID: string;
  taskID: string;
  taskShortID: string;
  taskTitle: string;
  runID: string;
  sessionID: string;
  askID: string;
  taskTransitionID: string;
  message: string;
  detailJSON: string;
  suggestions: readonly string[];
  recommendedOptionIndex: number;
  question: AttentionQuestionPrompt | null;
  occurredAt: number;
}>;

export type AttentionPage = Readonly<{
  items: readonly AttentionItem[];
  nextPageToken: string;
  generatedAt: number;
}>;

export type TaskComment = Readonly<{
  id: string;
  taskID: string;
  body: string;
  author: string;
  createdAt: number;
  updatedAt: number;
}>;

export type CommentPage = Readonly<{
  comments: readonly TaskComment[];
  nextPageToken: string;
}>;

export type TaskRun = Readonly<{
  id: string;
  taskID: string;
  placementID: string;
  nodeID: string;
  nodeKind: string;
  scriptPath: string;
  sessionID: string;
  sessionName: string;
  role: string;
  status: string;
  generation: number;
  waitingAskID: string | null;
  startedAt: number | null;
  completedAt: number | null;
  interruptedAt: number | null;
  interruptionReason: string | null;
  interruptionDetail: string;
}>;

export type TaskTransition = Readonly<{
  id: string;
  transitionID: string;
  transitionName: string;
  sourceNodeName: string;
  state: string;
  commentary: string;
  outputValues: Readonly<Record<string, string>>;
  edges: readonly TransitionEdge[];
  version: number;
  createdAt: number;
  appliedAt: number | null;
}>;

export type TransitionEdge = Readonly<{
  id: string;
  edgeKey: string;
  targetNodeName: string;
  state: string;
  requiresApproval: boolean;
  outputRequirements: readonly string[];
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
  attention: readonly AttentionItem[];
  comments: readonly TaskComment[];
  runs: readonly TaskRun[];
  transitions: readonly TaskTransition[];
  worktreePath: string;
  createdAt: number;
  updatedAt: number;
  done: boolean;
  canceledAt: number | null;
  cancelReason: string | null;
}>;

export type ActivityItem = Readonly<{
  id: string;
  type: string;
  taskID: string;
  occurredAt: number;
  updatedAt: number;
  actor: string;
  summary: string;
  comment: TaskComment | null;
  transition: TaskTransition | null;
  run: TaskRun | null;
  attention: AttentionItem | null;
}>;

export type ActivityPage = Readonly<{
  items: readonly ActivityItem[];
  nextPageToken: string;
  generatedAt: number;
}>;
