import type { BoardColumn } from "@/api";
import type { BoardCardDragPayload } from "./BoardDragTypes";

export type BoardDropAction =
  Readonly<{ kind: "start" }> | Readonly<{ kind: "move" }> | Readonly<{ kind: "reject" }>;

export function classifyDrop(
  column: BoardColumn,
  dragPayload: BoardCardDragPayload,
  firstActiveColumnID: string | undefined,
): BoardDropAction {
  if (dragPayload.canStart && column.id === firstActiveColumnID) {
    return { kind: "start" };
  }
  if (column.kind === "join" && dragPayload.activeNodeIDs.length === 0) {
    return { kind: "reject" };
  }
  if (column.isBacklog) {
    return { kind: "move" };
  }
  if (isTerminalColumn(column)) {
    return { kind: "move" };
  }
  return { kind: "move" };
}

function isTerminalColumn(column: BoardColumn): boolean {
  return column.isDone || column.kind === "terminal";
}
