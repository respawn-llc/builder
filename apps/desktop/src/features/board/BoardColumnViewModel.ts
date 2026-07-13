import type { BoardCard, BoardColumn, BoardGroup, MarkdownPreview, TaskStatusKind } from "../../api";

export type KanbanGroupVM = Readonly<{
  id: string;
  key: string;
  name: string;
}>;

export type KanbanColumnVM = Readonly<{
  id: string;
  name: string;
  assigneeRole: string;
  taskCount: number;
}>;

export type KanbanCardVM = Readonly<{
  id: string;
  shortID: string;
  title: string;
  preview: MarkdownPreview;
  updatedAt: number;
  activeNodeIDs: readonly string[];
  statusKind: TaskStatusKind;
  statusRunIDs: readonly string[];
  workspaceChipLabel: string | null;
  borderTone: BoardCardBorderTone;
  actions: Readonly<{
    canInterrupt: boolean;
    canResume: boolean;
    canStart: boolean;
    manualMoveTargetNodeIDs: readonly string[];
  }>;
}>;

export type BoardWorkspaceContext = Readonly<{
  defaultWorkspaceID: string;
  attachedWorkspaceCount: number;
}>;

export type BoardCardBorderTone = "default" | "primary" | "secondary";

export function toKanbanGroupVM(group: BoardGroup): KanbanGroupVM {
  return {
    id: group.id,
    key: group.key,
    name: group.name,
  };
}

export function toKanbanColumnVM(column: BoardColumn): KanbanColumnVM {
  return {
    id: column.id,
    name: column.name,
    assigneeRole: column.assigneeRole,
    taskCount: column.taskCount,
  };
}

export function toKanbanCardVM(card: BoardCard, workspaceContext: BoardWorkspaceContext): KanbanCardVM {
  return {
    id: card.id,
    shortID: card.shortID,
    title: card.title,
    preview: card.preview,
    updatedAt: card.updatedAt,
    activeNodeIDs: card.activeNodeIDs,
    statusKind: card.status.kind,
    statusRunIDs: card.status.runIDs,
    workspaceChipLabel: workspaceChipLabel(card, workspaceContext),
    borderTone: boardCardBorderTone(card.status.kind),
    actions: {
      canInterrupt: card.actions.canInterrupt,
      canResume: card.actions.canResume,
      canStart: card.actions.canStart,
      manualMoveTargetNodeIDs: card.actions.manualMoveTargetNodeIDs,
    },
  };
}

function workspaceChipLabel(card: BoardCard, context: BoardWorkspaceContext): string | null {
  if (
    context.attachedWorkspaceCount <= 1 ||
    card.sourceWorkspace.availability === "unlinked" ||
    card.sourceWorkspace.id === context.defaultWorkspaceID
  ) {
    return null;
  }
  return card.sourceWorkspace.name;
}

function boardCardBorderTone(statusKind: TaskStatusKind): BoardCardBorderTone {
  switch (statusKind) {
    case "waiting_question":
      return "primary";
    case "waiting_approval":
      return "secondary";
    case "backlog":
    case "active":
    case "done":
    case "canceled":
    case "interrupted":
    case "running":
    case "queued":
      return "default";
  }
}
