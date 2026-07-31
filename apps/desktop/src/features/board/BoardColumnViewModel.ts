import type {
  BoardCard,
  BoardColumn,
  BoardGroup,
  MarkdownPreview,
  ProjectLabel,
  TaskStatusKind,
} from "@/api";
import { selectOrderedProjectLabels } from "@/shared/labels";

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
  dependencyProgress: BoardCard["dependencyProgress"];
  labels: readonly KanbanCardLabelVM[];
  workspaceChipLabel: string | null;
  borderTone: BoardCardBorderTone;
  actions: Readonly<{
    canInterrupt: boolean;
    canResume: boolean;
    canStart: boolean;
    canDelete: boolean;
    manualMoveTargetNodeIDs: readonly string[];
  }>;
}>;

export type KanbanCardLabelVM = Readonly<{
  id: string;
  name: string;
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

export function toKanbanCardVM(
  card: BoardCard,
  workspaceContext: BoardWorkspaceContext,
  catalog: readonly ProjectLabel[] | undefined,
): KanbanCardVM {
  return {
    id: card.id,
    shortID: card.shortID,
    title: card.title,
    preview: card.preview,
    updatedAt: card.updatedAt,
    activeNodeIDs: card.activeNodeIDs,
    statusKind: card.status.kind,
    dependencyProgress: card.dependencyProgress,
    labels: cardLabels(card.labelIDs, catalog),
    workspaceChipLabel: workspaceChipLabel(card, workspaceContext),
    borderTone: boardCardBorderTone(card.status.kind),
    actions: {
      canInterrupt: card.actions.canInterrupt,
      canResume: card.actions.canResume,
      canStart: card.actions.canStart,
      canDelete: card.actions.canDelete,
      manualMoveTargetNodeIDs: card.actions.manualMoveTargetNodeIDs,
    },
  };
}

function cardLabels(
  labelIDs: readonly string[],
  catalog: readonly ProjectLabel[] | undefined,
): readonly KanbanCardLabelVM[] {
  return selectOrderedProjectLabels(catalog, labelIDs);
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
    case "interrupted":
    case "running":
    case "queued":
      return "default";
  }
}
