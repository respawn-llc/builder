import type {
  BoardCard,
  BoardColumn,
  BoardGroup,
  TaskActions,
  TaskStatus,
  SelectedWorkflowBoard,
  WorkflowPickerItem,
  WorkspaceSummary,
  WorkspaceUnlinkBlocker,
} from "../api/models";

const now = Date.now();

export const inventoryCards = [
  { title: "Primitives", body: "Buttons, badges, fields, state cards, dialogs, Markdown." },
  { title: "Layouts", body: "Home panes, project edit, kanban groups, task detail tabs." },
  { title: "Hard states", body: "Hover menu, approval, question, cancel confirm, clipboard copy." },
  { title: "Mock data", body: "No service, no model, no persistent mutations." },
] as const;

export const mockNotices = [
  {
    body: "Static data loaded from dev showcase mocks.",
    id: "notice-info",
    title: "Showcase ready",
    tone: "info" as const,
  },
  {
    actionLabel: "Retry",
    body: "Disconnected mutation states are visible without breaking layout.",
    id: "notice-warn",
    onAction: () => undefined,
    title: "Read-only",
    tone: "warning" as const,
  },
  {
    body: "Task moved through the preview workflow.",
    id: "notice-success",
    title: "Task updated",
    tone: "success" as const,
  },
  {
    body: "Mutation failed; inspect logs or retry from the task detail.",
    id: "notice-danger",
    title: "Action failed",
    tone: "danger" as const,
  },
] as const;

export const mockWorkspaces: readonly WorkspaceSummary[] = [
  workspace({
    availability: "available",
    id: "workspace-api",
    isPrimary: true,
    name: "kent",
    rootPath: "/Users/nek/Developer/kent",
    updatedAgoMs: 90_000,
  }),
  workspace({
    availability: "available",
    id: "workspace-desktop",
    isPrimary: false,
    name: "desktop",
    rootPath: "/Users/nek/Developer/kent/apps/desktop",
    updatedAgoMs: 3_600_000,
  }),
  workspace({
    availability: "missing",
    id: "workspace-archive",
    isPrimary: false,
    name: "archived proof",
    rootPath: "/tmp/kent-proof-workspace",
    updatedAgoMs: 86_400_000,
  }),
];

export const mockUnlinkBlockers: readonly WorkspaceUnlinkBlocker[] = [
  { code: "active_task", count: 2, message: "2 active tasks still use this workspace." },
  { code: "default_workspace", count: 1, message: "Set another default workspace before unlinking." },
];

export const mockProjectRows = [
  {
    attention: 3,
    key: "BLDR",
    name: "Kent Desktop",
    path: "/Users/nek/Developer/kent",
    tasks: 42,
    valid: true,
    workflow: "MVP workflow",
  },
  {
    attention: 1,
    key: "DOCS",
    name: "Docs Refresh",
    path: "/Users/nek/Developer/kent/docs",
    tasks: 12,
    valid: true,
    workflow: "Docs workflow",
  },
  {
    attention: 0,
    key: "OLD",
    name: "Archived Prototype",
    path: "/tmp/kent-old",
    tasks: 8,
    valid: false,
    workflow: "Invalid workflow",
  },
] as const;

export const mockAttentionRows = [
  {
    kind: "question",
    message: "Need choose first visible preview surface.",
    shortId: "BLDR-104",
    title: "Add UI showcase board",
  },
  {
    kind: "approval",
    message: "Approve move from Design to Implementation.",
    shortId: "BLDR-103",
    title: "Review approval inbox",
  },
  {
    kind: "validation_blocker",
    message: "Workflow graph has missing edge target.",
    shortId: "",
    title: "Invalid workflow",
  },
] as const;

const statuses = {
  backlog: status("backlog", []),
  running: status("running", ["run-a7f2"]),
  question: status("waiting_question", ["run-22bc"]),
  approval: status("waiting_approval", ["run-11cd"]),
  interrupted: status("interrupted", ["run-99da"]),
  done: status("done", []),
} as const;

const taskActions = {
  start: actions({ canStart: true, manualMoveTargetNodeIDs: ["node-design"] }),
  running: actions({ canInterrupt: true }),
  waiting: actions({}),
  resume: actions({ canResume: true }),
  done: actions({}),
} as const;

const mockWorkflows: readonly WorkflowPickerItem[] = [
  workflow("workflow-mvp", "MVP workflow", true, true),
  workflow("workflow-invalid", "Invalid workflow", false, false),
  workflow("workflow-research", "Research workflow", false, true),
  workflow("workflow-release", "Release workflow", false, true),
];

const mockGroups: readonly BoardGroup[] = [
  { id: "group-intake", key: "intake", name: "Intake", nodeIDs: ["node-backlog"], sortOrder: 0 },
  {
    id: "group-delivery",
    key: "delivery",
    name: "Delivery",
    nodeIDs: ["node-design", "node-build", "node-review"],
    sortOrder: 1,
  },
  { id: "group-done", key: "done", name: "Archive", nodeIDs: ["node-done"], sortOrder: 2 },
];

const mockColumns: readonly BoardColumn[] = [
  column({
    groupID: "group-intake",
    id: "node-backlog",
    isBacklog: true,
    isDone: false,
    key: "backlog",
    name: "Backlog",
    sortOrder: 0,
    taskCount: 5,
  }),
  column({
    assigneeRole: "planner",
    groupID: "group-delivery",
    id: "node-design",
    isBacklog: false,
    isDone: false,
    key: "design",
    name: "Design",
    sortOrder: 1,
    taskCount: 3,
  }),
  column({
    assigneeRole: "agent",
    groupID: "group-delivery",
    id: "node-build",
    isBacklog: false,
    isDone: false,
    key: "build",
    name: "Implementation",
    sortOrder: 2,
    taskCount: 4,
  }),
  column({
    assigneeRole: "reviewer",
    groupID: "group-delivery",
    id: "node-review",
    isBacklog: false,
    isDone: false,
    key: "review",
    name: "Review",
    sortOrder: 3,
    taskCount: 2,
  }),
  column({
    groupID: "group-done",
    id: "node-done",
    isBacklog: false,
    isDone: true,
    key: "done",
    name: "Done",
    sortOrder: 4,
    taskCount: 8,
  }),
];

const mockCards: readonly BoardCard[] = [
  card({
    actions: taskActions.start,
    activeNodeIDs: ["node-backlog"],
    preview: "",
    id: "task-1",
    minutesAgo: 2,
    shortID: "BLDR-101",
    status: statuses.backlog,
    title: "No description fixture",
  }),
  card({
    actions: taskActions.start,
    activeNodeIDs: ["node-backlog"],
    preview: "One concise preview line.",
    id: "task-8",
    minutesAgo: 4,
    shortID: "BLDR-108",
    status: statuses.backlog,
    title: "Short description fixture",
  }),
  card({
    actions: taskActions.start,
    activeNodeIDs: ["node-backlog"],
    preview:
      "**Long preview fixture.** This bounded Markdown continues beyond the available card region so the always-on bottom fade can be reviewed independently from server truncation.",
    previewTruncated: true,
    id: "task-9",
    minutesAgo: 6,
    shortID: "BLDR-109",
    status: statuses.backlog,
    title: "Long description fixture",
  }),
  card({
    actions: taskActions.start,
    activeNodeIDs: ["node-backlog"],
    preview: "A compact body leaves the title wrapping policy easy to inspect.",
    id: "task-10",
    minutesAgo: 8,
    shortID: "BLDR-110",
    status: statuses.backlog,
    title: "Two-line title fixture with enough words to wrap across the compact board card",
  }),
  card({
    actions: taskActions.resume,
    activeNodeIDs: ["node-backlog"],
    preview: "The footer keeps its natural action height without creating dead space.",
    id: "task-11",
    minutesAgo: 10,
    shortID: "BLDR-111",
    status: statuses.interrupted,
    title: "Action footer fixture",
  }),
  card({
    actions: taskActions.running,
    activeNodeIDs: ["node-design"],
    preview: "Keep workflow picker accessible by hover, focus, and pin states.",
    id: "task-2",
    minutesAgo: 12,
    shortID: "BLDR-102",
    status: statuses.running,
    title: "Prototype hover menu states",
  }),
  card({
    actions: taskActions.waiting,
    activeNodeIDs: ["node-build"],
    preview: "Approval card should include transition snapshot and target nodes.",
    id: "task-3",
    minutesAgo: 24,
    shortID: "BLDR-103",
    status: statuses.approval,
    title: "Review approval inbox",
  }),
  card({
    actions: taskActions.waiting,
    activeNodeIDs: ["node-review"],
    preview: "Question card exercises suggestions, recommended choice, and freeform answer.",
    id: "task-4",
    minutesAgo: 36,
    shortID: "BLDR-104",
    status: statuses.question,
    title: "Answer model clarification",
  }),
  card({
    actions: taskActions.resume,
    activeNodeIDs: ["node-review"],
    preview: "Interrupted task exposes resume control.",
    id: "task-5",
    minutesAgo: 48,
    shortID: "BLDR-105",
    status: statuses.interrupted,
    title: "Resume interrupted run",
  }),
  card({
    actions: taskActions.done,
    activeNodeIDs: ["node-done"],
    preview: "Completed proof remains visible through the regular Done node card stream.",
    id: "task-6",
    minutesAgo: 70,
    shortID: "BLDR-090",
    status: statuses.done,
    title: "Capture dark proof",
  }),
  card({
    actions: taskActions.done,
    activeNodeIDs: ["node-done"],
    preview: "Older done task appears through Done pagination when needed.",
    id: "task-7",
    minutesAgo: 120,
    shortID: "BLDR-089",
    status: statuses.done,
    title: "Compact board screenshot",
  }),
];

export const mockBoardNodeCards: Readonly<Record<string, readonly BoardCard[]>> = {
  "node-backlog": mockCards.filter((card) => card.activeNodeIDs.includes("node-backlog")),
  "node-design": mockCards.filter((card) => card.activeNodeIDs.includes("node-design")),
  "node-build": mockCards.filter((card) => card.activeNodeIDs.includes("node-build")),
  "node-review": mockCards.filter((card) => card.activeNodeIDs.includes("node-review")),
  "node-done": mockCards.filter((card) => card.activeNodeIDs.includes("node-done")),
};

export const mockBoard: SelectedWorkflowBoard = {
  columns: mockColumns,
  generatedAt: now,
  groups: mockGroups,
  projectID: "project-kent",
  projectKey: "BLDR",
  projectName: "Kent Desktop",
  defaultWorkspaceID: "workspace-kent",
  attachedWorkspaceCount: 2,
  selectedWorkflow: mockWorkflows[0] ?? workflow("workflow-mvp", "MVP workflow", true, true),
  workflows: mockWorkflows,
};

function workspace(
  input: Readonly<{
    availability: WorkspaceSummary["availability"];
    id: string;
    isPrimary: boolean;
    name: string;
    rootPath: string;
    updatedAgoMs: number;
  }>,
): WorkspaceSummary {
  return {
    availability: input.availability,
    id: input.id,
    isPrimary: input.isPrimary,
    name: input.name,
    rootPath: input.rootPath,
    updatedAt: now - input.updatedAgoMs,
  };
}

function workflow(
  id: string,
  name: string,
  isProjectDefault: boolean,
  validForTaskCreation: boolean,
): WorkflowPickerItem {
  return {
    description: `${name} preview`,
    version: 7,
    id,
    isProjectDefault,
    name,
    validForTaskCreation,
    validationErrors: validForTaskCreation
      ? []
      : [
          {
            blocksContext: true,
            code: "missing_target",
            details: { fieldName: "", inputName: "", placeholder: "", providerEdgeID: "" },
            edgeID: "edge-1",
            message: "Missing target node.",
            nodeID: "node-x",
            relatedIDs: [],
            transitionGroupID: "",
            workflowID: id,
          },
        ],
  };
}

function column(
  input: Omit<BoardColumn, "assigneeRole" | "kind" | "outputFields" | "transitionOutputFields"> &
    Readonly<{ assigneeRole?: string; kind?: string }>,
): BoardColumn {
  return {
    ...input,
    assigneeRole: input.assigneeRole ?? "",
    kind: input.kind ?? (input.isBacklog ? "start" : input.isDone ? "terminal" : "agent"),
    outputFields: [],
    transitionOutputFields: [],
  };
}

function card(
  input: Readonly<{
    actions: TaskActions;
    activeNodeIDs: readonly string[];
    preview: string;
    previewTruncated?: boolean;
    id: string;
    minutesAgo: number;
    shortID: string;
    status: TaskStatus;
    title: string;
  }>,
): BoardCard {
  return {
    actions: input.actions,
    activeNodeIDs: input.activeNodeIDs,
    preview: { markdown: input.preview, truncated: input.previewTruncated ?? false },
    id: input.id,
    shortID: input.shortID,
    sourceWorkspace: mockWorkspaces[0] ?? fallbackWorkspace,
    status: input.status,
    title: input.title,
    updatedAt: now - input.minutesAgo * 60_000,
    workflowID: "workflow-mvp",
  };
}

function status(kind: TaskStatus["kind"], runIDs: readonly string[]): TaskStatus {
  return { attentionTypes: [], kind, nativeState: kind, nodeIDs: [], runIDs };
}

function actions(overrides: Partial<TaskActions>): TaskActions {
  return {
    canCancel: false,
    canInterrupt: false,
    canResume: false,
    canStart: false,
    manualMoveTargetNodeIDs: [],
    ...overrides,
  };
}

const fallbackWorkspace: WorkspaceSummary = workspace({
  availability: "available",
  id: "workspace-fallback",
  isPrimary: true,
  name: "kent",
  rootPath: "/Users/nek/Developer/kent",
  updatedAgoMs: 0,
});
