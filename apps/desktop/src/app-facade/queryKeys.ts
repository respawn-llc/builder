import {
  canonicalTaskLabelFilter,
  defaultBoardNodeCardsSort,
  type BoardNodeCardsSort,
  type TaskLabelFilter,
} from "@/api";

const attentionKey = ["attention"] as const;

function labelFilterKey(filter: TaskLabelFilter): readonly string[] {
  const canonical = canonicalTaskLabelFilter(filter);
  switch (canonical.kind) {
    case "none":
    case "unlabeled":
      return [canonical.kind];
    case "named":
      return [
        canonical.kind,
        canonical.mode,
        "included",
        ...canonical.labelIDs,
        "excluded",
        ...canonical.excludedLabelIDs,
      ];
  }
}

function boardSortKey(sort: BoardNodeCardsSort | undefined): readonly string[] {
  const canonical = sort ?? defaultBoardNodeCardsSort;
  return ["sort", canonical.field, canonical.direction];
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
  allBoardNodeCards: ["board-node-cards"],
  allProjectLabels: ["project-labels"],
  allTaskLabels: ["task-labels"],
  allActivity: ["activity"],
  allComments: ["comments"],
  allPendingAsks: ["pending-asks"],
  boardWorkflowRoot: (projectID: string, workflowID: string | undefined) => ["board", projectID, workflowID],
  projectBoardsRoot: (projectID: string) => ["board", projectID],
  board: (
    projectID: string,
    workflowID: string | undefined,
    labelFilter: TaskLabelFilter,
    sort?: BoardNodeCardsSort,
  ) => [
    "board",
    projectID,
    workflowID,
    ...labelFilterKey(labelFilter),
    ...boardSortKey(sort),
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
  boardNodeCardsRoot: (
    projectID: string,
    workflowID: string,
    labelFilter: TaskLabelFilter,
    sort?: BoardNodeCardsSort,
  ) => [
    "board-node-cards",
    projectID,
    workflowID,
    ...labelFilterKey(labelFilter),
    ...boardSortKey(sort),
  ],
  boardNodeCards: (
    projectID: string,
    workflowID: string,
    nodeID: string,
    options: Readonly<{ labelFilter: TaskLabelFilter; sort?: BoardNodeCardsSort }>,
  ) => [
    "board-node-cards",
    projectID,
    workflowID,
    ...labelFilterKey(options.labelFilter),
    ...boardSortKey(options.sort),
    nodeID,
  ],
  projectTaskListsRoot: (projectID: string) => ["task-list", projectID],
  task: (taskID: string) => ["task", taskID],
  taskDependencies: (taskID: string, direction?: string) => ["task-dependencies", taskID, direction ?? null],
  taskAttention: (taskID: string) => ["task-attention", taskID],
  activity: (taskID: string) => ["activity", taskID],
  comments: (taskID: string) => ["comments", taskID],
  pendingAsks: (sessionID: string | null) => ["pending-asks", sessionID],
};
