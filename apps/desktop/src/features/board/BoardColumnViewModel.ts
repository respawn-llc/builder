import type { BoardCard, BoardColumn, BoardGroup } from "../../api";

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
  bodyPreview: string;
  updatedAt: number;
  activeNodeIDs: readonly string[];
  statusKind: string;
  statusRunIDs: readonly string[];
  sourceWorkspaceName: string;
  actions: Readonly<{
    canInterrupt: boolean;
    canResume: boolean;
    canStart: boolean;
    manualMoveTargetNodeIDs: readonly string[];
  }>;
}>;

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

export function toKanbanCardVM(card: BoardCard): KanbanCardVM {
  return {
    id: card.id,
    shortID: card.shortID,
    title: card.title,
    bodyPreview: card.bodyPreview,
    updatedAt: card.updatedAt,
    activeNodeIDs: card.activeNodeIDs,
    statusKind: card.status.kind,
    statusRunIDs: card.status.runIDs,
    sourceWorkspaceName: card.sourceWorkspace.name,
    actions: {
      canInterrupt: card.actions.canInterrupt,
      canResume: card.actions.canResume,
      canStart: card.actions.canStart,
      manualMoveTargetNodeIDs: card.actions.manualMoveTargetNodeIDs,
    },
  };
}
