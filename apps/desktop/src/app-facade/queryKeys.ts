import type { TaskLabelFilter } from "@/api";

const attentionKey = ["attention"] as const;

function labelFilterKey(filter: TaskLabelFilter): readonly string[] {
  switch (filter.kind) {
    case "none":
    case "unlabeled":
      return [filter.kind];
    case "named":
      return [filter.kind, filter.mode, ...[...filter.labelIDs].sort()];
  }
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
  allProjectLabels: ["project-labels"],
  allTaskLabels: ["task-labels"],
  allActivity: ["activity"],
  allComments: ["comments"],
  allPendingAsks: ["pending-asks"],
  board: (projectID: string, workflowID: string | undefined, labelFilter: TaskLabelFilter) => [
    "board",
    projectID,
    workflowID,
    ...labelFilterKey(labelFilter),
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
  boardNodeCardsRoot: (projectID: string, workflowID: string, labelFilter: TaskLabelFilter) => [
    "board-node-cards",
    projectID,
    workflowID,
    ...labelFilterKey(labelFilter),
  ],
  boardNodeCards: (projectID: string, workflowID: string, nodeID: string, labelFilter: TaskLabelFilter) => [
    "board-node-cards",
    projectID,
    workflowID,
    ...labelFilterKey(labelFilter),
    nodeID,
  ],
  task: (taskID: string) => ["task", taskID],
  taskAttention: (taskID: string) => ["task-attention", taskID],
  activity: (taskID: string) => ["activity", taskID],
  comments: (taskID: string) => ["comments", taskID],
  pendingAsks: (sessionID: string | null) => ["pending-asks", sessionID],
};
