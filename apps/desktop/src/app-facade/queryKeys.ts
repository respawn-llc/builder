import {
  canonicalBoardFilter,
  defaultBoardNodeCardsSort,
  type BoardFilterInput,
  type BoardNodeCardsSort,
} from "@/api";

const attentionKey = ["attention"] as const;

function boardFilterKey(filter: BoardFilterInput): readonly string[] {
  const canonical = canonicalBoardFilter(filter);
  const label = canonical.labelFilter;
  switch (label.kind) {
    case "none":
    case "unlabeled":
      return [label.kind, "dependency", dependencyFilterKey(canonical.dependencyFilter)];
    case "named":
      return [
        label.kind,
        label.mode,
        "included",
        ...label.labelIDs,
        "excluded",
        ...label.excludedLabelIDs,
        "dependency",
        dependencyFilterKey(canonical.dependencyFilter),
      ];
  }
}

function dependencyFilterKey(filter: boolean | null): string {
  return filter === null ? "dependency:null" : filter ? "dependency:true" : "dependency:false";
}

export const queryKeys = {
  startup: ["startup"],
  readiness: ["startup", "readiness"],
  projects: ["projects"],
  allProjectEdits: ["project-edit"],
  projectEdit: (projectID: string) => ["project-edit", projectID],
  attention: attentionKey,
  allWorkspaces: ["workspaces"],
  workspaces: (projectID: string) => ["workspaces", projectID],
  allAttention: attentionKey,
  allBoards: ["board"],
  allWorkflows: ["workflow"],
  allWorkflowDefinitions: ["workflow-definition"],
  allWorkflowValidations: ["workflow-validation"],
  allWorkflowGraphLayouts: ["workflow-graph-layout"],
  allProjectWorkflowLinks: ["project-workflow-links"],
  allTasks: ["task"],
  allTaskLists: ["task-list"],
  allTaskSearches: ["task-search"],
  globalTaskSearches: ["task-search", null],
  allBoardNodeCards: ["board-node-cards"],
  allProjectLabels: ["project-labels"],
  allTaskLabels: ["task-labels"],
  allActivity: ["activity"],
  allComments: ["comments"],
  allPendingAsks: ["pending-asks"],
  boardWorkflowRoot: (projectID: string, workflowID: string | undefined) => ["board", projectID, workflowID],
  projectBoardsRoot: (projectID: string) => ["board", projectID],
  board: (projectID: string, workflowID: string | undefined, filter: BoardFilterInput) => [
    "board",
    projectID,
    workflowID,
    ...boardFilterKey(filter),
  ],
  workflows: (query: string) => ["workflow", query],
  workflowDefinition: (workflowID: string) => ["workflow-definition", workflowID],
  workflowDraftValidation: (
    workflowID: string,
    sourceVersion: number,
    version: number,
    metadataSignature: string,
  ) => ["workflow-draft-validation", workflowID, sourceVersion, version, metadataSignature],
  workflowDraftDerivedWiring: (workflowID: string, sourceVersion: number, graphSignature: string) => [
    "workflow-draft-derived-wiring",
    workflowID,
    sourceVersion,
    graphSignature,
  ],
  workflowValidation: (workflowID: string, mode: string) => ["workflow-validation", workflowID, mode],
  workflowScriptPathValidation: (workflowID: string, nodeID: string, scriptPath: string) => [
    "workflow-script-path-validation",
    workflowID,
    nodeID,
    scriptPath,
  ],
  workflowGraphLayout: (workflowID: string, version: number, valid: boolean, errors: readonly unknown[]) => [
    "workflow-graph-layout",
    workflowID,
    version,
    valid,
    errors,
  ],
  projectWorkflowLinks: (projectID: string) => ["project-workflow-links", projectID],
  projectLabels: (projectID: string) => ["project-labels", projectID],
  taskLabels: (taskID: string) => ["task-labels", taskID],
  projectBoardNodeCardsRoot: (projectID: string) => ["board-node-cards", projectID],
  boardNodeCardsWorkflowRoot: (projectID: string, workflowID: string) => [
    "board-node-cards",
    projectID,
    workflowID,
  ],
  boardNodeCardsRoot: (projectID: string, workflowID: string, filter: BoardFilterInput) => [
    "board-node-cards",
    projectID,
    workflowID,
    ...boardFilterKey(filter),
  ],
  boardNodeCards: ({
    projectID,
    workflowID,
    nodeID,
    filter,
    sort = defaultBoardNodeCardsSort,
  }: Readonly<{
    projectID: string;
    workflowID: string;
    nodeID: string;
    filter: BoardFilterInput;
    sort?: BoardNodeCardsSort;
  }>) => [
    "board-node-cards",
    projectID,
    workflowID,
    ...boardFilterKey(filter),
    nodeID,
    sort.field,
    sort.direction,
  ],
  projectTaskListsRoot: (projectID: string) => ["task-list", projectID],
  projectTaskSearches: (projectID: string) => ["task-search", projectID],
  taskSearch: (projectID: string | null, query: string) => ["task-search", projectID, query],
  task: (taskID: string) => ["task", taskID],
  taskDependencies: (taskID: string, direction?: string) => ["task-dependencies", taskID, direction ?? null],
  taskAttention: (taskID: string) => ["task-attention", taskID],
  activity: (taskID: string) => ["activity", taskID],
  comments: (taskID: string) => ["comments", taskID],
  pendingAsks: (sessionID: string | null) => ["pending-asks", sessionID],
};
